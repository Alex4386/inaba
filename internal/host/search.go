package host

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

// The messages of every thread, as the thread view shows them, for SearchThreads. A
// trigram index finds any substring of three characters or more, Korean included.
// shortcut: thread_id and seq are unindexed, so a rewind or a delete scans the table;
// keep them in an indexed side table if hosts with a lot of history feel it.
const createSearchMessages = `CREATE VIRTUAL TABLE IF NOT EXISTS search_messages USING fts5(thread_id UNINDEXED, seq UNINDEXED, kind UNINDEXED, at UNINDEXED, body, tokenize='trigram')`

// searchState is how many of a thread's events are in search_messages. Tail is the rowid of
// its last row while that is agent text the next event may continue, else 0.
type searchState struct {
	ThreadID string `gorm:"primaryKey"`
	Indexed  uint64
	Tail     int64
	// Updated is the thread's updated time in Unix nanoseconds, to put newer threads first.
	Updated int64
}

func migrateSearch(db *gorm.DB) error {
	if err := db.AutoMigrate(&searchState{}); err != nil {
		return err
	}
	return db.Exec(createSearchMessages).Error
}

func dropSearch(tx *gorm.DB, threadID string) error {
	if err := tx.Exec("DELETE FROM search_messages WHERE thread_id = ?", threadID).Error; err != nil {
		return err
	}
	return tx.Where("thread_id = ?", threadID).Delete(&searchState{}).Error
}

func (h *Host) SearchThreads(_ context.Context, req *inabav1.SearchThreadsRequest) (*inabav1.SearchThreadsResponse, error) {
	res := &inabav1.SearchThreadsResponse{}
	query := strings.TrimSpace(req.Query)
	// Without a database there is nothing to search in.
	if query == "" || h.save == nil {
		return res, nil
	}
	if err := h.indexThreads(); err != nil {
		return nil, err
	}
	limit := int(req.Limit)
	if limit == 0 {
		limit = 50
	}
	limit = min(limit, 200)
	sql := `SELECT m.thread_id, m.seq, m.kind, m.at, m.body FROM search_messages m JOIN search_states s ON s.thread_id = m.thread_id WHERE `
	arg := query
	if len([]rune(query)) >= 3 {
		sql += `search_messages MATCH ?`
		arg = `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
	} else {
		// Too short for a trigram: scanned.
		sql += `m.body LIKE ? ESCAPE '\'`
		arg = "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query) + "%"
	}
	sql += ` ORDER BY s.updated DESC, m.seq DESC LIMIT ?`
	var rows []struct {
		ThreadID string
		Seq      uint64
		Kind     string
		At       int64
		Body     string
	}
	if err := h.save.db.Raw(sql, arg, limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		text, start, end := snippet(r.Body, query)
		res.Hits = append(res.Hits, &inabav1.SearchHit{ThreadId: r.ThreadID, Seq: r.Seq, Kind: r.Kind, Snippet: text, MatchStart: start, MatchEnd: end, Time: timestamppb.New(time.Unix(0, r.At))})
	}
	return res, nil
}

// indexThreads brings search_messages up to every thread's log, and drops what is left
// of threads that are gone.
func (h *Host) indexThreads() error {
	h.searchMu.Lock()
	defer h.searchMu.Unlock()
	db := h.save.db
	var states []searchState
	if err := db.Find(&states).Error; err != nil {
		return err
	}
	h.mu.Lock()
	threads := maps.Clone(h.threads)
	h.mu.Unlock()
	for _, st := range states {
		if threads[st.ThreadID] == nil {
			if err := db.Transaction(func(tx *gorm.DB) error { return dropSearch(tx, st.ThreadID) }); err != nil {
				return err
			}
		}
	}
	known := map[string]searchState{}
	for _, st := range states {
		known[st.ThreadID] = st
	}
	for id, t := range threads {
		st, ok := known[id]
		t.mu.Lock()
		last, updated := uint64(len(t.events)), t.meta.UpdatedAt.AsTime().UnixNano()
		// A log shorter than what was indexed is not the one indexed: start over.
		stale := st.Indexed > last
		evs := t.events[min(st.Indexed, last):]
		if stale {
			evs = t.events
		}
		t.mu.Unlock()
		if ok && last == st.Indexed {
			continue
		}
		err := db.Transaction(func(tx *gorm.DB) error {
			if stale {
				if err := dropSearch(tx, id); err != nil {
					return err
				}
				st = searchState{}
			}
			st.ThreadID, st.Updated = id, updated
			return indexEvents(tx, &st, evs)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// indexEvents folds events into search_messages as web/src/lib/thread-log.ts folds them
// into messages: a user message is one, and agent text runs on until something else
// is shown between.
// shortcut: an update to a tool already shown, a plan replacing the turn's plan, and
// a normal turn end also end a run here, though the view would not split it there;
// follow apply() closer if a search hit ever points into the middle of an answer.
func indexEvents(tx *gorm.DB, st *searchState, evs []*inabav1.Event) error {
	var run strings.Builder
	var runSeq uint64
	var runAt int64
	insert := func(seq uint64, kind string, at int64, body string) (int64, error) {
		if err := tx.Exec("INSERT INTO search_messages(thread_id, seq, kind, at, body) VALUES (?, ?, ?, ?, ?)", st.ThreadID, seq, kind, at, body).Error; err != nil {
			return 0, err
		}
		var id int64
		return id, tx.Raw("SELECT last_insert_rowid()").Scan(&id).Error
	}
	// flush writes the run so far: a new row, or more of the open tail.
	flush := func() error {
		if run.Len() == 0 && runSeq == 0 {
			return nil
		}
		var err error
		if st.Tail != 0 {
			err = tx.Exec("UPDATE search_messages SET body = body || ? WHERE rowid = ?", run.String(), st.Tail).Error
		} else {
			st.Tail, err = insert(runSeq, "assistant", runAt, run.String())
		}
		run.Reset()
		runSeq = 0
		return err
	}
	end := func() error {
		err := flush()
		st.Tail = 0
		return err
	}
	for _, e := range evs {
		var err error
		switch p := e.Payload.(type) {
		case *inabav1.Event_TextDelta:
			if p.TextDelta.Kind != inabav1.TextDelta_KIND_MESSAGE {
				err = end()
				break
			}
			if st.Tail == 0 && runSeq == 0 {
				runSeq, runAt = e.Seq, e.Time.AsTime().UnixNano()
			}
			run.WriteString(p.TextDelta.Text)
		case *inabav1.Event_UserMessage:
			if err = end(); err == nil {
				_, err = insert(e.Seq, "user", e.Time.AsTime().UnixNano(), p.UserMessage.Text)
			}
		case *inabav1.Event_Rewound:
			if err = end(); err == nil {
				err = tx.Exec("DELETE FROM search_messages WHERE thread_id = ? AND seq >= ?", st.ThreadID, p.Rewound.ToSeq).Error
			}
		case *inabav1.Event_ProviderChanged, *inabav1.Event_ToolCall, *inabav1.Event_PermissionRequest, *inabav1.Event_TurnStarted,
			*inabav1.Event_TurnEnded, *inabav1.Event_Plan, *inabav1.Event_TurnChanges, *inabav1.Event_ModeChanged, *inabav1.Event_Raw:
			// Each shows something of its own after the text.
			err = end()
		}
		if err != nil {
			return err
		}
		// Counted, not taken from e.Seq: a log with a lost write has gaps in its seqs.
		st.Indexed++
	}
	if err := flush(); err != nil {
		return err
	}
	return tx.Save(st).Error
}

// snippet is the text around the first match of query in body, on one line, with
// where the match is in it in UTF-16 code units.
func snippet(body, query string) (string, uint32, uint32) {
	text := []rune(strings.Join(strings.Fields(body), " "))
	lower := func(rs []rune) []rune {
		out := make([]rune, len(rs))
		for i, r := range rs {
			out[i] = unicode.ToLower(r)
		}
		return out
	}
	q := lower([]rune(strings.Join(strings.Fields(query), " ")))
	low := lower(text)
	at, n := 0, 0
	for i := 0; i+len(q) <= len(low); i++ {
		if slices.Equal(low[i:i+len(q)], q) {
			at, n = i, len(q)
			break
		}
	}
	from, to := max(0, at-40), min(len(text), at+n+80)
	var head string
	if from > 0 {
		head = "…"
	}
	head += string(text[from:at])
	out := head + string(text[at:to])
	if to < len(text) {
		out += "…"
	}
	start := len(utf16.Encode([]rune(head)))
	return out, uint32(start), uint32(start + len(utf16.Encode(text[at:at+n])))
}
