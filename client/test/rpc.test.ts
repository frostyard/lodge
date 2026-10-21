// The rpc subpath (docs/specs/lodge-client.md, The rpc subpath): the
// session over a line channel, the kit's event envelope, and the app's API
// through lodge with its heartbeat and its reconnect.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  CodeNotConnected,
  connectLodgeAPI,
  type EventEnvelope,
  JsonRpcSession,
  RPCError,
  type VisibilityDocument,
} from "../src/rpc.js";

/** FakeSocket is one WebSocket, driven by the test. */
class FakeSocket {
  static made: FakeSocket[] = [];
  readonly url: string;
  readonly sent: string[] = [];
  closed = false;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: unknown }) => void) | null = null;
  onclose: ((event: { code: number }) => void) | null = null;

  constructor(url: string) {
    this.url = url;
    FakeSocket.made.push(this);
  }

  send(data: string): void {
    this.sent.push(data);
  }

  close(): void {
    this.ended(1000);
  }

  open(): void {
    this.onopen?.();
  }

  receive(message: unknown): void {
    this.onmessage?.({ data: JSON.stringify(message) });
  }

  ended(code: number): void {
    if (this.closed) return;
    this.closed = true;
    this.onclose?.({ code });
  }
}

/** fakeDocument is a page whose visibility the test sets. */
function fakeDocument(state: DocumentVisibilityState = "visible") {
  const listeners = new Set<() => void>();
  let current = state;
  return {
    doc: {
      get visibilityState() {
        return current;
      },
      addEventListener: (_type: string, listener: () => void) => listeners.add(listener),
      removeEventListener: (_type: string, listener: () => void) => listeners.delete(listener),
    } as VisibilityDocument,
    set(next: DocumentVisibilityState) {
      current = next;
      for (const listener of listeners) listener();
    },
  };
}

/** line returns the parsed JSON-RPC message of a sent line. */
function line(socket: FakeSocket, index = 0): { id: number; method: string; params: unknown } {
  return JSON.parse(socket.sent[index] ?? "{}") as { id: number; method: string; params: unknown };
}

/** opened returns the first socket, opened, with the API it became. */
async function opened(view: ReturnType<typeof fakeDocument>) {
  const pending = connectLodgeAPI(
    { apiURL: () => "wss://lodge.test/api" },
    {
      document: view.doc,
      webSocket: FakeSocket as unknown as typeof WebSocket,
    },
  );
  const socket = FakeSocket.made[0];
  if (socket === undefined) throw new Error("no socket");
  socket.open();
  return { socket, api: await pending };
}

beforeEach(() => {
  FakeSocket.made = [];
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("JsonRpcSession", () => {
  it("sends a call and answers it by id", async () => {
    const sent: string[] = [];
    const session = new JsonRpcSession(async (text) => {
      sent.push(text);
    });
    const answer = session.call<{ n: number }>("counts", {});
    const call = JSON.parse(sent[0] ?? "{}") as { id: number; method: string; params: unknown };
    expect([call.method, call.params]).toEqual(["counts", {}]);
    session.receive(JSON.stringify({ jsonrpc: "2.0", id: call.id, result: { n: 3 } }));
    await expect(answer).resolves.toEqual({ n: 3 });
  });

  it("rejects with an RPCError that carries the code and data", async () => {
    const sent: string[] = [];
    const session = new JsonRpcSession(async (text) => {
      sent.push(text);
    });
    const answer = session.call("items.get", { id: 4 });
    const id = (JSON.parse(sent[0] ?? "{}") as { id: number }).id;
    session.receive(
      JSON.stringify({ jsonrpc: "2.0", id, error: { code: 1001, message: "no such item", data: { id: 4 } } }),
    );
    await expect(answer).rejects.toBeInstanceOf(RPCError);
    await answer.catch((err: RPCError) => {
      expect([err.code, err.message, err.data]).toEqual([1001, "no such item", { id: 4 }]);
    });
  });

  it("hands over the kit's event envelope, and drops other notifications", () => {
    const session = new JsonRpcSession(async () => {});
    const heard: EventEnvelope[] = [];
    session.onEvent((event) => heard.push(event));
    session.receive(
      JSON.stringify({
        jsonrpc: "2.0",
        method: "event",
        params: { seq: 2, event: "items.changed", data: { views: ["unread"] } },
      }),
    );
    session.receive(JSON.stringify({ jsonrpc: "2.0", method: "event", params: { event: "other" } }));
    session.receive(JSON.stringify({ jsonrpc: "2.0", method: "something.else", params: {} }));
    expect(heard).toEqual([{ seq: 2, event: "items.changed", data: { views: ["unread"] } }, { event: "other" }]);
  });

  it("fails its pending calls when it closes, once", async () => {
    const session = new JsonRpcSession(async () => {});
    const answer = session.call("counts", {});
    let closes = 0;
    session.onClose(() => closes++);
    session.close("the socket went away");
    session.close("again");
    await expect(answer).rejects.toMatchObject({ code: CodeNotConnected });
    expect(closes).toBe(1);
  });
});

describe("connectLodgeAPI", () => {
  it("resolves once the socket opens, and calls through it", async () => {
    const view = fakeDocument();
    const pending = connectLodgeAPI(
      { apiURL: () => "wss://lodge.test/app/read/api" },
      {
        document: view.doc,
        webSocket: FakeSocket as unknown as typeof WebSocket,
      },
    );
    const socket = FakeSocket.made[0];
    expect(socket?.url).toBe("wss://lodge.test/app/read/api");
    socket?.open();
    const api = await pending;

    const answer = api.call<{ n: number }>("counts", {});
    expect(socket?.sent[0]).toBe(`${JSON.stringify({ jsonrpc: "2.0", id: 1, method: "counts", params: {} })}\n`);
    socket?.receive({ jsonrpc: "2.0", id: 1, result: { n: 3 } });
    await expect(answer).resolves.toEqual({ n: 3 });
  });

  it("sends rpc.hello first when it is given, and is ready after its answer", async () => {
    const view = fakeDocument();
    const pending = connectLodgeAPI(
      { apiURL: () => "wss://lodge.test/api" },
      {
        document: view.doc,
        webSocket: FakeSocket as unknown as typeof WebSocket,
        hello: { protocol: 1, client: "read-web" },
      },
    );
    const socket = FakeSocket.made[0];
    if (socket === undefined) throw new Error("no socket");
    socket.open();
    expect(line(socket).method).toBe("rpc.hello");
    expect(line(socket).params).toEqual({ protocol: 1, client: "read-web" });
    let resolved = false;
    void pending.then(() => {
      resolved = true;
    });
    await Promise.resolve();
    expect(resolved).toBe(false);
    socket.receive({ jsonrpc: "2.0", id: 1, result: { protocol: 1 } });
    const api = await pending;
    await Promise.resolve();
    expect(api.ready).toBe(true);
  });

  it("pings every 30 s while visible, at once on becoming visible, and never while hidden", async () => {
    const view = fakeDocument();
    const { socket } = await opened(view);
    const pings = () => socket.sent.filter((text) => text.includes('"rpc.ping"')).length;

    await vi.advanceTimersByTimeAsync(30_000);
    expect(pings()).toBe(1);

    view.set("hidden");
    await vi.advanceTimersByTimeAsync(60_000);
    expect(pings()).toBe(1);

    view.set("visible");
    await vi.advanceTimersByTimeAsync(0);
    expect(pings()).toBe(2);
  });

  it("reconnects after a drop while visible, with a growing delay", async () => {
    const view = fakeDocument();
    const { api } = await opened(view);
    expect(api.ready).toBe(true);

    FakeSocket.made[0]?.ended(1006);
    expect(api.ready).toBe(false);
    await vi.advanceTimersByTimeAsync(999);
    expect(FakeSocket.made).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(FakeSocket.made).toHaveLength(2);
    FakeSocket.made[1]?.open();
    expect(api.ready).toBe(true);
  });

  it("waits for the page to be visible before it reconnects, and stops when closed", async () => {
    const view = fakeDocument();
    const { api } = await opened(view);

    view.set("hidden");
    FakeSocket.made[0]?.ended(1006);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(FakeSocket.made).toHaveLength(1);

    view.set("visible");
    await vi.advanceTimersByTimeAsync(0);
    expect(FakeSocket.made).toHaveLength(2);

    api.close();
    FakeSocket.made[1]?.ended(1006);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(FakeSocket.made).toHaveLength(2);
  });

  it("rejects when the first connection fails", async () => {
    const view = fakeDocument();
    const pending = connectLodgeAPI(
      { apiURL: () => "wss://lodge.test/api" },
      {
        document: view.doc,
        webSocket: FakeSocket as unknown as typeof WebSocket,
      },
    );
    FakeSocket.made[0]?.ended(1006);
    await expect(pending).rejects.toThrow("cannot reach");
  });
});
