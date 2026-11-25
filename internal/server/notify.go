package server

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/push"
	"github.com/Alex4386/inaba/internal/store"
)

const (
	// pushSlots is how many pushes may be on their way at once, across every host.
	pushSlots   = 8
	pushTimeout = 10 * time.Second
	maxLabel    = 64
	maxTargetID = 64
	// A host may push this many alerts at once, then one every alertEvery: hosts are run by
	// ordinary users, and one sending in a loop must not flood every member or hog the slots.
	alertBurst = 30
	alertEvery = 2 * time.Second
)

// pushHosts are the push services a browser subscription may point at. Anything else is
// refused, or the server would POST to whatever URL a user hands it.
var pushHosts = []string{"fcm.googleapis.com", "updates.push.services.mozilla.com", ".push.apple.com", ".notify.windows.com"}

// notifications serves NotificationService; a type of its own, since its method names
// would otherwise sit beside the server's other services' on Server.
type notifications struct {
	inabav1connect.UnimplementedNotificationServiceHandler
	s *Server
}

// setUpPush makes the senders, with the VAPID keys made on first use and kept in the store.
func (s *Server) setUpPush() error {
	public, private, err := s.store.VAPIDKeys()
	if err != nil {
		return err
	}
	// How a push service can reach whoever runs the server: its https address, or a placeholder.
	subscriber := "admin@localhost"
	if strings.HasPrefix(s.cfg.PublicURL, "https://") {
		subscriber = s.cfg.PublicURL
	}
	s.vapidPublic = public
	s.senders = map[string]push.Sender{"web": push.NewWeb(public, private, subscriber)}
	s.pushSlots = make(chan struct{}, pushSlots)
	return nil
}

// watchAlerts follows what happens on a host that just connected and pushes it to the people
// who can see the host, until the tunnel closes.
// shortcut: alerts are not queued while the tunnel is down, so a thread deleted then keeps its snoozes, as a deleted project always does (only ids, and they mute nothing); add a host-side outbox when missed pushes matter.
// shortcut: a stream that breaks while the tunnel stays up is not opened again, so that host pushes nothing until it reconnects; retry with backoff if hosts gain write timeouts.
func (s *Server) watchAlerts(hostID string, l *link) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-l.session.CloseChan():
		case <-ctx.Done():
		}
		cancel()
	}()
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return l.session.Open() }}
	defer transport.CloseIdleConnections()
	client := inabav1connect.NewAlertServiceClient(connect.NewClient(connecthttp.NewTransport(&http.Client{Transport: transport}, "http://host")))
	stream, err := client.WatchAlerts(ctx, &inabav1.WatchAlertsRequest{})
	if err != nil {
		if ctx.Err() == nil {
			// An older host has no alert service; it works on, without pushes.
			slog.Info("the host sends no alerts", "host", hostID, "error", err)
		}
		return
	}
	defer stream.Close()
	for {
		al, err := stream.Receive()
		if err != nil {
			if ctx.Err() == nil {
				slog.Info("stopped hearing the host's alerts", "host", hostID, "error", err)
			}
			return
		}
		if !s.admitAlert(hostID, al) {
			slog.Warn("the host sends too many alerts; dropped one", "host", hostID, "event", al.Event)
			continue
		}
		s.fanOut(hostID, al)
	}
}

// admitAlert says whether an alert from the host may go out. Every alert spends from the host's
// budget, but a question or a failure always goes: a blocked agent that stays silent stalls the
// work. Finished turns and thread deletions past the budget are dropped; a dropped deletion only
// leaves snoozes that mute nothing.
// shortcut: a host sending questions in a loop is only slowed by the push slots, not stopped; give urgent alerts a looser limiter of their own if that is ever abused.
func (s *Server) admitAlert(hostID string, al *inabav1.Alert) bool {
	s.mu.Lock()
	if s.alertLimits == nil {
		s.alertLimits = map[string]*rate.Limiter{}
	}
	limit := s.alertLimits[hostID]
	if limit == nil {
		limit = rate.NewLimiter(rate.Every(alertEvery), alertBurst)
		s.alertLimits[hostID] = limit
	}
	s.mu.Unlock()
	allowed := limit.Allow()
	return allowed || al.Event == "input.needed" || al.Event == "turn.failed"
}

// fanOut pushes an alert to the devices of everyone who can see the host, but for those who
// snoozed the thread or its project and those looking at the thread right now. A snooze mutes
// finished turns; a question or a failure still gets through, since a blocked agent that
// stays silent stalls the work. It returns once every push has gone or timed out.
// shortcut: it runs in the host's receive loop and waits on slots shared by every host, so a slow push service holds back other hosts' alerts too; give each send its own queue if that shows up.
func (s *Server) fanOut(hostID string, al *inabav1.Alert) {
	if al.Event == "thread.deleted" {
		if err := s.store.DropSnoozes(hostID, "thread", al.ThreadId); err != nil {
			slog.Warn("could not drop a deleted thread's snoozes", "host", hostID, "thread", al.ThreadId, "error", err)
		}
		return
	}
	users, err := s.store.UsersOf(hostID)
	if err != nil {
		slog.Warn("could not tell who to notify", "host", hostID, "error", err)
		return
	}
	urgent := al.Event == "input.needed" || al.Event == "turn.failed"
	msg := alertMessage(hostID, al)
	var wg sync.WaitGroup
	for _, u := range users {
		if s.viewing(u, hostID, al.ThreadId) || (!urgent && s.snoozed(u, hostID, al)) {
			continue
		}
		devices, err := s.store.Devices(u)
		if err != nil {
			slog.Warn("could not read the account's devices", "user", u, "error", err)
			continue
		}
		for _, d := range devices {
			s.pushSlots <- struct{}{}
			wg.Go(func() {
				defer func() { <-s.pushSlots }()
				_ = s.send(d, msg)
			})
		}
	}
	wg.Wait()
}

// send pushes one message to one device, forgetting a device its push service says is gone.
func (s *Server) send(d store.PushDevice, msg push.Message) error {
	sender := s.senders[d.Platform]
	if sender == nil {
		return errors.New("no sender for " + d.Platform)
	}
	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	err := sender.Send(ctx, d, msg)
	switch {
	case errors.Is(err, push.ErrGone):
		if err := s.store.DeleteDevice("", d.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			slog.Warn("could not forget a gone device", "device", d.ID, "error", err)
		}
	case err != nil:
		slog.Info("could not push to a device", "device", d.ID, "error", err)
	default:
		_ = s.store.TouchDevice(d.ID)
	}
	return err
}

// viewing says whether the user has the thread open and on screen right now, so a push would
// only repeat what they see. A tab in the background does not count.
func (s *Server) viewing(userID, hostID, threadID string) bool {
	if threadID == "" {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.present {
		if p.userID == userID && p.hostID == hostID && p.threadID == threadID && p.visible && now.Sub(p.seen) <= presenceTTL {
			return true
		}
	}
	return false
}

func (s *Server) snoozed(userID, hostID string, al *inabav1.Alert) bool {
	snoozes, err := s.store.Snoozes(userID, hostID)
	if err != nil {
		return false
	}
	for _, z := range snoozes {
		if (z.Kind == "thread" && z.TargetID == al.ThreadId) || (z.Kind == "project" && al.ProjectId != "" && z.TargetID == al.ProjectId) {
			return true
		}
	}
	return false
}

// alertMessage is what a device shows for an alert.
// shortcut: the text is English, made here; send the event and let the service worker word it when a second language matters.
func alertMessage(hostID string, al *inabav1.Alert) push.Message {
	title := al.ThreadTitle
	if title == "" {
		title = "Thread"
	}
	if al.ProjectName != "" {
		title = al.ProjectName + " · " + title
	}
	// One tag per thread and kind of event: a later "Finished" must not take the place of a
	// question still waiting on screen, or of one still queued at the push service.
	m := push.Message{Title: title, Tag: al.ThreadId + "/" + al.Event, URL: "/ui/hosts/" + url.PathEscape(hostID) + "/threads/" + url.PathEscape(al.ThreadId)}
	switch al.Event {
	case "input.needed":
		m.Body, m.High = "Needs your input", true
	case "turn.failed":
		m.Body = "Failed: " + al.Error
	default:
		m.Body = "Finished"
	}
	if al.Automation {
		m.Body = "Automation · " + m.Body
	}
	return m
}

// me is the signed-in caller's id; the control socket and webhooks have no devices.
func me(ctx context.Context) (string, error) {
	if id := userID(ctx); id != "" {
		return id, nil
	}
	return "", connect.NewError(connect.CodeUnauthenticated, "sign in to get notifications")
}

func (n notifications) GetPushConfig(ctx context.Context, _ *inabav1.GetPushConfigRequest) (*inabav1.GetPushConfigResponse, error) {
	if _, err := me(ctx); err != nil {
		return nil, err
	}
	return &inabav1.GetPushConfigResponse{VapidPublicKey: n.s.vapidPublic}, nil
}

var platforms = map[string]inabav1.PushPlatform{"web": inabav1.PushPlatform_PUSH_PLATFORM_WEB, "apns": inabav1.PushPlatform_PUSH_PLATFORM_APNS, "fcm": inabav1.PushPlatform_PUSH_PLATFORM_FCM}

func deviceMessage(d *store.PushDevice) *inabav1.PushDevice {
	m := &inabav1.PushDevice{Id: d.ID, Platform: platforms[d.Platform], Label: d.Label, Endpoint: d.Endpoint, CreatedAt: timestamppb.New(d.CreatedAt)}
	if d.LastUsedAt != nil {
		m.LastUsedAt = timestamppb.New(*d.LastUsedAt)
	}
	return m
}

// validEndpoint says whether a web push endpoint is on a known push service, over https.
func validEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || len(endpoint) > 1024 {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range pushHosts {
		if host == allowed || (strings.HasPrefix(allowed, ".") && strings.HasSuffix(host, allowed)) {
			return true
		}
	}
	return false
}

// keyBytes decodes a subscription key, base64url with or without padding, and checks its length.
func keyBytes(key string, n int) bool {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(key, "="))
	return err == nil && len(b) == n
}

func (n notifications) RegisterDevice(ctx context.Context, req *inabav1.RegisterDeviceRequest) (*inabav1.PushDevice, error) {
	uid, err := me(ctx)
	if err != nil {
		return nil, err
	}
	if req.Platform != inabav1.PushPlatform_PUSH_PLATFORM_WEB {
		return nil, connect.NewError(connect.CodeInvalidArgument, "only web push is supported for now")
	}
	if !validEndpoint(req.Endpoint) {
		return nil, connect.NewError(connect.CodeInvalidArgument, "the endpoint is not a known push service")
	}
	// An uncompressed P-256 point and a 16-byte secret, as RFC 8291 has them.
	if !keyBytes(req.P256Dh, 65) || !keyBytes(req.Auth, 16) {
		return nil, connect.NewError(connect.CodeInvalidArgument, "the subscription's keys are not valid")
	}
	label := strings.TrimSpace(req.Label)
	if utf8.RuneCountInString(label) > maxLabel || !utf8.ValidString(label) {
		return nil, connect.NewError(connect.CodeInvalidArgument, "a label is at most 64 characters")
	}
	// shortcut: a device belongs to the account, not the session, so revoking a session or letting it expire keeps pushing to that browser until the device is removed in Settings; tie devices to sessions if lost devices matter.
	d := &store.PushDevice{UserID: uid, Platform: "web", Endpoint: req.Endpoint, P256dh: req.P256Dh, Auth: req.Auth, Label: label}
	if err := n.s.store.RegisterDevice(d); errors.Is(err, store.ErrTooMany) {
		return nil, connect.NewError(connect.CodeResourceExhausted, "an account has at most 20 devices; remove one first")
	} else if err != nil {
		return nil, internal("could not save the device", err)
	}
	return deviceMessage(d), nil
}

func (n notifications) ListDevices(ctx context.Context, _ *inabav1.ListDevicesRequest) (*inabav1.ListDevicesResponse, error) {
	uid, err := me(ctx)
	if err != nil {
		return nil, err
	}
	devices, err := n.s.store.Devices(uid)
	if err != nil {
		return nil, internal("could not read the devices", err)
	}
	res := &inabav1.ListDevicesResponse{}
	for i := range devices {
		res.Devices = append(res.Devices, deviceMessage(&devices[i]))
	}
	return res, nil
}

func (n notifications) DeleteDevice(ctx context.Context, req *inabav1.DeleteDeviceRequest) (*inabav1.DeleteDeviceResponse, error) {
	uid, err := me(ctx)
	if err != nil {
		return nil, err
	}
	if err := n.s.store.DeleteDevice(uid, req.Id); errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, "no device "+req.Id)
	} else if err != nil {
		return nil, internal("could not remove the device", err)
	}
	return &inabav1.DeleteDeviceResponse{}, nil
}

func (n notifications) TestDevice(ctx context.Context, req *inabav1.TestDeviceRequest) (*inabav1.TestDeviceResponse, error) {
	uid, err := me(ctx)
	if err != nil {
		return nil, err
	}
	d, err := n.s.store.Device(uid, req.Id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, "no device "+req.Id)
	} else if err != nil {
		return nil, internal("could not read the device", err)
	}
	if err := n.s.send(*d, push.Message{Title: "inaba", Body: "Test notification", URL: "/ui/", Tag: "test"}); errors.Is(err, push.ErrGone) {
		return nil, connect.NewError(connect.CodeNotFound, "the device unsubscribed; it was removed")
	} else if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, "the push service did not take the notification")
	}
	return &inabav1.TestDeviceResponse{}, nil
}

var snoozeKinds = map[inabav1.SnoozeKind]string{inabav1.SnoozeKind_SNOOZE_KIND_PROJECT: "project", inabav1.SnoozeKind_SNOOZE_KIND_THREAD: "thread"}

// snoozeTarget checks what a snooze call names: a host the caller can see, a kind, and an id.
// The id itself is the host's, which the server does not check; a wrong one only mutes nothing.
func (n notifications) snoozeTarget(ctx context.Context, hostID string, kind inabav1.SnoozeKind, targetID string) (uid, kindName string, err error) {
	if uid, err = me(ctx); err != nil {
		return "", "", err
	}
	if _, err := n.s.reachable(ctx, hostID); err != nil {
		return "", "", err
	}
	if kindName = snoozeKinds[kind]; kindName == "" {
		return "", "", connect.NewError(connect.CodeInvalidArgument, "a snooze is of a project or a thread")
	}
	if targetID == "" || len(targetID) > maxTargetID {
		return "", "", connect.NewError(connect.CodeInvalidArgument, "a target id is 1 to 64 characters")
	}
	return uid, kindName, nil
}

func snoozeMessage(z *store.Snooze) *inabav1.Snooze {
	m := &inabav1.Snooze{HostId: z.HostID, TargetId: z.TargetID, Kind: inabav1.SnoozeKind_SNOOZE_KIND_THREAD}
	if z.Kind == "project" {
		m.Kind = inabav1.SnoozeKind_SNOOZE_KIND_PROJECT
	}
	if z.Until != nil {
		m.Until = timestamppb.New(*z.Until)
	}
	return m
}

func (n notifications) ListSnoozes(ctx context.Context, req *inabav1.ListSnoozesRequest) (*inabav1.ListSnoozesResponse, error) {
	uid, err := me(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := n.s.reachable(ctx, req.HostId); err != nil {
		return nil, err
	}
	snoozes, err := n.s.store.Snoozes(uid, req.HostId)
	if err != nil {
		return nil, internal("could not read the snoozes", err)
	}
	res := &inabav1.ListSnoozesResponse{}
	for i := range snoozes {
		res.Snoozes = append(res.Snoozes, snoozeMessage(&snoozes[i]))
	}
	return res, nil
}

func (n notifications) SetSnooze(ctx context.Context, req *inabav1.Snooze) (*inabav1.Snooze, error) {
	uid, kind, err := n.snoozeTarget(ctx, req.HostId, req.Kind, req.TargetId)
	if err != nil {
		return nil, err
	}
	z := &store.Snooze{UserID: uid, HostID: req.HostId, Kind: kind, TargetID: req.TargetId, CreatedAt: time.Now()}
	if req.Until != nil {
		until := req.Until.AsTime()
		if !until.After(time.Now()) {
			return nil, connect.NewError(connect.CodeInvalidArgument, "a snooze wakes in the future")
		}
		z.Until = &until
	}
	if err := n.s.store.SetSnooze(z); err != nil {
		return nil, internal("could not save the snooze", err)
	}
	return snoozeMessage(z), nil
}

func (n notifications) ClearSnooze(ctx context.Context, req *inabav1.ClearSnoozeRequest) (*inabav1.ClearSnoozeResponse, error) {
	uid, kind, err := n.snoozeTarget(ctx, req.HostId, req.Kind, req.TargetId)
	if err != nil {
		return nil, err
	}
	if err := n.s.store.ClearSnooze(uid, req.HostId, kind, req.TargetId); err != nil {
		return nil, internal("could not clear the snooze", err)
	}
	return &inabav1.ClearSnoozeResponse{}, nil
}
