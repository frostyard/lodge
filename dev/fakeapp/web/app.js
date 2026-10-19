// The fake app's side of the shell's protocol, by hand
// (docs/specs/lodge-client.md). A real app uses @frostyard/lodge-client.
const id = location.pathname.split("/")[2];
const base = `/app/${id}`;
const routeNow = () => location.pathname.slice(base.length) + location.search + location.hash || "/";

if (window.parent === window) {
  // Outside the shell: go to the canonical URL.
  const r = routeNow();
  location.replace(`/${id}${r === "/" ? "" : r}`);
}

const $ = (s) => document.querySelector(s);
const log = (msg) => {
  $("#log").textContent = `${new Date().toLocaleTimeString()} ${msg}\n${$("#log").textContent}`.slice(0, 4000);
};
let shortcut = "Ctrl+Backquote";
let route = routeNow();
const send = (msg) => window.parent.postMessage({ lodge: 1, ...msg }, location.origin);

function show(r) {
  route = r;
  $("#route").textContent = r;
}
show(route);

window.addEventListener("message", (e) => {
  if (e.origin !== location.origin || e.source !== window.parent) return;
  const m = e.data;
  if (!m || m.lodge !== 1) return;
  switch (m.type) {
    case "init":
      shortcut = m.shortcut;
      $("#state").textContent = `init: contract ${m.contract}, theme ${m.theme}, shortcut ${m.shortcut}`;
      document.documentElement.dataset.theme = m.theme;
      if (m.contentOrigin) {
        $("#content").src = `${m.contentOrigin}/${id}/hello.html`;
        $("#content").hidden = false;
        $("#content-wrap").hidden = false;
      }
      send({ type: "route", path: route, title: titleOf(route), replace: true });
      break;
    case "navigate":
      show(m.route);
      send({ type: "route", path: m.route, title: titleOf(m.route), replace: true });
      break;
    case "visible":
      log(`visible: ${m.visible}`);
      break;
    case "theme":
      document.documentElement.dataset.theme = m.scheme;
      break;
  }
});

const titleOf = (r) => (r === "/" ? "" : `Page ${r.split(/[/?#]/)[1].toUpperCase()}`);

for (const b of document.querySelectorAll("[data-route]")) {
  b.addEventListener("click", () => {
    show(b.dataset.route);
    send({ type: "route", path: b.dataset.route, title: titleOf(b.dataset.route) });
  });
}
$("#switcher").addEventListener("click", () => send({ type: "shortcut", key: shortcut }));
$("#open").addEventListener("click", () => send({ type: "open", url: id === "one" ? "/two/a" : "/one/a" }));

// The switcher's chord never reaches the app.
window.addEventListener(
  "keydown",
  (e) => {
    const parts = shortcut.split("+");
    const code = parts.pop();
    const mods = { Ctrl: e.ctrlKey, Alt: e.altKey, Shift: e.shiftKey, Meta: e.metaKey };
    const want = Object.fromEntries(Object.keys(mods).map((k) => [k, parts.includes(k)]));
    if (e.code === code && !e.isComposing && Object.keys(mods).every((k) => mods[k] === want[k])) {
      e.preventDefault();
      e.stopPropagation();
      send({ type: "shortcut", key: shortcut });
    }
  },
  true,
);

// The session: one WebSocket, kept alive while the page is visible.
let ws;
let n = 0;
function connect() {
  ws = new WebSocket(`${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}${base}/api`);
  ws.onopen = () => {
    $("#socket").textContent = "open";
  };
  ws.onclose = (e) => {
    $("#socket").textContent = `closed (${e.code})`;
  };
  ws.onmessage = (e) => log(`← ${e.data}`);
}
const ping = () => {
  if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ jsonrpc: "2.0", id: ++n, method: "rpc.ping" }));
};
$("#ping").addEventListener("click", ping);
setInterval(() => {
  if (document.visibilityState === "visible") ping();
}, 30000);
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible" && ws?.readyState === WebSocket.CLOSED) connect();
});
connect();

send({ type: "ready", contract: 1 });
