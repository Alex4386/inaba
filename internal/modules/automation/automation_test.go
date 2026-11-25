package automation

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/internal/testdir"
)

// An automation's runs start threads past the host's check, so one that starts them outside the
// projects needs the permission to do that from whoever sets it up.
func TestAutomationOutsideProjectsNeedsThreadsAnywhere(t *testing.T) {
	h, _ := setup(t, "")
	handler, err := h.Handler()
	if err != nil {
		t.Fatal(err)
	}
	granted := rpc.PermAutomations
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set(rpc.PermissionsHeader, granted)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	client := inabav1connect.NewAutomationServiceClient(connect.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL)))
	a := &inabav1.Automation{Name: "x", Prompt: "y", NewThread: &inabav1.NewThread{Provider: "fake", Cwd: t.TempDir()}}
	if _, err := client.CreateAutomation(context.Background(), a); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("create in a folder without threads-anywhere: %v", err)
	}
	if _, err := client.UpdateAutomation(context.Background(), a); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("update to a folder without threads-anywhere: %v", err)
	}
	granted += "," + rpc.PermThreadsAnywhere
	if _, err := client.CreateAutomation(context.Background(), a); err != nil {
		t.Fatalf("create in a folder with threads-anywhere: %v", err)
	}
}

func TestNextRun(t *testing.T) {
	// A Wednesday morning on the host's clock.
	now := time.Date(2026, 11, 25, 8, 0, 0, 0, time.Local)
	daily := func(at string, days ...uint32) *inabav1.Schedule {
		return &inabav1.Schedule{When: &inabav1.Schedule_Daily{Daily: &inabav1.Daily{At: at, Weekdays: days}}}
	}
	for name, c := range map[string]struct {
		s    *inabav1.Schedule
		want time.Time
	}{
		"every":        {&inabav1.Schedule{When: &inabav1.Schedule_EveryMinutes{EveryMinutes: 90}}, now.Add(90 * time.Minute)},
		"later today":  {daily("09:30"), time.Date(2026, 11, 25, 9, 30, 0, 0, time.Local)},
		"passed today": {daily("07:00"), time.Date(2026, 11, 26, 7, 0, 0, 0, time.Local)},
		"next monday":  {daily("07:00", 1), time.Date(2026, 11, 30, 7, 0, 0, 0, time.Local)},
		"once, ahead":  {&inabav1.Schedule{When: &inabav1.Schedule_OnceAt{OnceAt: timestamppb.New(now.Add(time.Hour))}}, now.Add(time.Hour)},
		"once, behind": {&inabav1.Schedule{When: &inabav1.Schedule_OnceAt{OnceAt: timestamppb.New(now.Add(-time.Hour))}}, time.Time{}},
		"no schedule":  {nil, time.Time{}},
	} {
		got := nextRun(c.s, now)
		if (got == nil) != c.want.IsZero() || (got != nil && !got.AsTime().Equal(c.want)) {
			t.Errorf("%s: next run %v, want %v", name, got.AsTime(), c.want)
		}
	}
}

func TestRender(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := render("Triage {{payload}} at {{time}}", []byte(`{"a":1}`), now); got != `Triage {"a":1} at 2026-01-02T03:04:05Z` {
		t.Errorf("in place: %q", got)
	}
	if got := render("Look", []byte("body"), now); got != "Look\n\n```\nbody\n```" {
		t.Errorf("appended: %q", got)
	}
	if got := render("Daily {{payload}}", nil, now); got != "Daily " {
		t.Errorf("no payload: %q", got)
	}
}

func TestAutomationsRunByHandScheduleAndWebhook(t *testing.T) {
	dbPath := filepath.Join(testdir.New(t), "inaba.db")
	h, s := setup(t, dbPath)
	ctx := rpc.Trusted(context.Background())
	dir := t.TempDir()
	runs := func(id string, want int) []*inabav1.AutomationRun {
		t.Helper()
		for range 300 {
			list, _ := s.ListAutomations(ctx, &inabav1.ListAutomationsRequest{})
			for _, a := range list.Automations {
				if a.Id == id && len(a.Runs) >= want {
					return a.Runs
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("automation %s did not run %d times", id, want)
		return nil
	}

	if _, err := s.CreateAutomation(context.Background(), &inabav1.Automation{Name: "x", Prompt: "y"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("create without the projects permission: %v", err)
	}
	if _, err := s.CreateAutomation(ctx, &inabav1.Automation{Name: "x", Prompt: "y"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("create with nowhere to send: %v", err)
	}

	// A new thread for each run, started by hand.
	fresh, err := s.CreateAutomation(ctx, &inabav1.Automation{Name: "nightly", Prompt: "check {{time}}", Enabled: true,
		NewThread: &inabav1.NewThread{Provider: "fake", Cwd: dir}})
	if err != nil || fresh.WebhookSecret != "" || fresh.Automation.NextRun != nil {
		t.Fatalf("create: %v %v", fresh, err)
	}
	run, err := s.RunAutomation(ctx, &inabav1.RunAutomationRequest{Id: fresh.Automation.Id})
	if err != nil || run.Error != "" || run.ThreadId == "" || run.Trigger != "manual" {
		t.Fatalf("run by hand: %v %v", run, err)
	}
	if th, _ := h.GetThread(ctx, &inabav1.GetThreadRequest{ThreadId: run.ThreadId}); !strings.HasPrefix(th.Title, "nightly · ") {
		t.Fatalf("the run's thread is %v", th)
	}

	// An existing thread, on a schedule that comes due right away.
	th, _ := h.CreateThread(ctx, &inabav1.CreateThreadRequest{Provider: "fake", Cwd: dir})
	soon, err := s.CreateAutomation(ctx, &inabav1.Automation{Name: "soon", Prompt: "tick", Enabled: true, ThreadId: th.Id,
		Schedule: &inabav1.Schedule{When: &inabav1.Schedule_OnceAt{OnceAt: timestamppb.New(time.Now().Add(50 * time.Millisecond))}}})
	if err != nil {
		t.Fatal(err)
	}
	if r := runs(soon.Automation.Id, 1)[0]; r.Trigger != "schedule" || r.ThreadId != th.Id || r.Error != "" {
		t.Fatalf("scheduled run: %v", r)
	}
	if got := userTexts(t, h, th.Id); !strings.HasPrefix(got, "user:tick") {
		t.Fatalf("the thread got %q", got)
	}

	// A webhook, called the ways callers prove themselves.
	hook, err := s.CreateAutomation(ctx, &inabav1.Automation{Name: "hook", Prompt: "Issue: {{payload}}", Enabled: true, Webhook: true, ThreadId: th.Id})
	if err != nil || hook.WebhookSecret == "" {
		t.Fatalf("webhook automation: %v %v", hook, err)
	}
	handler, err := h.Handler()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	call := func(body string, header map[string]string) int {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+"/hooks/"+hook.Automation.Id, strings.NewReader(body))
		for k, v := range header {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	mac := hmac.New(sha256.New, []byte(hook.WebhookSecret))
	mac.Write([]byte(`{"n":2}`))
	signed := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	for name, c := range map[string]struct {
		body   string
		header map[string]string
		want   int
	}{
		"no secret":     {`{}`, nil, http.StatusUnauthorized},
		"wrong secret":  {`{}`, map[string]string{hookTokenHeader: "nope"}, http.StatusUnauthorized},
		"bad signature": {`{"n":3}`, map[string]string{"X-Hub-Signature-256": signed}, http.StatusUnauthorized},
	} {
		if got := call(c.body, c.header); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
	if got := call(`{"n":1}`, map[string]string{hookTokenHeader: hook.WebhookSecret, "Idempotency-Key": "d1"}); got != http.StatusAccepted {
		t.Fatalf("bearer call: %d", got)
	}
	if got := call(`{"n":1}`, map[string]string{hookTokenHeader: hook.WebhookSecret, "Idempotency-Key": "d1"}); got != http.StatusOK {
		t.Fatalf("a repeated delivery: %d", got)
	}
	if got := call(`{"n":2}`, map[string]string{"X-Hub-Signature-256": signed, "X-GitHub-Delivery": "d2"}); got != http.StatusAccepted {
		t.Fatalf("signed call: %d", got)
	}
	runs(hook.Automation.Id, 2)
	for range 300 {
		if got := userTexts(t, h, th.Id); strings.Count(got, "user:") == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	evs, _ := h.ListEvents(ctx, &inabav1.ListEventsRequest{ThreadId: th.Id})
	var texts []string
	for _, e := range evs.Events {
		if m := e.GetUserMessage(); m != nil {
			texts = append(texts, m.Text)
		}
	}
	if strings.Join(texts, "|") != `tick|Issue: {"n":1}|Issue: {"n":2}` && strings.Join(texts, "|") != `tick|Issue: {"n":2}|Issue: {"n":1}` {
		t.Fatalf("messages sent: %q", texts)
	}

	// Automations, their runs and their secrets outlive the host process.
	s.stop()
	h.Close()
	_, s2 := setup(t, dbPath)
	list, _ := s2.ListAutomations(ctx, &inabav1.ListAutomationsRequest{})
	if len(list.Automations) != 3 || list.Automations[0].Name != "nightly" || len(list.Automations[0].Runs) != 1 {
		t.Fatalf("after a restart: %v", list.Automations)
	}
	if !hookAuthorized(s2.list[hook.Automation.Id].secret, http.Header{hookTokenHeader: {hook.WebhookSecret}}, nil) {
		t.Fatal("the webhook secret did not survive the restart")
	}
}
