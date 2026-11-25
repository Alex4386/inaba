import type { DescService } from "@bufbuild/protobuf";
import { type Client, createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { AutomationService } from "./gen/inaba/v1/automation_pb";
import { McpService } from "./gen/inaba/v1/mcp_pb";
import { BrowserService } from "./gen/inaba/v1/browser_pb";
import { FileService } from "./gen/inaba/v1/files_pb";
import { ShellService } from "./gen/inaba/v1/shell_pb";
import { GitService, HousekeepingService, ModuleService, PeerService, ProjectService, TerminalService, ThreadService } from "./gen/inaba/v1/host_pb";
import { AdminService, AuthService, HostService, NotificationService } from "./gen/inaba/v1/server_pb";

const serverTransport = createConnectTransport({ baseUrl: location.origin });
export const auth = createClient(AuthService, serverTransport);
export const hosts = createClient(HostService, serverTransport);
export const admin = createClient(AdminService, serverTransport);
export const notifications = createClient(NotificationService, serverTransport);

/** A service of any host, through the server's relay: the admin panel and host settings reach hosts other than the one in use. */
export const onHost = <T extends DescService>(hostId: string, service: T) => createClient(service, createConnectTransport({ baseUrl: `${location.origin}/hosts/${hostId}` }));
export const housekeepingOf = (hostId: string) => onHost(hostId, HousekeepingService);

// Browsers cap a data channel message; the host uses the same limit.
const MAX_CHANNEL_MESSAGE = 16 * 1024;
const encoder = new TextEncoder();

function concat(a: Uint8Array, b: Uint8Array): Uint8Array<ArrayBuffer> {
  const out = new Uint8Array(a.length + b.length);
  out.set(a);
  out.set(b, a.length);
  return out;
}

/**
 * One API call over its own data channel, spoken as HTTP/1.0: the request goes out
 * as text, and the response body is everything until the host closes the channel.
 */
function channelFetch(pc: RTCPeerConnection, path: string, init: RequestInit = {}): Promise<Response> {
  return new Promise((resolve, reject) => {
    const dc = pc.createDataChannel("rpc");
    dc.binaryType = "arraybuffer";
    const body =
      init.body == null ? new Uint8Array() : typeof init.body === "string" ? encoder.encode(init.body) : new Uint8Array(init.body as ArrayBuffer);
    let head = `${init.method ?? "GET"} ${path} HTTP/1.0\r\nContent-Length: ${body.length}\r\n`;
    new Headers(init.headers).forEach((value, name) => {
      head += `${name}: ${value}\r\n`;
    });

    let pending = new Uint8Array();
    let stream: ReadableStreamDefaultController<Uint8Array> | undefined;
    const fail = (error: unknown) => {
      if (stream) stream.error(error);
      else reject(error);
      dc.close();
    };
    init.signal?.addEventListener("abort", () => fail(new DOMException("The call was cancelled.", "AbortError")));

    dc.onopen = () => {
      const request = concat(encoder.encode(head + "\r\n"), body);
      for (let i = 0; i < request.length; i += MAX_CHANNEL_MESSAGE) dc.send(request.subarray(i, i + MAX_CHANNEL_MESSAGE));
    };
    dc.onmessage = (event) => {
      const chunk = new Uint8Array(event.data as ArrayBuffer);
      if (stream) return stream.enqueue(chunk);
      pending = concat(pending, chunk);
      const text = new TextDecoder("latin1").decode(pending);
      const end = text.indexOf("\r\n\r\n");
      if (end < 0) return;
      const [statusLine, ...lines] = text.slice(0, end).split("\r\n");
      const headers = new Headers();
      for (const line of lines) headers.append(line.slice(0, line.indexOf(":")), line.slice(line.indexOf(":") + 1).trim());
      const rest = pending.subarray(end + 4);
      const bodyStream = new ReadableStream<Uint8Array>({
        start(controller) {
          stream = controller;
          if (rest.length) controller.enqueue(rest);
        },
        cancel: () => dc.close(),
      });
      resolve(new Response(bodyStream, { status: Number(statusLine.split(" ")[1]), headers }));
    };
    dc.onclose = () => {
      if (stream) {
        try {
          stream.close();
        } catch {
          // Already failed or cancelled.
        }
      } else reject(new TypeError("The direct connection closed before the host answered."));
    };
    dc.onerror = () => fail(new TypeError("The direct connection failed."));
  });
}

/**
 * A host's API. Calls go through the server's relay until a direct WebRTC connection
 * to the host is up, then over that; if it drops, they go back to the relay.
 */
export class HostLink {
  readonly threads: Client<typeof ThreadService>;
  readonly terminal: Client<typeof TerminalService>;
  readonly projects: Client<typeof ProjectService>;
  readonly git: Client<typeof GitService>;
  readonly automations: Client<typeof AutomationService>;
  readonly modules: Client<typeof ModuleService>;
  readonly mcp: Client<typeof McpService>;
  readonly browser: Client<typeof BrowserService>;
  readonly files: Client<typeof FileService>;
  readonly shell: Client<typeof ShellService>;
  /** Whether calls currently go straight to the host. */
  direct = false;

  private pc: RTCPeerConnection | undefined;
  private closed = false;
  private retry: ReturnType<typeof setTimeout> | undefined;
  private readonly prefix: string;

  constructor(
    readonly hostId: string,
    private readonly iceUrls: string[],
    private readonly onChange: () => void,
  ) {
    this.prefix = `/hosts/${hostId}`;
    const transport = createConnectTransport({
      baseUrl: location.origin + this.prefix,
      fetch: (input, init) => {
        const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url);
        return this.direct && this.pc ? channelFetch(this.pc, url.pathname.slice(this.prefix.length) + url.search, init) : fetch(input, init);
      },
    });
    this.threads = createClient(ThreadService, transport);
    this.terminal = createClient(TerminalService, transport);
    this.projects = createClient(ProjectService, transport);
    this.git = createClient(GitService, transport);
    this.automations = createClient(AutomationService, transport);
    this.modules = createClient(ModuleService, transport);
    this.mcp = createClient(McpService, transport);
    this.browser = createClient(BrowserService, transport);
    this.files = createClient(FileService, transport);
    this.shell = createClient(ShellService, transport);
    void this.connect();
  }

  close() {
    this.closed = true;
    clearTimeout(this.retry);
    this.pc?.close();
  }

  private async connect() {
    if (this.closed) return;
    const pc = new RTCPeerConnection({ iceServers: this.iceUrls.map((urls) => ({ urls })) });
    this.pc = pc;
    const lost = () => {
      if (this.pc !== pc) return;
      this.pc = undefined;
      pc.close();
      if (this.direct) {
        this.direct = false;
        this.onChange();
      }
      if (!this.closed) this.retry = setTimeout(() => void this.connect(), 30_000);
    };
    pc.onconnectionstatechange = () => {
      if (pc.connectionState === "connected" && !this.direct) {
        this.direct = true;
        this.onChange();
      } else if (pc.connectionState === "failed" || pc.connectionState === "disconnected" || pc.connectionState === "closed") lost();
    };
    try {
      // A channel must exist for the offer to ask for data channels at all.
      pc.createDataChannel("init");
      await pc.setLocalDescription(await pc.createOffer());
      // The host takes one complete offer, so wait until every candidate is in it.
      if (pc.iceGatheringState !== "complete") {
        await new Promise<void>((done) => {
          pc.onicegatheringstatechange = () => pc.iceGatheringState === "complete" && done();
          setTimeout(done, 5_000);
        });
      }
      // The offer itself always travels through the relay.
      const relay = createClient(PeerService, createConnectTransport({ baseUrl: location.origin + this.prefix }));
      const answer = await relay.connect({ offerSdp: pc.localDescription!.sdp, iceUrls: this.iceUrls });
      await pc.setRemoteDescription({ type: "answer", sdp: answer.answerSdp });
    } catch {
      lost();
    }
  }
}
