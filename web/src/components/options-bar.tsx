import { BotIcon, CheckIcon, ChevronDownIcon, ChevronUpIcon, SearchIcon, SquareTerminalIcon, StarIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { Picker } from "@/components/picker";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { selectTriggerClass } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import type { SessionOption } from "@/gen/inaba/v1/host_pb";
import { useI18n } from "@/lib/i18n";
import { bindingOf, formatBinding, matches, useKeybindings } from "@/lib/keybindings";
import { cn } from "@/lib/utils";

// The options people reach for most come first; the agent's own order breaks ties.
const optionOrder = ["model", "thought_level", "mode"];

/** A provider the model picker can switch to, before a thread exists. */
export type PickerProvider = { id: string; name: string; icon?: string; /** A plain terminal rather than an agent. */ terminal?: boolean };

const FAVORITES_KEY = "inaba.favoriteModels";
function loadFavorites(): string[] {
  try {
    return JSON.parse(localStorage.getItem(FAVORITES_KEY) ?? "[]");
  } catch {
    return [];
  }
}

/** How many models show before "Show more". */
const FIRST = 4;

/**
 * The model, its reasoning effort, the switches that go with it and the access mode, in
 * one place: providers down the side, models in the middle,
 * reasoning beside them, and the rest along the bottom.
 */
function ModelPicker({
  model,
  effort,
  switches,
  mode,
  onSet,
  providers,
  provider,
  onProvider,
  field = false,
  busy = false,
}: {
  /** Shown as a field in a settings row, like a select, rather than by the composer. */
  field?: boolean;
  /** A change made here is still on its way. */
  busy?: boolean;
  /** None for an agent with no models to pick, or a plain terminal: the picker still switches providers. */
  model?: SessionOption;
  effort?: SessionOption;
  switches: SessionOption[];
  mode?: SessionOption;
  onSet: (optionId: string, value: string) => unknown;
  providers?: PickerProvider[];
  provider?: string;
  onProvider?: (id: string) => unknown;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [more, setMore] = useState(false);
  const [onlyFavorites, setOnlyFavorites] = useState(false);
  // Favorites are one's own, so they stay in this browser.
  const [favorites, setFavorites] = useState(loadFavorites);
  const toggleFavorite = (value: string) => {
    const next = favorites.includes(value) ? favorites.filter((v) => v !== value) : [...favorites, value];
    setFavorites(next);
    try {
      localStorage.setItem(FAVORITES_KEY, JSON.stringify(next));
    } catch {
      // A browser that keeps nothing still picks models; it just forgets the stars.
    }
  };
  const q = query.trim().toLowerCase();
  const choices = model?.choices ?? [];
  const matching = choices.filter((c) => (!onlyFavorites || favorites.includes(c.value)) && (!q || c.name.toLowerCase().includes(q) || c.value.toLowerCase().includes(q)));
  const listed = more || q || onlyFavorites ? matching : matching.slice(0, FIRST);
  const pick = (value: string) => {
    if (model) onSet(model.id, value);
    setOpen(false);
  };
  // The model shortcuts (⌘1 to ⌘4 unless changed) pick the models shown first while the picker is open.
  useKeybindings();
  useEffect(() => {
    if (!open) return;
    const key = (e: KeyboardEvent) => {
      const i = listed.slice(0, FIRST).findIndex((_, i) => matches(e, `model.${i + 1}`));
      if (i >= 0) {
        e.preventDefault();
        pick(listed[i].value);
      }
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  });
  const current = choices.find((c) => c.value === model?.value);
  const shownProvider = providers?.find((p) => p.id === provider);
  const currentEffort = effort?.choices.find((c) => c.value === effort.value);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      {/* In a settings row it looks like the selects beside it and opens below; by the composer it opens above. */}
      <PopoverTrigger
        render={field ? <button type="button" data-size="default" className={cn(selectTriggerClass, "max-w-64")} /> : <Button type="button" size="sm" variant="ghost" className="max-w-64" />}
      >
        {busy ? <Spinner /> : !model && shownProvider && (shownProvider.terminal ? <SquareTerminalIcon /> : <BotIcon />)}
        <span className="truncate">{current?.name ?? model?.value ?? shownProvider?.name}</span>
        {currentEffort && effort?.value !== "default" && <span className="text-muted-foreground">{currentEffort.name}</span>}
        {field ? <ChevronDownIcon className="text-muted-foreground size-4" /> : <ChevronUpIcon data-icon="inline-end" />}
      </PopoverTrigger>
      <PopoverContent className="w-[min(36rem,calc(100vw-2rem))] gap-0 overflow-hidden p-0" align="end" side={field ? "bottom" : "top"}>
        <div className="flex min-h-64">
          {/* Favorites, then the providers to switch to while no thread exists yet. */}
          <div className="flex flex-col items-center gap-1 border-r p-1.5">
            <Button type="button" size="icon-sm" variant={onlyFavorites ? "secondary" : "ghost"} aria-pressed={onlyFavorites} aria-label={t("picker.favorites")} title={t("picker.favorites")} onClick={() => setOnlyFavorites((v) => !v)}>
              <StarIcon className={cn(onlyFavorites && "fill-current")} />
            </Button>
            {providers?.map((p) => (
              <Button
                key={p.id}
                type="button"
                size="icon-sm"
                variant={p.id === provider ? "secondary" : "ghost"}
                aria-pressed={p.id === provider}
                aria-label={p.name}
                title={p.name}
                disabled={busy}
                onClick={() => {
                  // A provider shows all its models, leaving the favorites view.
                  setOnlyFavorites(false);
                  onProvider?.(p.id);
                }}
              >
                {p.icon ? <img src={p.icon} alt="" className={cn("size-4 rounded-sm object-cover", !p.icon.startsWith("data:") && "dark:invert")} /> : p.terminal ? <SquareTerminalIcon /> : <BotIcon />}
              </Button>
            ))}
          </div>
          <div className="grid max-h-[min(24rem,60svh)] min-w-0 flex-1 content-start gap-1 overflow-y-auto p-2">
            <div className={cn("bg-popover sticky top-0 z-10", !model && "hidden")}>
              <SearchIcon className="text-muted-foreground absolute top-1/2 left-2 size-4 -translate-y-1/2" />
              <input
                autoFocus
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder={t("picker.search")}
                className="focus-visible:border-ring h-8 w-full rounded-md border-b border-transparent bg-transparent pl-8 text-sm outline-none"
              />
            </div>
            {listed.length === 0 && (
              <p className="text-muted-foreground px-2 py-3 text-sm">
                {t(!model ? (shownProvider?.terminal ? "picker.terminal" : "picker.noModels") : onlyFavorites && favorites.length === 0 ? "picker.noFavorites" : "picker.none")}
              </p>
            )}
            {listed.map((c, i) => (
              <div key={c.value} className={cn("hover:bg-muted group flex items-center gap-2 rounded-md px-2", c.value === model?.value && "bg-muted")}>
                <button type="button" className="flex min-w-0 flex-1 items-center gap-2 py-1.5 text-left text-sm" title={c.description || c.value} onClick={() => pick(c.value)}>
                  <span className="truncate">{c.name}</span>
                  {c.value === model?.value && <CheckIcon className="size-3.5 shrink-0" />}
                </button>
                <button
                  type="button"
                  aria-label={t("picker.favorite", { name: c.name })}
                  aria-pressed={favorites.includes(c.value)}
                  className={cn("text-muted-foreground hover:text-foreground opacity-0 group-hover:opacity-100", favorites.includes(c.value) && "opacity-100")}
                  onClick={() => toggleFavorite(c.value)}
                >
                  <StarIcon className={cn("size-3.5", favorites.includes(c.value) && "fill-current")} />
                </button>
                {i < FIRST && !q && !onlyFavorites && <kbd className="text-muted-foreground bg-background rounded border px-1 font-sans text-[10px]">{formatBinding(bindingOf(`model.${i + 1}`))}</kbd>}
              </div>
            ))}
            {!more && !q && !onlyFavorites && matching.length > FIRST && (
              <button type="button" className="text-muted-foreground hover:text-foreground px-2 py-1.5 text-left text-sm" onClick={() => setMore(true)}>
                {t("picker.showMore")}
              </button>
            )}
          </div>
          {effort && (
            <div className="grid w-44 content-start gap-0.5 border-l p-2">
              <div className="text-muted-foreground flex min-w-0 items-center justify-between gap-2 px-2 py-1.5 text-xs">
                <span className="shrink-0">{effort.name}</span>
                <span className="min-w-0 truncate">{current?.name}</span>
              </div>
              {effort.choices.map((c) => (
                <button key={c.value} type="button" className="hover:bg-muted flex items-center rounded-md px-2 py-1.5 text-left text-sm" title={c.description} onClick={() => onSet(effort.id, c.value)}>
                  <span className="flex-1">{c.name}</span>
                  {c.value === effort.value && <CheckIcon className="size-3.5" />}
                </button>
              ))}
            </div>
          )}
        </div>
        {(switches.length > 0 || mode) && (
          <div className="bg-muted/30 flex flex-wrap items-center gap-x-4 gap-y-2 border-t px-3 py-2">
            {/* Switches look like switches, so an off one does not read as a label for what sits beside it. */}
            {switches.map((o) => (
              <label key={o.id} className="flex items-center gap-2 text-sm" title={o.description || o.name}>
                <Switch size="sm" checked={o.value === "true"} onCheckedChange={(on) => onSet(o.id, on ? "true" : "false")} />
                {o.name}
              </label>
            ))}
            {mode && (
              <div className="ml-auto flex items-center gap-1 text-sm">
                <span className="text-muted-foreground">{mode.name}</span>
                <Picker
                  size="sm"
                  className="border-0 shadow-none"
                  label={mode.name}
                  value={mode.value}
                  onChange={(value) => onSet(mode.id, value)}
                  options={mode.choices.map((c) => ({ value: c.value, label: c.name }))}
                />
              </div>
            )}
          </div>
        )}
      </PopoverContent>
    </Popover>
  );
}

/** The settings the agent offers for this thread: its model, its mode, and whatever else it has. */
export function OptionsBar({
  options: given,
  onSet,
  providers,
  provider,
  onProvider,
  field = false,
}: {
  options: SessionOption[];
  /** May return a promise; the picked value shows until it settles. */
  onSet: (optionId: string, value: string) => unknown;
  /** Providers to switch between, offered in the model picker before a thread exists. */
  providers?: PickerProvider[];
  provider?: string;
  onProvider?: (id: string) => unknown;
  /** In a settings row: the picker looks like the selects beside it. */
  field?: boolean;
}) {
  // What was just picked shows at once, until the agent has taken it, so the control does not snap back meanwhile.
  const [pending, setPending] = useState<Record<string, string>>({});
  const [switching, setSwitching] = useState<string>();
  const set = (id: string, value: string) => {
    setPending((p) => ({ ...p, [id]: value }));
    void Promise.resolve(onSet(id, value)).finally(() =>
      setPending((p) => {
        // A later pick of the same option is still on its way, and keeps showing.
        if (p[id] !== value) return p;
        const { [id]: _, ...rest } = p;
        return rest;
      }),
    );
  };
  const switchTo =
    onProvider &&
    ((id: string) => {
      setSwitching(id);
      void Promise.resolve(onProvider(id)).finally(() => setSwitching(undefined));
    });
  const options = given.map((o) => (o.id in pending ? { ...o, value: pending[o.id] } : o));
  const rank = (o: SessionOption) => optionOrder.indexOf(o.category) + 1 || optionOrder.length + 1;
  const model = options.find((o) => o.category === "model" && !o.boolean);
  // With a model to pick, its effort, its switches (speed among them) and the access mode go in its picker.
  const isSwitch = (o: SessionOption) => o.boolean && (o.category === "model_config" || o.category === "speed");
  const inPicker = (o: SessionOption) => Boolean(model) && (o === model || (o.category === "thought_level" && !o.boolean) || isSwitch(o) || (o.category === "mode" && !o.boolean));
  const rest = options.filter((o) => !inPicker(o));
  return (
    <div className="flex min-w-0 flex-wrap items-center justify-end gap-1.5">
      {/* One picker for every provider, models or not, so switching agents always looks the same. */}
      {(model || providers) && (
        <ModelPicker
          model={model}
          effort={model && options.find((o) => o.category === "thought_level" && !o.boolean)}
          switches={model ? options.filter(isSwitch) : []}
          mode={model && options.find((o) => o.category === "mode" && !o.boolean)}
          onSet={set}
          providers={providers}
          provider={switching ?? provider}
          onProvider={switchTo}
          field={field}
          busy={switching !== undefined || Object.keys(pending).length > 0}
        />
      )}
      {[...rest]
        .sort((a, b) => rank(a) - rank(b))
        .map((option) =>
          option.boolean ? (
            <Button
              key={option.id}
              type="button"
              size="sm"
              variant={option.value === "true" ? "secondary" : "ghost"}
              aria-pressed={option.value === "true"}
              title={option.description || option.name}
              disabled={option.id in pending}
              onClick={() => set(option.id, option.value === "true" ? "false" : "true")}
            >
              {option.id in pending && <Spinner data-icon="inline-start" />}
              {option.name}
            </Button>
          ) : (
            <Picker
              key={option.id}
              size="sm"
              className="border-0 shadow-none"
              label={option.name}
              value={option.value}
              onChange={(value) => set(option.id, value)}
              options={option.choices.map((c) => ({ value: c.value, label: c.name }))}
            />
          ),
        )}
    </div>
  );
}
