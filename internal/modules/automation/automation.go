// Package automation is the automations module: work that starts on a schedule or
// from a webhook, and notifiers that tell other services how threads are doing.
package automation

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect/v2"
	"go.uber.org/fx"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/pkg/module"
)

// Module is the automations module, for a host to run.
var Module = module.Module{
	Name:        "automations",
	Title:       "Automations",
	Description: "Work that starts on a schedule or from a webhook, and notifications to other services when threads finish or need an answer.",
	Default:     true,
	Option: fx.Options(
		fx.Provide(newService),
		module.Provide((*service).contribution),
		fx.Invoke(func(lc fx.Lifecycle, s *service) {
			lc.Append(fx.StartStopHook(s.start, s.stop))
		}),
	),
}

// service is the module while it runs: the host's automations and notifiers, and
// the clock that runs them.
type service struct {
	inabav1connect.UnimplementedAutomationServiceHandler
	threads module.Threads
	events  module.Events
	*automations
	// stopFollowing ends the subscription to the host's events.
	stopFollowing func()
}

func newService(threads module.Threads, events module.Events, store module.Store) (*service, error) {
	s := &service{threads: threads, events: events, automations: newAutomations()}
	if store.DB != nil {
		if err := s.load(store.DB); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *service) contribution() module.Contribution {
	return module.Contribution{
		Services: func(server *connect.Server) { inabav1connect.RegisterAutomationServiceHandler(server, s) },
		Routes:   map[string]http.Handler{"POST /hooks/{id}": http.HandlerFunc(s.serveHook)},
	}
}

func (s *service) start() {
	go s.schedule()
	s.stopFollowing = s.events.Follow(func(e *inabav1.Event) bool {
		return e.GetTurnEnded() != nil || e.GetPermissionRequest() != nil
	}, s.heard)
}

func (s *service) stop() {
	s.close()
	if s.stopFollowing != nil {
		s.stopFollowing()
	}
}

// automationLabel marks a thread an automation started, with the automation's id.
const automationLabel = "automation"

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// connectMessage is the text of an error as its handler wrote it for a person.
func connectMessage(err error) string {
	var rpcErr *connect.Error
	if errors.As(err, &rpcErr) {
		return rpcErr.Message()
	}
	return err.Error()
}

const (
	// Runs kept on an automation, newest first.
	keptRuns = 20
	// Webhook delivery ids remembered, to tell a retry from a new delivery.
	keptDeliveries = 100
	// The largest webhook body taken; the server holds callers to the same.
	maxHookBody = 1 << 20
	// The most of a webhook body put into a message.
	maxPayloadText = 64 << 10
	// How long a run may take to start its session and send its message.
	runTimeout = 2 * time.Minute
)

type automationRow struct {
	ID     string `gorm:"primaryKey"`
	Data   []byte
	Secret string
	// Recent webhook delivery ids, one per line.
	Deliveries string
}

type automation struct {
	a          *inabav1.Automation
	secret     string
	deliveries []string
}

// automations is the host's automations and the clock that runs them.
type automations struct {
	mu        sync.Mutex
	list      map[string]*automation
	notifiers map[string]*notifier
	db        *gorm.DB
	// wake makes the scheduler look again, after a change to what is due when.
	wake chan struct{}
	stop chan struct{}
	once sync.Once
}

func newAutomations() *automations {
	return &automations{list: map[string]*automation{}, notifiers: map[string]*notifier{}, wake: make(chan struct{}, 1), stop: make(chan struct{})}
}

func (as *automations) close() { as.once.Do(func() { close(as.stop) }) }

func (as *automations) poke() {
	select {
	case as.wake <- struct{}{}:
	default:
	}
}

// save writes one automation; the caller holds as.mu.
func (as *automations) save(au *automation) {
	if as.db == nil {
		return
	}
	data, _ := proto.Marshal(au.a)
	row := automationRow{ID: au.a.Id, Data: data, Secret: au.secret, Deliveries: strings.Join(au.deliveries, "\n")}
	if err := as.db.Save(&row).Error; err != nil {
		slog.Error("could not save an automation", "automation", au.a.Id, "error", err.Error())
	}
}

// load brings back the saved automations. One that came due while the host was down
// runs once as soon as the scheduler looks.
func (as *automations) load(db *gorm.DB) error {
	if err := db.AutoMigrate(&automationRow{}, &notifierRow{}); err != nil {
		return err
	}
	var rows []automationRow
	if err := db.Find(&rows).Error; err != nil {
		return err
	}
	var notifiers []notifierRow
	if err := db.Find(&notifiers).Error; err != nil {
		return err
	}
	as.mu.Lock()
	defer as.mu.Unlock()
	as.db = db
	for _, row := range rows {
		a := &inabav1.Automation{}
		if err := proto.Unmarshal(row.Data, a); err != nil {
			return err
		}
		au := &automation{a: a, secret: row.Secret}
		if row.Deliveries != "" {
			au.deliveries = strings.Split(row.Deliveries, "\n")
		}
		as.list[a.Id] = au
	}
	for _, row := range notifiers {
		n := &inabav1.Notifier{}
		if err := proto.Unmarshal(row.Data, n); err != nil {
			return err
		}
		as.notifiers[n.Id] = &notifier{n: n, secret: row.Secret}
	}
	as.poke()
	return nil
}

// nextRun is the first time after now the schedule calls for, or nil when it calls for none.
func nextRun(s *inabav1.Schedule, now time.Time) *timestamppb.Timestamp {
	switch w := s.GetWhen().(type) {
	case *inabav1.Schedule_EveryMinutes:
		return timestamppb.New(now.Add(time.Duration(w.EveryMinutes) * time.Minute))
	case *inabav1.Schedule_OnceAt:
		if at := w.OnceAt.AsTime(); at.After(now) {
			return w.OnceAt
		}
	case *inabav1.Schedule_Daily:
		h, m, _ := parseClock(w.Daily.At)
		day := now.In(time.Local)
		for i := range 8 {
			d := day.AddDate(0, 0, i)
			at := time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, time.Local)
			if at.After(now) && (len(w.Daily.Weekdays) == 0 || slices.Contains(w.Daily.Weekdays, uint32(at.Weekday()))) {
				return timestamppb.New(at)
			}
		}
	}
	return nil
}

func parseClock(at string) (hour, minute int, ok bool) {
	hs, ms, found := strings.Cut(at, ":")
	h, err1 := strconv.Atoi(hs)
	m, err2 := strconv.Atoi(ms)
	return h, m, found && err1 == nil && err2 == nil && h >= 0 && h < 24 && m >= 0 && m < 60 && len(ms) == 2
}

// checkAutomation reports what makes an automation unable to run, or the caller unable to set it up.
func (s *service) checkAutomation(ctx context.Context, a *inabav1.Automation) error {
	if err := rpc.May(ctx, rpc.PermAutomations); err != nil {
		return err
	}
	// Its runs start threads as trusted, past the host's own check, so a thread outside the
	// projects is the caller's to start now.
	if a.ThreadId == "" && a.NewThread.GetProjectId() == "" {
		if err := rpc.May(ctx, rpc.PermThreadsAnywhere); err != nil {
			return err
		}
	}
	bad := func(msg string) error { return connect.NewError(connect.CodeInvalidArgument, msg) }
	if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.Prompt) == "" {
		return bad("an automation needs a name and a message to send")
	}
	switch w := a.Schedule.GetWhen().(type) {
	case *inabav1.Schedule_EveryMinutes:
		if w.EveryMinutes == 0 {
			return bad("a schedule runs at most every minute")
		}
	case *inabav1.Schedule_Daily:
		if _, _, ok := parseClock(w.Daily.At); !ok {
			return bad("the time of day is HH:MM, as 09:30")
		}
		for _, d := range w.Daily.Weekdays {
			if d > 6 {
				return bad("days are 0 for Sunday to 6 for Saturday")
			}
		}
	}
	trusted := rpc.Trusted(context.Background())
	if a.ThreadId != "" {
		_, err := s.threads.GetThread(trusted, &inabav1.GetThreadRequest{ThreadId: a.ThreadId})
		return err
	}
	ns := a.NewThread
	providers, err := s.threads.ListProviders(trusted, &inabav1.ListProvidersRequest{})
	if err != nil {
		return err
	}
	if ns == nil || !slices.ContainsFunc(providers.Providers, func(p *inabav1.Provider) bool { return p.Id == ns.Provider }) {
		return bad("send the message to a thread, or name a provider for a new thread each run")
	}
	if ns.ProjectId == "" && ns.Cwd == "" {
		return bad("a new thread needs a project or a folder")
	}
	if ns.Worktree && ns.ProjectId == "" {
		return bad("a worktree is made from a project; name one")
	}
	return nil
}

func newSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *service) ListAutomations(ctx context.Context, _ *inabav1.ListAutomationsRequest) (*inabav1.ListAutomationsResponse, error) {
	if err := rpc.May(ctx, rpc.PermAutomations); err != nil {
		return nil, err
	}
	as := s.automations
	as.mu.Lock()
	defer as.mu.Unlock()
	res := &inabav1.ListAutomationsResponse{}
	for _, au := range as.list {
		res.Automations = append(res.Automations, proto.Clone(au.a).(*inabav1.Automation))
	}
	slices.SortFunc(res.Automations, func(a, b *inabav1.Automation) int { return a.CreatedAt.AsTime().Compare(b.CreatedAt.AsTime()) })
	return res, nil
}

func (s *service) CreateAutomation(ctx context.Context, req *inabav1.Automation) (*inabav1.CreateAutomationResponse, error) {
	if err := s.checkAutomation(ctx, req); err != nil {
		return nil, err
	}
	a := proto.Clone(req).(*inabav1.Automation)
	a.Id, a.CreatedAt, a.Runs = randomID(), timestamppb.Now(), nil
	a.NextRun = nextRun(a.Schedule, time.Now())
	au := &automation{a: a}
	res := &inabav1.CreateAutomationResponse{}
	if a.Webhook {
		au.secret = newSecret()
		res.WebhookSecret = au.secret
	}
	as := s.automations
	as.mu.Lock()
	as.list[a.Id] = au
	as.save(au)
	res.Automation = proto.Clone(a).(*inabav1.Automation)
	as.mu.Unlock()
	as.poke()
	return res, nil
}

func (s *service) UpdateAutomation(ctx context.Context, req *inabav1.Automation) (*inabav1.Automation, error) {
	if err := s.checkAutomation(ctx, req); err != nil {
		return nil, err
	}
	as := s.automations
	as.mu.Lock()
	defer as.mu.Unlock()
	au := as.list[req.Id]
	if au == nil {
		return nil, connect.NewError(connect.CodeNotFound, "no automation "+req.Id)
	}
	a := proto.Clone(req).(*inabav1.Automation)
	a.CreatedAt, a.Runs = au.a.CreatedAt, au.a.Runs
	a.NextRun = nextRun(a.Schedule, time.Now())
	if a.Webhook && au.secret == "" {
		// Turned on without a secret: one is made, to be fetched with RotateWebhookSecret.
		au.secret = newSecret()
	}
	au.a = a
	as.save(au)
	as.poke()
	return proto.Clone(a).(*inabav1.Automation), nil
}

func (s *service) DeleteAutomation(ctx context.Context, req *inabav1.DeleteAutomationRequest) (*inabav1.DeleteAutomationResponse, error) {
	if err := rpc.May(ctx, rpc.PermAutomations); err != nil {
		return nil, err
	}
	as := s.automations
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.list[req.Id] == nil {
		return nil, connect.NewError(connect.CodeNotFound, "no automation "+req.Id)
	}
	delete(as.list, req.Id)
	if as.db != nil {
		_ = as.db.Delete(&automationRow{ID: req.Id}).Error
	}
	return &inabav1.DeleteAutomationResponse{}, nil
}

func (s *service) RotateWebhookSecret(ctx context.Context, req *inabav1.RotateWebhookSecretRequest) (*inabav1.RotateWebhookSecretResponse, error) {
	if err := rpc.May(ctx, rpc.PermAutomations); err != nil {
		return nil, err
	}
	as := s.automations
	as.mu.Lock()
	defer as.mu.Unlock()
	au := as.list[req.Id]
	if au == nil {
		return nil, connect.NewError(connect.CodeNotFound, "no automation "+req.Id)
	}
	au.secret = newSecret()
	as.save(au)
	return &inabav1.RotateWebhookSecretResponse{WebhookSecret: au.secret}, nil
}

func (s *service) RunAutomation(ctx context.Context, req *inabav1.RunAutomationRequest) (*inabav1.AutomationRun, error) {
	if err := rpc.May(ctx, rpc.PermAutomations); err != nil {
		return nil, err
	}
	return s.runAutomation(req.Id, "manual", nil)
}

// render fills the message in for one run.
func render(prompt string, payload []byte, now time.Time) string {
	text := strings.ReplaceAll(prompt, "{{time}}", now.Format(time.RFC3339))
	if len(payload) == 0 {
		return strings.ReplaceAll(text, "{{payload}}", "")
	}
	body := payload
	if len(body) > maxPayloadText {
		body = body[:maxPayloadText]
	}
	shown := strings.ToValidUTF8(string(body), "")
	if !utf8.Valid(payload[:len(body)]) || len(payload) > maxPayloadText {
		shown += "\n[cut short]"
	}
	if strings.Contains(text, "{{payload}}") {
		return strings.ReplaceAll(text, "{{payload}}", shown)
	}
	return text + "\n\n```\n" + shown + "\n```"
}

// runAutomation sends the automation's message once and records how that went.
func (s *service) runAutomation(id, trigger string, payload []byte) (*inabav1.AutomationRun, error) {
	as := s.automations
	as.mu.Lock()
	au := as.list[id]
	if au == nil {
		as.mu.Unlock()
		return nil, connect.NewError(connect.CodeNotFound, "no automation "+id)
	}
	a := proto.Clone(au.a).(*inabav1.Automation)
	as.mu.Unlock()

	now := time.Now()
	run := &inabav1.AutomationRun{Time: timestamppb.New(now), Trigger: trigger, ThreadId: a.ThreadId}
	// The automation's author was allowed to set it up; its runs act on that.
	ctx, cancel := context.WithTimeout(rpc.Trusted(context.Background()), runTimeout)
	defer cancel()
	var err error
	if run.ThreadId == "" {
		ns := a.NewThread
		create := &inabav1.CreateThreadRequest{Provider: ns.GetProvider(), ProjectId: ns.GetProjectId(), Cwd: ns.GetCwd(), Options: ns.GetOptions(), Title: a.Name + " · " + now.Format("Jan 2 15:04")}
		if ns.GetWorktree() {
			create.Worktree = &inabav1.Worktree{}
		}
		var th *inabav1.Thread
		if th, err = s.threads.CreateThread(ctx, create); err == nil {
			run.ThreadId = th.Id
			err = s.threads.SetLabel(th.Id, automationLabel, a.Id)
		}
	}
	if err == nil {
		_, err = s.threads.StartTurn(ctx, &inabav1.StartTurnRequest{ThreadId: run.ThreadId, Text: render(a.Prompt, payload, now)})
	}
	if err != nil {
		run.Error = connectMessage(err)
		slog.Warn("an automation run failed", "automation", id, "trigger", trigger, "error", run.Error)
	}

	as.mu.Lock()
	if au := as.list[id]; au != nil {
		au.a.Runs = append([]*inabav1.AutomationRun{run}, au.a.Runs...)
		if len(au.a.Runs) > keptRuns {
			au.a.Runs = au.a.Runs[:keptRuns]
		}
		as.save(au)
	}
	as.mu.Unlock()
	return run, nil
}

// schedule runs automations as they come due, until the host closes.
func (s *service) schedule() {
	as := s.automations
	for {
		now := time.Now()
		var due []string
		wait := time.Hour
		as.mu.Lock()
		for id, au := range as.list {
			next := au.a.NextRun
			if !au.a.Enabled || next == nil {
				continue
			}
			if !next.AsTime().After(now) {
				due = append(due, id)
				// Counted from now: a host that was down runs a missed run once, not each one it missed.
				au.a.NextRun = nextRun(au.a.Schedule, now)
				as.save(au)
				next = au.a.NextRun
				if next == nil {
					continue
				}
			}
			wait = min(wait, next.AsTime().Sub(now))
		}
		as.mu.Unlock()
		for _, id := range due {
			go func() { _, _ = s.runAutomation(id, "schedule", nil) }()
		}
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-as.wake:
			timer.Stop()
		case <-as.stop:
			timer.Stop()
			return
		}
	}
}

// serveHook is an automation's webhook, which inaba-server passes on to the host
// with the caller's Bearer token moved into hookTokenHeader.
func (s *service) serveHook(w http.ResponseWriter, r *http.Request) {
	reply := func(status int, body map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
	as := s.automations
	as.mu.Lock()
	au := as.list[r.PathValue("id")]
	var secret string
	var on bool
	if au != nil && au.a.Webhook {
		secret, on = au.secret, au.a.Enabled
	}
	as.mu.Unlock()
	if secret == "" {
		reply(http.StatusNotFound, map[string]any{"message": "no such webhook"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHookBody+1))
	if err != nil || len(body) > maxHookBody {
		reply(http.StatusRequestEntityTooLarge, map[string]any{"message": "the body is larger than 1 MiB"})
		return
	}
	if !hookAuthorized(secret, r.Header, body) {
		reply(http.StatusUnauthorized, map[string]any{"message": "send the webhook's secret as a Bearer token, or sign the body with it in X-Hub-Signature-256"})
		return
	}
	if !on {
		reply(http.StatusConflict, map[string]any{"message": "the automation is turned off"})
		return
	}
	if delivery := firstHeader(r.Header, "X-GitHub-Delivery", "X-Gitlab-Event-UUID", "Idempotency-Key"); delivery != "" {
		as.mu.Lock()
		au := as.list[r.PathValue("id")]
		seen := au == nil || slices.Contains(au.deliveries, delivery)
		if !seen {
			au.deliveries = append(au.deliveries, delivery)
			if len(au.deliveries) > keptDeliveries {
				au.deliveries = au.deliveries[len(au.deliveries)-keptDeliveries:]
			}
			as.save(au)
		}
		as.mu.Unlock()
		if seen {
			reply(http.StatusOK, map[string]any{"duplicate": true})
			return
		}
	}
	// Starting an agent can take longer than a webhook sender waits.
	go func() { _, _ = s.runAutomation(r.PathValue("id"), "webhook", body) }()
	reply(http.StatusAccepted, map[string]any{"accepted": true})
}

// hookTokenHeader carries a webhook caller's Bearer token from the server to the host,
// whose relay drops Authorization.
const hookTokenHeader = "X-Inaba-Hook-Token"

func hookAuthorized(secret string, header http.Header, body []byte) bool {
	if token := header.Get(hookTokenHeader); token != "" {
		return subtle.ConstantTimeCompare([]byte(token), []byte(secret)) == 1
	}
	sig, ok := strings.CutPrefix(header.Get("X-Hub-Signature-256"), "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

func firstHeader(h http.Header, names ...string) string {
	for _, n := range names {
		if v := h.Get(n); v != "" {
			return v
		}
	}
	return ""
}
