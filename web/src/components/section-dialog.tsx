import type { ReactNode } from "react";
import { Picker } from "@/components/picker";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

/** One setting: what it is on the left, its control on the right. */
export function Row({ title, description, children }: { title: string; description: string; children: ReactNode }) {
  return (
    // Side by side at any width: the words wrap in their column rather than push the control below them.
    <div className="flex items-center justify-between gap-4 px-4 py-3">
      <div className="grid min-w-0 flex-1 gap-0.5">
        <span className="text-sm font-medium">{title}</span>
        <span className="text-muted-foreground text-xs">{description}</span>
      </div>
      {/* Its controls give way within their own column, wrapping there if they must, never past the edge. */}
      <div className="flex max-w-[60%] min-w-0 flex-wrap items-center justify-end gap-2 [&>*]:min-w-0">{children}</div>
    </div>
  );
}

export const Card = ({ children }: { children: ReactNode }) => <div className="bg-card divide-y rounded-xl border">{children}</div>;

/**
 * A large dialog of named sections: a list beside the content on a wide screen and a
 * dropdown above it on a narrow one.
 */
export function SectionDialog<T extends string>({
  title,
  sections,
  section,
  onSection,
  onClose,
  children,
  page,
}: {
  title: string;
  sections: [T, string, ReactNode][];
  /** The section shown; the dialog is open while it is set. */
  section?: T;
  onSection: (section: T) => void;
  onClose: () => void;
  children: (section: T) => ReactNode;
  /** A page inside the section, such as one project in a list of them: its name follows the section's, which then leads back. */
  page?: { label: string; onBack: () => void };
}) {
  const current = sections.find(([id]) => id === section) ?? sections[0];
  return (
    <Dialog open={section !== undefined} onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="flex h-[min(40rem,85svh)] flex-col gap-0 overflow-hidden p-0 sm:max-w-4xl sm:flex-row">
        <div className="bg-sidebar border-b p-2 pr-12 sm:hidden">
          <Picker className="w-full" label={title} value={current[0]} onChange={onSection} options={sections.map(([id, label, icon]) => ({ value: id, label, icon }))} />
        </div>
        <nav className="bg-sidebar hidden w-52 shrink-0 flex-col gap-0.5 border-r p-2 sm:flex" aria-label={title}>
          {sections.map(([id, label, icon]) => (
            <Button key={id} variant="ghost" className={cn("justify-start", id === current[0] && "bg-muted")} onClick={() => onSection(id)}>
              <span data-icon="inline-start" className="contents">
                {icon}
              </span>
              {label}
            </Button>
          ))}
        </nav>
        <div className="flex min-h-0 min-w-0 flex-1 flex-col">
          <header className="hidden h-12 shrink-0 items-center gap-2 border-b px-5 text-sm sm:flex">
            <span className="text-muted-foreground">{title}</span>
            <span className="text-muted-foreground">/</span>
            {page ? (
              <>
                <button type="button" className="text-muted-foreground hover:text-foreground" onClick={page.onBack}>
                  {current[1]}
                </button>
                <span className="text-muted-foreground">/</span>
                <DialogTitle className="truncate text-sm font-medium">{page.label}</DialogTitle>
              </>
            ) : (
              <DialogTitle className="text-sm font-medium">{current[1]}</DialogTitle>
            )}
            <DialogDescription className="sr-only">{title}</DialogDescription>
          </header>
          {/* One column no wider than the dialog, and so is every single-column grid in it (a grid sizes an implicit
              column by its widest child): nothing in a section may push it sideways into a scrollbar. */}
          <div className="grid grid-cols-[minmax(0,1fr)] content-start gap-4 overflow-x-hidden overflow-y-auto p-4 sm:p-5 [&_.grid]:auto-cols-[minmax(0,1fr)]">{children(current[0])}</div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
