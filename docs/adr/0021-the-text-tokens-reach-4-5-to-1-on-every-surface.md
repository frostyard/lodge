# 0021 — The text tokens reach 4.5:1 on every surface

- **Status:** Accepted (Brian, 2026-11-10)
- **Date:** 2026-11-10

## Context

[ADR-0018](0018-a-ui-package-beside-the-client.md) gave the apps one shared
look and left its first release at frostmail's values, so that nothing moved
while the package was adopted. [ADR-0019](0019-the-accent-is-frostyards-cold-blue.md)
changed the accent on contrast grounds. The three text tokens still carry
frostmail's greys, and two of them are too faint to read.

Facts that bound the choice:

- **WCAG 2.x asks 4.5:1 of body text.** The tokens and the surfaces reach
  these ratios (WCAG 2.x, computed; window / sidebar / toolbar / banner):

  | Token | Light | Dark |
  | --- | --- | --- |
  | `--text-primary` (`#1d1d1f`, `#f5f5f7`) | 16.83 / 15.05 / 15.72 / 15.46 | 15.31 / 13.16 / 12.80 / 12.80 |
  | `--text-secondary` (`#6e6e73`, `#a1a1a6`) | 5.07 / 4.54 / 4.74 / 4.66 | 6.48 / 5.57 / 5.42 / 5.42 |
  | `--text-tertiary` (`#a1a1a6`, `#6e6e73`) | 2.57 / 2.30 / 2.40 / 2.36 | 3.29 / 2.82 / 2.75 / 2.75 |

  The four surfaces are `--bg-window`, `--bg-sidebar`, `--bg-toolbar` and
  `--bg-banner`.
- **The tertiary is real text.** An app draws a date, a count, a hint or a
  secondary line in it, at 11px to 13px, so the level for body text applies
  and not the level for large text.
- **The secondary had no room left.** In the light it is 4.54:1 on the
  sidebar. A tertiary that reaches 4.5:1 must sit below it, so the
  secondary moves as well; otherwise the two tokens become one colour.
- **One value per scheme.** The apps follow lodge's `data-theme`, so a light
  value and a dark value are enough, and both dark blocks hold the same one.
- **The hue stays.** The greys are neutral with a little blue: `#6e6e73` is
  a grey of 110 with 5 more on its blue channel. Each new value is also a
  grey with 5 more on its blue channel; only its lightness moves.

## Decision

**The three text tokens reach 4.5:1 on each of the four surfaces.** They
take these values:

| Token | Light | Dark |
| --- | --- | --- |
| `--text-primary` | `#1d1d1f` | `#f5f5f7` |
| `--text-secondary` | `#5b5b60` (was `#6e6e73`) | `#aaaaaf` (was `#a1a1a6`) |
| `--text-tertiary` | `#6c6c71` (was `#a1a1a6`) | `#95959a` (was `#6e6e73`) |

The ratios now, in the same order (window / sidebar / toolbar / banner):

| Token | Light | Dark |
| --- | --- | --- |
| `--text-primary` | 16.83 / 15.05 / 15.72 / 15.46 | 15.31 / 13.16 / 12.80 / 12.80 |
| `--text-secondary` | 6.75 / 6.04 / 6.31 / 6.20 | 7.21 / 6.19 / 6.02 / 6.02 |
| `--text-tertiary` | 5.22 / 4.67 / 4.88 / 4.80 | 5.59 / 4.80 / 4.67 / 4.67 |

- **The step rule holds.** On every surface, primary is above secondary and
  secondary is above tertiary. Each scheme keeps the same step: the tertiary
  at about 4.67:1 on its worst surface, the secondary at about 6.0:1. A
  later change of these values keeps that order.
- **`--text-primary` does not move.** It already reaches 12.80:1 on its
  worst surface.
- **A test is the guard.** `ui/test/contrast.test.ts` reads `tokens.css`,
  computes the ratio of each text token on each of the four surfaces in the
  light block and in both dark blocks, and fails under 4.5:1 with the
  scheme, the token, the surface and the ratio in its message. It also fails
  when primary, secondary and tertiary are out of order. It reads only
  `#rrggbb` values, so a future `rgb()` value fails loudly instead of passing
  unmeasured.
- **The change is a value change only.** No token name, class name or
  component changes. It is `ui/v0.7.0`: before 1.0, a token value change is
  a minor ([specs/ui-package.md](../specs/ui-package.md#rules)).

## Consequences

- **Secondary and tertiary text gets darker in the light and lighter in the
  dark.** A date, a count, a hint and a preview line become readable; the
  look also becomes a little flatter, because faint grey was part of it.
- **The step between secondary and tertiary is smaller.** It is about
  1.29:1 where it was about 1.97:1. The two still differ, and the order
  holds on every surface, but the difference is easier to miss than before.
- **Each app takes it when it bumps to `ui/v0.7.0`.** frostmail,
  frostnotes, frostread, the shell and Porter each bundle their own copy,
  so an app keeps the old greys until its own PR moves the dependency.
- **`--badge-text` still fails, and is left for a later change.**
  `#6e6e73` on `#e5e5ea` is 4.04:1 in the light, and `#a1a1a6` on `#3a3a3c`
  is 4.41:1 in the dark. A badge's count is small text, so it needs the same
  level. It is not one of the three text tokens, so this decision leaves
  it alone; frostmail and frostnotes draw counts and labels with it today.
- **The other tokens stay.** A separator, a selection and the accent have
  their own contrast rules; this decision does not touch them.

## Alternatives considered

- **Keep the secondary and pick a tertiary just above 4.5:1:** no room in
  the light. The old secondary is 4.54:1 on the sidebar, so the step to a
  tertiary at 4.5:1 is about 1.01:1: the two tokens look the same.
- **The tertiary takes the old secondary (`#6e6e73` in the light):** 4.54:1
  on the sidebar, which leaves 0.04 of margin and no room for a step; the
  secondary must still move, so nothing is gained.
- **A tertiary at 3:1** (the level WCAG 2.x asks of large text): the
  tertiary carries small text, at 11px to 13px, so 4.5:1 is the level that
  applies.
- **Deeper values for a bigger margin:** a tertiary near 6:1 forces the
  secondary near 8:1, which makes the two greys read as the primary and
  removes the hierarchy the three tokens exist for.
- **Leave the values to each app:** the package is the one look the apps
  share; a per-app contrast fix would undo it, and each app would measure
  it again.

## References

- Shapes: [specs/ui-package.md](../specs/ui-package.md#tokens), [design/overview.md](../design/overview.md#the-kit-and-the-client), [the roadmap](../plans/0001-roadmap.md)
- Builds on: [ADR-0018](0018-a-ui-package-beside-the-client.md), [ADR-0019](0019-the-accent-is-frostyards-cold-blue.md)
- WCAG 2.x's contrast minimum: [1.4.3 Contrast (Minimum)](https://www.w3.org/WAI/WCAG21/Understanding/contrast-minimum.html)
- The package's history: [ui/CHANGELOG.md](../../ui/CHANGELOG.md)
