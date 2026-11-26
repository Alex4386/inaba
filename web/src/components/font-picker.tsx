import { CheckIcon, ChevronDownIcon, DownloadIcon } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { selectTriggerClass } from "@/components/ui/select";
import { showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { builtInCodeFonts, builtInUiFonts, codeFont, codeFontStack, setCodeFont, setUiFont, uiFont, uiFontStack, useCodeFont, useUiFont } from "@/lib/settings";
import { cn } from "@/lib/utils";

// Families people often have, for code and for the UI; any that is missing is left out.
const commonCode = ["SF Mono", "Menlo", "Monaco", "JetBrains Mono", "Fira Code", "Cascadia Code", "Source Code Pro", "IBM Plex Mono", "Consolas", "Ubuntu Mono", "Hack"];
const commonUi = ["Inter", "Noto Sans KR", "Apple SD Gothic Neo", "Malgun Gothic", "Segoe UI", "Helvetica Neue", "Roboto", "Noto Sans"];

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

/** Picks the font code is shown in: JetBrains Mono, which inaba carries, or one of the common ones, or where the browser lets it, any installed on this computer. */
export function CodeFontPicker() {
  const { t } = useI18n();
  useCodeFont();
  return (
    <FontPicker
      current={codeFont()}
      stack={codeFontStack}
      set={setCodeFont}
      builtIn={builtInCodeFonts.map((f) => ({ family: f.family, label: f.label ? t("uiFont.builtIn", { name: f.label }) : t("codeFont.system") }))}
      common={commonCode}
      sample={"fn main() { let v = [1, 2]; } // 0O1lI"}
    />
  );
}

/** Picks the font the rest of the UI is shown in: Pretendard, which inaba carries, or another. */
export function UiFontPicker() {
  const { t } = useI18n();
  useUiFont();
  return (
    <FontPicker
      current={uiFont()}
      stack={uiFontStack}
      set={setUiFont}
      builtIn={builtInUiFonts.map((f) => ({ family: f.family, label: f.label ? t("uiFont.builtIn", { name: f.label }) : t("uiFont.system") }))}
      common={commonUi}
      sample={t("uiFont.sample")}
    />
  );
}

function FontPicker({
  current,
  stack,
  set,
  builtIn,
  common,
  sample,
}: {
  current: string;
  stack: (family?: string) => string;
  set: (family: string) => void;
  /** Choices that need nothing installed: built in, or the system's own. */
  builtIn: { family: string; label: string }[];
  common: string[];
  sample: string;
}) {
  const { t } = useI18n();
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
    set(family);
    setOpen(false);
  };
  const item = (family: string, label = family) => (
    <CommandItem key={family || "_default"} value={label} onSelect={() => pick(family)}>
      <span className="truncate" style={{ fontFamily: stack(family) }}>
        {label}
      </span>
      {current === family && <CheckIcon className="ml-auto" />}
    </CommandItem>
  );
  const currentLabel = builtIn.find((f) => f.family === current)?.label ?? current;
  return (
    <div className="grid justify-items-end gap-2">
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger render={<button type="button" data-size="default" className={cn(selectTriggerClass, "max-w-56")} />}>
          <span className="truncate" style={{ fontFamily: stack() }}>
            {currentLabel}
          </span>
          <ChevronDownIcon className="text-muted-foreground size-4" />
        </PopoverTrigger>
        <PopoverContent className="w-72 p-0" align="end">
          <Command>
            <CommandInput placeholder={t("codeFont.search")} />
            <CommandList>
              <CommandEmpty>{t("codeFont.none")}</CommandEmpty>
              <CommandGroup heading={t("codeFont.common")}>
                {builtIn.map((f) => item(f.family, f.label))}
                {present.map((f) => item(f))}
              </CommandGroup>
              {local && local.length > 0 && <CommandGroup heading={t("codeFont.installed")}>{local.filter((f) => !present.includes(f) && !builtIn.some((b) => b.family === f)).map((f) => item(f))}</CommandGroup>}
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
      <span className="text-muted-foreground text-xs" style={{ fontFamily: stack() }}>
        {sample}
      </span>
    </div>
  );
}
