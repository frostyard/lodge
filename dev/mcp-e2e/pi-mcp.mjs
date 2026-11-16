// Drives lodge's MCP gateway with pi-mcp, the MCP client of Pi Pocket's mcp
// module, the way that module uses it (docs/specs/lodge-http.md, The MCP
// gateway; ADR-0024): a sign-in through lodge's OAuth server with a loopback
// callback, a session on the streamable HTTP transport with a bearer token,
// request_access, the owner's approval, notifications/tools/list_changed on
// the open session, and calls of the new tools.
//
//   LODGE_ORIGIN=http://127.0.0.1:8480 node pi-mcp.mjs
//
// dev/mcp-e2e/run.sh starts the development build and runs this script.

import assert from "node:assert/strict";
import { McpAuthRequiredError, McpClient, StreamableHttpTransport } from "@earendil-works/pi-mcp";
import {
	authorizeMcp,
	McpOAuthProvider,
	MemoryOAuthStateStore,
	OAuthCallbackServer,
	parseWwwAuthenticate,
} from "@earendil-works/pi-mcp/oauth";
import { approve, finish } from "./owner.mjs";

const origin = process.env.LODGE_ORIGIN ?? "http://127.0.0.1:8480";
const serverUrl = `${origin}/gateway/mcp`;
const principal = process.env.E2E_PRINCIPAL ?? "pi-agent";
const folder = "/work";

function step(text) {
	console.log(`pi-mcp: ${text}`);
}

// within fails when promise does not settle within ms.
function within(promise, ms, what) {
	let timer;
	const timeout = new Promise((_, reject) => {
		timer = setTimeout(() => reject(new Error(`${what}: nothing within ${ms} ms`)), ms);
	});
	return Promise.race([promise, timeout]).finally(() => clearTimeout(timer));
}

function names(tools) {
	return tools.map((t) => t.name).sort();
}

// 1. Without a token, the gateway asks for a sign-in and names its resource
// metadata, as Pi Pocket's module reads it.
{
	const client = new McpClient({ name: "pi-pocket-mcp", version: "e2e" });
	await assert.rejects(client.connect(new StreamableHttpTransport({ url: serverUrl })), (err) => {
		assert.ok(err instanceof McpAuthRequiredError, `want McpAuthRequiredError, got ${err}`);
		assert.equal(err.status, 401);
		const challenge = parseWwwAuthenticate(err.wwwAuthenticate);
		assert.equal(challenge.resourceMetadataUrl?.href, `${origin}/.well-known/oauth-protected-resource/gateway/mcp`);
		return true;
	});
	await client.close().catch(() => {});
	step("401 with the resource metadata");
}

// 2. The sign-in: dynamic registration, the authorization redirect, the
// owner's approval (principal, scope mcp), the loopback callback and the
// code exchange, as Pi Pocket's mcp/oauth.ts runs them.
const callback = await OAuthCallbackServer.listen({ host: "127.0.0.1", path: "/callback", timeoutMs: 30_000 });
const store = new MemoryOAuthStateStore();
let authorizationUrl;
const oauth = new McpOAuthProvider({
	serverUrl,
	redirectUrl: callback.redirectUrl,
	clientMetadata: { client_name: "Pi Pocket (lodge e2e)" },
	store,
	onRedirect: (url) => {
		authorizationUrl = url;
	},
});
try {
	assert.equal(await authorizeMcp(oauth, { serverUrl, scope: "mcp" }), "REDIRECT");
	assert.ok(authorizationUrl, "the sign-in gave no authorization page");
	const state = await oauth.state();
	const answer = callback.waitForCallback(state, "/callback");
	const back = await approve(origin, authorizationUrl.href, { principal, scope: "mcp" });
	await finish(back);
	const { code, iss } = await answer;
	assert.equal(
		await authorizeMcp(oauth, { serverUrl, authorizationCode: code, ...(iss ? { iss } : {}) }),
		"AUTHORIZED",
	);
} finally {
	await callback.close();
}
const tokens = await oauth.tokens();
assert.ok(tokens?.access_token?.startsWith("lgo_at_"), "no lodge access token");
assert.equal(tokens.scope, "mcp");
step(`signed in as principal ${principal}`);

// 3. A session: the server is lodge, and it announces tools.listChanged.
const transport = new StreamableHttpTransport({
	url: serverUrl,
	authProvider: { token: async () => (await oauth.tokens())?.access_token },
});
const client = new McpClient({ name: "pi-pocket-mcp", version: "e2e", requestTimeoutMs: 30_000 });
let changes = 0;
let changed;
const nextChange = () =>
	new Promise((resolve) => {
		changed = resolve;
	});
client.onNotification("notifications/tools/list_changed", () => {
	changes++;
	changed?.();
});
await client.connect(transport);
assert.equal(client.serverInfo?.name, "lodge");
assert.equal(client.serverCapabilities?.tools?.listChanged, true);
const session = transport.sessionId;
assert.ok(session, "no MCP session");
let tools = await client.listTools();
assert.deepEqual(names(tools), ["request_access"]);
const ask = tools[0];
assert.match(ask.description ?? "", /notelite/);
step("session open: request_access only");

// 4. request_access queues a consent request and returns its link.
const asked = await client.callTool("request_access", {
	app: "notelite",
	items: [{ cap: "notelite:read" }, { cap: "notelite:write", sel: { folder } }],
});
assert.notEqual(asked.isError, true, JSON.stringify(asked));
const { url, request, expires } = asked.structuredContent;
assert.match(url, /\/oauth\/consent\/[^/]+$/);
assert.ok(request && expires, JSON.stringify(asked.structuredContent));
step(`access request ${request}`);

// 5. The owner approves it; the open session hears list_changed, stays the
// same session, and lists the new tools.
const heard = nextChange();
await approve(origin, url, { item: ["0", "1"], [`path.1.folder`]: folder });
await within(heard, 10_000, "notifications/tools/list_changed");
assert.equal(transport.sessionId, session, "the session changed");
tools = await client.listTools();
assert.deepEqual(names(tools), [
	"notelite_create_note",
	"notelite_delete_note",
	"notelite_get_note",
	"notelite_list_notes",
	"request_access",
]);
const list = tools.find((t) => t.name === "notelite_list_notes");
assert.equal(list.annotations?.readOnlyHint, true);
step(`list_changed (${changes}), the new tools listed on session ${session}`);

// 6. Calls: the app answers with the grant lodge sends it.
const made = await client.callTool("notelite_create_note", { folder, title: "from pi-mcp" });
assert.notEqual(made.isError, true, JSON.stringify(made));
assert.equal(made.structuredContent?.note?.folder, folder);
const outside = await client.callTool("notelite_create_note", { folder: "/home", title: "outside" });
assert.equal(outside.isError, true, "a note outside the granted folder was created");
const listed = await client.callTool("notelite_list_notes", { folder });
assert.notEqual(listed.isError, true, JSON.stringify(listed));
assert.ok(
	listed.structuredContent?.notes?.some((n) => n.title === "from pi-mcp"),
	JSON.stringify(listed.structuredContent),
);
step("calls answered by the app; a folder outside the grant is refused");

await client.close();
step("done");
