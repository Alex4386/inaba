import { CheckIcon, ChevronDownIcon, DownloadIcon } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { selectTriggerClass } from "@/components/ui/select";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { codeFont, codeFontStack, setCodeFont, useCodeFont } from "@/lib/settings";
import { cn } from "@/lib/utils";

// Monospace families people often have; any that is missing falls back to the system's.
const common = ["SF Mono", "Menlo", "Monaco", "JetBrains Mono", "Fira Code", "Cascadia Code", "Source Code Pro", "IBM Plex Mono", "Consolas", "Ubuntu Mono", "Hack"];

// Whether a family is installed: text drawn in it, with serif behind it, measures unlike serif alone.
function installed(family: string) {
  const c = document.createElement("canvas").getContext("2d");
  if (!c) return true;
  const width = (font: string) => ((c.font = `20px ${font}`), c.measureText("mmmmwwwwiiii0O1lI").width);
  return width(`"${family}", serif`) !== width("serif");
}

// Chrome's Local Font Access: the fonts installed on this computer, after the person allows it.
type LocalFont = { family: string };
const queryLocalFonts = (window as unknown as { queryLocalFonts?: () => Promise<LocalFont[]> }).queryLocalFonts;

/** Picks the font code is shown in, from common ones or, where the browser lets it, any installed on this computer. */
export function CodeFontPicker() {
  const { t } = useI18n();
  useCodeFont();
  const current = codeFont();
  const [open, setOpen] = useState(false);
  // Only the common ones this computer has, so each shows in its own face.
  const [present] = useState(() => common.filter(installed));
  const [local, setLocal] = useState<string[]>();
  const load = () =>
    queryLocalFonts?.().then((fonts) => {
      // A refused or unanswered prompt gives no fonts rather than an error.
      if (fonts.length === 0) toast.error(t("codeFont.noAccess"));
      setLocal([...new Set(fonts.map((f) => f.family))].sort((a, b) => a.localeCompare(b)));
    }, showError);
  const pick = (family: string) => {
    setCodeFont(family);
    setOpen(false);
  };
  const item = (family: string, label = family) => (
    <CommandItem key={family || "_system"} value={label} onSelect={() => pick(family)}>
      <span className="truncate" style={{ fontFamily: codeFontStack(family) }}>
        {label}
      </span>
      {current === family && <CheckIcon className="ml-auto" />}
    </CommandItem>
  );
  return (
    <div className="grid justify-items-end gap-2">
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger render={<button type="button" data-size="default" className={cn(selectTriggerClass, "max-w-56")} />}>
          <span className="truncate" style={{ fontFamily: codeFontStack() }}>
            {current || t("codeFont.system")}
          </span>
          <ChevronDownIcon className="text-muted-foreground size-4" />
        </PopoverTrigger>
        <PopoverContent className="w-72 p-0" align="end">
          <Command>
            <CommandInput placeholder={t("codeFont.search")} />
            <CommandList>
              <CommandEmpty>{t("codeFont.none")}</CommandEmpty>
              <CommandGroup heading={t("codeFont.common")}>
                {item("", t("codeFont.system"))}
                {present.map((f) => item(f))}
              </CommandGroup>
              {local && local.length > 0 && <CommandGroup heading={t("codeFont.installed")}>{local.filter((f) => !present.includes(f)).map((f) => item(f))}</CommandGroup>}
            </CommandList>
          </Command>
          {/* Asking for this computer's fonts takes the person's say-so, in Chrome's own prompt. */}
          {queryLocalFonts && !local?.length && (
            <div className="border-t p-1">
              <Button variant="ghost" size="sm" className="w-full justify-start" onClick={() => void load()}>
                <DownloadIcon data-icon="inline-start" />
                {t("codeFont.loadInstalled")}
              </Button>
            </div>
          )}
        </PopoverContent>
      </Popover>
      <code className="text-muted-foreground text-xs">{"fn main() { let v = [1, 2]; } // 0O1lI"}</code>
    </div>
  );
}
