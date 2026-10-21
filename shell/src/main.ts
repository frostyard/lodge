// Inter, bundled into the shell (docs/specs/shell.md, Colours): lodge-ui
// depends on it, and a link into this checkout does not install that
// dependency, so the shell imports the same version itself.
import "@fontsource-variable/inter";

// The shell page's entry: lodge serves it for /, /<id> and /<id>/<route>
// (docs/specs/lodge-http.md, Shell routes).
import "./style.css";
import { LodgeShell } from "./lodge.js";
import { Settings } from "./storage.js";

const shell = new LodgeShell({
  window,
  settings: new Settings(),
  async fetchRail() {
    const r = await fetch("/lodge/apps", { cache: "no-store", credentials: "same-origin" });
    if (!r.ok) throw new Error(`/lodge/apps answered ${r.status}`);
    return r.json();
  },
});
document.body.replaceChildren(shell.root);
void shell.start();
