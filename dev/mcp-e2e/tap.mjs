// An instrumented transport for a client that this repository does not
// control: loaded with `node --import`, it wraps the process's fetch, and
// writes 1 JSON line to the file LODGE_E2E_TAP for each JSON-RPC message
// that crosses /gateway/mcp: the requests the client sends, and the
// messages it receives, on a JSON answer or on an SSE stream, each with the
// MCP session it belongs to. inspector.mjs loads it into MCP Inspector's
// mcpdo daemon (through NODE_OPTIONS), so the test reads what reached
// Inspector's own MCP client, not lodge's view of it.

import { appendFileSync } from "node:fs";

const out = process.env.LODGE_E2E_TAP;
const inner = globalThis.fetch;

function record(entry) {
	appendFileSync(out, `${JSON.stringify(entry)}\n`);
}

function urlOf(input) {
	if (typeof input === "string") {
		return input;
	}
	return input instanceof URL ? input.href : input.url;
}

// messages are the JSON-RPC messages of a JSON body: 1 or a batch.
function messages(text) {
	try {
		const value = JSON.parse(text);
		return Array.isArray(value) ? value : [value];
	} catch {
		return [];
	}
}

function note(direction, session, message) {
	if (message && typeof message === "object") {
		record({ direction, session, method: message.method ?? null, id: message.id ?? null });
	}
}

// readStream records each event of an SSE body as it arrives.
async function readStream(body, session) {
	const reader = body.pipeThrough(new TextDecoderStream()).getReader();
	let buffer = "";
	for (;;) {
		const { value, done } = await reader.read();
		if (done) {
			return;
		}
		buffer += value.replaceAll("\r\n", "\n");
		let end = buffer.indexOf("\n\n");
		while (end >= 0) {
			const event = buffer.slice(0, end);
			buffer = buffer.slice(end + 2);
			const data = event
				.split("\n")
				.filter((line) => line.startsWith("data:"))
				.map((line) => line.slice(5).trimStart())
				.join("\n");
			for (const m of messages(data)) {
				note("received", session, m);
			}
			end = buffer.indexOf("\n\n");
		}
	}
}

if (out && inner) {
	globalThis.fetch = async (input, init = {}) => {
		const url = urlOf(input);
		if (!url.includes("/gateway/mcp")) {
			return inner(input, init);
		}
		const sent = new Headers(init.headers ?? (input instanceof Request ? input.headers : undefined));
		const requestSession = sent.get("mcp-session-id") ?? "";
		if (typeof init.body === "string") {
			for (const m of messages(init.body)) {
				note("sent", requestSession, m);
			}
		}
		const res = await inner(input, init);
		const session = res.headers.get("mcp-session-id") ?? requestSession;
		const type = res.headers.get("content-type") ?? "";
		if (!res.body || !(type.includes("text/event-stream") || type.includes("application/json"))) {
			return res;
		}
		const [mine, theirs] = res.body.tee();
		if (type.includes("text/event-stream")) {
			readStream(mine, session).catch(() => {});
		} else {
			new Response(mine)
				.text()
				.then((text) => {
					for (const m of messages(text)) {
						note("received", session, m);
					}
				})
				.catch(() => {});
		}
		return new Response(theirs, { status: res.status, statusText: res.statusText, headers: res.headers });
	};
}
