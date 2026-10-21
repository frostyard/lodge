.PHONY: help build e2e-build dist install fmt lint lint-version-check test race cross verify check ci clean client-check client-fmt ui-check ui-fmt shell-check shell-fmt shell-build

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

# golangci-lint's release, read from mise.toml (frostyard/core ADR-0043).
GOLANGCI_LINT_VERSION := $(strip $(shell sed -n 's/^golangci-lint = "\(.*\)"/\1/p' mise.toml))
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
