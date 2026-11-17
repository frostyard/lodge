# notelite: hearth's example app, copied

This directory is a copy of [frostyard/hearth](https://github.com/frostyard/hearth)'s
`examples/notes-lite`, at tag `v0.2.0` (commit `723cbc9`). hearth is MIT
licensed; its `LICENSE` covers this copy.

**Why it is copied.** The example is its own Go module (`example.com/notes-lite`),
so lodge cannot import it as a dependency. lodge needs a real app on hearth for
its tests and for the development build: 1 that serves the Connect protocol, the
generated permission checks and the tool catalog, so that the MCP gateway, the
`/app/<id>/connect/` route and the manifest and catalog loaders have something
real to talk to. lodge still depends on no hearth *code* outside this directory
and `dev/hearthapp` ([internal/repocheck](../../repocheck)'s test holds that).

**What was changed**, and nothing else:

- `notes.go`: the package is `notelite` (it was `notes`), its doc comment names
  it, and it imports the generated package by this module's path
  (`github.com/frostyard/lodge/internal/hearthtest/notelite/notesv1`) instead of
  `example.com/notes-lite/gen/example/notes/v1`.
- `manifest.go` is new: it embeds `manifest.json`.
- `notesv1/` holds the 4 generated files of
  `gen/example/notes/v1` byte for byte, and `manifest.json` is
  `gen/manifest.json` byte for byte. `notesv1/notesv1connect/notes.connect.go`
  is the generated Connect route, with only the v1 import changed to
  `github.com/frostyard/lodge/internal/hearthtest/notelite/notesv1`.
- The example's own tests, `cmd/`, `proto/` and `Makefile` are not copied.

`internal/hearthtest` serves it on an app's lodge socket: hearth's adapter, the
generated Connect route, `GET /health`, and the tool catalog from
`notesv1.NoteServiceTools`.
