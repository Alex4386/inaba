package host

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/gen/inaba/v1/inabav1connect"
	"github.com/Alex4386/inaba/pkg/module"
)

// automationLabel is the label the automation module puts on threads it starts; the
// module's own constant, repeated so the host need not import a module.
const automationLabel = "automation"

// alertBacklog is how many alerts may wait for a slow server before some are dropped.
const alertBacklog = 64

// alerts serves AlertService: what inaba-server pushes to people's devices.
type alerts struct {
	inabav1connect.UnimplementedAlertServiceHandler
	h *Host
}

// WatchAlerts streams finished and failed turns and questions as they happen, and deleted
// threads so the server can forget their snoozes. Forked copies of events are left out.
func (a alerts) WatchAlerts(ctx context.Context, _ *inabav1.WatchAlertsRequest, stream inabav1connect.AlertServiceWatchAlertsServerStream) error {
	h := a.h
	ch := make(chan *inabav1.Alert, alertBacklog)
	offer := func(al *inabav1.Alert) {
		select {
		case ch <- al:
		default:
			slog.Warn("the server fell behind on alerts; dropped one", "thread", al.ThreadId, "event", al.Event)
		}
	}
	stop := h.Follow(func(e *inabav1.Event) bool {
		return e.GetTurnEnded() != nil || e.GetPermissionRequest() != nil
	}, func(te module.ThreadEvent) {
		if te.Copied {
			return
		}
		if al := h.alertFor(te); al != nil {
			offer(al)
		}
	})
	defer stop()
	stopDeleted := h.OnDelete(func(threadID string) {
		offer(&inabav1.Alert{Event: "thread.deleted", ThreadId: threadID, At: timestamppb.Now()})
	})
	defer stopDeleted()
	// Answer the call now that it listens, so the caller knows nothing after this is missed.
	if err := stream.SendHeaders(); err != nil {
		return err
	}
	for {
		select {
		case al := <-ch:
			if err := stream.Send(al); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// alertFor is the alert a thread event makes, nil for one that makes none.
func (h *Host) alertFor(te module.ThreadEvent) *inabav1.Alert {
	th := te.Thread
	al := &inabav1.Alert{ThreadId: th.GetId(), ProjectId: th.GetProjectId(), ThreadTitle: th.GetTitle(), Automation: th.GetLabels()[automationLabel] != "", At: timestamppb.Now()}
	switch {
	case te.Event.GetTurnEnded() != nil:
		ended := te.Event.GetTurnEnded()
		al.Event, al.Error = "turn.ended", ended.Error
		switch {
		case al.Error != "":
			al.Event = "turn.failed"
		case ended.StopReason == "cancelled":
			// Someone stopped it on purpose; telling everyone it finished is only noise.
			return nil
		}
	case te.Event.GetPermissionRequest() != nil:
		al.Event = "input.needed"
	default:
		return nil
	}
	if h.projects != nil && al.ProjectId != "" {
		if p, err := h.projects.Get(al.ProjectId); err == nil {
			al.ProjectName = p.Name
		}
	}
	return al
}
