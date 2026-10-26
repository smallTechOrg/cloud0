# cloud0 — build, run and gate targets. Run from the repo root.
# GO111MODULE=off is persisted in the user's global Go env on this machine and
# GOTOOLCHAIN=local keeps Go 1.23.3 from auto-downloading a newer toolchain.
export GO111MODULE=on
export GOTOOLCHAIN=local

GO   ?= /opt/homebrew/bin/go
# pnpm must run on the pinned Node 22 LTS. The asdf `pnpm` shim is bound to a
# newer Node (parent-directory .tool-versions) and pnpm 11 ignores
# use-node-version, so run pnpm's entry file with the pinned node directly.
# Override with `make <target> PNPM=pnpm` if your `pnpm` already runs on Node 22.
NODE     ?= /Users/sai/.local/bin/node
PNPM_BIN ?= $(shell asdf which pnpm 2>/dev/null || command -v pnpm)
PNPM     ?= $(NODE) $(PNPM_BIN)
BIN  := bin/cloud0
DIST := internal/webui/dist

.PHONY: web-install web-build web build build-go migrate run vet test web-test e2e gate scaffold-gate clean

## Frontend
web-install:
	cd web && $(PNPM) install --frozen-lockfile

web-build:
	cd web && $(PNPM) build
	find $(DIST) -mindepth 1 -not -name .gitkeep -delete
	cp -R web/out/. $(DIST)/
	test -f $(DIST)/index.html

web: web-install web-build

## Backend
build-go:
	$(GO) build -o $(BIN) ./cmd/cloud0

build: web build-go

migrate:
	./$(BIN) migrate up
	./$(BIN) migrate status

run:
	./$(BIN) serve

vet:
	$(GO) vet ./...

test: vet
	$(GO) test ./internal/... -count=1 -timeout 15m
	$(GO) test ./tests/... -count=1 -p 1 -timeout 15m

## Frontend tests
web-test:
	cd web && $(PNPM) test

e2e:
	cd web && $(PNPM) exec playwright test

## Gates (spec/roadmap.md)
scaffold-gate: build migrate test

# Phase 1 gate: build -> migrate up/status -> vet + internal + tests/{scaffold,smoke}
# (serialised, real BigQuery) -> Vitest -> Playwright journey.
gate: build migrate test web-test e2e

clean:
	rm -rf bin web/out web/.next
	find $(DIST) -mindepth 1 -not -name .gitkeep -delete
