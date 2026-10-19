# lodge

A tailnet-only web shell for frostyard's first-party apps. One address on
your tailnet opens Notes, Mail and, later, an assistant. A quick switcher
moves between them, and each app stays loaded in its frame
([overview](docs/design/overview.md)).

- **lodge is the only process on the network.** It joins your tailnet as a
  node of its own, `lodge`, and signs browsers in by their tailnet identity.
  Allow-lists of logins and devices decide who gets in. There are no
  passwords and no cookies, and nothing is on the public internet.
- **Apps are daemons behind it.** Each one runs as its own system user on a
  Unix socket that only lodge can reach. lodge serves each app's web files
  itself, and tells the daemon who is calling.
- **Agents get a scoped token per app,** at `/mcp/<app>`.
- **Untrusted bytes,** such as mail parts and attachments, come from a
  second node, `lodge-content`, under a sandbox policy.
- **One inference layer** routes the apps' model requests to a local model
  or to configured providers, and holds every credential.

Apps build on the Go kit (`kit/`) and the frontend library
`@frostyard/lodge-client` (`client/`). The contract between lodge and an app
is [docs/specs/app-contract.md](docs/specs/app-contract.md).

Pre-release. The design documents date from 2026-10-18 and are Proposed.
The kit and the client are released as `kit/v0.1.0` and `client/v0.1.0`,
and lodge v1 and its inference layer are built. The order of work is the
[roadmap](docs/plans/0001-roadmap.md).

## Develop

Requirements: Go 1.27 and `mise install` (golangci-lint) on the host.

```sh
make check   # format, tidy, vet, lint, tests (and the docs' links)
make ci      # check's verify, plus the race detector and cross-builds
make build   # build/lodge
```

`make help` lists every target. Agents start at [AGENTS.md](AGENTS.md).

## License

MIT.
