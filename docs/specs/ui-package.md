# Spec: the UI package, `@frostyard/lodge-ui`

What `ui/` exports, and the rules an app that consumes it must keep. Its
consumers: frostmail, frostnotes, frostread, the shell and Porter, each of
which bundles its own copy at build time.

Rationale: [ADR-0018](../adr/0018-a-ui-package-beside-the-client.md). Where
it fits: [design/overview.md](../design/overview.md#the-kit-and-the-client).

**Status:** version 0.3.0, written on 2026-10-21. 0.1.0 carries frostmail's
values unchanged, so an app that moves to it looks the same; 0.2.0 adds the
Sheet's options; 0.3.0 adds `Menu`.

## Interface

### Exports

| Export | File | React needed |
| --- | --- | --- |
| `@frostyard/lodge-ui/tokens.css` | `src/tokens.css` | no |
| `@frostyard/lodge-ui/accents.css` | `src/accents.css` | no |
| `@frostyard/lodge-ui/theme.css` | `src/theme.css` | no |
| `@frostyard/lodge-ui/base.css` | `src/base.css` | no |
| `@frostyard/lodge-ui/fonts.css` | `src/fonts.css` | no |
| `@frostyard/lodge-ui/theme` | `src/theme.ts` | no |
| `@frostyard/lodge-ui/react` | `src/react/index.ts` | yes |

The package is ESM source that an app's bundler compiles. Its only runtime
dependency is `@fontsource-variable/inter`; `react` (^19) and
`lucide-react` (^1) and `react-dom` (^19) are optional peer dependencies, and only `./react` uses
them.

### Tokens

Every token is a CSS custom property. An app MUST use them, never a literal
colour.

| Group | Names |
| --- | --- |
| Surfaces | `--bg-window`, `--bg-sidebar`, `--bg-toolbar`, `--bg-banner`, `--bg-code` |
| Text | `--text-primary`, `--text-secondary`, `--text-tertiary` |
| Lines and selection | `--separator`, `--selection-inactive`, `--selection-sidebar` |
| Accent | `--accent`, `--accent-contrast`, `--focus-ring` |
| Badge | `--badge-bg`, `--badge-text` |
| Marker and overlay | `--bg-highlight`, `--backdrop`, `--popover-shadow` |
| Density | `--control-h`, `--row-h`, `--pad-x` |
| Domain (`accents.css`) | `--flag-1` to `--flag-7`, `--quote-1` to `--quote-3`, `--avatar-0` to `--avatar-7` |

- **Light** is the `:root` block; **dark** comes from
  `prefers-color-scheme: dark` under `:root:not([data-theme="light"])`, and
  from `[data-theme="dark"]`, which lodge's shell sets.
- **Both dark blocks hold the same values**, and every colour of the light
  block appears in both. `test/tokens.test.ts` checks it.
- **Density** is 28px, 32px and 10px, and 44px, 44px and 12px under
  `@media (pointer: coarse)`. The dark blocks do not set it.

### The Tailwind theme

`theme.css` maps the tokens and the shared type roles to Tailwind v4 names:

| Utility | Token or value |
| --- | --- |
| `bg-window`, `bg-sidebar`, `bg-toolbar`, `bg-banner` | the four surfaces |
| `text-primary`, `text-secondary`, `text-tertiary` | the three text tokens |
| `border-separator` | `--separator` |
| `bg-accent`, `text-accent-contrast`, `text-focus` | the accent tokens |
| `bg-selection-inactive`, `bg-selection-sidebar` | the selection tokens |
| `bg-badge`, `text-badge-text` | the badge tokens |
| `bg-code`, `bg-highlight`, `bg-backdrop`, `shadow-popover` | the extras |
| `bg-flag-*`, `text-flag-*`, `bg-quote-*`, `bg-avatar-*`, `text-avatar-*` | `accents.css` |
| `font-sans` | `"Inter Variable", system-ui, sans-serif` |
| `narrow:` | `@media (max-width: 599px)` |

The shared type roles, at frostmail's sizes: `text-list-sender`,
`text-list-subject`, `text-list-preview`, `text-list-date`,
`text-sidebar-row`, `text-sidebar-section`, `text-reader-sender`,
`text-reader-subject`, `text-reader-meta`, `text-reader-body`,
`text-toolbar-title`, `text-toolbar-subtitle`, `text-empty`.

### `./theme`

| Export | Contract |
| --- | --- |
| `applyScheme(root, scheme)` | Sets `data-theme`, or removes it for `null`, so the system's scheme applies |
| `followLodge(lodge, root?)` | Applies the shell's `theme` now and on each `theme` event; returns the unsubscribe |
| `NARROW_QUERY` | `"(max-width: 599px)"` |
| `isNarrow(win?)` | Whether the window is narrow now |
| `onNarrowChange(win, onChange)` | Calls `onChange(matches)` on a change; returns the unsubscribe |

`followLodge` takes anything with lodge-client's `theme` field and its
`on("theme")`, so a `Lodge` is one. `theme.ts` imports no React.

### `./react`

| Export | Contract |
| --- | --- |
| `IconButton` | A button with `label` (its accessible name), `keys` (in the tooltip), `icon` (a lucide component), `size` (28 or 24), `pressed`, `disabled`, `onClick`; 44px under a coarse pointer |
| `Splitter` | A focusable `role="separator"` with `orientation`, `label`, `value` (as `aria-valuenow`), `onStart`, `onMove(delta)`, `onEnd`; arrow keys move it 16px |
| `Menu` | A menu at a point, or a narrow window's sheet (`sheet`): `MenuEntry` rows are an item (`id`, `label`, `icon`, `keys`, `checked`, `disabled`), a separator, or a submenu (`id`, `label`, `icon`, `disabled`, `items`); `onSelect` and `onClose`; the arrow keys move, ArrowRight opens a submenu, ArrowLeft and Escape close one level, Enter chooses, Tab and a press outside close; focus returns to what had it. It portals onto the body, so react-dom is an optional peer |
| `Sheet` | A modal frame with `title` (`aria-labelledby`), `description` (`aria-describedby`), `onClose`, `closeLabel`; Escape, the close button and the overlay close it; focus moves in and returns. Options: `width` (a number of pixels or a CSS length, 480 by default), `className` (classes for the frame), `showClose` (default true), `closeOnOverlay` (default true; a form sets false), `initialFocus` (`"first"` for the first field, a ref, or the frame by default) and `titleClassName` (default `font-semibold text-empty`) |
| `useNarrow()` | `isNarrow` as a hook, following the window |
| `cx(...parts)` | Joins the class names that are set |

## Rules

1. **The CSS custom properties are the contract.** Their names are
   frostmail's, and an app keeps them.
2. **No file in the package imports `tailwindcss`**, as a whole or in part,
   and none carries a preflight. The app imports Tailwind's own layers or
   `tailwindcss`, then this package.
3. **No React in the CSS, and none in `theme.ts`.**
4. **`theme.css` maps only names `tokens.css` or `accents.css` define.**
4a. **A Sheet option changes only what it names.** `width` and `className`
   change the frame; `showClose`, `closeOnOverlay` and `initialFocus` change
   what opens and closes it; `titleClassName` changes the heading. The
   defaults are 0.1.0's behaviour.
5. **Sharing is at build time.** Each app bundles its own copy; nothing is
   served from a shared path at run time.
6. **An app adds `@source "../node_modules/@frostyard/lodge-ui/src";` to its
   Tailwind entry**, because Tailwind v4 does not scan `node_modules`.
7. **A version follows semver.** Before 1.0, a token value change is a
   minor and says so in the changelog; a new token is a minor; a removed or
   renamed token is a major.

`test/rules.test.ts` checks rules 2, 3 and 4 on the sources themselves;
`test/tokens.test.ts` checks the values against the apps they came from.

## How an app consumes it

```json
"@frostyard/lodge-ui": "github:frostyard/lodge#ui/v0.1.0&path:ui"
```

```css
@import "tailwindcss";
@import "@frostyard/lodge-ui/tokens.css";
@import "@frostyard/lodge-ui/accents.css";
@import "@frostyard/lodge-ui/theme.css";
@import "@frostyard/lodge-ui/base.css";
@import "@frostyard/lodge-ui/fonts.css";

@source "../node_modules/@frostyard/lodge-ui/src";
```

`theme.css` MUST come after Tailwind's own theme, never instead of it. The
shell takes the package from the checkout with `link:../ui`, as it does for
`client/`.

## References

- Rationale: [ADR-0018](../adr/0018-a-ui-package-beside-the-client.md)
- Context: [design/overview.md](../design/overview.md#the-kit-and-the-client); the package's own README is `ui/README.md`
- Builds on: [ADR-0007](../adr/0007-lodge-serves-every-apps-web-build-from-disk.md)
