package host

import (
	"cmp"
	"context"
	"slices"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
)

const (
	usageDefaultWindow = 30 * 24 * time.Hour
	// Keeps a summary to one pass over at most a quarter of history and a short day list.
	usageMaxWindow  = 90 * 24 * time.Hour
	usageTopThreads = 10
)

// GetUsageSummary answers UsageService: what the host's agents used over a window, summed
// from the events the host keeps. No permission beyond using the host: it shows totals and
// thread titles, which anyone on the host already sees.
// shortcut: a deleted thread takes its events, and so its usage, with it; keep a usage
// ledger apart from threads if spend must survive deletes.
func (h *Host) GetUsageSummary(_ context.Context, req *inabav1.GetUsageSummaryRequest) (*inabav1.UsageSummary, error) {
	to := time.Now()
	if req.To != nil {
		if err := req.To.CheckValid(); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, "to is not a valid time")
		}
		to = req.To.AsTime()
	}
	from := to.Add(-usageDefaultWindow)
	if req.From != nil {
		if err := req.From.CheckValid(); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, "from is not a valid time")
		}
		from = req.From.AsTime()
	}
	if !from.Before(to) {
		return nil, connect.NewError(connect.CodeInvalidArgument, "from must be before to")
	}
	if to.Sub(from) > usageMaxWindow {
		from = to.Add(-usageMaxWindow)
	}
	h.mu.Lock()
	threads := make([]*thread, 0, len(h.threads))
	for _, t := range h.threads {
		threads = append(threads, t)
	}
	h.mu.Unlock()
	logs := make([]usageLog, 0, len(threads))
	for _, t := range threads {
		// The log only grows and its events do not change once appended, so it is read unlocked.
		events, _ := t.after(0, 0)
		logs = append(logs, usageLog{meta: t.snapshot(), events: events})
	}
	return summarizeUsage(logs, from, to, time.Local), nil
}

type usageLog struct {
	meta   *inabav1.Thread
	events []*inabav1.Event
}

// summarizeUsage sums the logs over [from, to], with days in loc. Cost comes from the
// cumulative cost agents report in UsageChanged, as the rise between reports; tokens come
// from TurnEnded. Rewound turns still count: they were paid for.
// shortcut: one pass over every event of every thread, in memory; index usage events by
// time if hosts with millions of events make this slow.
func summarizeUsage(logs []usageLog, from, to time.Time, loc *time.Location) *inabav1.UsageSummary {
	out := &inabav1.UsageSummary{From: timestamppb.New(from), To: timestamppb.New(to), Totals: &inabav1.UsageTotals{}}
	day := map[string]*inabav1.UsageTotals{}
	f := from.In(loc)
	for d := time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, loc); !d.After(to); d = d.AddDate(0, 0, 1) {
		entry := &inabav1.UsageSummary_Day{Date: d.Format(time.DateOnly), Totals: &inabav1.UsageTotals{}}
		out.Days = append(out.Days, entry)
		day[entry.Date] = entry.Totals
	}
	providers, projects := map[string]*inabav1.UsageTotals{}, map[string]*inabav1.UsageTotals{}
	dayProviders := map[string]map[string]*inabav1.UsageTotals{}
	limits := map[string]*inabav1.UsageSummary_ProviderLimits{}
	for _, l := range logs {
		// The provider a thread started with is the first switch's from, or the one it has now.
		provider := l.meta.Provider
		for _, e := range l.events {
			if p := e.GetProviderChanged(); p != nil {
				provider = p.From
				break
			}
		}
		thread := &inabav1.UsageTotals{}
		counted := map[*inabav1.UsageTotals]bool{}
		add := func(at time.Time, cost float64, tokens *inabav1.TurnUsage, turn bool) {
			group := func(m map[string]*inabav1.UsageTotals, id string) *inabav1.UsageTotals {
				if m[id] == nil {
					m[id] = &inabav1.UsageTotals{}
				}
				return m[id]
			}
			date := at.In(loc).Format(time.DateOnly)
			var dayProvider *inabav1.UsageTotals
			if day[date] != nil {
				if dayProviders[date] == nil {
					dayProviders[date] = map[string]*inabav1.UsageTotals{}
				}
				dayProvider = group(dayProviders[date], provider)
			}
			for _, b := range []*inabav1.UsageTotals{out.Totals, day[date], dayProvider, group(providers, provider), group(projects, l.meta.ProjectId), thread} {
				if b == nil {
					continue
				}
				if !counted[b] {
					counted[b] = true
					b.Threads++
				}
				b.Cost += cost
				if turn {
					b.Turns++
				}
				b.InputTokens += tokens.GetInputTokens()
				b.OutputTokens += tokens.GetOutputTokens()
				b.CachedReadTokens += tokens.GetCachedReadTokens()
				b.CachedWriteTokens += tokens.GetCachedWriteTokens()
				b.ThoughtTokens += tokens.GetThoughtTokens()
				b.TotalTokens += tokens.GetTotalTokens()
			}
		}
		var lastCost float64
		for _, e := range l.events {
			at := e.Time.AsTime()
			if at.After(to) {
				break
			}
			inWindow := !at.Before(from)
			switch p := e.Payload.(type) {
			case *inabav1.Event_ProviderChanged:
				provider = p.ProviderChanged.Provider
				// The new provider's agent session counts its cost from zero.
				lastCost = 0
			case *inabav1.Event_TurnEnded:
				if inWindow && !p.TurnEnded.Copied {
					add(at, 0, p.TurnEnded.Usage, true)
				}
			case *inabav1.Event_UsageChanged:
				u := p.UsageChanged.Usage
				if len(u.GetLimits()) > 0 {
					if old := limits[provider]; old == nil || !at.Before(old.ReportedAt.AsTime()) {
						limits[provider] = &inabav1.UsageSummary_ProviderLimits{Provider: provider, Limits: u.Limits, ReportedAt: e.Time}
					}
				}
				// A report without a cost says nothing about spend; agents leave it out at will.
				if u.GetCost() == 0 && u.GetCurrency() == "" {
					continue
				}
				// shortcut: costs in a second currency are left out rather than converted;
				// keep totals per currency if hosts mix providers that bill differently.
				if out.Currency == "" {
					out.Currency = u.Currency
				}
				if u.Currency != out.Currency {
					continue
				}
				// The cost is the session's running total. It falls when a new agent session
				// starts counting again, and then all of it is new spend.
				// shortcut: spend a new session makes before its first report is lost when that
				// first report is still below the old total; rare, as agents report every turn.
				spent := u.Cost - lastCost
				if spent < 0 {
					spent = u.Cost
				}
				lastCost = u.Cost
				if inWindow && spent > 0 {
					add(at, spent, nil, false)
				}
			}
		}
		if thread.Threads > 0 {
			out.TopThreads = append(out.TopThreads, &inabav1.UsageSummary_ThreadUsage{
				ThreadId: l.meta.Id, Title: l.meta.Title, ProjectId: l.meta.ProjectId, Provider: l.meta.Provider, Totals: thread})
		}
	}
	most := func(a, b *inabav1.UsageTotals) int {
		return cmp.Or(cmp.Compare(b.Cost, a.Cost), cmp.Compare(b.TotalTokens, a.TotalTokens))
	}
	groups := func(m map[string]*inabav1.UsageTotals) []*inabav1.UsageSummary_Group {
		var gs []*inabav1.UsageSummary_Group
		for id, t := range m {
			gs = append(gs, &inabav1.UsageSummary_Group{Id: id, Totals: t})
		}
		slices.SortFunc(gs, func(a, b *inabav1.UsageSummary_Group) int {
			return cmp.Or(most(a.Totals, b.Totals), cmp.Compare(a.Id, b.Id))
		})
		return gs
	}
	out.Providers, out.Projects = groups(providers), groups(projects)
	for _, d := range out.Days {
		d.Providers = groups(dayProviders[d.Date])
	}
	slices.SortFunc(out.TopThreads, func(a, b *inabav1.UsageSummary_ThreadUsage) int {
		return cmp.Or(most(a.Totals, b.Totals), cmp.Compare(a.ThreadId, b.ThreadId))
	})
	out.TopThreads = out.TopThreads[:min(len(out.TopThreads), usageTopThreads)]
	for _, l := range limits {
		out.Limits = append(out.Limits, l)
	}
	slices.SortFunc(out.Limits, func(a, b *inabav1.UsageSummary_ProviderLimits) int { return cmp.Compare(a.Provider, b.Provider) })
	return out
}
