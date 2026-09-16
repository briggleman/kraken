---
title: Agent configuration
description: agent.yaml, the KRAKEN_* variables and the command-line flags — three spellings of the same settings, with the precedence rule that decides which one wins.
section: configure
order: 21
---

Every Agent setting has three spellings: a key in `agent.yaml`, a `KRAKEN_*`
environment variable, and a `--flag`. The annotated template is
[`deploy/agent.example.yaml`](https://github.com/briggleman/kraken/blob/main/deploy/agent.example.yaml).

## Precedence

Lowest to highest:

```text
defaults  <  paths derived from root  <  the config file  <  KRAKEN_* env  <  flags
```

Environment outranks the file on purpose: adding a config file to a host already
driven by compose, systemd or a Windows service must not silently change its
settings.

Ask the Agent what it actually resolved, without starting it:

```sh
kraken-agent --root /var/lib/kraken --print-config
```

## Where the file lives

The Agent looks for, in order: `--config <path>`, `KRAKEN_CONFIG`,
`<root>/agent.yaml`, then the OS-conventional location. JSON works too — the
file is parsed as YAML and JSON is valid YAML.

```sh
kraken-agent --root /var/lib/kraken           # finds <root>/agent.yaml
kraken-agent --config /etc/kraken/agent.yaml  # or name it explicitly
```

## The one setting most installs need

```yaml
root: /var/lib/kraken
```

Everything else defaults beneath it:

| path | what |
| --- | --- |
| `<root>/state` | mTLS bundle, SFTP host key, `agent.log`, the update sentinel |
| `<root>/server-data` | per-server game data, one subdirectory per server |
| `<root>/backups` | local backup archives |
| `<root>/certs` | `agent.pem` + `agent-key.pem` + `ca.pem`, adopted automatically once all three exist — which is exactly what `krakenctl enroll -out <root>/certs` writes |

On Windows the installer uses `C:\kraken` and the same layout beneath it.

## Keys

| key | env | flag | what it does |
| --- | --- | --- | --- |
| `root` | `KRAKEN_ROOT` | `--root` | directory the other paths default beneath |
| `node_id` | `KRAKEN_NODE_ID` | `--node-id` | stable identity in the Panel's node list |
| `node_os` | `KRAKEN_NODE_OS` | `--node-os` | `linux` or `windows`; must match the Docker daemon's container mode |
| `wine` | `KRAKEN_NODE_WINE` | `--wine` | advertise Wine, so Windows-only games can be placed on this Linux node |
| `addr` | `KRAKEN_AGENT_ADDR` | `--addr` | gRPC listen address, e.g. `:9090` |
| `sftp_addr` | `KRAKEN_SFTP_ADDR` | `--sftp-addr` | SFTP listen address, e.g. `:2022` |
| `state_dir` | `KRAKEN_STATE_DIR` | `--state-dir` | Agent-owned state |
| `data_dir` | `KRAKEN_DATA_DIR` | `--data-dir` | per-server game data root |
| `host_data_dir` | `KRAKEN_HOST_DATA_DIR` | `--host-data-dir` | the data root **as the Docker daemon sees it** — containerized Agent only |
| `backup_dir` | `KRAKEN_BACKUP_DIR` | `--backup-dir` | local backup destination |
| `sftp_host_key` | `KRAKEN_SFTP_HOST_KEY` | `--sftp-host-key` | SSH host key path; generated on first run |
| `tls_cert` / `tls_key` / `tls_ca` | `KRAKEN_TLS_CERT` / `_KEY` / `_CA` | `--tls-cert` / `--tls-key` / `--tls-ca` | name the mTLS bundle explicitly instead of taking `<root>/certs` |
| `panel_url` | `KRAKEN_PANEL_URL` | `--panel-url` | Panel base URL for auto-enrollment when no bundle exists |
| `enroll_token` | `KRAKEN_ENROLL_TOKEN` | `--enroll-token` | the one-time bootstrap token from the Add node dialog |
| `ca_fingerprint` | `KRAKEN_CA_FINGERPRINT` | `--ca-fingerprint` | pinned SHA-256 of the Panel CA, verified during enrollment |
| `tunnel` | `KRAKEN_TUNNEL` | `--tunnel` | dial out and serve over a reverse tunnel; no inbound gRPC port |
| `tunnel_addr` | `KRAKEN_TUNNEL_ADDR` | `--tunnel-addr` | the Panel's tunnel endpoint; defaults to the `panel_url` host on `:9443` |
| `runtime` | `KRAKEN_RUNTIME` | `--runtime` | `docker` (default) or `fake` for a runtime-less smoke test |
| `windows_isolation` | `KRAKEN_WINDOWS_ISOLATION` | `--windows-isolation` | `hyperv`, `process`, or `default` |
| `image_pull` | `KRAKEN_IMAGE_PULL` | `--image-pull` | `always` (default), `if-not-present`, or `never` |
| `image_prune` | `KRAKEN_IMAGE_PRUNE` | `--image-prune` | weekly prune of dangling images: `on` (default) or `off` |
| `allow_insecure_grpc` | `KRAKEN_ALLOW_INSECURE_GRPC` | `--allow-insecure-grpc` | serve plaintext gRPC on a non-loopback address |

Modes, which do something and exit rather than configuring a run: `--version`,
`--print-config`, and on Windows `--service install|uninstall|start|stop|status`.

## The ones with teeth

**`node_id`** is the node's identity. Changing it later re-registers the node and
orphans every server installed under the old id.

**`host_data_dir`** exists for exactly one situation: a containerized Agent whose
data root is mounted at a different path inside the container than on the host.
Bind sources are resolved by the host's Docker daemon, so it needs the host path.
Prefer mounting the data root at the same absolute path on both sides and leaving
this unset.

**`image_pull`** decides when a game-server image is fetched. `always` — the
default — tries the registry on every install and every operator-driven start,
falling back to the copy already on the node when the pull fails. Every bundled
spec names a moving tag, so `always` is what keeps a node current. A
crash-restart never pulls, and a start waits only a few seconds for the refresh:
a download that takes longer finishes in the background and takes effect on the
next start, so starting a server is never held up by a registry. An install waits
for the whole download.

**`image_prune`** removes dangling (untagged) images weekly — the previous copy
of a moving tag after a re-pull, which nothing else reclaims. Only images no tag
points at are removed, and Docker never removes one backing a container, running
or stopped. There is no retention floor: a dangling image is gone a week after it
was untagged, and rolling back to it means downloading it again.

:::security
**`allow_insecure_grpc` hands the Docker socket to the network.** Agent gRPC has
no application-level authentication — mTLS is the entire trust boundary — so the
Agent refuses to serve plaintext on a non-loopback address unless this says
otherwise. Enroll the node instead; this switch exists for a diagnosis, not for
a deployment.
:::

## Changing a setting

```sh
sudo $EDITOR /etc/kraken/agent.env    # or <root>/agent.yaml
sudo systemctl restart kraken-agent
```

```powershell
notepad C:\kraken\agent.yaml
Restart-Service kraken-agent
```

Then confirm with `--print-config` rather than assuming which spelling won.
