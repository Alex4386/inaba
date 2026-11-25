package automation

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/pkg/module"
)

// The events a notifier can be sent.
var notifierEvents = []string{"turn.ended", "turn.failed", "input.needed"}

const (
	keptNotifierDeliveries = 20
	notifyTimeout          = 10 * time.Second
)

// retryDelays is the wait before each try of a delivery. A var, so tests need not wait.
var retryDelays = []time.Duration{0, 2 * time.Second, 10 * time.Second}

type notifierRow struct {
	ID     string `gorm:"primaryKey"`
	Data   []byte
	Secret string
}

type notifier struct {
	n      *inabav1.Notifier
	secret string
}

// outgoing is one event on its way to the notifiers that want it.
type outgoing struct {
	event  string
	thread *inabav1.Thread
	// The TurnEnded or PermissionRequest the event is about.
	payload *inabav1.Event
}

// heard passes an event of the host's on to the notifiers that want it.
func (s *service) heard(te module.ThreadEvent) {
	if te.Copied {
		return
	}
	out := outgoing{thread: te.Thread, payload: te.Event}
	switch p := te.Event.Payload.(type) {
	case *inabav1.Event_TurnEnded:
		out.event = "turn.ended"
		if p.TurnEnded.Error != "" {
			out.event = "turn.failed"
		}
	case *inabav1.Event_PermissionRequest:
		out.event = "input.needed"
	default:
		return
	}
	as := s.automations
	as.mu.Lock()
	var to []*notifier
	for _, n := range as.notifiers {
		if (len(n.n.Events) == 0 || slices.Contains(n.n.Events, out.event)) && (!n.n.AutomationsOnly || out.thread.Labels[automationLabel] != "") {
			to = append(to, &notifier{n: n.n, secret: n.secret})
		}
	}
	as.mu.Unlock()
	body := notificationBody(out)
	for _, n := range to {
		// One slow receiver must not hold up the others.
		go s.send(n, out.event, out.thread.GetId(), body)
	}
}

func notificationBody(out outgoing) []byte {
	body := map[string]any{"event": out.event, "time": time.Now().UTC().Format(time.RFC3339)}
	if th := out.thread; th != nil {
		body["thread"] = map[string]any{
			"id": th.Id, "title": th.Title, "provider": th.Provider, "status": th.Status.String(),
			"projectId": th.ProjectId, "cwd": th.Cwd, "labels": th.Labels,
		}
	}
	switch p := out.payload.GetPayload().(type) {
	case *inabav1.Event_TurnEnded:
		body["stopReason"], body["error"] = p.TurnEnded.StopReason, p.TurnEnded.Error
	case *inabav1.Event_PermissionRequest:
		body["question"] = p.PermissionRequest.Title
	}
	data, _ := json.Marshal(body)
	return data
}

// send delivers one event to one notifier, trying again after a failure the receiver
// may get over, and records how it went.
func (s *service) send(n *notifier, event, threadID string, body []byte) *inabav1.NotifierDelivery {
	mac := hmac.New(sha256.New, []byte(n.secret))
	mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	d := &inabav1.NotifierDelivery{Time: timestamppb.Now(), Event: event, ThreadId: threadID}
	id := randomID()
	for _, wait := range retryDelays {
		time.Sleep(wait)
		d.Status, d.Error = 0, ""
		ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.n.Url, bytes.NewReader(body))
		if err != nil {
			cancel()
			d.Error = err.Error()
			break
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "inaba")
		req.Header.Set("X-Inaba-Event", event)
		req.Header.Set("X-Inaba-Delivery", id)
		req.Header.Set("X-Inaba-Signature-256", signature)
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			res.Body.Close()
			d.Status = uint32(res.StatusCode)
		} else {
			d.Error = err.Error()
		}
		cancel()
		// A client error will not change on its own; a server error or no answer may.
		if err == nil && res.StatusCode < 500 && res.StatusCode != http.StatusTooManyRequests {
			break
		}
	}
	if d.Error == "" && (d.Status < 200 || d.Status > 299) {
		d.Error = "the receiver answered " + strconv.Itoa(int(d.Status))
	}
	as := s.automations
	as.mu.Lock()
	if cur := as.notifiers[n.n.Id]; cur != nil {
		cur.n.Deliveries = append([]*inabav1.NotifierDelivery{d}, cur.n.Deliveries...)
		if len(cur.n.Deliveries) > keptNotifierDeliveries {
			cur.n.Deliveries = cur.n.Deliveries[:keptNotifierDeliveries]
		}
		as.saveNotifier(cur)
	}
	as.mu.Unlock()
	return d
}

// saveNotifier writes one notifier; the caller holds as.mu.
func (as *automations) saveNotifier(n *notifier) {
	if as.db == nil {
		return
	}
	data, _ := proto.Marshal(n.n)
	if err := as.db.Save(&notifierRow{ID: n.n.Id, Data: data, Secret: n.secret}).Error; err != nil {
		slog.Error("could not save a notifier", "notifier", n.n.Id, "error", err.Error())
	}
}

func (s *service) ListNotifiers(ctx context.Context, _ *inabav1.ListNotifiersRequest) (*inabav1.ListNotifiersResponse, error) {
	if err := rpc.May(ctx, rpc.PermAutomations); err != nil {
		return nil, err
	}
	as := s.automations
	as.mu.Lock()
	defer as.mu.Unlock()
	res := &inabav1.ListNotifiersResponse{}
	for _, n := range as.notifiers {
		res.Notifiers = append(res.Notifiers, proto.Clone(n.n).(*inabav1.Notifier))
	}
	slices.SortFunc(res.Notifiers, func(a, b *inabav1.Notifier) int { return a.CreatedAt.AsTime().Compare(b.CreatedAt.AsTime()) })
	return res, nil
}

func (s *service) CreateNotifier(ctx context.Context, req *inabav1.Notifier) (*inabav1.CreateNotifierResponse, error) {
	if err := rpc.May(ctx, rpc.PermAutomations); err != nil {
		return nil, err
	}
	if u, err := url.Parse(req.Url); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "a notifier needs an http or https address")
	}
	for _, e := range req.Events {
		if !slices.Contains(notifierEvents, e) {
			return nil, connect.NewError(connect.CodeInvalidArgument, "unknown event "+e+"; the events are turn.ended, turn.failed and input.needed")
		}
	}
	n := &notifier{n: proto.Clone(req).(*inabav1.Notifier), secret: newSecret()}
	n.n.Id, n.n.CreatedAt, n.n.Deliveries = randomID(), timestamppb.Now(), nil
	as := s.automations
	as.mu.Lock()
	as.notifiers[n.n.Id] = n
	as.saveNotifier(n)
	res := &inabav1.CreateNotifierResponse{Notifier: proto.Clone(n.n).(*inabav1.Notifier), Secret: n.secret}
	as.mu.Unlock()
	return res, nil
}

func (s *service) DeleteNotifier(ctx context.Context, req *inabav1.DeleteNotifierRequest) (*inabav1.DeleteNotifierResponse, error) {
	if err := rpc.May(ctx, rpc.PermAutomations); err != nil {
		return nil, err
	}
	as := s.automations
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.notifiers[req.Id] == nil {
		return nil, connect.NewError(connect.CodeNotFound, "no notifier "+req.Id)
	}
	delete(as.notifiers, req.Id)
	if as.db != nil {
		_ = as.db.Delete(&notifierRow{ID: req.Id}).Error
	}
	return &inabav1.DeleteNotifierResponse{}, nil
}

func (s *service) TestNotifier(ctx context.Context, req *inabav1.TestNotifierRequest) (*inabav1.NotifierDelivery, error) {
	if err := rpc.May(ctx, rpc.PermAutomations); err != nil {
		return nil, err
	}
	as := s.automations
	as.mu.Lock()
	n := as.notifiers[req.Id]
	var copied *notifier
	if n != nil {
		copied = &notifier{n: n.n, secret: n.secret}
	}
	as.mu.Unlock()
	if copied == nil {
		return nil, connect.NewError(connect.CodeNotFound, "no notifier "+req.Id)
	}
	return s.send(copied, "test", "", notificationBody(outgoing{event: "test"})), nil
}
