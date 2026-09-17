# Wiki screenshots

The figures under `docs/wiki/assets/shots/` are captured from the **fake-live
stack** with fictional data, never from a real Panel. This folder is how they
were made, so the next person can redo one after a UI change instead of
guessing at the state behind it.

Rules the captures follow:

- No real node names, addresses, endpoints, emails, credentials, tokens or
  fingerprints. Nodes are `reef-01` (linux) and `trench-02` (windows, tunnel
  mode); servers are `Midgard Weekend`, `Palpagos Co-op`, `Blood Moon Castle`
  and `Rail World`. Every address this machine owns is rewritten to RFC 5737
  documentation ranges before the shutter (`hostMap()` in `shots.mjs`).
- Tokens and fingerprints that do appear belong to a throwaway in-memory CA
  that dies with the Panel process.
- Two fake-runtime knobs exist for these pictures and nothing else:
  `KRAKEN_FAKE_INSTALL_DELAY` (per-step install delay, so *installing* can be
  seen) and the console command `crash` (drops a fake server into *crashed*).
- One cosmetic fill: the fake runtime has no SFTP listener, so the SFTP card
  scene writes the port (`2022`) and connection string a real node would show.
  Everything else on every capture is what the UI rendered.

## Stack

Three processes plus the web dev server, all from `.claude/launch.json`-style
entries (add temporary ones locally; do not commit them):

| process | env of note |
| --- | --- |
| Panel | `KRAKEN_ENV=dev KRAKEN_QUICKSTART=1 KRAKEN_LOCAL_AGENT_ADDR=127.0.0.1:9099 KRAKEN_BOOTSTRAP_ADMIN_PASSWORD=admin KRAKEN_TRUSTED_PROXIES=127.0.0.1,::1` (in-memory store) |
| Agent A (`reef-01`) | `KRAKEN_RUNTIME=fake KRAKEN_NODE_OS=linux KRAKEN_AGENT_ADDR=127.0.0.1:9099 KRAKEN_FAKE_INSTALL_DELAY=12s` + own state/data dirs |
| Agent B (`trench-02`) | as A but `KRAKEN_NODE_OS=windows KRAKEN_AGENT_ADDR=127.0.0.1:9098 KRAKEN_TUNNEL=1 KRAKEN_ENROLL_TOKEN=… KRAKEN_CA_FINGERPRINT=…` |
| web | `npm --prefix web run dev` (vite on :5173, proxies `/api` to :8080) |

Trusted proxies let `curl -H 'X-Forwarded-For: 203.0.113.77'` write distinct
client addresses into the audit log for the reverse-proxy figure.

## Seeding recipe

1. Log in (`admin`/`admin`), rotate the password, save the token to
   `%TEMP%\kraken-shot-token.txt` (or point `KRAKEN_SHOT_TOKEN_FILE` at it).
2. Start the Panel alone: `login`, then delete the quickstart node and take
   `fleet-empty` (`home --out fleet-empty --h 700`), then `nodeAdd`.
3. Start Agent A; `POST /nodes {name:"reef-01", address:"127.0.0.1:9099",
   port_start:27000, port_end:27199}`; `PATCH` its memory to 65536.
4. Create Valheim (`install_bepinex:true`) and Palworld on it, start both.
   Seed files with `/files/mkdir` + `/files/write`, set the node's backup
   target to `sftp nas.example.internal:22` with replication on, create three
   backups (`pre-mod-update`, `nightly`, `before-wipe`).
5. Mint a bootstrap token, start Agent B, `POST /nodes` with
   `connection_mode:"tunnel"` and its `tunnel_id`, create V Rising on it.
6. Type `crash` in the Palworld console (`depth --server "Palpagos Co-op"
   --cmd crash`). Create Factorio right before the fleet capture so it is
   still installing.
7. Failures for the audit figure: a 404, a 409 (over-memory deploy), a 400,
   two bad logins, then stop Agent B and hit its server for 502s.
8. For the update affordance, restart Agent A built with
   `-ldflags "-X github.com/briggleman/kraken/internal/shared/version.Version=0.51.0"`.

## Scenes → figures

| figure | command |
| --- | --- |
| `signin` | `node shots.mjs login` |
| `fleet-empty` | `node shots.mjs home --out fleet-empty --h 700` |
| `node-add` | `node shots.mjs nodeAdd` |
| `node-port-pool` | `node shots.mjs nodeCfg --out node-settings --h 810` |
| `fleet` | `node shots.mjs fleet --h 1900` |
| `node-agent-update` | `node shots.mjs fleet --keepdrift 1 --out node-drift --h 560` |
| `deploy` | `node shots.mjs deploy --game Valheim --name "Midgard Weekend II"` |
| `deploy-bepinex` | `node shots.mjs deploy --game Valheim --name "Midgard Modded" --bepinex 1 --out deploy-bepinex` |
| `files` | `node shots.mjs depth --tab files --hover adminlist.txt --out files --w 1920 --h 1080` |
| `sftp` | `node shots.mjs depth --sftp 1 --out sftp --w 1920 --h 1080` |
| `backups` | `node shots.mjs depth --section Backups --out backups --w 1920 --h 1080` |
| `audit-failures` | `node shots.mjs audit --filter fail --out audit-failures` |
| `audit-client-addresses` | `node shots.mjs audit --q auth/login --out audit-proxy --h 700` |
| `specs` | `node shots.mjs specs` |

Then `npm run webp` converts `shots/*.png` (device-pixel-ratio 2) to the
committed WebPs, and `make wiki` picks the new dimensions up.

Setup: `npm install` here (playwright-core drives the Edge already on the
machine; nothing is downloaded), and the dev server on :5173 must be up.
