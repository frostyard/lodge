.PHONY: help build e2e-build dist install fmt lint lint-version-check test race cross verify check ci clean client-check client-fmt ui-check ui-fmt shell-check shell-fmt shell-build vulncheck deb deb-check bump svu-version-check

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

# golangci-lint's release, read from mise.toml (frostyard/core ADR-0043).
GOLANGCI_LINT_VERSION := $(strip $(shell sed -n 's/^golangci-lint = "\(.*\)"/\1/p' mise.toml))
# govulncheck's release; the Go checksum database verifies the module.
GOVULNCHECK_VERSION := v1.9.0
# svu's release, read from mise.toml; make bump tags releases with it.
SVU_VERSION := $(strip $(shell sed -n 's/^svu = "\(.*\)"/\1/p' mise.toml))
SVU ?= svu
# The Go modules: lodge, and the kit app daemons import (kit/go.mod). Every
# gate runs in each.
MODULES := . kit
IN_MODULES = for m in $(MODULES); do (cd $$m && $(1)) || exit 1; done
# Tracked or new Go files, skipping files deleted but not yet staged.
GO_FILES = $(shell git ls-files --cached --others --exclude-standard '*.go' | while read -r f; do [ -e "$$f" ] && echo "$$f"; done)

help: ## List targets
	@grep -E '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | sed 's/:.*## /\t/' | expand -t 18

build: ## Build lodge into build/
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o build/ ./cmd/...

# The development build (docs/specs/lodge-http.md, The development build):
# loopback only, one fixed identity, no tailnet. Never install it.
e2e-build: ## Build build/lodge-e2e (e2e tag), build/lodgectl and build/lodge-fakeapp
	CGO_ENABLED=0 go build -trimpath -tags e2e -ldflags '$(LDFLAGS)' -o build/lodge-e2e ./cmd/lodge
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o build/lodgectl ./cmd/lodgectl
	CGO_ENABLED=0 go build -trimpath -o build/lodge-fakeapp ./dev/fakeapp

# A tarball to install on a host (packaging/install.sh), for linux/DIST_ARCH:
# the binaries, the shell's bundle and the packaging. The shell builds in
# the nsl machine first (make shell-build).
DIST_ARCH ?= amd64
DIST_NAME = lodge-$(VERSION)-linux-$(DIST_ARCH)

dist: ## Package lodge for linux/DIST_ARCH (amd64) into dist/, after make shell-build
	@test -f shell/dist/index.html || { echo "shell/dist is not built: run make shell-build (nsl)"; exit 1; }
	rm -rf dist/$(DIST_NAME) dist/$(DIST_NAME).tar.gz
	mkdir -p dist/$(DIST_NAME)/bin dist/$(DIST_NAME)/packaging
	CGO_ENABLED=0 GOOS=linux GOARCH=$(DIST_ARCH) go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(DIST_NAME)/bin/ ./cmd/lodge ./cmd/lodgectl
	cp -R shell/dist dist/$(DIST_NAME)/shell
	cp -R packaging/install.sh packaging/config.example.json packaging/systemd packaging/sysusers.d packaging/tmpfiles.d packaging/app-template dist/$(DIST_NAME)/packaging/
	tar -C dist -czf dist/$(DIST_NAME).tar.gz $(DIST_NAME)
	@echo "dist/$(DIST_NAME).tar.gz: unpack it on the host and run sudo ./$(DIST_NAME)/packaging/install.sh"

# The Debian package (frostyard/core ADR-0011, ADR-0012, ADR-0058). A snapshot
# version starts with a literal 0.0.0-; a release passes DEB_VERSION=X.Y.Z (the
# tag without v). The tree is staged from scratch in build/deb/, apart from a
# host build in build/. nFPM is pinned in mise.toml.
DEB_VERSION ?= 0.0.0-$(COMMIT)-snapshot
DEB_FILE = dist/frostyard-lodge_$(DEB_VERSION)_amd64.deb
DEB_LDFLAGS = -s -w -X main.version=$(DEB_VERSION) -X main.commit=$(COMMIT)

deb: ## Build dist/frostyard-lodge_<DEB_VERSION>_amd64.deb with nFPM, after make shell-build
	@test -f shell/dist/index.html || { echo "shell/dist is not built: run make shell-build (nsl)"; exit 1; }
	rm -rf build/deb
	mkdir -p build/deb/bin dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '$(DEB_LDFLAGS)' -o build/deb/bin/ ./cmd/lodge ./cmd/lodgectl
	packaging/install.sh DESTDIR="$(CURDIR)/build/deb/root" ACTIVATE=no BIN_SRC="$(CURDIR)/build/deb/bin"
	chmod -R u=rwX,go=rX build/deb/root
	DEB_VERSION="$(DEB_VERSION)" nfpm package -p deb -f packaging/nfpm.yaml -t dist/
	$(MAKE) --no-print-directory deb-check DEB_VERSION="$(DEB_VERSION)"

deb-check: ## Check the .deb: metadata, only /usr paths, root ownership, no writable entries, the payload
	packaging/deb-check.py "$(DEB_FILE)" frostyard-lodge "$(DEB_VERSION)" \
		--exec /usr/bin/lodge --exec /usr/bin/lodgectl \
		--file /usr/lib/systemd/system/lodge.service \
		--file /usr/lib/sysusers.d/lodge.conf \
		--file /usr/lib/tmpfiles.d/lodge.conf \
		--file /usr/share/lodge/shell/index.html \
		--file /usr/share/lodge/config.example.json

# make install DESTDIR=… PREFIX=… (see packaging/install.sh), from build/
# and shell/dist/.
install: build ## Install lodge from build/ and shell/dist/ (DESTDIR, PREFIX, BINDIR)
	packaging/install.sh DESTDIR="$(DESTDIR)" PREFIX="$(PREFIX)" BINDIR="$(BINDIR)"

fmt: ## Format Go code
	gofmt -w $(GO_FILES)

# The development build's files carry the e2e tag, so they are linted,
# vetted and tested with it as well.
E2E_PKGS := ./cmd/lodge/... ./internal/e2eid/...

lint: lint-version-check ## Run the pinned golangci-lint
	$(call IN_MODULES,golangci-lint run)
	golangci-lint run --build-tags e2e $(E2E_PKGS)

lint-version-check:
	@test -n "$(GOLANGCI_LINT_VERSION)" || { echo "mise.toml pins no golangci-lint"; exit 1; }
	@installed="$$(golangci-lint version --short 2>/dev/null)" || { \
		echo "golangci-lint $(GOLANGCI_LINT_VERSION) is required (install with: mise install)"; exit 1; }; \
	if [ "$$installed" != "$(GOLANGCI_LINT_VERSION)" ]; then \
		echo "expected golangci-lint $(GOLANGCI_LINT_VERSION), found $$installed (install with: mise install)"; exit 1; \
	fi

# Every hermetic test runs, with no name filter (frostyard/core ADR-0038).
test: ## Run the Go tests
	$(call IN_MODULES,go test ./...)
	go test -tags e2e ./internal/e2eid/...

race: ## Run the Go tests under the race detector
	$(call IN_MODULES,go test -race ./...)

# lodge is pure Go (CGO_ENABLED=0); both server architectures must build.
cross: ## Build every package for linux/amd64 and linux/arm64
	$(call IN_MODULES,CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...)
	$(call IN_MODULES,CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...)

# The gate triad (frostyard/core ADR-0043, ADR-0044). verify is credential-free
# and leaves a clean checkout clean; check formats first; ci adds the race
# detector, the cross-builds and the frontend checks.
verify: ## Tidy, vet, format, lint and tests
	$(call IN_MODULES,go mod tidy -diff)
	$(call IN_MODULES,go vet ./...)
	go vet -tags e2e $(E2E_PKGS)
	test -z "$$(gofmt -l $(GO_FILES))"
	$(MAKE) --no-print-directory lint
	$(MAKE) --no-print-directory test

# Needs the network, and a new upstream advisory can fail it, so it is in no
# gate (check, verify, ci); CI runs it as its own job.
vulncheck: ## Report reachable vulnerabilities (govulncheck; needs the network)
	$(call IN_MODULES,go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...)

check: fmt verify ## Format, then verify (the developer gate)

ci: verify race cross client-check ui-check shell-check ## verify plus the race detector, cross-builds, client/, ui/ and shell/ (nsl)

clean: ## Remove build output
	rm -rf build dist coverage.out coverage.html

# The frontends build and test in the nsl machine (dev/nsl/provision.sh),
# never on the host; CI, whose runner has Node, runs the same targets with
# IN_NSL='sh -c'.
NSL_MACHINE ?= lodge
IN_NSL ?= nsl run -m $(NSL_MACHINE) sh -lc

client-check: ## Lint (Biome), typecheck and test client/ (nsl)
	$(IN_NSL) 'cd client && pnpm install --frozen-lockfile --silent && pnpm run check'

client-fmt: ## Format client/ and organize its imports with Biome (nsl)
	$(IN_NSL) 'cd client && pnpm install --frozen-lockfile --silent && pnpm run fmt'

ui-check: ## Lint (Biome), typecheck and test ui/ (nsl)
	$(IN_NSL) 'cd ui && pnpm install --frozen-lockfile --silent && pnpm run check'

ui-fmt: ## Format ui/ and organize its imports with Biome (nsl)
	$(IN_NSL) 'cd ui && pnpm install --frozen-lockfile --silent && pnpm run fmt'

# The shell (docs/specs/shell.md) uses client/'s shell half through a link:
# dependency, and builds into shell/dist/, which installs at
# /usr/share/lodge/shell/.
shell-check: ## Lint (Biome), typecheck and test shell/ (nsl)
	$(IN_NSL) 'cd shell && pnpm install --frozen-lockfile --silent && pnpm run check'

shell-fmt: ## Format shell/ and organize its imports with Biome (nsl)
	$(IN_NSL) 'cd shell && pnpm install --frozen-lockfile --silent && pnpm run fmt'

shell-build: ## Build the shell into shell/dist/ (nsl)
	$(IN_NSL) 'cd shell && pnpm install --frozen-lockfile --silent && pnpm run build'

# Pinned svu, from mise.toml (core ADR-0043). `svu --version` prints a
# "GitVersion:    MAJOR.MINOR.PATCH" line.
svu-version-check:
	@test -n "$(SVU_VERSION)" || { echo "mise.toml pins no svu"; exit 1; }
	@if ! command -v "$(SVU)" >/dev/null 2>&1; then \
		echo "svu $(SVU_VERSION) is required for make bump (not installed; run: mise install)"; \
		exit 1; \
	fi; \
	installed="$$($(SVU) --version 2>&1 | sed -n 's/^GitVersion:[[:space:]]*//p' | head -1)"; \
	if [ -z "$$installed" ]; then installed=unknown; fi; \
	if [ "$$installed" != "$(SVU_VERSION)" ]; then \
		echo "expected svu $(SVU_VERSION), found $$installed (run: mise install)"; \
		exit 1; \
	fi

# The only sanctioned way to tag a release (core ADR-0012): the tag's
# workflow builds, attests and publishes the .deb.
# svu reads only vX.Y.Z tags (.svu.yml's tag.pattern) and fails when there is
# none, so the first release is v0.1.0.
bump: ## Tag the next version with svu and push the tag (main, clean, equal to origin/main; runs make check)
	@$(MAKE) --no-print-directory svu-version-check
	@test "$$(git rev-parse --abbrev-ref HEAD)" = main || { echo "make bump runs on main"; exit 1; }
	@git fetch --tags origin main
	@test "$$(git rev-parse HEAD)" = "$$(git rev-parse origin/main)" || { echo "main is not origin/main: pull or push first"; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "the working tree is not clean: commit or stash first"; exit 1; }
	@$(MAKE) --no-print-directory check
	@test -z "$$(git status --porcelain)" || { echo "make check changed the tree: commit that first"; exit 1; }
	@set -eu; \
	version=$$(if [ -z "$$(git tag -l 'v[0-9]*')" ]; then echo v0.1.0; else $(SVU) next; fi); \
	printf '%s\n' "$$version" | grep -Eqx 'v[0-9]+\.[0-9]+\.[0-9]+' || { echo "svu next gave '$$version', not vX.Y.Z"; exit 1; }; \
	git tag -a "$$version" -m "Version $$version"; \
	echo "Tagged $$version; pushing it to origin..."; \
	git push origin "refs/tags/$$version"
