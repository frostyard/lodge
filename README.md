<!-- readme-kind: service -->
# lodge

[![CI](https://github.com/frostyard/lodge/actions/workflows/ci.yml/badge.svg)](https://github.com/frostyard/lodge/actions/workflows/ci.yml)

lodge is a tailnet-only web shell for frostyard's first-party apps: Notes,
Mail, Read and the assistant Porter, for the people on one owner's tailnet.
One address opens them all, a quick switcher moves between them, and each
app stays loaded in its frame. It runs on 1 Linux host, beside the apps'
daemons.

**Status:** pre-release. Notes, Mail, Read and Porter run behind it.

**Documentation:** [docs/README.md](docs/README.md)

## What it does

- **lodge is the only process on the network.** It joins your tailnet as a
  node of its own and signs browsers in by their tailnet identity, against
  allow-lists of logins and devices. Nothing is on the public internet
  ([overview](docs/design/overview.md)).
- **Apps are daemons behind it.** Each one runs as its own system user on a
  Unix socket that only lodge can reach. lodge serves each app's web files
  itself, and tells the daemon who is calling
  ([app contract](docs/specs/app-contract.md)).
- **Agents get a scoped token per app,** at `/mcp/<app>`, and agents on
  lodge's host use a local agent socket
  ([lodge's HTTP surface](docs/specs/lodge-http.md#mcpid)).
- **Untrusted bytes,** such as mail parts and attachments, come from a
  second node, `lodge-content`, under a sandbox policy
  ([content routes](docs/specs/lodge-http.md#content-routes)).
- **One inference layer** routes the apps' model requests to a local model
  or to configured providers, and holds every credential
  ([inference](docs/specs/inference.md)).
- **The apps build on lodge's libraries:** the Go kit (`kit/`), the
  frontend library `@frostyard/lodge-client` (`client/`) and the shared
  look `@frostyard/lodge-ui` (`ui/`), each pinned by its own tag
  ([AGENTS.md](AGENTS.md#releases-and-dependencies)).

## Interface

| Route | What it serves |
| --- | --- |
| `/`, `/<id>/<route>` | the shell page, with each app in its frame |
| `/app/<id>/api` | the app's JSON-RPC session, over a WebSocket |
| `/app/<id>/<path>` | the app's web files |
| `/mcp/<id>` | the app's MCP endpoint, for an agent with a token |
| `/device/<id>/<path>` | the app's own device routes |
| `lodge-content`: `/<id>/<path>` | the app's untrusted content, sandboxed |

[lodge's HTTP surface](docs/specs/lodge-http.md) is the contract with
browsers and agents, and the [app contract](docs/specs/app-contract.md) the
contract with apps. `lodgectl` manages lodge over its control socket.

## Deploy

lodge runs on 1 systemd host as the `frostyard-lodge` package, with each
app's daemon beside it. A release is a `vX.Y.Z` tag: its workflow builds,
attests and publishes the `.deb`, and snosi publishes it as the sysext
`lodge`. A host takes that sysext, or installs a release by hand.
[packaging/README.md](packaging/README.md) is the runbook: install,
[update](packaging/README.md#update),
[roll back](packaging/README.md#roll-back) and
[release](packaging/README.md#release).

## Develop

```sh
mise install   # the pinned golangci-lint, svu and nFPM
make check     # format, tidy, vet, lint and tests (and the docs' links)
make ci        # check's verify, the race detector, cross-builds, frontends (nsl)
```

Go 1.27 runs on the host; the frontends build in the nsl machine `lodge`
(`dev/nsl/provision.sh`). `make help` lists every target.

## Where to go next

- [AGENTS.md](AGENTS.md): the rules for agents and contributors.
- [docs/README.md](docs/README.md): the decisions, designs, specs and plans.
- [SECURITY.md](SECURITY.md): report a vulnerability privately.
- [LICENSE](LICENSE): MIT.
