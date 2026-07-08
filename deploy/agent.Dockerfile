# syntax=docker/dockerfile:1
#
# Kraken Agent container image (Linux nodes).
#
# The Agent talks to the local Docker daemon to launch game-server
# containers, so the container must be run with /var/run/docker.sock
# bind-mounted and network_mode: host (game servers bind host ports
# directly). See deploy/docker-compose.full.yml.
#
# Build context is the repo root:
#   docker build -f deploy/agent.Dockerfile -t ghcr.io/briggleman/kraken-agent:dev .

# ---- go build -----------------------------------------------------------
FROM golang:1.26-alpine AS gobuild
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY proto/ ./proto/
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
ENV CGO_ENABLED=0
RUN go build -trimpath \
    -ldflags "-s -w \
      -X github.com/briggleman/kraken/internal/shared/version.Version=${VERSION} \
      -X github.com/briggleman/kraken/internal/shared/version.Commit=${COMMIT} \
      -X github.com/briggleman/kraken/internal/shared/version.Date=${DATE}" \
    -o /out/agent ./cmd/agent

# ---- runtime ------------------------------------------------------------
# The Agent needs read/write on /var/run/docker.sock (owned by root:docker
# on most hosts). Running as root inside the container is the same
# effective trust level as the bare-metal path, which likewise needs Docker
# access. Sites that want tighter isolation can build a variant that runs
# as a uid matched to their host's docker group.
FROM gcr.io/distroless/static-debian12:latest
LABEL org.opencontainers.image.title="kraken-agent" \
      org.opencontainers.image.description="Kraken node daemon — runs game servers via the local Docker socket." \
      org.opencontainers.image.source="https://github.com/briggleman/kraken" \
      org.opencontainers.image.licenses="GPL-3.0"
COPY --from=gobuild /out/agent /agent
# Bind-mounted game data + backups + SFTP host key (generated on first run).
VOLUME ["/data", "/agent-backups", "/var/lib/kraken"]
EXPOSE 9090 2022
ENTRYPOINT ["/agent"]
