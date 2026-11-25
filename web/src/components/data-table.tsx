// The list table every admin and listing page uses: sortable columns, a search,
// rows that open, a "…" menu per row, and a loading and empty state.
import type { LucideIcon } from "lucide-react";
import { ArrowDownIcon, ArrowUpIcon, ChevronsUpDownIcon, MoreHorizontalIcon, SearchIcon } from "lucide-react";
import { Fragment, type MouseEvent, type ReactNode, useState } from "react";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

export interface DataTableColumn<T> {
  header: ReactNode;
  cell: (row: T) => ReactNode;
  /** Makes the column sortable by this value. */
  sortBy?: (row: T) => string | number | boolean | null | undefined;
  align?: "left" | "center" | "right";
  className?: string;
}

export interface DataTableRowAction<T> {
  label: string;
  icon?: LucideIcon;
  onSelect: (row: T) => void;
  destructive?: boolean;
  separatorBefore?: boolean;
  hidden?: (row: T) => boolean;
}

export interface TableEmpty {
  icon?: LucideIcon;
  title: string;
  description?: string;
}

const alignClass = { left: "text-left", center: "text-center", right: "text-right" };

function compare(a: unknown, b: unknown) {
  if (a == null && b == null) return 0;
  if (a == null) return 1;
  if (b == null) return -1;
  if (typeof a === "number" && typeof b === "number") return a - b;
  if (typeof a === "boolean" && typeof b === "boolean") return Number(a) - Number(b);
  return String(a).localeCompare(String(b), undefined, { numeric: true, sensitivity: "base" });
}

// A click on a control inside the row, or one that ends a text selection, is not a click on the row.
function fromInside(event: MouseEvent<HTMLTableRowElement>) {
  const target = event.target as HTMLElement;
  if (!event.currentTarget.contains(target) || window.getSelection()?.toString()) return true;
  return target.closest('a, button, input, select, textarea, label, [role="switch"], [role="checkbox"], [role="menuitem"]') !== null;
}

export function DataTable<T>({
  columns,
  rows,
  getRowKey,
  onRowClick,
  rowActions,
  search,
  actions,
  empty,
  className,
}: {
  columns: DataTableColumn<T>[];
  /** Undefined while loading. */
  rows?: T[];
  getRowKey: (row: T) => string;
  onRowClick?: (row: T) => void;
  rowActions?: DataTableRowAction<T>[];
  search?: { placeholder: string; matches: (row: T, term: string) => boolean };
  /** Beside the search, such as a button that adds a row. */
  actions?: ReactNode;
  empty: TableEmpty;
  className?: string;
}) {
  const { t } = useI18n();
  const [term, setTerm] = useState("");
  const [sort, setSort] = useState<{ index: number; dir: "asc" | "desc" }>();
  const withActions = Boolean(rowActions?.length);
  const span = columns.length + (withActions ? 1 : 0);
  const searching = term.trim() !== "";

  let shown = rows ?? [];
  if (search && searching) shown = shown.filter((row) => search.matches(row, term.trim().toLowerCase()));
  const by = sort && columns[sort.index]?.sortBy;
  if (sort && by) shown = [...shown].sort((a, b) => (sort.dir === "asc" ? 1 : -1) * compare(by(a), by(b)));
  const toggle = (index: number) => setSort((s) => (s?.index !== index ? { index, dir: "asc" } : s.dir === "asc" ? { index, dir: "desc" } : undefined));

  return (
    <div className={cn("flex flex-col gap-3", className)}>
      {(search || actions) && (
        <div className="flex flex-wrap items-center justify-between gap-2">
          {search ? (
            <InputGroup className="w-full max-w-sm">
              <InputGroupAddon>
                <SearchIcon />
              </InputGroupAddon>
              <InputGroupInput aria-label={search.placeholder} placeholder={search.placeholder} value={term} onChange={(e) => setTerm(e.target.value)} />
            </InputGroup>
          ) : (
            <span />
          )}
          {actions && <div className="flex items-center gap-2">{actions}</div>}
        </div>
      )}
      <div className="bg-card overflow-x-auto rounded-lg border">
        <Table>
          <TableHeader>
            <TableRow>
              {columns.map((column, index) => {
                const active = sort?.index === index ? sort.dir : undefined;
                const align = column.align ?? "left";
                return (
                  <TableHead key={index} aria-sort={active === "asc" ? "ascending" : active === "desc" ? "descending" : undefined} className={cn(alignClass[align], column.className)}>
                    {column.sortBy ? (
                      <button
                        type="button"
                        onClick={() => toggle(index)}
                        className={cn("hover:text-foreground -mx-1 inline-flex items-center gap-1 rounded px-1 transition-colors", active && "text-foreground", align === "right" && "flex-row-reverse")}
                      >
                        {column.header}
                        {active === "asc" ? <ArrowUpIcon className="size-3" /> : active === "desc" ? <ArrowDownIcon className="size-3" /> : <ChevronsUpDownIcon className="size-3 opacity-50" />}
                      </button>
                    ) : (
                      column.header
                    )}
                  </TableHead>
                );
              })}
              {withActions && (
                <TableHead className="w-12">
                  <span className="sr-only">{t("table.actions")}</span>
                </TableHead>
              )}
            </TableRow>
          </TableHeader>
          <TableBody>
            {!rows &&
              Array.from({ length: 4 }, (_, r) => (
                <TableRow key={`loading-${r}`} className="hover:bg-transparent">
                  {Array.from({ length: span }, (_, c) => (
                    <TableCell key={c}>
                      <Skeleton className={cn("h-3.5", c === 0 ? "w-40" : "w-16")} style={{ opacity: 1 - r * 0.2 }} />
                    </TableCell>
                  ))}
                </TableRow>
              ))}
            {rows && shown.length === 0 && (
              <TableRow className="hover:bg-transparent">
                <TableCell colSpan={span} className="h-auto py-10 whitespace-normal">
                  <Empty className="p-0">
                    <EmptyHeader>
                      {(searching ? SearchIcon : empty.icon) &&
                        (() => {
                          const Icon = searching ? SearchIcon : empty.icon!;
                          return (
                            <EmptyMedia variant="icon">
                              <Icon />
                            </EmptyMedia>
                          );
                        })()}
                      <EmptyTitle>{searching ? t("table.noMatches") : empty.title}</EmptyTitle>
                      {(searching || empty.description) && <EmptyDescription>{searching ? t("table.noMatchesHelp") : empty.description}</EmptyDescription>}
                    </EmptyHeader>
                  </Empty>
                </TableCell>
              </TableRow>
            )}
            {shown.map((row) => (
              <TableRow
                key={getRowKey(row)}
                className={cn("group/row", onRowClick && "cursor-pointer")}
                onClick={onRowClick && ((e) => !fromInside(e) && onRowClick(row))}
              >
                {columns.map((column, index) => (
                  <TableCell key={index} className={cn(alignClass[column.align ?? "left"], column.className)}>
                    {column.cell(row)}
                  </TableCell>
                ))}
                {withActions && (
                  <TableCell className="w-12 text-right">
                    <RowActions row={row} actions={rowActions!} />
                  </TableCell>
                )}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}

function RowActions<T>({ row, actions }: { row: T; actions: DataTableRowAction<T>[] }) {
  const { t } = useI18n();
  const visible = actions.filter((a) => !a.hidden?.(row));
  if (visible.length === 0) return null;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button variant="ghost" size="icon-sm" className="text-muted-foreground opacity-60 group-hover/row:opacity-100 focus-visible:opacity-100 data-popup-open:opacity-100" />}>
        <MoreHorizontalIcon />
        <span className="sr-only">{t("table.rowActions")}</span>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-auto whitespace-nowrap">
        {visible.map((a) => (
          <Fragment key={a.label}>
            {a.separatorBefore && <DropdownMenuSeparator />}
            <DropdownMenuItem variant={a.destructive ? "destructive" : "default"} onClick={() => a.onSelect(row)}>
              {a.icon && <a.icon />}
              {a.label}
            </DropdownMenuItem>
          </Fragment>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** A page's title, what it is for, how many it lists, and its main action. */
export function PageHeader({ title, description, count, actions }: { title: string; description?: string; count?: number; actions?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div>
        <div className="flex items-center gap-2">
          <h2 className="text-xl font-semibold tracking-tight">{title}</h2>
          {count !== undefined && <span className="bg-secondary text-secondary-foreground rounded-md px-1.5 text-xs font-medium tabular-nums">{count}</span>}
        </div>
        {description && <p className="text-muted-foreground mt-1 text-sm">{description}</p>}
      </div>
      {actions}
    </div>
  );
}

export function StatGrid({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cn("bg-border grid gap-px overflow-hidden rounded-lg border", className)}>{children}</div>;
}

/** One number on a dashboard; with onClick it opens what it counts. */
export function Stat({ label, value, icon: Icon, tone, onClick }: { label: string; value: ReactNode; icon?: LucideIcon; tone?: string; onClick?: () => void }) {
  const body = (
    <>
      {Icon && <Icon className={cn("size-4 shrink-0", tone ?? "text-muted-foreground")} />}
      <div className="flex min-w-0 flex-col gap-0.5 text-left">
        <span className="text-muted-foreground truncate text-xs">{label}</span>
        <span className="truncate text-lg leading-tight font-semibold tabular-nums">{value}</span>
      </div>
    </>
  );
  const base = "bg-background flex items-center gap-3 px-4 py-3";
  return onClick ? (
    <button type="button" onClick={onClick} className={cn(base, "hover:bg-muted/50 focus-visible:ring-ring/50 outline-none transition-colors focus-visible:ring-3 focus-visible:ring-inset")}>
      {body}
    </button>
  ) : (
    <div className={base}>{body}</div>
  );
}
