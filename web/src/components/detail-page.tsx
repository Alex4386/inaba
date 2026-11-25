// The shell detail pages are built on: a header with the record's name and its
// actions, then tabs, the first an overview of fields and the rest the lists that belong to it.
import { MoreHorizontalIcon, type LucideIcon } from "lucide-react";
import { Fragment, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Spinner } from "@/components/ui/spinner";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

export interface DetailTab {
  id: string;
  label: string;
  content: ReactNode;
}

export function DetailPage({
  media,
  title,
  subtitle,
  actions,
  tabs,
  activeTab,
  onTab,
  loading,
}: {
  /** An avatar or icon before the title. */
  media?: ReactNode;
  title: ReactNode;
  subtitle?: ReactNode;
  actions?: ReactNode;
  tabs: DetailTab[];
  activeTab?: string;
  onTab: (id: string) => void;
  loading?: boolean;
}) {
  const current = tabs.find((tab) => tab.id === activeTab) ?? tabs[0];
  if (loading)
    return (
      <div className="text-muted-foreground flex items-center gap-2 py-12 text-sm">
        <Spinner />
      </div>
    );
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          {media}
          <div className="min-w-0">
            <h2 className="truncate text-xl font-semibold tracking-tight">{title}</h2>
            {subtitle && <div className="text-muted-foreground mt-0.5 flex flex-wrap items-center gap-1.5 text-sm">{subtitle}</div>}
          </div>
        </div>
        {actions && <div className="flex items-center gap-2">{actions}</div>}
      </div>
      <div className="border-b">
        <nav className="-mb-px flex flex-nowrap gap-1 overflow-x-auto">
          {tabs.map((tab) => (
            <button
              key={tab.id}
              type="button"
              onClick={() => onTab(tab.id)}
              aria-current={tab.id === current?.id ? "page" : undefined}
              className={cn(
                "shrink-0 border-b-2 px-3 py-2 text-sm font-medium whitespace-nowrap transition-colors",
                tab.id === current?.id ? "border-primary text-foreground" : "text-muted-foreground hover:text-foreground border-transparent",
              )}
            >
              {tab.label}
            </button>
          ))}
        </nav>
      </div>
      <div>{current?.content}</div>
    </div>
  );
}

/** Label and value pairs for an overview tab. */
export function DetailFields({ fields }: { fields: { label: string; value: ReactNode }[] }) {
  return (
    <dl className="grid gap-x-8 gap-y-4 sm:grid-cols-2 lg:grid-cols-3">
      {fields.map((field) => (
        <div key={field.label} className="flex min-w-0 flex-col gap-0.5">
          <dt className="text-muted-foreground text-xs">{field.label}</dt>
          <dd className="text-sm break-words">{field.value}</dd>
        </div>
      ))}
    </dl>
  );
}

/** A section of an overview tab: a heading over its fields or table. */
export function DetailSection({ title, description, actions, children }: { title: string; description?: string; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-end justify-between gap-2">
        <div>
          <h3 className="text-sm font-medium">{title}</h3>
          {description && <p className="text-muted-foreground mt-0.5 text-sm">{description}</p>}
        </div>
        {actions}
      </div>
      {children}
    </section>
  );
}

export interface ResourceAction {
  label: string;
  icon?: LucideIcon;
  onClick: () => void;
  destructive?: boolean;
  separatorBefore?: boolean;
  hidden?: boolean;
}

/** The "…" menu beside a detail page's main buttons. */
export function ResourceMenu({ menu }: { menu: ResourceAction[] }) {
  const { t } = useI18n();
  const visible = menu.filter((a) => !a.hidden);
  if (visible.length === 0) return null;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button variant="outline" size="icon" />}>
        <MoreHorizontalIcon />
        <span className="sr-only">{t("table.rowActions")}</span>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-auto whitespace-nowrap">
        {visible.map((a) => (
          <Fragment key={a.label}>
            {a.separatorBefore && <DropdownMenuSeparator />}
            <DropdownMenuItem variant={a.destructive ? "destructive" : "default"} onClick={a.onClick}>
              {a.icon && <a.icon />}
              {a.label}
            </DropdownMenuItem>
          </Fragment>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
