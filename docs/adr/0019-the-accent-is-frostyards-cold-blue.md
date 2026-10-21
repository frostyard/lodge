# 0019 — The accent is frostyard's cold blue

- **Status:** Accepted (Irulan, by Brian's delegation, 2026-10-21)
- **Date:** 2026-10-21

## Context

[ADR-0018](0018-a-ui-package-beside-the-client.md) gave the apps one shared
look and left its first release at frostmail's values, so that nothing moved
while the package was adopted. The accent was Apple's blue: `#007aff` in
the light and `#0a84ff` in the dark, with white text on it.

The package's look change is the last step of that migration. Brian
delegated the choice of colour to Irulan, who chose on 2026-10-21.

Facts that bound the choice:

- **Contrast decides it.** WCAG 2.x asks 4.5:1 for body text. Apple's blue
  reaches 4.02:1 against white, and white text on it the same, so it fails.
  The frostyard design system's `--blue` (`#168bd0`) reaches 3.72:1, and
  its neighbour `#1584ca` 4.05:1: both fail too.
- **The suite has a brand palette.** frostyard/core's design tokens
  (`.agents/skills/frostyard-design/tokens/colors.css`) hold the colours the
  app icons are drawn from: midnight tiles in `#168bd0`, `#47b8ef` and
  `#aee9ff`.
- **One value per theme.** The apps follow lodge's `data-theme`, so a light
  and a dark value are enough.

## Decision

**The accent is frostyard's cold blue.** frostmail, frostnotes, frostread,
Porter and the shell all take it from `@frostyard/lodge-ui`:

| Token | Light | Dark | The palette's name |
| --- | --- | --- | --- |
| `--accent` | `#2c6380` | `#47b8ef` | `--line-strong`; `--sky` |
| `--accent-contrast` | `#ffffff` | `#02131f` | `--on-primary` |
| `--focus-ring` | `rgb(44 99 128 / 0.75)` | `rgb(71 184 239 / 0.7)` | |

- **It is a token change only.** No class name and no component code
  changes; `--accent` and the two tokens that carry its colour move, and
  every use of them follows.
- **The contrast holds** (WCAG 2.x, computed):

  | Pair | Light | Dark |
  | --- | --- | --- |
  | the accent on `--bg-window` | 6.56:1 | 7.44:1 |
  | the accent on `--bg-sidebar` | 5.86:1 | 6.39:1 |
  | the accent on `--selection-sidebar` | 4.79:1 | |
  | `--accent-contrast` on the accent | 6.56:1 | 8.40:1 |
  | the focus ring on the window | about 3.7:1 | about 4.3:1 |

  The ring clears the 3:1 a focus indicator needs.
- **The icon tiles agree.** The dark accent is the tiles' own sky
  (`#47b8ef`), and the light accent is the palette's deepest blue
  (`#2c6380`).
- **A token whose value is a tint of the accent changes with it**:
  `--focus-ring` was `rgb(0 122 255 / 0.5)` and is
  `rgb(44 99 128 / 0.75)`.
- **Two tokens that only resemble it do not:** `--flag-5` in
  `accents.css` is Apple's blue flag, one of seven flag colours the apps
  name (a message's flag), not a tint of the accent; it stays `#007aff`
  in the light and `#0a84ff` in the dark. No other token is the accent or a
  tint of it.

[specs/ui-package.md](../specs/ui-package.md#tokens) carries the values.

## Consequences

- **frostmail and frostnotes no longer look like Apple Mail or Obsidian's
  default blue.** The accent is cooler and, in the light, much deeper; a
  selection, a primary button, a link, the unread dot and a focus ring
  all change. The neutral surfaces, the type and the layout do not.
- The links and the selected rows that use `--accent` in the light now
  carry white text at 6.56:1, where they carried white on `#007aff` at
  4.02:1: a readable change as well as a visible one.
- The tokens' minor version moves to 0.5.0. An app takes the new accent
  when it bumps the dependency; until then it keeps the old one, which is
  why the apps bump one at a time.
- **The shell's home-screen icons still draw the old blue.** Its
  `scripts/icons.mjs` writes the accent into the PNGs as a literal
  (`#007aff`), and its manifest's `theme_color` is the chrome
  (`#f2f2f4`), not the accent, so the manifest is right as it is. The
  icons should be redrawn in a later change, with `#2c6380`.
- The brand's other accents (`--sky`, `--ice`) stay the design system's
  business; the apps take one accent, not a palette.

## Alternatives considered

- **Apple's blue (`#007aff`), kept:** 4.02:1 for text in the light, below
  the 4.5:1 the apps need; and it is not frostyard's colour.
- **`#168bd0`, the design system's `--blue`:** 3.72:1 on white, and 3.72:1
  for white text on it. Rejected on contrast.
- **`#1584ca`, a step deeper:** 4.05:1. Still short of 4.5:1.
- **A different accent per app:** the suite shares one look; two accents
  would undo what the package is for.
- **The accent as a Tailwind class change:** a token change is enough, and
  it reaches the shell and Porter, which take no Tailwind.

## References

- Shapes: [specs/ui-package.md](../specs/ui-package.md#tokens), [design/overview.md](../design/overview.md#the-kit-and-the-client), [ADR-0018](0018-a-ui-package-beside-the-client.md)
- Builds on: [ADR-0018](0018-a-ui-package-beside-the-client.md)
- frostyard/core's design tokens: [colors.css](https://github.com/frostyard/core/blob/main/.agents/skills/frostyard-design/tokens/colors.css)
