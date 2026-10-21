# @frostyard/lodge-ui

The look that frostyard's apps share: the design tokens as CSS custom
properties, the Tailwind v4 theme over them, the base layer, and the React
primitives the apps use
([ui-package.md](../docs/specs/ui-package.md)). Its rationale is
[ADR-0018](../docs/adr/0018-a-ui-package-beside-the-client.md).

Version 0.1.0 carries frostmail's values unchanged, so that an app that
moves to it looks the same. A change to a value is a look change, and ships
as a release of its own.

It is ESM source with no build step, and each app's bundler compiles it. The
CSS and `./theme` need neither React nor an icon set; `./react` needs both.

## How an app takes it

Pin it to a release, a lodge tag `ui/vX.Y.Z`, as a pnpm git dependency in
this subdirectory, as the apps pin `@frostyard/lodge-client`:

```json
"@frostyard/lodge-ui": "github:frostyard/lodge#ui/v0.1.0&path:ui"
```

Between releases an app may pin a pushed lodge commit instead
(`#<commit>&path:ui`). The shell takes it from the checkout, with
`link:../ui`, as it does for `client/`.

**The dependencies React and lucide-react are optional peers**, so an app
that takes only the CSS or `./theme` installs neither.

### CSS

Import the files in this order, after Tailwind's own layers:

```css
@import "tailwindcss"; /* or Tailwind's layers one by one, as frostnotes does */
@import "@frostyard/lodge-ui/tokens.css";
@import "@frostyard/lodge-ui/accents.css"; /* optional: flags, quotes, avatars */
@import "@frostyard/lodge-ui/theme.css";
@import "@frostyard/lodge-ui/base.css"; /* optional: the page's base layer */
@import "@frostyard/lodge-ui/fonts.css"; /* optional: Inter, bundled */
```

- `tokens.css` is the colours, light and dark, and the density tokens
  (`--control-h`, `--row-h`, `--pad-x`, larger under a coarse pointer).
- `accents.css` is the domain tokens: `--flag-1` to `--flag-7`, `--quote-1`
  to `--quote-3`, `--avatar-0` to `--avatar-7`. An app that does not use
  them does not import the file.
- `theme.css` maps the tokens and the shared type roles to Tailwind
  utilities: `bg-window`, `text-secondary`, `border-separator`,
  `text-sidebar-row`, `text-reader-body`, `shadow-popover`, and the
  `narrow:` variant at `max-width: 599px`.
- `base.css` fills the page, sets 13px on 18px Inter, a focus ring, and no
  text selection.
- `fonts.css` imports `@fontsource-variable/inter`, which this package
  depends on, so the app needs no font dependency of its own.

**An app adds this line to its Tailwind entry**, because Tailwind v4 does
not scan `node_modules`:

```css
@source "../node_modules/@frostyard/lodge-ui/src";
```

Without it, a utility that only the package's components use (`size-7`,
`bg-backdrop`) is missing from the app's CSS.

### React

```tsx
import { IconButton, Sheet, Splitter, cx, useNarrow } from "@frostyard/lodge-ui/react";
```

`IconButton` takes a label, the keys it answers to, and a lucide icon.
`Splitter` is a focusable separator that drags and moves 16px per arrow key.
`Sheet` is a modal frame: an overlay over `--backdrop`, `role="dialog"`,
Escape, and focus moved in and given back. Its defaults are a 480px frame,
a close button, an overlay that closes it and focus on the frame; an app
can change any of them:

```tsx
<Sheet
  title="Time"
  width={360}                       // a number of pixels, or a CSS length
  className="rounded-[10px]"        // classes for the frame
  titleClassName="text-[13px] font-semibold"
  showClose={false}
  closeOnOverlay={false}            // a form: only Escape or its buttons close it
  initialFocus="first"              // the first field, else the first control
  onClose={close}
>
  <input aria-label="Date" />
</Sheet>
```

```tsx
<IconButton label="Search" keys="Ctrl + F" icon={Search} onClick={openSearch} />
<Sheet title="Add a feed" description="The feed's address." onClose={close}>
  <input aria-label="Feed address" />
</Sheet>
```

### The scheme and the narrow window

`@frostyard/lodge-ui/theme` is framework-free, so the shell and Porter use
it too:

```ts
import { applyScheme, followLodge, isNarrow, onNarrowChange } from "@frostyard/lodge-ui/theme";

const stop = followLodge(lodge); // the shell's scheme, and every change of it
applyScheme(document.documentElement, null); // back to the system's scheme
```

## Rules

1. **The CSS custom properties are the contract.** Their names are
   frostmail's, and the Tailwind utilities are the same names the apps
   already use.
2. **No preflight, and no import of `tailwindcss`.** The app owns its
   preflight (frostnotes ADR-0025), and this package never imports
   Tailwind. `test/rules.test.ts` checks it.
3. **No React in the CSS or in `./theme`.** The shell and Porter are
   vanilla TypeScript, and `test/rules.test.ts` checks it.
4. **A token change is a release.** Before 1.0, a value change is a minor
   and says so in the changelog; a new token is a minor; a removed or
   renamed token is a major.
5. **No runtime dependency but the font.** React and lucide-react are
   optional peers.
6. **Every app bundles its own copy.** lodge serves each app's web build
   from disk, and no daemon serves a shared stylesheet
   ([ADR-0007](../docs/adr/0007-lodge-serves-every-apps-web-build-from-disk.md)).

## Building and testing

Only in the nsl machine (`make ui-check`; [dev/nsl/provision.sh](../dev/nsl/provision.sh)),
never on the host:

```sh
nsl run -m lodge sh -lc 'cd ui && pnpm install && pnpm run check'
```

`pnpm run check` is Biome, the type check and the tests. `make ui-fmt`
formats.
