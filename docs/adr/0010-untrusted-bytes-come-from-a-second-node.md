# 0010 — Untrusted bytes come from a second node

- **Status:** Proposed
- **Date:** 2026-10-18

## Context

Apps show bytes that nobody vetted:

- mail's HTML bodies, inline parts and attachments, from anyone who can
  send mail;
- notes' attachments: PDFs, SVGs, HTML files and images synced from
  devices, or written by agents.

Shown on the lodge origin, such a file runs with every app's rights
([ADR-0009](0009-apps-switch-in-frames-that-stay-loaded.md)). An SVG or HTML
file with script, a type a browser sniffs, or a viewer that runs a
document's code would all act as the user.

The desktop apps contain this themselves. frostmail's reader is a
sandboxed frame with embedded images and no network
([frostmail ADR-0014](https://github.com/frostyard/frostmail/blob/main/docs/adr/0014-images-embedded-in-the-reader-frame.md)).
frostnotes' M4 draft served notes' files by hash on the hub's origin under
a `sandbox` policy. In a browser, a policy is one layer. A second origin
puts every bypass of that layer outside the apps.

On 2026-10-18 the user decided that a second tsnet node, `lodge-content`,
serves untrusted bytes.

## Decision

- **`lodge-content` is a second tailnet node in the lodge process,** with
  its own name, certificate and state
  ([ADR-0004](0004-lodge-stays-on-the-tailnet-and-joins-it-itself.md)). Its
  origin, written `CONTENT` (`https://lodge-content.<tailnet>.ts.net`), is
  cross-origin to `LODGE` and on the same site.
- **It serves only apps that set `"content": true`,** at
  `https://CONTENT/<id>/<path>`. lodge proxies that path to the app's
  `GET /content/<path>`, query included, as caller `content`. Only `GET`
  and `HEAD` are served.
- **Every content request is signed in as a browser's is**
  ([ADR-0005](0005-browsers-sign-in-with-their-tailnet-identity.md)). WhoIs
  must name the peer from its own address, on an untagged node whose login
  and node are allowed. The request carries the same identity headers.
  There are no cookies or tokens, so the content origin never shares a
  session with lodge's.
- **Every content response is sandboxed.** lodge adds its own policy:
  `default-src 'none'`, images only from `data:`, inline styles, `sandbox`,
  and `frame-ancestors https://LODGE`. It also adds `nosniff`,
  `Referrer-Policy: no-referrer` and `Cross-Origin-Resource-Policy:
  same-site`. An app's own policy is kept beside lodge's, so it can only
  narrow it. No script runs in a content document, and only lodge's pages
  can frame one.
- **Apps learn the content origin from lodge-client.** The shell's `init`
  message carries `contentOrigin`, and lodge-client's `contentURL(path)`
  returns `https://CONTENT/<id>/<path>`
  ([specs/lodge-client.md](../specs/lodge-client.md)). For an app with
  `content: true`, its frame's policy allows `CONTENT` in `img-src`,
  `media-src` and `frame-src`, whatever `remoteImages` says.
- **A content URL may open as a top-level page in a new tab,** for a PDF
  or a full-size image. It is still sandboxed there.
- **PDFs get no exception yet.** If Chrome's viewer refuses a sandboxed
  PDF (M4 review item 12, to verify in the roadmap's Phase 2), the fix is
  an exception for `application/pdf` that keeps `default-src 'none'` and
  drops `sandbox`. It would be pinned in
  [specs/lodge-http.md](../specs/lodge-http.md#content-routes) only after
  that check. Until then apps offer a download link as the fallback (notes
  serves `?download=1`).

## Consequences

- A file that escapes the sandbox, through a browser bug, a sniffed type
  or a viewer, lands on an origin that holds no session, no storage and no
  app. It cannot script the shell or any app's frame.
- **Other nodes on the tailnet are the same site.** Their pages could embed
  content URLs as images or media (`same-site` resource policy) if they
  knew the URLs, and the user's identity would carry. Content paths should
  name things by unguessable keys, such as notes' hashes, and the content
  is never framed outside lodge.
- The tailnet gains a second node and a second certificate in the
  Certificate Transparency logs, and the owner allows nothing extra: the
  same allow-lists apply.
- Apps with untrusted content serve it on a path of their own,
  `/content/…`, which admits only caller `content`.

## Alternatives considered

- **The lodge origin with a `sandbox` policy alone,** as M4's draft had for
  notes' files: one layer between a hostile file and every app's session.
- **A second port on the `lodge` node:** a different origin with one name.
  It breaks ADR-0004's single port, and tailnet policy, logs and policies
  would all have to name a port of the node that serves the apps.
- **`srcdoc` in a sandboxed frame,** with no URL: this works for an HTML
  body. But attachments opened in a tab, media and full-size images still
  need a URL, and that URL would be on the lodge origin.
- **A public content domain:** outside the tailnet, ruled out by
  ADR-0004.

## References

- Shapes: [specs/lodge-http.md](../specs/lodge-http.md#content-routes), [specs/app-contract.md](../specs/app-contract.md#public-urls), [specs/lodge-client.md](../specs/lodge-client.md), [design/overview.md](../design/overview.md#untrusted-content), [roadmap — Phase 2](../plans/0001-roadmap.md#phase-2--lodge-v1-with-notes-as-its-only-app), [Phase 4](../plans/0001-roadmap.md#phase-4--mail-in-lodge)
- Builds on: [ADR-0004](0004-lodge-stays-on-the-tailnet-and-joins-it-itself.md), [ADR-0005](0005-browsers-sign-in-with-their-tailnet-identity.md), [ADR-0009](0009-apps-switch-in-frames-that-stay-loaded.md)
- Related: frostmail [ADR-0005](https://github.com/frostyard/frostmail/blob/main/docs/adr/0005-html-mail-rendering.md), [ADR-0014](https://github.com/frostyard/frostmail/blob/main/docs/adr/0014-images-embedded-in-the-reader-frame.md); frostnotes [ADR-0023](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0023-the-app-shows-files-by-their-hash-and-uploads-them-through-notesd.md)
