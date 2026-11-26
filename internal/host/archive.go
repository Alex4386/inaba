package host

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"connectrpc.com/connect/v2"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/rpc"
)

// settingRow is one of the host's own settings, by name.
type settingRow struct {
	Key   string `gorm:"primaryKey"`
	Value string
}

func (settingRow) TableName() string { return "host_settings" }

const (
	archiveAfterKey = "archive_after_days"
	// How often quiet threads are looked for.
	archiveEvery = time.Hour
	// At most ten years: a longer wait is no policy.
	maxArchiveDays = 3650
	// The policy of a host that never set one, so finished threads do not pile up in the sidebar.
	defaultArchiveDays = 3
)

// archiveAfter is the archive policy in days, 0 for none.
func (h *Host) archiveAfter() uint32 {
	if h.projects == nil || h.projects.db == nil {
		return 0
	}
	var row settingRow
	if err := h.projects.db.Limit(1).Find(&row, "key = ?", archiveAfterKey).Error; err != nil {
		return 0
	}
	if row.Value == "" {
		return defaultArchiveDays
	}
	days, _ := strconv.ParseUint(row.Value, 10, 32)
	return uint32(days)
}

func (h *Host) GetArchivePolicy(context.Context, *inabav1.GetArchivePolicyRequest) (*inabav1.ArchivePolicy, error) {
	return &inabav1.ArchivePolicy{AfterDays: h.archiveAfter()}, nil
}

func (h *Host) SetArchivePolicy(ctx context.Context, req *inabav1.ArchivePolicy) (*inabav1.ArchivePolicy, error) {
	if err := may(ctx, rpc.PermHostSettings); err != nil {
		return nil, err
	}
	if req.AfterDays > maxArchiveDays {
		return nil, connect.NewError(connect.CodeInvalidArgument, "archive after at most 3650 days")
	}
	if h.projects == nil || h.projects.db == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "this host keeps no settings")
	}
	if err := h.projects.db.Save(&settingRow{Key: archiveAfterKey, Value: strconv.FormatUint(uint64(req.AfterDays), 10)}).Error; err != nil {
		return nil, connect.NewError(connect.CodeInternal, "could not save the policy").WithCause(err)
	}
	h.archiveQuiet(time.Now())
	return &inabav1.ArchivePolicy{AfterDays: req.AfterDays}, nil
}

// archiveQuiet archives the threads nothing happened in for longer than the policy says,
// leaving pinned, working and already archived ones, and says how many it put away.
func (h *Host) archiveQuiet(now time.Time) int {
	days := h.archiveAfter()
	if days == 0 {
		return 0
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	h.mu.Lock()
	var quiet []string
	for id, t := range h.threads {
		t.mu.Lock()
		m := t.meta
		if m.Labels["archived"] == "" && m.Labels["pinned"] == "" && m.Status == inabav1.ThreadStatus_THREAD_STATUS_IDLE && m.UpdatedAt != nil && m.UpdatedAt.AsTime().Before(cutoff) {
			quiet = append(quiet, id)
		}
		t.mu.Unlock()
	}
	h.mu.Unlock()
	for _, id := range quiet {
		if err := h.SetLabel(id, "archived", "1"); err != nil {
			slog.Warn("could not archive a quiet thread", "thread", id, "error", err)
		}
	}
	return len(quiet)
}

// bringBack unarchives a thread someone writes in, and its project, so it shows again where
// people look for it.
func (h *Host) bringBack(meta *inabav1.Thread) {
	if meta.Labels["archived"] != "" {
		if err := h.SetLabel(meta.Id, "archived", ""); err != nil {
			slog.Warn("could not unarchive a thread written in", "thread", meta.Id, "error", err)
		}
	}
	if meta.ProjectId == "" || h.projects == nil {
		return
	}
	if p, err := h.projects.Get(meta.ProjectId); err == nil && p.Archived {
		if _, err := h.projects.Update(p.ID, map[string]any{"archived": false}); err != nil {
			slog.Warn("could not unarchive the project of a thread written in", "project", p.ID, "error", err)
		}
	}
}

// archiveLoop applies the archive policy now and then, until stop closes.
func (h *Host) archiveLoop(stop <-chan struct{}) {
	h.archiveQuiet(time.Now())
	t := time.NewTicker(archiveEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			h.archiveQuiet(now)
		}
	}
}
