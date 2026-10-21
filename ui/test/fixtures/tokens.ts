// The values this package started from, so that a change to the CSS is
// caught rather than shipped: the tokens are meant to move to these values
// with no visual change, and a later look change is a release of its own.
//
// frostmail: app/src/styles/tokens.css at commit
// 4328cb7453d879a07fc3b3208f83193107423c68 (2026-10-21). A token with no
// dark value here is one frostmail does not override in the dark: it keeps
// its light value, and this package writes that value in both dark blocks so
// that they are complete.
//
// frostnotes: app/src/styles/tokens.css at commit
// 059442da0eee8127c43d6dd175627c4b087976ef (2026-10-21), the generic extras
// only. Its domain extras (--bg-tag, --callout-cyan) stay in that app.
export interface Value {
  light: string;
  dark?: string;
}

export const frostmail: Record<string, Value> = {
  "--bg-window": { light: "#ffffff", dark: "#1e1e1e" },
  "--bg-sidebar": { light: "#f2f2f4", dark: "#2a2a2c" },
  "--bg-toolbar": { light: "#f7f7f8", dark: "#2c2c2e" },
  "--bg-banner": { light: "#f5f5f7", dark: "#2c2c2e" },
  "--text-primary": { light: "#1d1d1f", dark: "#f5f5f7" },
  "--text-secondary": { light: "#6e6e73", dark: "#a1a1a6" },
  "--text-tertiary": { light: "#a1a1a6", dark: "#6e6e73" },
  "--separator": { light: "#e3e3e6", dark: "#3a3a3c" },
  "--accent": { light: "#007aff", dark: "#0a84ff" },
  "--accent-contrast": { light: "#ffffff" },
  "--selection-inactive": { light: "#e4e4e7", dark: "#3a3a3c" },
  "--selection-sidebar": { light: "#dcdce0", dark: "#3a3a3c" },
  "--badge-bg": { light: "#e5e5ea", dark: "#3a3a3c" },
  "--badge-text": { light: "#6e6e73", dark: "#a1a1a6" },
  "--focus-ring": { light: "rgb(0 122 255 / 0.5)", dark: "rgb(10 132 255 / 0.6)" },
  "--flag-1": { light: "#ff3b30", dark: "#ff453a" },
  "--flag-2": { light: "#ff9500", dark: "#ff9f0a" },
  "--flag-3": { light: "#ffcc00", dark: "#ffd60a" },
  "--flag-4": { light: "#34c759", dark: "#30d158" },
  "--flag-5": { light: "#007aff", dark: "#0a84ff" },
  "--flag-6": { light: "#af52de", dark: "#bf5af2" },
  "--flag-7": { light: "#8e8e93", dark: "#98989d" },
  "--quote-1": { light: "#2e7bd6", dark: "#5aa0f0" },
  "--quote-2": { light: "#2ea44f", dark: "#4cc76a" },
  "--quote-3": { light: "#c2410c", dark: "#f0884a" },
  "--avatar-0": { light: "#5e5ce6" },
  "--avatar-1": { light: "#0a84ff" },
  "--avatar-2": { light: "#30b0c7" },
  "--avatar-3": { light: "#34c759" },
  "--avatar-4": { light: "#ff9f0a" },
  "--avatar-5": { light: "#ff375f" },
  "--avatar-6": { light: "#bf5af2" },
  "--avatar-7": { light: "#8e8e93" },
};

export const frostnotes: Record<string, Value> = {
  "--bg-code": { light: "#f5f5f7", dark: "#2a2a2c" },
  "--bg-highlight": { light: "rgb(255 204 0 / 0.4)", dark: "rgb(255 214 10 / 0.3)" },
  "--popover-shadow": { light: "0 8px 24px rgb(0 0 0 / 0.16)", dark: "0 8px 24px rgb(0 0 0 / 0.5)" },
  "--backdrop": { light: "rgb(0 0 0 / 0.2)", dark: "rgb(0 0 0 / 0.4)" },
};
