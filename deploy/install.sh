#!/usr/bin/env bash
#
# Kraken bare-metal installer.
#
# Downloads the release binaries from GitHub, verifies their checksums,
# installs them to /usr/local/bin, and (optionally) drops in systemd
# units. Idempotent — safe to re-run to upgrade.
#
#   # both roles, latest release:
#   curl -fsSL https://raw.githubusercontent.com/briggleman/kraken/main/deploy/install.sh | sudo bash
#
#   # agent-only on a second host:
#   curl -fsSL https://raw.githubusercontent.com/briggleman/kraken/main/deploy/install.sh | sudo bash -s -- --role agent
#
#   # remote agent, single command (token from the Panel's Add Node dialog):
#   curl -fsSL https://raw.githubusercontent.com/briggleman/kraken/main/deploy/install.sh | #     sudo bash -s -- --role agent --panel-url http://panel:8080 #       --enroll-token <TOKEN> --ca-fingerprint <SHA256>
#
#   # pin a version, or skip systemd:
#   sudo bash deploy/install.sh --version v0.5.0 --no-systemd

set -euo pipefail

# ---- defaults ----------------------------------------------------------
ROLE="both"           # panel | agent | both
VERSION=""            # empty → resolve latest from GitHub
PREFIX="/usr/local"
INSTALL_SYSTEMD=1
REPO="briggleman/kraken"
KRAKEN_USER="kraken"
STATE_DIR="/var/lib/kraken"
CONFIG_DIR="/etc/kraken"
PANEL_URL=""          # remote-agent auto-enroll: Panel base URL
ENROLL_TOKEN=""       # remote-agent auto-enroll: one-time bootstrap token
CA_FINGERPRINT=""     # remote-agent auto-enroll: pinned Panel CA fingerprint
TUNNEL=0              # reverse-connection mode: agent dials the Panel, no inbound ports

log()   { printf '\033[36m→\033[0m %s\n' "$*"; }
warn()  { printf '\033[33m!\033[0m %s\n' "$*" >&2; }
die()   { printf '\033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }

# ---- args --------------------------------------------------------------
while [ $# -gt 0 ]; do
  case "$1" in
    --role) ROLE="${2:-}"; shift 2 ;;
    --version) VERSION="${2:-}"; shift 2 ;;
    --prefix) PREFIX="${2:-}"; shift 2 ;;
    --no-systemd) INSTALL_SYSTEMD=0; shift ;;
    --systemd) INSTALL_SYSTEMD=1; shift ;;
    --panel-url) PANEL_URL="${2:-}"; shift 2 ;;
    --enroll-token) ENROLL_TOKEN="${2:-}"; shift 2 ;;
    --ca-fingerprint) CA_FINGERPRINT="${2:-}"; shift 2 ;;
    --tunnel) TUNNEL=1; shift ;;
    -h|--help)
      sed -n '3,20p' "$0"
      exit 0
      ;;
    *) die "unknown flag: $1 (use --help)" ;;
  esac
done

case "$ROLE" in
  panel|agent|both) ;;
  *) die "--role must be panel, agent, or both (got '$ROLE')" ;;
esac

if [ -n "$ENROLL_TOKEN" ] && [ -z "$PANEL_URL" ]; then
  die "--enroll-token requires --panel-url (the Panel the token was minted on)"
fi
if [ -n "$ENROLL_TOKEN" ] && [ "$ROLE" = "panel" ]; then
  die "--enroll-token only makes sense for an agent install (--role agent or both)"
fi

# ---- preconditions -----------------------------------------------------
[ "$(id -u)" -eq 0 ] || die "must run as root (use sudo)"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH_RAW="$(uname -m)"
case "$ARCH_RAW" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) die "unsupported arch: $ARCH_RAW" ;;
esac
[ "$OS" = "linux" ] || die "this installer supports Linux; got $OS. Windows nodes install manually."

need() { command -v "$1" >/dev/null 2>&1 || die "missing dependency: $1"; }
need curl
need install
need sha256sum

# ---- version resolution ------------------------------------------------
if [ -z "$VERSION" ]; then
  log "resolving latest release from github.com/$REPO"
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" |
    sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
  [ -n "$VERSION" ] || die "could not resolve latest release tag"
fi
log "installing Kraken $VERSION ($OS/$ARCH, role=$ROLE)"

# ---- download + verify -------------------------------------------------
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

BASE="https://github.com/${REPO}/releases/download/${VERSION}"

log "downloading SHA256SUMS"
curl -fsSL -o "$TMP/SHA256SUMS" "$BASE/SHA256SUMS" ||
  die "could not download $BASE/SHA256SUMS"

fetch() {
  local name="$1"
  local url="$BASE/$name"
  log "  $name"
  curl -fsSL -o "$TMP/$name" "$url" || die "download failed: $url"
}

pick_binaries() {
  case "$ROLE" in
    panel) echo "kraken-panel-${OS}-${ARCH} kraken-krakenctl-${OS}-${ARCH}" ;;
    agent) echo "kraken-agent-${OS}-${ARCH} kraken-krakenctl-${OS}-${ARCH}" ;;
    both)  echo "kraken-panel-${OS}-${ARCH} kraken-agent-${OS}-${ARCH} kraken-krakenctl-${OS}-${ARCH}" ;;
  esac
}

BINARIES="$(pick_binaries)"
for name in $BINARIES; do fetch "$name"; done

log "verifying sha256 sums"
(
  cd "$TMP"
  # Filter SHA256SUMS to just the files we downloaded, then check.
  grep -F -- "$(printf ' %s\n' $BINARIES | sed 's/^ //')" SHA256SUMS > SHA256SUMS.filtered ||
    die "no matching lines in SHA256SUMS for $BINARIES"
  sha256sum -c SHA256SUMS.filtered
) || die "checksum verification failed"

# ---- install binaries --------------------------------------------------
install -d "$PREFIX/bin"
for name in $BINARIES; do
  # Strip the -linux-amd64 suffix so /usr/local/bin has stable names.
  short="${name%%-${OS}-${ARCH}}"
  install -m 0755 "$TMP/$name" "$PREFIX/bin/$short"
  log "installed $PREFIX/bin/$short"
done

# ---- user + directories ------------------------------------------------
if ! id -u "$KRAKEN_USER" >/dev/null 2>&1; then
  log "creating system user '$KRAKEN_USER'"
  useradd --system --home-dir "$STATE_DIR" --shell /usr/sbin/nologin "$KRAKEN_USER"
fi

if [ "$ROLE" = "agent" ] || [ "$ROLE" = "both" ]; then
  if getent group docker >/dev/null 2>&1; then
    if ! id -nG "$KRAKEN_USER" | tr ' ' '\n' | grep -qx docker; then
      log "adding '$KRAKEN_USER' to the docker group"
      usermod -aG docker "$KRAKEN_USER"
    fi
  else
    warn "docker group not found — install Docker before starting kraken-agent"
  fi
fi

install -d -m 0750 -o "$KRAKEN_USER" -g "$KRAKEN_USER" "$STATE_DIR"
install -d -m 0750 -o root         -g "$KRAKEN_USER" "$CONFIG_DIR"

# ---- environment files -------------------------------------------------
write_if_missing() {
  local path="$1"; shift
  if [ -e "$path" ]; then
    log "  (existing) $path"
    return
  fi
  cat > "$path"
  chmod 0640 "$path"
  chown root:"$KRAKEN_USER" "$path"
  log "  wrote $path"
}

# set_env_var KEY VALUE FILE — replace an existing KEY= line (commented or
# not) or append one. Used for the enroll settings, which must land even when
# agent.env already exists (re-running with a fresh token is the recovery path
# for an expired one).
set_env_var() {
  local key="$1" value="$2" file="$3"
  if grep -qE "^#? ?${key}=" "$file" 2>/dev/null; then
    sed -i -E "s|^#? ?${key}=.*|${key}=${value}|" "$file"
  else
    printf '%s=%s
' "$key" "$value" >> "$file"
  fi
}

if [ "$ROLE" = "panel" ] || [ "$ROLE" = "both" ]; then
  # Only auto-generate a KRAKEN_SECRETS_KEY when the file doesn't already
  # exist; otherwise operator edits are preserved across re-runs.
  if [ ! -e "$CONFIG_DIR/panel.env" ]; then
    KEY="$(head -c 32 /dev/urandom | base64)"
    write_if_missing "$CONFIG_DIR/panel.env" <<EOF
# Kraken Panel environment. Managed by deploy/install.sh — edit freely.
KRAKEN_HTTP_ADDR=:8080
# Point at your Postgres (docker compose -f deploy/docker-compose.yml up -d
# will run one on the same host).
KRAKEN_DATABASE_URL=postgres://kraken:kraken@127.0.0.1:5432/kraken?sslmode=disable
# Master key sealing every at-rest secret. Keep this file 0640 root:kraken.
KRAKEN_SECRETS_KEY=$KEY
# Bootstrap admin — leave the password empty to have one generated + logged.
KRAKEN_BOOTSTRAP_ADMIN_USER=admin
KRAKEN_BOOTSTRAP_ADMIN_PASSWORD=
KRAKEN_QUICKSTART=true
KRAKEN_LOCAL_AGENT_ADDR=127.0.0.1:9090
EOF
  else
    log "  (existing) $CONFIG_DIR/panel.env"
  fi
fi

if [ "$ROLE" = "agent" ] || [ "$ROLE" = "both" ]; then
  # For a single-host install (both roles), bind the Agent's gRPC to
  # loopback: the co-located Panel dials 127.0.0.1:9090 and there is no
  # legitimate reason to accept LAN connections. The Agent's gRPC has no
  # application-level auth (mTLS is the trust boundary), so binding on
  # all interfaces without TLS configured would expose the docker socket
  # to the LAN.
  #
  # For --role agent (this host is a REMOTE Agent enrolled with a Panel
  # elsewhere), keep the default :9090 external bind. The Agent will
  # refuse to serve plaintext gRPC on that address until you run
  # `krakenctl enroll` and set KRAKEN_TLS_CERT/KEY/CA.
  if [ "$ROLE" = "both" ]; then
    agent_addr="127.0.0.1:9090"
    # Single-host install: point the Agent at the co-located Panel so
    # it auto-enrolls on first start (cert bundle persists to
    # $STATE_DIR/agent.pem and is reused on restarts).
    panel_url_line="KRAKEN_PANEL_URL=http://127.0.0.1:8080"
  else
    agent_addr=":9090"
    # Remote Agent: the operator runs `krakenctl enroll` manually.
    panel_url_line="# KRAKEN_PANEL_URL=http://<panel-host>:8080  # uncomment for auto-enroll"
  fi
  write_if_missing "$CONFIG_DIR/agent.env" <<EOF
# Kraken Agent environment. Managed by deploy/install.sh — edit freely.
KRAKEN_AGENT_ADDR=$agent_addr
KRAKEN_SFTP_ADDR=:2022
KRAKEN_NODE_OS=linux
KRAKEN_DATA_DIR=$STATE_DIR/server-data
KRAKEN_BACKUP_DIR=$STATE_DIR/agent-backups
$panel_url_line
EOF
  install -d -m 0750 -o "$KRAKEN_USER" -g "$KRAKEN_USER" \
    "$STATE_DIR/server-data" "$STATE_DIR/agent-backups"

  # Remote auto-enroll: wire the Panel URL + one-time token + CA pin into
  # agent.env so the Agent enrolls itself on first start. These are written
  # even when agent.env already exists — re-running with a fresh token is the
  # recovery path for an expired or Panel-restart-invalidated one. The token
  # is one-time and the persisted cert bundle survives restarts, so the stale
  # KRAKEN_ENROLL_TOKEN left behind is inert.
  if [ -n "$PANEL_URL" ]; then
    set_env_var KRAKEN_PANEL_URL "$PANEL_URL" "$CONFIG_DIR/agent.env"
    log "  set KRAKEN_PANEL_URL=$PANEL_URL"
  fi
  if [ -n "$ENROLL_TOKEN" ]; then
    set_env_var KRAKEN_ENROLL_TOKEN "$ENROLL_TOKEN" "$CONFIG_DIR/agent.env"
    log "  set KRAKEN_ENROLL_TOKEN=<one-time token>"
    if [ -n "$CA_FINGERPRINT" ]; then
      set_env_var KRAKEN_CA_FINGERPRINT "$CA_FINGERPRINT" "$CONFIG_DIR/agent.env"
      log "  set KRAKEN_CA_FINGERPRINT=$CA_FINGERPRINT"
    else
      warn "no --ca-fingerprint given — enrollment will trust whatever CA the Panel returns"
    fi
  fi
  if [ "$TUNNEL" -eq 1 ]; then
    # Reverse-connection mode: the agent dials the Panel and serves over the
    # tunnel, so this host needs no inbound gRPC firewall rule at all.
    set_env_var KRAKEN_TUNNEL 1 "$CONFIG_DIR/agent.env"
    log "  set KRAKEN_TUNNEL=1 (reverse connection — no inbound ports needed)"
  fi

  if [ "$ROLE" = "agent" ] && [ -z "$ENROLL_TOKEN" ]; then
    warn "remote-Agent install: this host now expects mTLS on :9090."
    warn "Either re-run with --panel-url/--enroll-token (from the Panel's"
    warn "Add node dialog), or mint a token and run:"
    warn "  sudo krakenctl enroll -panel http://<panel-host>:8080 -token <TOKEN>"
    warn "The Agent refuses plaintext gRPC on non-loopback addresses without"
    warn "TLS. Set KRAKEN_ALLOW_INSECURE_GRPC=1 in agent.env to override."
  fi
fi

# ---- systemd -----------------------------------------------------------
if [ "$INSTALL_SYSTEMD" -eq 1 ] && [ -d /etc/systemd/system ]; then
  install_unit() {
    local name="$1"
    local src_url="https://raw.githubusercontent.com/${REPO}/${VERSION}/deploy/systemd/${name}"
    if [ -f "deploy/systemd/${name}" ]; then
      install -m 0644 "deploy/systemd/${name}" "/etc/systemd/system/${name}"
    else
      curl -fsSL -o "/etc/systemd/system/${name}" "$src_url" ||
        die "could not fetch $src_url"
      chmod 0644 "/etc/systemd/system/${name}"
    fi
    log "installed /etc/systemd/system/${name}"
  }

  if [ "$ROLE" = "panel" ] || [ "$ROLE" = "both" ]; then
    install_unit kraken-panel.service
  fi
  if [ "$ROLE" = "agent" ] || [ "$ROLE" = "both" ]; then
    install_unit kraken-agent.service
  fi

  systemctl daemon-reload
  if [ "$ROLE" = "agent" ] && [ -n "$ENROLL_TOKEN" ]; then
    # One-command install: the enroll settings are in place, so bring the
    # agent up now — it enrolls with the Panel on first start.
    log "starting kraken-agent (enrolls with $PANEL_URL on first start)"
    systemctl enable --now kraken-agent
    log "watch enrollment with: journalctl -u kraken-agent -f"
  else
    log "systemd units installed. Enable + start with:"
    case "$ROLE" in
      panel) echo "    sudo systemctl enable --now kraken-panel" ;;
      agent) echo "    sudo systemctl enable --now kraken-agent" ;;
      both)  echo "    sudo systemctl enable --now kraken-panel kraken-agent" ;;
    esac
  fi
elif [ "$INSTALL_SYSTEMD" -eq 1 ]; then
  warn "no systemd on this host — skipped unit install"
fi

log "done. review $CONFIG_DIR/*.env before starting the services."
