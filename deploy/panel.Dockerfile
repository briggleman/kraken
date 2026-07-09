# syntax=docker/dockerfile:1
#
# Kraken Panel container image.
#
# Multi-stage build: Node compiles the React UI, Go compiles the Panel with
# the UI embedded via //go:embed (see internal/panel/webui). The runtime
# layer is distroless — one static binary, no shell, no package manager.
#
# Build context is the repo root:
#   docker build -f deploy/panel.Dockerfile -t ghcr.io/briggleman/kraken-panel:dev .

# ---- web build ----------------------------------------------------------
FROM node:20-alpine AS webbuild
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
# vite.config.ts writes into ../internal/panel/webui/dist, so we need that
# path to exist during the web build.
RUN mkdir -p /src/internal/panel/webui/dist
RUN npm run build

# ---- go build -----------------------------------------------------------
FROM golang:1.26-alpine AS gobuild
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY proto/ ./proto/
# Bring in the compiled web bundle so //go:embed picks it up.
COPY --from=webbuild /src/internal/panel/webui/dist ./internal/panel/webui/dist
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
ENV CGO_ENABLED=0
RUN go build -trimpath \
    -ldflags "-s -w \
      -X github.com/briggleman/kraken/internal/shared/version.Version=${VERSION} \
      -X github.com/briggleman/kraken/internal/shared/version.Commit=${COMMIT} \
      -X github.com/briggleman/kraken/internal/shared/version.Date=${DATE}" \
    -o /out/panel ./cmd/panel

# Empty state-dir stamp used as the COPY source below to pre-create the
# runtime's /var/lib/kraken with nonroot ownership. Distroless has no
# shell so a plain `RUN mkdir` in the runtime stage isn't possible;
# COPY --chown is the idiomatic workaround.
RUN mkdir -p /out/state

# ---- runtime ------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="kraken-panel" \
      org.opencontainers.image.description="Kraken control-plane API + embedded web UI." \
      org.opencontainers.image.source="https://github.com/briggleman/kraken" \
      org.opencontainers.image.licenses="GPL-3.0"
COPY --from=gobuild /out/panel /panel
# Pre-create /var/lib/kraken owned by the nonroot uid (65532 in distroless)
# so a freshly-mounted named volume inherits that ownership — Docker
# copies both contents and perms from the image dir into a brand-new
# volume on first mount. Without this, the Panel (running nonroot) can't
# write panel-client.pem into the state dir on first boot.
# The compose files include a panel-init container that chowns *existing*
# volumes to close the retrofit gap for upgrades from earlier images.
COPY --from=gobuild --chown=65532:65532 /out/state /var/lib/kraken
VOLUME ["/var/lib/kraken"]
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/panel"]
