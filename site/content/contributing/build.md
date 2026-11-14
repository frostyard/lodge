# Build from source

lodge is Go on the host, and TypeScript for the client library, the UI
package and the shell. The Go code builds on the host. The frontends build
in the nsl machine `lodge` (`dev/nsl/provision.sh`), never on the host.

## Build and check

```sh
mise install   # the pinned golangci-lint, svu and nFPM
make check     # format, tidy, vet, lint and tests (and the docs' links)
make ci        # check's verify, the race detector, cross-builds, frontends (nsl)
```

`make help` lists every target. `make build` builds lodge into `build/`, and
`make shell-build` builds the shell in the nsl machine.

## The documentation site

This site lives in `site/`. Build it and preview it:

```sh
make site        # strict build into site/dist
make site-serve  # preview at http://127.0.0.1:8000/lodge/
```

The site needs `python3`. It installs the locked requirements in
`site/requirements.txt` into a virtual environment at `build/site-venv`. No
gate (`make check`, `make ci`) builds the site.

## Write for users

Pages live in `site/content/`. A page says what a user does, not how the
code works. The maintainers' contracts (ADRs, designs, specs and plans)
stay in `docs/`. A change to user-visible behavior updates the affected
page in the same pull request.

Read
[AGENTS.md](https://github.com/frostyard/lodge/blob/main/AGENTS.md) for the
repository's rules, and the
[documentation index](https://github.com/frostyard/lodge/blob/main/docs/README.md)
for the decisions, designs, specs and plans.
