import { Slider } from "@base-ui/react/slider";
import { CheckIcon, RotateCcwIcon } from "lucide-react";
import { useState } from "react";
import { Picker } from "@/components/picker";
import { Badge } from "@/components/reui/badge";
import { Row } from "@/components/section-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { selectTriggerClass } from "@/components/ui/select";
import { type MessageKey, useI18n } from "@/lib/i18n";
import { accentLowContrast, parseHex, radiusRange, readableForeground, setThemePrefs, type ThemePreset, themePresets, useThemePrefs } from "@/lib/settings";
import { cn } from "@/lib/utils";

const presetKey: Record<ThemePreset, MessageKey> = {
  neutral: "theme.preset.neutral",
  cool: "theme.preset.cool",
  warm: "theme.preset.warm",
  contrast: "theme.preset.contrast",
};

// A few common accents; any other comes from the hex field.
const swatches = ["#2563eb", "#7c3aed", "#db2777", "#e11d48", "#ea580c", "#d97706", "#16a34a", "#0d9488"];

/** Rows for Settings › Appearance: preset, accent, corner radius, a live preview and a reset. Goes inside a Card. */
export function ThemeEditor() {
  const { t } = useI18n();
  const prefs = useThemePrefs();
  return (
    <>
      <Row title={t("theme.preset")} description={t("theme.presetHelp")}>
        <Picker label={t("theme.preset")} value={prefs.preset} onChange={(preset) => setThemePrefs({ preset })} options={themePresets.map((p) => ({ value: p, label: t(presetKey[p]) }))} />
      </Row>
      <Row title={t("theme.accent")} description={t("theme.accentHelp")}>
        <AccentPicker value={prefs.accent} />
      </Row>
      <Row title={t("theme.radius")} description={t("theme.radiusHelp")}>
        <Slider.Root
          className="w-40"
          value={prefs.radius}
          onValueChange={(radius) => setThemePrefs({ radius })}
          min={radiusRange.min}
          max={radiusRange.max}
          step={radiusRange.step}
        >
          <Slider.Control className="flex h-5 w-full touch-none items-center select-none">
            <Slider.Track className="bg-muted relative h-1.5 w-full rounded-full">
              <Slider.Indicator className="bg-primary rounded-full" />
              <Slider.Thumb aria-label={t("theme.radius")} className="bg-background border-primary focus-visible:ring-ring/50 size-4 rounded-full border-2 shadow-sm outline-none focus-visible:ring-3" />
            </Slider.Track>
          </Slider.Control>
        </Slider.Root>
      </Row>
      <div className="grid gap-3 px-4 py-3">
        <div className="flex items-center justify-between gap-2">
          <span className="text-sm font-medium">{t("theme.preview")}</span>
          <Button variant="ghost" size="sm" onClick={() => setThemePrefs()}>
            <RotateCcwIcon />
            {t("theme.reset")}
          </Button>
        </div>
        {/* Real components drawn with the live tokens, so the preview cannot drift from the app. */}
        <div className="bg-background flex flex-wrap items-center gap-2 rounded-lg border p-3">
          <Button size="sm">{t("theme.previewPrimary")}</Button>
          <Button size="sm" variant="secondary">
            {t("theme.previewSecondary")}
          </Button>
          <Button size="sm" variant="outline">
            {t("theme.previewOutline")}
          </Button>
          <Badge variant="outline">{t("theme.previewBadge")}</Badge>
          <Input className="h-8 w-36" placeholder={t("theme.previewInput")} aria-label={t("theme.previewInput")} />
          <span className="text-muted-foreground text-xs">{t("theme.previewMuted")}</span>
        </div>
      </div>
    </>
  );
}

/** A swatch grid plus a hex field; "" is the preset's own primary. */
function AccentPicker({ value }: { value: string }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState(value);
  // Marked only once a commit fails, so a half-typed color isn't flagged mid-typing.
  const [tried, setTried] = useState(false);
  const invalid = tried && draft.trim() !== "" && !parseHex(draft);
  const pick = (accent: string) => {
    setThemePrefs({ accent });
    setDraft(accent);
  };
  // The field applies only a valid color; an invalid one stays marked and changes nothing.
  const commit = () => {
    setTried(true);
    const hex = parseHex(draft);
    if (hex) pick(hex);
    else if (draft.trim() === "") pick("");
  };
  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) {
          setDraft(value);
          setTried(false);
        }
      }}
    >
      <PopoverTrigger render={<button type="button" data-size="default" className={cn(selectTriggerClass, "w-40")} />}>
        <span className="flex items-center gap-2">
          <span className="bg-primary size-4 rounded-full border" />
          <span className="font-mono text-xs">{value || t("theme.accentDefault")}</span>
        </span>
      </PopoverTrigger>
      <PopoverContent align="end" className="grid w-60 gap-3">
        <div className="grid grid-cols-4 gap-2">
          {swatches.map((hex) => (
            <button
              key={hex}
              type="button"
              aria-label={hex}
              aria-pressed={value === hex}
              onClick={() => pick(hex)}
              className="focus-visible:ring-ring/50 flex h-8 items-center justify-center rounded-md border outline-none focus-visible:ring-3"
              style={{ backgroundColor: hex, color: readableForeground(hex) }}
            >
              {value === hex && <CheckIcon className="size-4" />}
            </button>
          ))}
        </div>
        <Input
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value);
            setTried(false);
          }}
          onBlur={commit}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.nativeEvent.isComposing) commit();
          }}
          placeholder="#2563eb"
          aria-label={t("theme.accentHex")}
          aria-invalid={invalid}
          className="font-mono"
          spellCheck={false}
        />
        {invalid && <span className="text-destructive text-xs">{t("theme.accentInvalid")}</span>}
        {value && accentLowContrast(value) && <span className="text-muted-foreground text-xs">{t("theme.accentLowContrast")}</span>}
        <Button variant="outline" size="sm" onClick={() => pick("")} disabled={value === ""}>
          {t("theme.accentDefault")}
        </Button>
      </PopoverContent>
    </Popover>
  );
}
