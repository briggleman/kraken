# Kraken developer commands. See CLAUDE.md for the full workflow.
#
# Common targets:
#   make            build web + all Go binaries (default)
#   make check      everything CI runs (fmt · vet · staticcheck · test)
#   make db-up      start Postgres (persistent, safe to re-run)
#   make dev-panel  go run ./cmd/panel
#   make dev-agent  go run ./cmd/agent
#   make dev-web    Vite dev server (:5173, proxies /api → :8080)
#   make images     build Panel + Agent Docker images locally
#   make up         docker compose full stack (Postgres + Panel + Agent)
#   make clean      remove bin/ and generated web assets
#
# Windows: install make with `winget install GnuWin32.Make` or use WSL.
# The recipes assume a POSIX shell (bash/sh); on Windows run them from
# Git Bash or WSL.

SHELL := /bin/sh

# Version stamping — mirrors the release-binaries workflow so a local
# `make build` produces a binary that reports the same tag/commit/date the
# release build would.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
MOD     := github.com/briggleman/kraken/internal/shared/version
LDFLAGS := -s -w \
           -X $(MOD).Version=$(VERSION) \
           -X $(MOD).Commit=$(COMMIT) \
           -X $(MOD).Date=$(DATE)

.DEFAULT_GOAL := build
.PHONY: help build build-web build-go proto \
        test test-race fmt vet staticcheck lint check \
        db-up db-down db-reset \
        dev-panel dev-agent dev-web seed \
        image-panel image-agent images \
        up down \
        clean version

# ---- build ---------------------------------------------------------------

## Build the web bundle so //go:embed picks up real assets.
build-web:
	npm --prefix web ci
	npm --prefix web run build

## Build all three Go binaries into bin/.
build-go:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

## Build web + all binaries. The default target.
build: build-web build-go

## Regenerate gRPC bindings from proto/.
proto:
	./scripts/genproto.sh

# ---- test / lint ---------------------------------------------------------

test:
	go test ./...

test-race:
	go test -race ./...

## Fail if any file needs gofmt. Matches the CI check.
fmt:
	@out="$$(gofmt -l internal cmd)"; \
	if [ -n "$$out" ]; then \
		echo "these files need gofmt -w:"; echo "$$out"; exit 1; \
	fi

vet:
	go vet ./...

## Install staticcheck on demand, then run it.
staticcheck:
	@command -v staticcheck >/dev/null 2>&1 || go install honnef.co/go/tools/cmd/staticcheck@latest
	staticcheck ./...

lint: fmt vet staticcheck

## Everything CI runs (equivalent to a green ci.yml go job).
check: build-web fmt vet staticcheck test-race

# ---- postgres ------------------------------------------------------------

## Start Postgres. Persistent volume, safe to re-run.
db-up:
	docker compose -f deploy/docker-compose.yml up -d postgres

## Stop Postgres — keeps the pgdata volume so state survives.
db-down:
	docker compose -f deploy/docker-compose.yml down

## DESTRUCTIVE: wipe the pgdata volume — every server/spec/user gone.
db-reset:
	docker compose -f deploy/docker-compose.yml down -v

# ---- dev runs ------------------------------------------------------------

## Run the Panel (:8080). Env inherits from your shell.
dev-panel:
	go run ./cmd/panel

## Run the Agent (:9090). Env inherits from your shell.
dev-agent:
	go run ./cmd/agent

## Run the Vite dev server (:5173, HMR + /api proxy).
dev-web:
	npm --prefix web run dev

## Seed a demo node + Palworld spec + running server. Needs Panel + Agent up.
seed:
	./scripts/seed-dev.sh

# ---- docker images -------------------------------------------------------

image-panel:
	docker build -f deploy/panel.Dockerfile \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) \
		-t ghcr.io/briggleman/kraken-panel:dev .

image-agent:
	docker build -f deploy/agent.Dockerfile \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) \
		-t ghcr.io/briggleman/kraken-agent:dev .

images: image-panel image-agent

# ---- full stack (docker compose) -----------------------------------------

## Bring up the full stack (Postgres + Panel + Agent). Needs deploy/.env.
up:
	docker compose --env-file deploy/.env -f deploy/docker-compose.full.yml up -d

## Take the full stack down — keeps volumes.
down:
	docker compose --env-file deploy/.env -f deploy/docker-compose.full.yml down

# ---- housekeeping --------------------------------------------------------

## Remove bin/ and the generated web bundle (committed markers preserved).
clean:
	rm -rf bin/
	node web/scripts/clean-dist.mjs

## Print the version make would stamp into a build.
version:
	@echo "VERSION=$(VERSION)"
	@echo "COMMIT=$(COMMIT)"
	@echo "DATE=$(DATE)"

# ---- help ----------------------------------------------------------------

## List the annotated targets.
help:
	@awk '/^## / { doc=substr($$0, 4); next } \
	     /^[a-z][a-z0-9-]*:/ && doc { \
	       target=$$1; sub(":", "", target); \
	       printf "  \033[36m%-14s\033[0m %s\n", target, doc; doc="" \
	     }' $(MAKEFILE_LIST)
