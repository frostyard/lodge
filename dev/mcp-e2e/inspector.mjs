// Drives lodge's MCP gateway with MCP Inspector's connection CLI, mcpdo
// (docs/specs/lodge-http.md, The MCP gateway; ADR-0024): a sign-in through
// lodge's OAuth server, a session that mcpdo's daemon holds open across
// commands, request_access, the owner's approval, the new tools on the same
// session, and a call.
//
// mcpdo prints no notification but a log line, so this script reads
// notifications/tools/list_changed on lodge's side: lodge logs each tools
// change of a principal with the number of open sessions it notified
// (LODGE_LOG, the development build's log).
//
//   LODGE_ORIGIN=http://127.0.0.1:8480 LODGE_LOG=lodge.log node inspector.mjs
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
const lodgeLog = process.env.LODGE_LOG;
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
const env = {
	...process.env,
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

// changes counts lodge's log lines that say principal's tools changed with
// at least 1 open session notified.
async function changes() {
	if (!lodgeLog) {
		return -1;
	}
	const text = await readFile(lodgeLog, "utf8");
	return text
		.split("\n")
		.filter((line) => line.includes("mcp tools changed") && line.includes(`principal=${principal}`))
		.filter((line) => Number(/sessions=(\d+)/.exec(line)?.[1] ?? 0) > 0).length;
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

	// 3. request_access, and the owner's approval.
	const before = await changes();
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
	if (before >= 0) {
		assert.ok((await changes()) > before, "lodge notified no open session of the tools change");
		step("lodge sent notifications/tools/list_changed to the open session");
	}

	// 4. The same connection lists the new tools, and a call works.
	tools = (await mcpdo("@lodge", "tools/list")).tools.map((t) => t.name).sort();
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
