# 0018 — A UI package beside the client

- **Status:** Accepted (Brian, 2026-10-21)
- **Date:** 2026-10-21

## Context

Frostmail and frostnotes look like one family because frostnotes copied
frostmail's stylesheet. The two `tokens.css` files share 96 lines of about
110, the Tailwind theme block is the same for 36 lines, and the base layer
matches. frostnotes said what would end that
([frostnotes ADR-0011](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0011-look-like-frostmails-sibling-with-obsidians-layout.md)):
"A shared package is worth it only when a third app needs the same pieces".

The third app is here. frostread, a feed reader, is built beside them, and
the suite's UI audit of 2026-10-21 found four palettes in it: frostmail's and frostnotes' shared one,
the shell's cream and green on `system-ui`, and Porter's white and indigo.
It also found the same button, splitter and dialog written twice, and the
two apps' copies already drifting.

The constraints on any shared look:

- **Each app bundles its own copy.** lodge serves every app's web build
  from disk and the app CSP allows its own path only, so nothing can be
  served once and shared at run time
  ([ADR-0007](0007-lodge-serves-every-apps-web-build-from-disk.md)).
- **The app owns its preflight.** frostnotes patches Tailwind's preflight
  and imports Tailwind's layers one by one, so a shared stylesheet must not
  import `tailwindcss` as a whole, and must carry no preflight
  ([frostnotes ADR-0025](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0025-patch-what-costs-the-editor-its-frames.md)).
- **`@frostyard/lodge-client` has no runtime dependencies.** The shell and
  Porter are vanilla TypeScript, and React or CSS in the client would land
  in every consumer of the frame protocol
  ([lodge-client.md](../specs/lodge-client.md)).
- **The shell and the Tauri builds count too.** An app's look must work
  without a lodge frame, and the shell's own page should be able to take
  the tokens without React.
- **The design system is the brand source, not an app's token set.** It is
  dark only, at marketing scale, and loads Inter from Google Fonts, which
  the CSP blocks.

## Decision

**A package `ui/` in the lodge repository, `@frostyard/lodge-ui`, holds
the look that the apps share.** The owner chose on 2026-10-21 to build it
as proposed.

- **Home and release.** It lives at `lodge/ui`, is tagged `ui/vX.Y.Z`, and
  an app pins it as a pnpm git dependency in that subdirectory:
  `github:frostyard/lodge#ui/v0.1.0&path:ui`. There is no registry and no
  CI token; a git tag is the release.
  - **This answers the roadmap's open question** of where the client is
    published: git tags, for both `client/vX.Y.Z` and `ui/vX.Y.Z`, as the
    apps already do.
- **Tokens first, with zero visual change.** 0.1.0 carries frostmail's
  values as they are, so that frostmail and frostnotes can move to it
  without a pixel moving. A change to a value is a later release of its
  own.
- **The CSS custom properties are the contract**, with frostmail's names:
  `--bg-window`, `--text-secondary`, `--separator`, `--accent` and the rest.
  They work in vanilla TypeScript and in Tailwind, and they are what a
  supported change of look edits.
- **The files are separate, so an app takes what it uses**: `tokens.css`
  (colours, both schemes, and the density tokens), `accents.css` (flags,
  quotes, avatars), `theme.css` (the Tailwind `@theme` map and the shared
  type roles, and the `narrow` variant), `base.css` (13/18 Inter, the focus
  ring, no selection), `fonts.css` (Inter, as a dependency of the package).
- **It carries no preflight and never imports `tailwindcss`.** An app
  imports `theme.css` after Tailwind's own layers.
- **The React primitives are a `./react` subpath**, and React and
  lucide-react are optional peer dependencies: the CSS and `./theme` need
  neither. 0.1.0 has `IconButton`, `Splitter`, `Sheet` and `useNarrow`.
- **`./theme` is framework-free**: the scheme from lodge's `theme` message,
  and the narrow query, for the shell and Porter as well as the apps.
- **Sharing happens at build time only.** Each app bundles its own copy of
  the CSS and the font, as ADR-0007 and the CSP require.
- **The design system stays the brand source** for the accent and state
  hues and for the icons. The look change comes later, as token values
  only.
- **The JSON-RPC session does not come here.** It goes to
  `@frostyard/lodge-client` as a dependency-free subpath, later: it is the
  counterpart of the kit's `kit/jsonrpc`
  ([ADR-0012](0012-the-kit-shares-the-rpc-runtime-not-the-generator.md)).

[specs/ui-package.md](../specs/ui-package.md) pins the exports, the token
names, the rules and how an app consumes it.

## Consequences

- One stylesheet, one set of names and one Tailwind theme for every app.
  A fix to a token lands once.
- **Migration is later work, per app.** frostmail replaces its
  `tokens.css` and its `@theme` block; frostnotes does the same and keeps
  its preflight, its editor styles and its own extras; the shell and Porter
  move to the tokens and Inter, which is the first visible change, and the
  shell's icons and manifest colours move with it. Each of those is its own
  change in its own repository, and none is done here.
- **While the apps migrate, two copies exist.** frostnotes keeps the six
  extras it owns until it moves; a token fixed in `ui/` must be fixed there
  too until then.
- **The version is a contract.** Before 1.0, a token value change is a
  minor and says so in the changelog, a new token is a minor, and a removed
  or renamed token is a major.
- **Density tokens reach only what is migrated.** frostmail has 461
  arbitrary Tailwind sizes; `--control-h` and the others change nothing in
  a component that does not use them.
- **The package must keep working outside lodge**, for the Tauri builds:
  `theme.ts` assumes no `Lodge`, and `applyScheme` takes any root.
- **A shared asset served at run time stays impossible**, for as long as
  each app's CSP allows only its own path.

## Alternatives considered

- **A. Extend `@frostyard/lodge-client`:** it would break the client's "no
  runtime dependencies", and React and CSS would land in the shell and
  Porter. The client is the right home for the JSON-RPC session, not for
  this.
- **C. The frostyard design system:** a marketing site's tokens, dark only,
  with no package, no gates and Google Fonts. It stays the brand source.
- **D. A new repository, `frostyard/ui`:** its own tags, nsl machine, CI
  and workflowcheck, all for three in-suite consumers. It can move there
  later if an app outside lodge needs it.
- **E. Publish to npm or GitHub Packages:** registry auth inside the nsl
  machine, and a token in CI, while git tags work.
- **F. A vendored copy per app (the status quo):** the drift is already
  visible in the menus and dialogs, and every app pays it again.

## References

- Shapes: [specs/ui-package.md](../specs/ui-package.md), [design/overview.md](../design/overview.md#the-kit-and-the-client), the roadmap's [later work](../plans/0001-roadmap.md)
- Builds on: [ADR-0007](0007-lodge-serves-every-apps-web-build-from-disk.md), [ADR-0012](0012-the-kit-shares-the-rpc-runtime-not-the-generator.md)
- Extended by: [ADR-0019](0019-the-accent-is-frostyards-cold-blue.md) (the accent's values)
- External: frostnotes [ADR-0011](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0011-look-like-frostmails-sibling-with-obsidians-layout.md), frostnotes [ADR-0025](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0025-patch-what-costs-the-editor-its-frames.md)
