# 0025 — Allow WebAssembly in app frames

- **Status:** Proposed
- **Date:** 2026-11-23

## Context

FrostNotes’ shared desktop and browser editor compiles to WebAssembly. Lodge serves Wasm files with the appropriate media type, but the frame policy also needs to permit browser compilation.

## Decision

Include `'wasm-unsafe-eval'` in every app frame’s script policy. All apps receive this capability through the existing contract. Bundle scripts and stylesheets continue to load from the app’s installed assets directory.

## Consequences

Trusted app code can instantiate modules from bytes it supplies. App authors therefore control which bytes reach the compiler. JavaScript string evaluation still requires a separate permission. The shell and the content origin keep their existing policies.

## Alternatives considered

An opt-in manifest field would require installers and the gateway to negotiate a capability that Lodge intends to offer to every app.

## References

- [Gateway design](../design/overview.md)
- [HTTP frame policy](../specs/lodge-http.md#headers)
- [Installed app bundle](../specs/app-contract.md#install-layout)
