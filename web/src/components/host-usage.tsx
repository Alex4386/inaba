// A host's usage: what its agents spent over a range, day by day, by provider, project and
// thread, and the limit windows agents report. The host sums it from its own events.
import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";
import { ActivityIcon, BotIcon, CoinsIcon, MessagesSquareIcon } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { onHost } from "@/api";
import { DataTable, Stat, StatGrid } from "@/components/data-table";
import { Meter, type Part, SplitBar } from "@/components/metrics";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ProjectService, UsageService, type UsageSummary, type UsageSummary_Group, type UsageTotals } from "@/gen/inaba/v1/host_pb";
import { useI18n } from "@/lib/i18n";
import { otherTone, seriesTones, type Tone } from "@/lib/usage";
import { cn } from "@/lib/utils";

const ranges = [7, 30, 90] as const;

/** Usage over 7, 30 or 90 days, for anyone who can use the host. */
export function HostUsage({ hostId, onOpenThread }: { hostId: string; onOpenThread: (id: string) => void }) {
  const { t, locale } = useI18n();
  const [days, setDays] = useState<(typeof ranges)[number]>(30);
  const [summary, setSummary] = useState<UsageSummary>();
  const [failed, setFailed] = useState(false);
  const [projectNames, setProjectNames] = useState<Map<string, string>>(new Map());
  useEffect(() => {
    let live = true;
    setSummary(undefined);
    setFailed(false);
    onHost(hostId, UsageService)
      .getUsageSummary({ from: timestampFromDate(new Date(Date.now() - days * 86_400_000)) })
      .then(
        (s) => live && setSummary(s),
        () => live && setFailed(true),
      );
    return () => {
      live = false;
    };
  }, [hostId, days]);
  useEffect(() => {
    onHost(hostId, ProjectService)
      .listProjects({})
      .then(
        (r) => setProjectNames(new Map(r.projects.map((p) => [p.id, p.name]))),
        () => setProjectNames(new Map()),
      );
  }, [hostId]);

  // Cost when any agent reported one; tokens otherwise, so a host whose agents report no cost still shows something true.
  const byCost = Boolean(summary?.currency);
  const money = useMemo(() => (summary?.currency ? new Intl.NumberFormat(locale, { style: "currency", currency: summary.currency }) : undefined), [locale, summary?.currency]);
  const compact = useMemo(() => new Intl.NumberFormat(locale, { notation: "compact" }), [locale]);
  const measure = (u?: UsageTotals) => (u ? (byCost ? u.cost : Number(u.totalTokens)) : 0);
  const format = (v: number) => (money ? money.format(v) : t("usage.tokenCount", { count: compact.format(v) }));
  const projectName = (id: string) => (id ? (projectNames.get(id) ?? id) : t("usage.noProject"));
  // An empty id is a thread whose provider the host could not tell; "" is not a name to show.
  const providerName = (id: string) => id || t("usage.noProvider");
  const providerTones = useMemo(() => seriesTones(summary?.providers.map((p) => p.id) ?? []), [summary]);
  const projectTones = useMemo(() => seriesTones(summary?.projects.map((p) => p.id) ?? []), [summary]);

  const picker = (
    <Tabs value={String(days)} onValueChange={(v) => setDays(Number(v) as (typeof ranges)[number])}>
      <TabsList aria-label={t("usage.range")}>
        {ranges.map((r) => (
          <TabsTrigger key={r} value={String(r)} className="px-2 text-xs">
            {t("usage.days", { count: String(r) })}
          </TabsTrigger>
        ))}
      </TabsList>
    </Tabs>
  );
  if (failed)
    return (
      <div className="grid gap-4">
        {picker}
        <p className="text-muted-foreground text-sm">{t("usage.failed")}</p>
      </div>
    );
  const totals = summary?.totals;
  const parts = (groups: UsageSummary_Group[], tones: Map<string, Tone>, name: (id: string) => string): Part[] => {
    const shown = groups.filter((g) => !tones.get(g.id)?.other).map((g) => ({ label: name(g.id), value: measure(g.totals), tone: tones.get(g.id)?.bg ?? otherTone.bg }));
    const rest = groups.filter((g) => tones.get(g.id)?.other).reduce((n, g) => n + measure(g.totals), 0);
    return rest > 0 ? [...shown, { label: t("usage.other"), value: rest, tone: otherTone.bg }] : shown;
  };

  return (
    // Sized by its own width, not the window's: in the host settings dialog four cards cut their numbers short.
    <div className="@container grid gap-5">
      {picker}
      <StatGrid className="grid-cols-2 @3xl:grid-cols-4">
        <Stat label={t(byCost ? "usage.totalCost" : "usage.totalTokens")} value={totals ? format(measure(totals)) : "—"} icon={CoinsIcon} />
        <Stat label={t("usage.turns")} value={totals ? totals.turns.toLocaleString(locale) : "—"} icon={MessagesSquareIcon} />
        <Stat label={t("usage.threads")} value={totals ? totals.threads.toLocaleString(locale) : "—"} icon={ActivityIcon} />
        <Stat label={t("usage.topProvider")} value={summary?.providers[0] ? providerName(summary.providers[0].id) : "—"} icon={BotIcon} />
      </StatGrid>

      {summary && <DayChart summary={summary} measure={measure} format={format} tones={providerTones} byCost={byCost} name={providerName} />}

      <section className="grid gap-3">
        <h3 className="text-sm font-medium">{t("usage.limits")}</h3>
        {summary && summary.limits.length === 0 && <p className="text-muted-foreground text-sm">{t("usage.noLimits")}</p>}
        {summary?.limits.map((p) => (
          <div key={p.provider} className="bg-card grid gap-3 rounded-lg border p-4">
            <div className="flex items-baseline justify-between gap-2">
              <span className="text-sm font-medium">{providerName(p.provider)}</span>
              {p.reportedAt && <span className="text-muted-foreground text-xs">{t("usage.reported", { when: timestampDate(p.reportedAt).toLocaleString(locale) })}</span>}
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              {p.limits.map((l) => (
                <Meter key={l.name} value={l.usedFraction} label={l.resetsAt ? `${l.name} · ${t("usage.resets", { when: timestampDate(l.resetsAt).toLocaleString(locale) })}` : l.name} />
              ))}
            </div>
          </div>
        ))}
      </section>

      {summary && measure(totals) > 0 && (
        <div className="grid gap-4 lg:grid-cols-2">
          <section className="bg-card flex flex-col gap-3 rounded-lg border p-4">
            <h3 className="text-sm font-medium">{t("usage.byProvider")}</h3>
            <SplitBar parts={parts(summary.providers, providerTones, providerName)} format={format} />
          </section>
          <section className="bg-card flex flex-col gap-3 rounded-lg border p-4">
            <h3 className="text-sm font-medium">{t("usage.byProject")}</h3>
            <SplitBar parts={parts(summary.projects, projectTones, projectName)} format={format} />
          </section>
        </div>
      )}

      <section className="grid gap-3">
        <h3 className="text-sm font-medium">{t("usage.topThreads")}</h3>
        <DataTable
          rows={summary?.topThreads}
          getRowKey={(r) => r.threadId}
          onRowClick={(r) => onOpenThread(r.threadId)}
          empty={{ icon: ActivityIcon, title: t("usage.none"), description: t("usage.noneHelp") }}
          columns={[
            { header: t("usage.thread"), cell: (r) => <span className="block max-w-56 truncate">{r.title || t("browser.untitled")}</span>, sortBy: (r) => r.title },
            { header: t("usage.project"), cell: (r) => <span className="text-muted-foreground">{projectName(r.projectId)}</span>, sortBy: (r) => projectName(r.projectId) },
            { header: t("usage.provider"), cell: (r) => providerName(r.provider), sortBy: (r) => providerName(r.provider) },
            { header: t(byCost ? "usage.cost" : "usage.tokens"), align: "right", cell: (r) => <span className="tabular-nums">{format(measure(r.totals))}</span>, sortBy: (r) => measure(r.totals) },
            { header: t("usage.turns"), align: "right", cell: (r) => <span className="tabular-nums">{r.totals?.turns ?? 0}</span>, sortBy: (r) => r.totals?.turns ?? 0 },
          ]}
        />
      </section>
    </div>
  );
}

/** Cost or tokens per day as bars, stacked by provider, with a tooltip per day and a table of the same numbers. */
function DayChart({ summary, measure, format, tones, byCost, name }: { summary: UsageSummary; measure: (u?: UsageTotals) => number; format: (v: number) => string; tones: Map<string, Tone>; byCost: boolean; name: (id: string) => string }) {
  const { t, locale } = useI18n();
  const [table, setTable] = useState(false);
  const [at, setAt] = useState<number>();
  const days = summary.days;
  // One order for the stack, legend, tooltip and table: the tones' (sorted id) order, so it matches the colors.
  const providers = [...tones.keys()];
  const rank = new Map(providers.map((id, i) => [id, i]));
  const ordered = (groups: UsageSummary_Group[]) => [...groups].sort((a, b) => (rank.get(a.id) ?? 0) - (rank.get(b.id) ?? 0));
  const max = Math.max(0, ...days.map((d) => measure(d.totals)));
  const w = 640;
  const h = 160;
  const top = 16;
  // The axis labels are HTML below the SVG, so they keep their size when the SVG scales down on a phone.
  const bottom = 1;
  const left = 0;
  const plot = h - top - bottom;
  const slot = (w - left) / Math.max(1, days.length);
  // Thin bars with a gap between neighbors; wide ranges get narrower bars, never touching.
  const bar = Math.max(1, Math.min(18, slot - 2));
  const y = (v: number) => (max > 0 ? (v / max) * plot : 0);
  const dayLabel = (date: string) => new Date(`${date}T00:00:00`).toLocaleDateString(locale, { month: "short", day: "numeric" });
  const named = providers.filter((id) => !tones.get(id)?.other);
  const hovered = at !== undefined ? days[at] : undefined;

  return (
    <section className="grid gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-medium">{t(byCost ? "usage.costPerDay" : "usage.tokensPerDay")}</h3>
        <Button type="button" size="sm" variant="ghost" aria-pressed={table} onClick={() => setTable(!table)}>
          {t(table ? "usage.showChart" : "usage.showTable")}
        </Button>
      </div>
      {providers.length >= 2 && (
        <ul className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
          {named.map((id) => (
            <li key={id} className="flex items-center gap-1.5">
              <span className={cn("size-2 rounded-full", tones.get(id)?.bg)} aria-hidden />
              <span className="text-muted-foreground">{name(id)}</span>
            </li>
          ))}
          {named.length < providers.length && (
            <li className="flex items-center gap-1.5">
              <span className={cn("size-2 rounded-full", otherTone.bg)} aria-hidden />
              <span className="text-muted-foreground">{t("usage.other")}</span>
            </li>
          )}
        </ul>
      )}
      {table ? (
        <div className="bg-card max-h-80 overflow-auto rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("usage.date")}</TableHead>
                {providers.length >= 2 &&
                  providers.map((id) => (
                    <TableHead key={id} className="text-right">
                      {name(id)}
                    </TableHead>
                  ))}
                <TableHead className="text-right">{t("usage.total")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {days.map((d) => (
                <TableRow key={d.date}>
                  <TableCell>{dayLabel(d.date)}</TableCell>
                  {providers.length >= 2 &&
                    providers.map((id) => (
                      <TableCell key={id} className="text-right tabular-nums">
                        {format(measure(d.providers.find((p) => p.id === id)?.totals))}
                      </TableCell>
                    ))}
                  <TableCell className="text-right font-medium tabular-nums">{format(measure(d.totals))}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      ) : (
        <div className="relative">
          <svg viewBox={`0 0 ${w} ${h}`} className="w-full" role="img" aria-label={t(byCost ? "usage.costPerDay" : "usage.tokensPerDay")} onMouseLeave={() => setAt(undefined)}>
            {/* One recessive gridline at the top of the scale, labelled; the baseline below it. */}
            <line x1={left} x2={w} y1={top} y2={top} className="stroke-border" strokeDasharray="2 3" />
            <line x1={left} x2={w} y1={top + plot} y2={top + plot} className="stroke-border" />
            {days.map((d, i) => {
              const x = left + i * slot + (slot - bar) / 2;
              // Bottom up in the legend's order, so a provider sits at the same height each day.
              const segments = ordered(d.providers);
              let base = top + plot;
              return (
                <g key={d.date}>
                  {segments.map((s) => {
                    const height = y(measure(s.totals));
                    if (height <= 0) return null;
                    base -= height;
                    // A 1px surface gap between stacked segments keeps them apart.
                    return <rect key={s.id} x={x} y={base} width={bar} height={Math.max(0.5, height - 1)} rx={Math.min(2, bar / 2)} className={cn(tones.get(s.id)?.fill ?? otherTone.fill, at !== undefined && at !== i && "opacity-50")} />;
                  })}
                  {/* The hit area is the whole day's column, larger than the bar. */}
                  <rect x={left + i * slot} y={0} width={slot} height={h} fill="transparent" onMouseEnter={() => setAt(i)} />
                </g>
              );
            })}
          </svg>
          <div className="text-muted-foreground flex justify-between text-xs">
            <span>{days[0] && dayLabel(days[0].date)}</span>
            <span>{days.length > 0 && dayLabel(days[days.length - 1].date)}</span>
          </div>
          <span className="text-muted-foreground pointer-events-none absolute top-0 left-1 text-[10px] tabular-nums">{format(max)}</span>
          {hovered && at !== undefined && (
            <div
              className="bg-popover text-popover-foreground pointer-events-none absolute top-6 z-10 grid min-w-36 gap-1 rounded-md border px-2.5 py-1.5 text-xs shadow-md"
              style={at < days.length / 2 ? { left: `${((at + 1) / days.length) * 100}%` } : { right: `${((days.length - at) / days.length) * 100}%` }}
            >
              <span className="font-medium">{dayLabel(hovered.date)}</span>
              {ordered(hovered.providers).map((p) => (
                <span key={p.id} className="flex items-center gap-1.5">
                  <span className={cn("size-2 rounded-full", tones.get(p.id)?.bg ?? otherTone.bg)} aria-hidden />
                  <span className="text-muted-foreground">{name(p.id)}</span>
                  <span className="ml-auto tabular-nums">{format(measure(p.totals))}</span>
                </span>
              ))}
              <span className="flex justify-between gap-3 border-t pt-1">
                <span className="text-muted-foreground">{t("usage.total")}</span>
                <span className="font-medium tabular-nums">{format(measure(hovered.totals))}</span>
              </span>
            </div>
          )}
        </div>
      )}
    </section>
  );
}
