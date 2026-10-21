// @frostyard/lodge-client/rpc: JSON-RPC 2.0 over an app's /api WebSocket
// through lodge's shell, and the client of it — the TypeScript counterpart
// of the kit's kit/jsonrpc ([ADR-0012](../../docs/adr/0012-the-kit-shares-the-rpc-runtime-not-the-generator.md),
// docs/specs/lodge-client.md#the-rpc-subpath). It has no runtime
// dependencies, and it ships as source, like the rest of the library.

/** EventEnvelope is the params of the kit's "event" notification. */
export interface EventEnvelope {
  /** A durable event's sequence number, when the app numbers its events. */
  seq?: number;
  /** Its name, such as "items.changed". */
  event: string;
  /** Its data, shaped by the app. */
  data?: unknown;
}

/** Transport is what the app's client calls: a call, events, and a close. */
export interface Transport {
  /** call sends one request and answers its result. */
  call<T>(method: string, params: unknown): Promise<T>;
  /** onEvent hears each event; it returns the unsubscribe. */
  onEvent(handler: (event: EventEnvelope) => void): () => void;
  /** onClose hears a connection's end; it returns the unsubscribe. */
  onClose(handler: (reason: string) => void): () => void;
  /**
   * blobURL is where an app's file loads from by its hash and extension,
   * when the transport serves one.
   */
  blobURL?(hash: string, ext: string): string;
}

/**
 * CodeNotConnected is a call's error while the socket is down. It is the
 * client's own code; the JSON-RPC range starts at -32768.
 */
export const CodeNotConnected = -32001;

/** RPCError is a JSON-RPC error from the app's daemon. */
export class RPCError extends Error {
  readonly code: number;
  readonly data: unknown;

  constructor(code: number, message: string, data?: unknown) {
    super(message);
    this.name = "RPCError";
    this.code = code;
    this.data = data;
  }
}

interface Pending {
  resolve: (value: unknown) => void;
  reject: (reason: Error) => void;
}

interface Incoming {
  id?: number | string | null;
  method?: string;
  params?: unknown;
  result?: unknown;
  error?: { code: number; message: string; data?: unknown };
}

/** EVENT_METHOD is the kit's notification method. */
export const EVENT_METHOD = "event";

/** SessionOptions configure a session. */
export interface SessionOptions {
  /** The URL an app's file loads from by its hash; see Transport.blobURL. */
  blobURL?: (hash: string, ext: string) => string;
}

/**
 * JsonRpcSession implements Transport over a line channel: the caller
 * supplies how to send a line, and feeds each received line to receive().
 */
export class JsonRpcSession implements Transport {
  readonly blobURL?: (hash: string, ext: string) => string;
  private nextId = 0;
  private readonly pending = new Map<number, Pending>();
  private readonly eventHandlers = new Set<(event: EventEnvelope) => void>();
  private readonly closeHandlers = new Set<(reason: string) => void>();
  private closedReason: string | undefined;
  private readonly sendLine: (line: string) => Promise<void>;

  constructor(sendLine: (line: string) => Promise<void>, options: SessionOptions = {}) {
    this.sendLine = sendLine;
    if (options.blobURL !== undefined) this.blobURL = options.blobURL;
  }

  call<T>(method: string, params: unknown): Promise<T> {
    if (this.closedReason !== undefined) {
      return Promise.reject(new RPCError(CodeNotConnected, `the connection closed: ${this.closedReason}`));
    }
    const id = ++this.nextId;
    const line = JSON.stringify({ jsonrpc: "2.0", id, method, params: params ?? {} });
    return new Promise<T>((resolve, reject) => {
      this.pending.set(id, { resolve: resolve as (value: unknown) => void, reject });
      this.sendLine(line).catch((err: unknown) => {
        this.pending.delete(id);
        reject(err instanceof Error ? err : new Error(String(err)));
      });
    });
  }

  onEvent(handler: (event: EventEnvelope) => void): () => void {
    this.eventHandlers.add(handler);
    return () => this.eventHandlers.delete(handler);
  }

  onClose(handler: (reason: string) => void): () => void {
    if (this.closedReason !== undefined) {
      handler(this.closedReason);
      return () => {};
    }
    this.closeHandlers.add(handler);
    return () => this.closeHandlers.delete(handler);
  }

  /** receive handles one line from the daemon. */
  receive(line: string): void {
    let msg: Incoming;
    try {
      msg = JSON.parse(line) as Incoming;
    } catch {
      this.close(`a malformed message: ${line.slice(0, 120)}`);
      return;
    }
    if (msg.id === undefined || msg.id === null) {
      if (msg.method === EVENT_METHOD && msg.params !== undefined) this.event(msg.params);
      return;
    }
    if (typeof msg.id !== "number") return;
    const pending = this.pending.get(msg.id);
    if (pending === undefined) return;
    this.pending.delete(msg.id);
    if (msg.error !== undefined) pending.reject(new RPCError(msg.error.code, msg.error.message, msg.error.data));
    else pending.resolve(msg.result ?? null);
  }

  /** close fails the pending calls and tells the close handlers; idempotent. */
  close(reason: string): void {
    if (this.closedReason !== undefined) return;
    this.closedReason = reason;
    for (const pending of this.pending.values()) {
      pending.reject(new RPCError(CodeNotConnected, `the connection closed: ${reason}`));
    }
    this.pending.clear();
    for (const handler of this.closeHandlers) handler(reason);
    this.closeHandlers.clear();
  }

  /** event hands an envelope to the event handlers; anything else is dropped. */
  private event(params: unknown): void {
    if (typeof params !== "object" || params === null) return;
    const envelope = params as EventEnvelope;
    if (typeof envelope.event !== "string") return;
    for (const handler of this.eventHandlers) handler(envelope);
  }
}

/** VisibilityDocument is the part of a document a connection reads. */
export interface VisibilityDocument {
  readonly visibilityState: DocumentVisibilityState;
  addEventListener(type: "visibilitychange", listener: () => void): void;
  removeEventListener(type: "visibilitychange", listener: () => void): void;
}

/** LodgeAPI is an app's API through lodge: a Transport whose socket it keeps. */
export interface LodgeAPI extends Transport {
  /** ready is whether the socket is open, and ready when a hello was given. */
  readonly ready: boolean;
  /** connect opens the socket now, unless one is open. */
  connect(): void;
  /** close closes the socket and stops reconnecting. */
  close(): void;
}

/** LodgeAPIOptions tune the connection; tests give fakes. */
export interface LodgeAPIOptions {
  /** The document whose visibility gates the heartbeat; the page's by default. */
  document?: VisibilityDocument;
  /** The WebSocket constructor; the browser's by default. */
  webSocket?: typeof WebSocket;
  /**
   * The rpc.hello sent after each open, before the connection is ready.
   * Apps whose protocol has one pass it here.
   */
  hello?: { protocol: number; client: string };
  /** How often a visible page pings rpc.ping; 30 s by default. */
  pingEveryMs?: number;
  /** The first retry delay after a drop; 1 s by default, doubling to 30 s. */
  retryMs?: number;
  /** The URL an app's file loads from by its hash; see Transport.blobURL. */
  blobURL?: (hash: string, ext: string) => string;
}

/** PING_MS is how often a visible page pings. */
export const PING_MS = 30_000;

/** RETRY_MS is the first retry delay, which doubles. */
export const RETRY_MS = 1_000;

/** MAX_RETRY_MS caps the retry delay. */
export const MAX_RETRY_MS = 30_000;

interface Hooks {
  firstReady(): void;
  firstFailure(message: string): void;
}

/** LodgeConnection is LodgeAPI over one socket at a time. */
class LodgeConnection implements LodgeAPI {
  readonly blobURL?: (hash: string, ext: string) => string;
  private readonly lodge: { apiURL(): string };
  private readonly doc: VisibilityDocument;
  private readonly WS: typeof WebSocket;
  private readonly pingEveryMs: number;
  private readonly retryMs: number;
  private readonly hello: { protocol: number; client: string } | undefined;
  private readonly hooks: Hooks;
  private readonly eventHandlers = new Set<(event: EventEnvelope) => void>();
  private readonly closeHandlers = new Set<(reason: string) => void>();
  private socket: WebSocket | null = null;
  private session: JsonRpcSession | null = null;
  private readyNow = false;
  private everReady = false;
  private stopped = false;
  private retry: ReturnType<typeof setTimeout> | undefined;
  private pinger: ReturnType<typeof setInterval> | undefined;
  private backoff: number;

  constructor(lodge: { apiURL(): string }, options: LodgeAPIOptions, hooks: Hooks) {
    this.lodge = lodge;
    this.doc = options.document ?? document;
    this.WS = options.webSocket ?? WebSocket;
    this.pingEveryMs = options.pingEveryMs ?? PING_MS;
    this.retryMs = options.retryMs ?? RETRY_MS;
    this.hello = options.hello;
    if (options.blobURL !== undefined) this.blobURL = options.blobURL;
    this.hooks = hooks;
    this.backoff = this.retryMs;
    this.doc.addEventListener("visibilitychange", this.onVisibility);
  }

  get ready(): boolean {
    return this.readyNow;
  }

  connect(): void {
    if (this.socket !== null || this.stopped) return;
    clearTimeout(this.retry);
    this.retry = undefined;
    let socket: WebSocket;
    try {
      socket = new this.WS(this.lodge.apiURL());
    } catch (err) {
      if (!this.everReady) {
        this.stopped = true;
        this.hooks.firstFailure(errorText(err));
        return;
      }
      this.retryLater();
      return;
    }
    const session = new JsonRpcSession(
      async (line) => socket.send(`${line}\n`),
      this.blobURL === undefined ? {} : { blobURL: this.blobURL },
    );
    session.onEvent((event) => {
      for (const handler of this.eventHandlers) handler(event);
    });
    session.onClose((reason) => {
      for (const handler of this.closeHandlers) handler(reason);
    });
    this.socket = socket;
    this.session = session;
    socket.onopen = () => {
      if (this.socket !== socket) return;
      const opened = () => {
        if (this.socket !== socket) return;
        this.readyNow = true;
        this.everReady = true;
        this.backoff = this.retryMs;
        this.pinger = setInterval(() => this.ping(), this.pingEveryMs);
        this.hooks.firstReady();
      };
      if (this.hello === undefined) {
        opened();
        return;
      }
      session.call("rpc.hello", this.hello).then(opened, (err: unknown) => {
        if (this.socket !== socket) return;
        socket.close();
        if (!this.everReady) this.hooks.firstFailure(errorText(err));
      });
    };
    socket.onmessage = (event) => {
      if (this.socket !== socket) return;
      for (const line of String(event.data).split("\n")) {
        if (line !== "") session.receive(line);
      }
    };
    socket.onclose = (event) => {
      if (this.socket !== socket) return;
      this.socket = null;
      this.session = null;
      this.readyNow = false;
      clearInterval(this.pinger);
      session.close(`the connection through lodge closed (${event.code})`);
      if (!this.everReady) {
        this.stopped = true;
        this.doc.removeEventListener("visibilitychange", this.onVisibility);
        this.hooks.firstFailure(`cannot reach the app's API through lodge (${event.code})`);
        return;
      }
      this.retryLater();
    };
  }

  call<T>(method: string, params: unknown): Promise<T> {
    const session = this.session;
    if (!this.readyNow || session === null) {
      return Promise.reject(new RPCError(CodeNotConnected, "not connected to lodge"));
    }
    return session.call<T>(method, params);
  }

  onEvent(handler: (event: EventEnvelope) => void): () => void {
    this.eventHandlers.add(handler);
    return () => this.eventHandlers.delete(handler);
  }

  onClose(handler: (reason: string) => void): () => void {
    this.closeHandlers.add(handler);
    return () => this.closeHandlers.delete(handler);
  }

  close(): void {
    this.stopped = true;
    clearTimeout(this.retry);
    clearInterval(this.pinger);
    this.doc.removeEventListener("visibilitychange", this.onVisibility);
    const socket = this.socket;
    this.socket = null;
    this.readyNow = false;
    if (socket !== null) {
      socket.close();
      this.session?.close("closed");
    }
    this.session = null;
  }

  /** ping keeps lodge's session: it ends one after 90 s without a frame. */
  private ping(): void {
    if (!this.readyNow || this.doc.visibilityState !== "visible") return;
    const session = this.session;
    if (session === null) return;
    session.call("rpc.ping", {}).catch(() => {});
  }

  private onVisibility = (): void => {
    if (this.doc.visibilityState !== "visible") return;
    if (this.socket === null) this.connect();
    else this.ping();
  };

  /** retryLater reconnects with a growing delay, and only while the page is visible. */
  private retryLater(): void {
    if (this.stopped || this.retry !== undefined) return;
    if (this.doc.visibilityState !== "visible") return; // onVisibility connects
    const delay = this.backoff;
    this.backoff = Math.min(this.backoff * 2, MAX_RETRY_MS);
    this.retry = setTimeout(() => {
      this.retry = undefined;
      this.connect();
    }, delay);
  }
}

/**
 * connectLodgeAPI opens the app's API through lodge, and keeps it open:
 * it opens only while the page is visible, pings rpc.ping every 30 s while
 * visible so lodge keeps its session (which ends after 90 s without a
 * frame), and reconnects with a growing delay after a drop. It resolves
 * once the first connection is ready, and rejects when that first attempt
 * fails: call it again to retry.
 */
export function connectLodgeAPI(lodge: { apiURL(): string }, options: LodgeAPIOptions = {}): Promise<LodgeAPI> {
  return new Promise((resolve, reject) => {
    const connection = new LodgeConnection(lodge, options, {
      firstReady: () => resolve(connection),
      firstFailure: (message) => reject(new Error(message)),
    });
    connection.connect();
  });
}

/** errorText is an error as a message. */
function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
