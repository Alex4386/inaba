package host

import (
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/ptyterm"
)

// threadRow is a thread as last known, and eventRow one entry of its log. Both hold
// the wire message as it is, so the schema here does not have to follow the protos.
type threadRow struct {
	ID   string `gorm:"primaryKey"`
	Meta []byte
}

type eventRow struct {
	ThreadID string `gorm:"primaryKey"`
	Seq      uint64 `gorm:"primaryKey;autoIncrement:false"`
	Data     []byte
}

// saver writes threads and their events to the host's database, so they outlive the
// process. A nil saver keeps everything in memory only. A failed write is logged and
// the thread carries on: losing a line of history is better than stopping an agent.
// shortcut: one insert per event, including every chunk of streamed text; batch them
// if the database shows up in a profile.
type saver struct{ db *gorm.DB }

func (s *saver) event(e *inabav1.Event) {
	if s == nil {
		return
	}
	data, err := proto.Marshal(e)
	if err == nil {
		err = s.db.Create(&eventRow{ThreadID: e.ThreadId, Seq: e.Seq, Data: data}).Error
	}
	if err != nil {
		slog.Error("could not save an event", "thread", e.ThreadId, "seq", e.Seq, "error", err)
	}
}

func (s *saver) meta(m *inabav1.Thread) {
	if s == nil {
		return
	}
	data, err := proto.Marshal(m)
	if err == nil {
		err = s.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&threadRow{ID: m.Id, Meta: data}).Error
	}
	if err != nil {
		slog.Error("could not save a thread", "thread", m.Id, "error", err)
	}
}

// offeredRow is what a provider's agent last offered as session options.
type offeredRow struct {
	Provider string `gorm:"primaryKey"`
	Options  []byte
}

func (s *saver) offered(providerID string, options []*inabav1.SessionOption) {
	if s == nil {
		return
	}
	data, err := proto.Marshal(&inabav1.OptionsChanged{Options: options})
	if err == nil {
		err = s.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&offeredRow{Provider: providerID, Options: data}).Error
	}
	if err != nil {
		slog.Error("could not save a provider's options", "provider", providerID, "error", err)
	}
}

func (s *saver) delete(threadID string) {
	if s == nil {
		return
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("thread_id = ?", threadID).Delete(&eventRow{}).Error; err != nil {
			return err
		}
		if err := dropSearch(tx, threadID); err != nil {
			return err
		}
		return tx.Where("id = ?", threadID).Delete(&threadRow{}).Error
	})
	if err != nil {
		slog.Error("could not delete a thread", "thread", threadID, "error", err)
	}
}

// OpenData opens the host's database and brings back the threads saved in it. Their
// processes are gone with the host that ran them: an agent thread starts its agent
// again, on the same native session, at its next turn, and a terminal is shown as
// exited until it is restarted.
func (h *Host) OpenData(path string) error {
	if err := h.LoadProjects(path); err != nil {
		return err
	}
	// Absolute, because worktrees are made by git running in another folder.
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	h.dataDir, h.dbPath = filepath.Dir(abs), abs
	db := h.projects.db
	if err := db.AutoMigrate(&threadRow{}, &eventRow{}, &offeredRow{}, &settingRow{}); err != nil {
		return err
	}
	if err := migrateSearch(db); err != nil {
		return err
	}
	h.save = &saver{db: db}

	var known []offeredRow
	if err := db.Find(&known).Error; err != nil {
		return err
	}
	for _, row := range known {
		options := &inabav1.OptionsChanged{}
		if err := proto.Unmarshal(row.Options, options); err != nil {
			return err
		}
		h.offered[row.Provider] = options.Options
	}

	var rows []threadRow
	if err := db.Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		meta := &inabav1.Thread{}
		if err := proto.Unmarshal(row.Meta, meta); err != nil {
			return err
		}
		prov, _ := h.provider(meta.Provider)
		if prov == nil {
			// Kept in the database: the provider may be back in the config next time.
			slog.Warn("leaving out a saved thread whose provider is not configured", "thread", meta.Id, "provider", meta.Provider)
			continue
		}
		var saved []eventRow
		if err := db.Where("thread_id = ?", meta.Id).Order("seq").Find(&saved).Error; err != nil {
			return err
		}
		t := &thread{prov: prov, save: h.save, learned: h.learn(meta.Provider), show: func(o []*inabav1.SessionOption) []*inabav1.SessionOption { return h.shown(meta.Provider, o) }, bus: h.bus, mcp: h.sessionMCP, meta: meta, changed: make(chan struct{})}
		for _, s := range saved {
			e := &inabav1.Event{}
			if err := proto.Unmarshal(s.Data, e); err != nil {
				return err
			}
			t.events = append(t.events, e)
		}
		// The log is the truth about how far the thread got.
		meta.LastSeq = uint64(len(t.events))
		// Also for activity, which threads saved before activity_seq existed lack: the newest
		// conversation event gives both it and when the thread was last active.
		meta.ActivitySeq = 0
		for i := len(t.events) - 1; i >= 0; i-- {
			if activity(t.events[i]) {
				meta.ActivitySeq, meta.UpdatedAt = t.events[i].Seq, t.events[i].Time
				break
			}
		}
		// And for the failed mark, which rewinds saved before they cleared it still carry.
		meta.LastTurnFailed = false
		for _, e := range t.events {
			switch p := e.Payload.(type) {
			case *inabav1.Event_TurnEnded:
				meta.LastTurnFailed = p.TurnEnded.Error != ""
			case *inabav1.Event_TurnStarted, *inabav1.Event_Rewound:
				meta.LastTurnFailed = false
			}
		}
		if meta.Mode == inabav1.ThreadMode_THREAD_MODE_TERMINAL {
			t.term = ptyterm.Exited()
		}
		// Nothing queued runs by itself after a restart; the user decides.
		meta.QueuePaused = len(meta.Queue) > 0
		running := meta.Status != inabav1.ThreadStatus_THREAD_STATUS_IDLE
		// Background workflows ran inside the agent's session, which the restart ended.
		background := meta.BackgroundAgents > 0
		meta.BackgroundAgents = 0
		resume := (running || background) && resumable(meta, t.events)
		if resume {
			// Its work goes on, first thing once ResumeTurns runs, with what was queued behind it.
			meta.Queue = append([]*inabav1.QueuedMessage{{Id: randomID(), Text: resumePrompt, Resume: true}}, meta.Queue...)
			meta.QueuePaused = false
		}
		if running {
			// A turn was running when the host stopped. Close it in the log, so readers
			// do not wait for an end that will not come.
			ended := &inabav1.TurnEnded{StopReason: hostRestarted}
			if !resume {
				ended.Error = "the host restarted while this turn was running"
			}
			t.emit(&inabav1.Event{Payload: &inabav1.Event_TurnEnded{TurnEnded: ended}})
		} else if n := len(t.events); n > 0 && t.events[n-1].GetTurnEnded().GetStopReason() == hostRestarted && t.events[n-1].GetTurnEnded().GetError() == "" {
			// It was to go on, but the host stopped again before the resume began: the user decides.
			t.emit(&inabav1.Event{Payload: &inabav1.Event_TurnEnded{TurnEnded: &inabav1.TurnEnded{Error: "the host restarted again before this turn could go on"}}})
		}
		h.threads[meta.Id] = t
	}
	// Quiet threads are archived as the host's policy says, from now until it closes.
	h.stopArchive = make(chan struct{})
	go h.archiveLoop(h.stopArchive)
	return nil
}

// hostRestarted is the stop reason of a turn the host's restart cut short.
const hostRestarted = "host_restarted"

// resumePrompt carries on a turn the host's restart cut short.
const resumePrompt = "The inaba host restarted while you were working on this turn. Your session was restarted, and any background agents or workflows you had running were stopped. Continue where you left off."

// resumable reports whether work cut short by a restart goes on by itself: an agent
// thread in use, busy lately, whose turn was not already a resume, so a host that keeps
// crashing does not keep starting it.
func resumable(meta *inabav1.Thread, events []*inabav1.Event) bool {
	if meta.Mode != inabav1.ThreadMode_THREAD_MODE_STRUCTURED || meta.Labels["archived"] != "" || len(events) == 0 {
		return false
	}
	// Work left off long ago, say while its provider was not configured, is not taken up unseen.
	if time.Since(events[len(events)-1].Time.AsTime()) > time.Hour {
		return false
	}
	for i := len(events) - 1; i >= 0; i-- {
		if m := events[i].GetUserMessage(); m != nil {
			return !m.Resume
		}
	}
	return false
}

// ResumeTurns starts the turns OpenData lined up to resume, each in the background, as
// agents can take minutes to start. Call it once the host can start agents, with the
// modules that give them their tools running.
func (h *Host) ResumeTurns() {
	h.mu.Lock()
	threads := slices.Collect(maps.Values(h.threads))
	h.mu.Unlock()
	for _, t := range threads {
		go func() {
			t.op.Lock()
			defer t.op.Unlock()
			if !t.deleted {
				t.runNext()
			}
		}()
	}
}
