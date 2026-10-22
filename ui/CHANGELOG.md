# Changelog

Every release of `@frostyard/lodge-ui`, newest first. Before 1.0, a token
value change is a minor and is named here; a new token is a minor; a removed
or renamed token is a major ([the spec](../docs/specs/ui-package.md#rules)).
Dates are the commits' dates. A release is a lodge tag `ui/vX.Y.Z`.

## 0.6.0 (2026-10-22)

- `WindowControls`: the window's 3 buttons, Minimize, Maximize or Restore,
  and Close, which frostmail and frostnotes drew each their own way. The
  default platform, `"linux"`, draws what they drew (a `flex shrink-0
  items-center gap-1` row of 28px `IconButton`s), so an app that moves to it
  looks the same. `platform="windows"` draws flat caption buttons, 46px wide
  and the row's height, with Windows' own red (`#c42b1c`) for Close on hover.
  `WindowCommand`, `WindowPlatform` and `windowPlatform()` come with it.
- `Prompt`, from frostnotes: the shared modal frame of a quick switcher, a
  command palette and a chooser, with `highlight`, `PromptMods`,
  `PromptInstruction` and `PromptRange`. The footer's size is written out
  (`text-[11px]/[14px]`) because `text-status` is frostnotes' type role, not
  the package's. It portals onto the body, so it needs react-dom.

## 0.5.0 (2026-10-21)

- The accent is frostyard's cold blue
  ([ADR-0019](../docs/adr/0019-the-accent-is-frostyards-cold-blue.md)): a
  value change of `--accent`, `--accent-contrast` and `--focus-ring`, light
  and dark. `--flag-5` keeps its own value. Tokens only; no class or
  component changed.

## 0.4.0 (2026-10-21)

- `Sheet` takes `ariaLabel` to be named without a heading (the type requires
  a `title` or an `ariaLabel`), `role` (`"dialog"` or `"alertdialog"`) and
  `portal` (render onto the body, for a frame inside a scroll or overflow
  container). Every default is 0.3.0's behaviour.
- A `Menu` row is named by its label alone; `aria-keyshortcuts` carries its
  keys in ARIA's form when each part has a name there.

## 0.3.0 (2026-10-21)

- `Menu`: frostmail's context menu and frostnotes' menu merged into one. It
  has items (with an icon, a check mark, keys and a disabled state),
  separators and submenus, the keyboard, an outside press that closes it,
  focus that goes in and returns, and the narrow window's sheet form. It
  portals onto the body, so react-dom is an optional peer.

## 0.2.0 (2026-10-21)

- `Sheet` options: `width`, `className`, `showClose`, `closeOnOverlay`,
  `initialFocus` and `titleClassName`. Every default is 0.1.0's behaviour.

## 0.1.0 (2026-10-21)

- The first release: the design tokens (frostmail's values, unchanged), the
  Tailwind v4 theme over them, the base layer, Inter, `./theme`, and the
  React primitives `IconButton`, `Splitter`, `Sheet`, `useNarrow` and `cx`.
