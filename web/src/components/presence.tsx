import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { createClient } from "@connectrpc/connect";
import { EyeIcon } from "lucide-react";
import { createConnectTransport } from "@connectrpc/connect-web";
import { Button } from "@/components/ui/button";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { type Presence, PresenceService } from "@/gen/inaba/v1/server_pb";
import { useI18n } from "@/lib/i18n";
import { closeNotifications } from "@/lib/push";
import { cn } from "@/lib/utils";

const presence = createClient(PresenceService, createConnectTransport({ baseUrl: location.origin }));

/** This tab's own id, so one person in two tabs counts as two places; kept across reloads, so a reloaded tab keeps its driver seat. */
function clientId() {
  try {
    let id = sessionStorage.getItem("inaba.client");
    if (!id) {
      id = crypto.randomUUID();
      sessionStorage.setItem("inaba.client", id);
    }
    return id;
  } catch {
    return crypto.randomUUID();
  }
}

/** This browser tab's id, as presence and a thread's driver know it. */
export let CLIENT = clientId();

// A duplicated tab copies the one it came from, id and all; it asks the other tabs at start and takes
// a new id if one already has it, so the tab opened first stays the thread's driver and the copy watches.
const PROBE_MS = 150;
const ready = new Promise<void>((resolve) => {
  let probing = true;
  try {
    const channel = new BroadcastChannel("inaba.client");
    channel.onmessage = (e: MessageEvent<{ ask?: string; mine?: string }>) => {
      if (e.data.ask === CLIENT) channel.postMessage({ mine: CLIENT });
      else if (probing && e.data.mine === CLIENT) {
        CLIENT = crypto.randomUUID();
        try {
          sessionStorage.setItem("inaba.client", CLIENT);
        } catch {
          // This tab keeps the new id for as long as it is open.
        }
      }
    };
    channel.postMessage({ ask: CLIENT });
  } catch {
    // Without BroadcastChannel, a duplicated tab shares its id; rare enough.
  }
  setTimeout(() => {
    probing = false;
    resolve();
  }, PROBE_MS);
});

/** Who drives the open thread, as the server last said, and when that question was sent. */
export type Seat = { driver: string; sentAt: number };

type Present = {
  people: Presence[];
  /** The other browser tabs on the open thread, one's own included; undefined until the first answer for it. */
  clients?: string[];
  seat?: Seat;
  /** Asks for the open thread's driver seat, from the holder this browser saw ("" for a free seat). */
  take: (from: string) => Promise<Seat | undefined>;
  /** Gives up the seat this browser holds. */
  release: () => Promise<Seat | undefined>;
  /** An administrator hands the seat, still held by from, to another browser on the thread. */
  give: (to: string, from: string) => Promise<Seat | undefined>;
  /** Asks again now rather than at the next beat. */
  refresh: () => void;
};

/** Says where this tab is every few seconds, and keeps who else is on the host and who drives the thread or host terminal. */
export function usePresence(hostId: string | undefined, threadId: string | undefined, projectId: string | undefined, terminalId?: string): Present {
  const [people, setPeople] = useState<Presence[]>([]);
  const [clients, setClients] = useState<string[]>();
  const [seat, setSeat] = useState<Seat>();
  // Somewhere new starts over: the old place's browsers say nothing about the new one.
  useEffect(() => {
    setClients(undefined);
    setSeat(undefined);
  }, [hostId, threadId, terminalId]);
  // The heartbeat as it is now, for calls made between renders.
  type Ask = { take?: boolean; from?: string; release?: boolean; giveTo?: string };
  const beatRef = useRef<(ask?: Ask) => Promise<Seat | undefined>>(async () => undefined);
  useEffect(() => {
    if (!hostId) {
      setPeople([]);
      beatRef.current = async () => undefined;
      return;
    }
    let live = true;
    const beat = (ask: Ask = {}) => {
      const sentAt = performance.now();
      const visible = document.visibilityState === "visible";
      return presence.heartbeat({ clientId: CLIENT, hostId, threadId: threadId ?? "", projectId: projectId ?? "", terminalId: terminalId ?? "", visible, ...ask }).then(
        (r) => {
          if (!live) return undefined;
          const next = { driver: r.driver, sentAt };
          setPeople(r.present);
          setClients(r.clients);
          setSeat(next);
          return next;
        },
        () => undefined,
      );
    };
    beatRef.current = (ask) => ready.then(() => beat(ask));
    void beatRef.current();
    const timer = setInterval(() => void beatRef.current(), 10_000);
    // A closed or left page says so at once, so its driver seat frees without waiting for presence to forget it.
    const gone = () => navigator.sendBeacon("/v1/presence", new Blob([JSON.stringify({ clientId: CLIENT, hostId })], { type: "application/json" }));
    window.addEventListener("pagehide", gone);
    // Hidden or shown again says so at once: pushes about this thread are held back only while it is on screen.
    // Hiding is sent right away with keepalive, since a page going to the background may be frozen before
    // a normal request leaves; it is not waited on, as nothing on the page changes for it.
    const shown = () => {
      if (document.visibilityState === "visible") {
        void beatRef.current();
        if (threadId) void closeNotifications(threadId).catch(() => {});
        return;
      }
      const body = JSON.stringify({ clientId: CLIENT, hostId, threadId: threadId ?? "", projectId: projectId ?? "", terminalId: terminalId ?? "", visible: false });
      void fetch("/inaba.v1.PresenceService/Heartbeat", { method: "POST", keepalive: true, credentials: "include", headers: { "Content-Type": "application/json" }, body }).catch(() => {});
    };
    if (threadId && document.visibilityState === "visible") void closeNotifications(threadId).catch(() => {});
    document.addEventListener("visibilitychange", shown);
    return () => {
      live = false;
      clearInterval(timer);
      window.removeEventListener("pagehide", gone);
      document.removeEventListener("visibilitychange", shown);
    };
  }, [hostId, threadId, projectId, terminalId]);
  const take = useCallback((from: string) => beatRef.current({ take: true, from }), []);
  const release = useCallback(() => beatRef.current({ release: true }), []);
  const give = useCallback((giveTo: string, from: string) => beatRef.current({ giveTo, from }), []);
  const refresh = useCallback(() => void beatRef.current(), []);
  return useMemo(() => ({ people, clients, seat, take, release, give, refresh }), [people, clients, seat, take, release, give, refresh]);
}

/** Who else is on the host, for the parts of the page that show them. */
export const PresenceContext = createContext<Present>({ people: [], take: async () => undefined, release: async () => undefined, give: async () => undefined, refresh: () => {} });
export const usePeople = () => useContext(PresenceContext).people;
export const usePresent = () => useContext(PresenceContext);

/** A color of a person's own, from their id, so the same person looks the same everywhere. */
const hue = (id: string) => [...id].reduce((h, c) => (h * 31 + c.charCodeAt(0)) % 360, 0);

/** A person as their picture, or their initials on a color of their own. */
export function UserAvatar({ id, name, url, className, size }: { id: string; name: string; url?: string; className?: string; size?: "sm" | "default" | "lg" }) {
  return (
    <Avatar className={className} size={size}>
      {url && <AvatarImage src={url} alt="" className="object-cover" />}
      <AvatarFallback className="text-white" style={{ backgroundColor: `oklch(0.6 0.14 ${hue(id)})` }}>
        {name.slice(0, 2).toUpperCase()}
      </AvatarFallback>
    </Avatar>
  );
}

/** People as overlapping avatars, with their names in a tooltip. Each person shows once. */
export function PeopleStack({ people, max = 3, size = "sm", className, label = "" }: { people: Presence[]; max?: number; size?: "sm" | "xs"; className?: string; /** Said before the names in the tooltip. */ label?: string }) {
  const unique = [...new Map(people.map((p) => [p.userId, p])).values()];
  if (unique.length === 0) return null;
  const shown = unique.slice(0, max);
  const dim = size === "xs" ? "size-4 text-[0.5rem]" : "size-6 text-[0.6rem]";
  return (
    <Tooltip>
      <TooltipTrigger render={<span className={cn("flex shrink-0 items-center -space-x-1.5", className)} aria-label={unique.map((p) => p.name).join(", ")} />}>
        {shown.map((p) => (
          <UserAvatar key={p.userId} id={p.userId} name={p.name} url={p.avatarUrl} className={cn("ring-background ring-2", dim)} />
        ))}
        {unique.length > max && <span className={cn("bg-muted text-muted-foreground ring-background flex items-center justify-center rounded-full ring-2", dim)}>+{unique.length - max}</span>}
      </TooltipTrigger>
      <TooltipContent>{label + unique.map((p) => p.name).join(", ")}</TooltipContent>
    </Tooltip>
  );
}

/** Watching rather than driving: said over what is watched, with the way in when there is one. Sits in a relative parent, below its header. */
export function WatchingBanner({ driver, holder, onTake }: { driver?: string; /** Whoever else holds the seat; none for oneself in another tab. */ holder?: Presence; onTake?: () => void }) {
  const { t } = useI18n();
  return (
    <div className="pointer-events-none absolute inset-x-0 top-14 z-10 flex justify-center px-3">
      <div role="status" className="pointer-events-auto flex max-w-full items-center gap-2 rounded-2xl border border-amber-500/40 bg-amber-100/95 py-1 pr-1 pl-3 text-sm text-amber-950 shadow-sm dark:bg-amber-950/90 dark:text-amber-100">
        <EyeIcon className="size-4 shrink-0" />
        <span className="py-0.5">{!driver ? t("drive.bannerFree") : holder ? t("drive.bannerOther", { name: holder.name }) : t("drive.bannerOwnTab")}</span>
        {onTake && (
          <Button size="xs" variant="outline" className="border-amber-500/50 bg-transparent" onClick={onTake}>
            {t(driver ? "drive.take" : "drive.claim")}
          </Button>
        )}
      </div>
    </div>
  );
}
