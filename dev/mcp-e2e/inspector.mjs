// Drives lodge's MCP gateway with MCP Inspector's connection CLI, mcpdo
// (docs/specs/lodge-http.md, The MCP gateway; ADR-0024): a sign-in through
// lodge's OAuth server, a session that mcpdo's daemon holds open across
// commands, request_access, the owner's approval, the new tools on the same
// session, and a call.
//
// mcpdo prints no notification, so tap.mjs instruments its daemon's
// transport (NODE_OPTIONS=--import): it records each JSON-RPC message that
// Inspector's MCP client sends or receives on /gateway/mcp, with its MCP
// session. The test reads notifications/tools/list_changed there, on the
// session that Inspector keeps open, and checks that the session stays the
// same.
//
//   LODGE_ORIGIN=http://127.0.0.1:8480 node inspector.mjs
//
// dev/mcp-e2e/run.sh starts the development build and runs this script.

import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { approve, finish } from "./owner.mjs";

const run = promisify(execFile);
const here = dirname(fileURLToPath(import.meta.url));
const mcpdoBin = join(here, "node_modules", ".bin", "mcpdo");
const origin = process.env.LODGE_ORIGIN ?? "http://127.0.0.1:8480";
const serverUrl = `${origin}/gateway/mcp`;
const principal = process.env.E2E_PRINCIPAL ?? "inspector-agent";
const folder = "/work";

function step(text) {
	console.log(`inspector: ${text}`);
}

// A home of its own: mcpdo keeps its catalog, its daemon and its secrets
// there, and nothing reaches the developer's ~/.mcp-inspector.
const home = await mkdtemp(join(tmpdir(), "lodge-mcp-e2e-"));
const catalog = join(home, "mcp.json");
await writeFile(catalog, JSON.stringify({ mcpServers: { lodge: { type: "http", url: serverUrl } } }));
const tap = join(home, "tap.jsonl");
await writeFile(tap, "");
const env = {
	...process.env,
	NODE_OPTIONS: `${process.env.NODE_OPTIONS ?? ""} --import=${join(here, "tap.mjs")}`.trim(),
	LODGE_E2E_TAP: tap,
	HOME: home,
	MCP_CATALOG_PATH: catalog,
	MCP_ALLOW_DEFAULT_CONNECTION: "0",
	NO_COLOR: "1",
};

async function mcpdo(...args) {
	try {
		const { stdout } = await run(mcpdoBin, ["--format", "json", ...args], { env, timeout: 60_000 });
		return JSON.parse(stdout);
	} catch (err) {
		throw new Error(`mcpdo ${args.join(" ")}: ${err.stderr || err.stdout || err.message}`);
	}
}

// tapped are the messages that Inspector's MCP client sent and received.
async function tapped() {
	const text = await readFile(tap, "utf8");
	return text
		.split("\n")
		.filter(Boolean)
		.map((line) => JSON.parse(line));
}

// lastSession is the session of the last tools/list that Inspector sent.
async function lastSession() {
	const sent = (await tapped()).filter((e) => e.direction === "sent" && e.method === "tools/list");
	assert.ok(sent.length > 0, "the tap saw no tools/list: is it loaded into mcpdo's daemon?");
	return sent.at(-1).session;
}

// listChanged counts the notifications/tools/list_changed that Inspector
// received on session.
async function listChanged(session) {
	return (await tapped()).filter(
		(e) => e.direction === "received" && e.method === "notifications/tools/list_changed" && e.session === session,
	).length;
}

// heard waits until Inspector has received more than count notifications on
// session: it arrives on a stream, so the file is read until a deadline.
async function heard(session, count, ms) {
	const end = Date.now() + ms;
	while (Date.now() < end) {
		if ((await listChanged(session)) > count) {
			return;
		}
		await new Promise((resolve) => setTimeout(resolve, 100));
	}
	assert.fail(`Inspector received no notifications/tools/list_changed on session ${session} within ${ms} ms`);
}

try {
	// 1. The sign-in: mcpdo starts it, and waits for the browser at its
	// loopback callback; the owner approves on lodge's consent page.
	const pending = await mcpdo("connect", "lodge");
	assert.equal(pending.pendingAuth, true, JSON.stringify(pending));
	const back = await approve(origin, pending.authUrl, { principal, scope: "mcp" });
	await finish(back);
	const shown = await mcpdo("connections/show", "@lodge");
	const info = JSON.stringify(shown);
	assert.match(info, /"name":\s*"lodge"/, `server info: ${info}`);
	assert.match(info, /"listChanged":\s*true/, `capabilities: ${info}`);
	step(`signed in as principal ${principal}`);

	// 2. A fresh principal holds no grant: request_access only.
	let tools = (await mcpdo("@lodge", "tools/list")).tools.map((t) => t.name).sort();
	assert.deepEqual(tools, ["request_access"]);
	const session = await lastSession();
	assert.ok(session, "Inspector's tools/list carried no MCP session");

	// 3. request_access, and the owner's approval: Inspector's open session
	// receives notifications/tools/list_changed.
	const before = await listChanged(session);
	const asked = await mcpdo(
		"@lodge",
		"tools/call",
		"request_access",
		JSON.stringify({ app: "notelite", items: [{ cap: "notelite:read" }, { cap: "notelite:write", sel: { folder } }] }),
	);
	const result = asked.result ?? asked;
	assert.notEqual(result.isError, true, JSON.stringify(asked));
	const { url } = result.structuredContent;
	assert.match(url, /\/oauth\/consent\/[^/]+$/);
	await approve(origin, url, { item: ["0", "1"], "path.1.folder": folder });
	await heard(session, before, 10_000);
	step(`Inspector received notifications/tools/list_changed on session ${session}`);

	// 4. The same session lists the new tools, and a call works.
	tools = (await mcpdo("@lodge", "tools/list")).tools.map((t) => t.name).sort();
	assert.equal(await lastSession(), session, "Inspector's session changed");
	assert.deepEqual(tools, [
		"notelite_create_note",
		"notelite_delete_note",
		"notelite_get_note",
		"notelite_list_notes",
		"request_access",
	]);
	const made = await mcpdo(
		"@lodge",
		"tools/call",
		"notelite_create_note",
		JSON.stringify({ folder, title: "from MCP Inspector" }),
	);
	const note = (made.result ?? made).structuredContent?.note;
	assert.equal(note?.folder, folder, JSON.stringify(made));
	step("the new tools listed and called");
	step("done");
} finally {
	await run(mcpdoBin, ["daemon", "stop"], { env }).catch(() => {});
	await stopLeftovers();
	await rm(home, { recursive: true, force: true });
}

// stopLeftovers stops the processes that mcpdo left behind for this run,
// such as a sign-in that still waits for its callback after a failure: those
// whose environment names this run's catalog.
async function stopLeftovers() {
	const mark = `MCP_CATALOG_PATH=${catalog}`;
	for (const pid of await readdir("/proc").catch(() => [])) {
		if (!/^\d+$/.test(pid) || Number(pid) === process.pid) {
			continue;
		}
		const environ = await readFile(`/proc/${pid}/environ`, "utf8").catch(() => "");
		if (environ.split("\0").includes(mark)) {
			try {
				process.kill(Number(pid));
			} catch {
				// It ended by itself.
			}
		}
	}
}
