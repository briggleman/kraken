import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api, clearToken } from "@/api/client";
import type { CatalogItem, DatabaseConfig, EnrollStatus, Node, SetupStatus, Spec } from "@/api/types";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { Input } from "@ds/components/core/Input";
import { Select } from "@ds/components/core/Select";
import { Badge } from "@ds/components/core/Badge";
import { Icon } from "@ds/components/core/Icon";
import { OsIcon } from "@/components/OsIcon";

const mono = "var(--font-mono)";
const STEPS = ["Database", "Secure", "Connect a node", "Add a game", "Deploy"] as const;

const SECTION_LABEL: React.CSSProperties = {
  fontFamily: mono,
  fontSize: 11,
  letterSpacing: "1.5px",
  color: "var(--text-faint)",
  marginBottom: 14,
};

const CATALOG_TH: React.CSSProperties = {
  textAlign: "left",
  padding: "10px 14px",
  fontFamily: mono,
  fontSize: 11,
  letterSpacing: "1px",
  color: "var(--text-faint)",
  fontWeight: 500,
  borderBottom: "1px solid var(--border-subtle)",
};

const CATALOG_TD: React.CSSProperties = {
  padding: "12px 14px",
  verticalAlign: "middle",
};

function StepDots({ step, done }: { step: number; done: boolean[] }) {
  return (
    <div style={{ display: "flex", alignItems: "center", marginBottom: 28 }}>
      {STEPS.map((label, i) => {
        const complete = done[i];
        return (
          <div key={label} style={{ display: "contents" }}>
            <div style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 8 }}>
              <div
                style={{
                  width: 32,
                  height: 32,
                  borderRadius: "50%",
                  border: i <= step || complete ? "1px solid var(--border-strong)" : "1px solid var(--border-subtle)",
                  color: complete ? "var(--text-on-accent)" : i === step ? "var(--accent)" : "var(--text-faint)",
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "center",
                  fontWeight: 700,
                  fontSize: 13,
                  fontFamily: mono,
                  boxShadow: complete ? "var(--elevation-glow-soft)" : "none",
                  background: complete ? "var(--gradient-accent)" : i === step ? "var(--accent-wash-12)" : "transparent",
                }}
              >
                {complete ? <Icon name="check" size={15} /> : i + 1}
              </div>
              <span style={{ fontSize: 10.5, fontFamily: mono, color: i === step ? "var(--text-primary)" : "var(--text-faint)" }}>{label}</span>
            </div>
            {i < STEPS.length - 1 && (
              <div style={{ flex: 1, height: 2, margin: "0 6px 22px", background: complete ? "var(--accent)" : "var(--border-subtle)" }} />
            )}
          </div>
        );
      })}
    </div>
  );
}

// AgentInstallInstructions surfaces per-OS install commands for a remote
// Agent, gated behind Linux / Windows tabs so the operator sees a shell
// matching the host they're about to run against. The Linux flow uses the
// bare-metal install.sh wrapper (installs the systemd unit); the Windows
// flow downloads the release binaries and enrolls directly (matches
// deploy/windows/README.md — nssm service install is documented there).
// Registration happens inline in the wizard once the agent enrolls, so
// these steps end at "agent running".
type AgentTarget = "linux" | "windows";
function AgentInstallInstructions({ panelOrigin, token }: { panelOrigin: string; token: string }) {
  const [target, setTarget] = useState<AgentTarget>("linux");

  const linuxCmds = [
    {
      title: "1 · INSTALL AGENT + SYSTEMD UNIT",
      body: "curl -fsSL https://raw.githubusercontent.com/briggleman/kraken/main/deploy/install.sh | sudo bash -s -- --role agent",
    },
    {
      title: "2 · OPEN FIREWALL PORTS (IF A FIREWALL IS ENABLED)",
      body: `sudo ufw allow 9090/tcp && sudo ufw allow 2022/tcp
# firewalld: sudo firewall-cmd --permanent --add-port={9090,2022}/tcp && sudo firewall-cmd --reload`,
    },
    {
      title: "3 · ENROLL + CONFIGURE (WRITES /etc/kraken)",
      body: `sudo krakenctl enroll -panel ${panelOrigin} -token ${token} -hosts <this-host-ip> -out /etc/kraken/certs
sudo tee -a /etc/kraken/agent.env >/dev/null <<'EOF'
KRAKEN_NODE_ID=$(hostname)
KRAKEN_TLS_CERT=/etc/kraken/certs/agent.pem
KRAKEN_TLS_KEY=/etc/kraken/certs/agent-key.pem
KRAKEN_TLS_CA=/etc/kraken/certs/ca.pem
EOF`,
    },
    {
      title: "4 · START THE AGENT",
      body: "sudo systemctl enable --now kraken-agent",
    },
  ];

  const winCmds = [
    {
      title: "1 · DOWNLOAD BINARIES (POWERSHELL, ADMIN)",
      body: `$ver = "latest"
$dest = "C:\\kraken"
New-Item -ItemType Directory -Force -Path "$dest\\bin","$dest\\state","$dest\\certs" | Out-Null
$base = if ($ver -eq "latest") { "https://github.com/briggleman/kraken/releases/latest/download" } else { "https://github.com/briggleman/kraken/releases/download/$ver" }
foreach ($f in @("kraken-agent-windows-amd64.exe","kraken-krakenctl-windows-amd64.exe")) {
  Invoke-WebRequest -Uri "$base/$f" -OutFile "$dest\\bin\\$f"
}`,
    },
    {
      title: "2 · ALLOW INBOUND PORTS (PORT RULE — SURVIVES BINARY RENAMES)",
      body: `New-NetFirewallRule -DisplayName "kraken-agent ports (TCP 9090 + 2022)" \`
  -Direction Inbound -Action Allow -Protocol TCP -LocalPort 9090,2022`,
    },
    {
      title: "3 · ENROLL (WRITES C:\\kraken\\certs)",
      body: `cd C:\\kraken\\bin
.\\kraken-krakenctl-windows-amd64.exe enroll \`
  -panel ${panelOrigin} -token ${token} \`
  -hosts $env:COMPUTERNAME,<this-host-ip> \`
  -out C:\\kraken\\certs`,
    },
    {
      title: "4 · RUN THE AGENT",
      body: `$env:KRAKEN_NODE_ID="$env:COMPUTERNAME".ToLower()
$env:KRAKEN_TLS_CERT="C:\\kraken\\certs\\agent.pem"
$env:KRAKEN_TLS_KEY="C:\\kraken\\certs\\agent-key.pem"
$env:KRAKEN_TLS_CA="C:\\kraken\\certs\\ca.pem"
$env:KRAKEN_NODE_OS="windows"
$env:KRAKEN_STATE_DIR="C:\\kraken\\state"
C:\\kraken\\bin\\kraken-agent-windows-amd64.exe`,
    },
  ];

  const cmds = target === "linux" ? linuxCmds : winCmds;

  return (
    <div>
      <OsTabs value={target} onChange={setTarget} />
      {cmds.map((c) => (
        <div key={c.title}>
          <div style={{ fontFamily: mono, fontSize: 11, color: "var(--text-faint)", margin: "10px 0 6px" }}>{c.title}</div>
          <CodeBlock text={c.body} />
        </div>
      ))}
      <p style={{ fontSize: 12.5, color: "var(--text-muted)", marginTop: 8, lineHeight: 1.6 }}>
        The node takes its name from <span style={{ fontFamily: mono, color: "var(--text-primary)" }}>KRAKEN_NODE_ID</span>.
        {target === "windows" && (
          <>
            {" "}To keep the Agent running as a Windows Service (with log rotation + auto-start), see the{" "}
            <a href="https://github.com/briggleman/kraken/blob/main/deploy/windows/README.md" target="_blank" rel="noreferrer" style={{ color: "var(--accent)" }}>
              Windows install walkthrough
            </a>
            .
          </>
        )}
      </p>
    </div>
  );
}

// OsTabs is the Linux/Windows switch used above the install commands. Two
// radio-style pills side-by-side with brand-color glyphs so the operator
// knows at a glance which shell they're looking at.
function OsTabs({ value, onChange }: { value: AgentTarget; onChange: (v: AgentTarget) => void }) {
  return (
    <div role="tablist" style={{ display: "flex", gap: 8, marginBottom: 4 }}>
      {(["linux", "windows"] as AgentTarget[]).map((os) => {
        const active = value === os;
        return (
          <button
            key={os}
            role="tab"
            aria-selected={active}
            onClick={() => onChange(os)}
            style={{
              display: "inline-flex",
              alignItems: "center",
              gap: 8,
              padding: "8px 14px",
              borderRadius: "var(--radius-sm)",
              border: `1px solid ${active ? "var(--accent)" : "var(--border-subtle)"}`,
              background: active ? "var(--accent-wash-12)" : "transparent",
              color: active ? "var(--text-primary)" : "var(--text-secondary)",
              cursor: "pointer",
              fontFamily: mono,
              fontSize: 12,
              letterSpacing: "1px",
              textTransform: "uppercase",
            }}
          >
            <OsIcon os={os} size={14} style={{ color: active ? "var(--accent)" : "var(--text-secondary)" }} />
            {os === "linux" ? "Linux Install" : "Windows Install"}
          </button>
        );
      })}
    </div>
  );
}

// CopyButton is an icon-only copy-to-clipboard affordance: the copy glyph
// flips to a green check for a beat after a successful copy.
function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    let ok = false;
    // navigator.clipboard only exists in secure contexts (https / localhost) —
    // a Panel served over plain http on a LAN IP doesn't have it, so fall back
    // to the legacy textarea + execCommand path.
    try {
      if (navigator.clipboard) {
        await navigator.clipboard.writeText(text);
        ok = true;
      }
    } catch {
      /* permission denied — try the fallback */
    }
    if (!ok) {
      const ta = document.createElement("textarea");
      ta.value = text;
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      try {
        ok = document.execCommand("copy");
      } catch {
        ok = false;
      }
      ta.remove();
    }
    if (ok) {
      setCopied(true);
      setTimeout(() => setCopied(false), 1600);
    }
  };
  return (
    <button
      onClick={copy}
      title={copied ? "Copied" : "Copy to clipboard"}
      aria-label="Copy to clipboard"
      style={{
        display: "inline-flex",
        alignItems: "center",
        justifyContent: "center",
        width: 28,
        height: 28,
        flexShrink: 0,
        borderRadius: "var(--radius-sm)",
        border: `1px solid ${copied ? "var(--status-running)" : "var(--border-subtle)"}`,
        background: "transparent",
        cursor: "pointer",
        color: copied ? "var(--status-running)" : "var(--text-secondary)",
        transition: "color 120ms ease, border-color 120ms ease",
      }}
    >
      <Icon name={copied ? "check" : "copy"} size={14} />
    </button>
  );
}

// CodeBlock renders a copy-able shell command.
function CodeBlock({ text }: { text: string }) {
  return (
    <div
      style={{
        display: "flex",
        alignItems: "flex-start",
        justifyContent: "space-between",
        gap: 10,
        padding: "10px 12px",
        borderRadius: "var(--radius-sm)",
        border: "1px solid var(--border-subtle)",
        background: "var(--bg-inset)",
        marginBottom: 10,
      }}
    >
      <code style={{ fontFamily: mono, fontSize: 12, color: "var(--text-primary)", whiteSpace: "pre-wrap", wordBreak: "break-all", lineHeight: 1.6 }}>{text}</code>
      <CopyButton text={text} />
    </div>
  );
}

// EnrollConsole is the live status feed for the remote-node flow: one line
// per lifecycle stage, styled like a terminal readout so the operator can
// see exactly where the process is (and where it stalled).
type ConsoleLine = { state: "done" | "active" | "error"; text: string };
function EnrollConsole({ lines }: { lines: ConsoleLine[] }) {
  if (lines.length === 0) return null;
  return (
    <div
      style={{
        borderRadius: "var(--radius-sm)",
        border: "1px solid var(--border-subtle)",
        background: "var(--bg-inset)",
        padding: "12px 14px",
        margin: "14px 0",
        display: "grid",
        gap: 8,
      }}
    >
      {lines.map((l, i) => (
        <div key={i} style={{ display: "flex", alignItems: "flex-start", gap: 9, fontFamily: mono, fontSize: 12.5, lineHeight: 1.5 }}>
          {l.state === "done" ? (
            <Icon name="check" size={13} style={{ color: "var(--status-running)", marginTop: 2, flexShrink: 0 }} />
          ) : l.state === "error" ? (
            <Icon name="octagon" size={13} style={{ color: "var(--status-crashed)", marginTop: 2, flexShrink: 0 }} />
          ) : (
            <span style={{ color: "var(--accent)", flexShrink: 0 }}>▸</span>
          )}
          <span
            style={{
              color: l.state === "done" ? "var(--text-secondary)" : l.state === "error" ? "var(--status-crashed)" : "var(--text-primary)",
            }}
          >
            {l.text}
          </span>
        </div>
      ))}
    </div>
  );
}

export function Setup() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<SetupStatus | null>(null);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [specs, setSpecs] = useState<Spec[]>([]);
  const [catalog, setCatalog] = useState<CatalogItem[]>([]);
  const [step, setStep] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [pinging, setPinging] = useState(false);
  const [importing, setImporting] = useState<string | null>(null);

  // Remote-node enrollment panel state. The wizard walks token → enroll →
  // register → online in place; `enroll` mirrors the Panel's token lifecycle.
  const [remoteOpen, setRemoteOpen] = useState(false);
  const [remoteToken, setRemoteToken] = useState<string | null>(null);
  const [tokenExpiresAt, setTokenExpiresAt] = useState<string | null>(null);
  const [enroll, setEnroll] = useState<EnrollStatus | null>(null);
  const [regAddress, setRegAddress] = useState("");
  const [registering, setRegistering] = useState(false);
  const [registeredNodeId, setRegisteredNodeId] = useState<string | null>(null);
  const [remoteError, setRemoteError] = useState<string | null>(null);

  // Database step state.
  const [dbCfg, setDbCfg] = useState<DatabaseConfig | null>(null);
  const [db, setDb] = useState({ host: "", port: 5432, user: "kraken", password: "", dbname: "kraken", sslmode: "disable" });
  const [dbBusy, setDbBusy] = useState<"test" | "connect" | null>(null);
  const [dbNotice, setDbNotice] = useState<string | null>(null);
  const [restarting, setRestarting] = useState(false);
  // Once-per-mount guard so the compose-defaults auto-probe doesn't loop.
  const [autoProbed, setAutoProbed] = useState(false);
  // Add-a-game state: bulk-import status + once-per-session auto-import guard.
  const [importingAll, setImportingAll] = useState(false);
  const [autoImportedGames, setAutoImportedGames] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const [st, n, sp, cat, dbc] = await Promise.all([
        api.setupStatus(),
        api.listNodes(),
        api.listSpecs(),
        api.listCatalog(),
        api.getDatabaseConfig(),
      ]);
      setStatus(st);
      setNodes(n.nodes ?? []);
      setSpecs(sp.specs ?? []);
      setCatalog(cat.catalog ?? []);
      setDbCfg(dbc);
      if (!dbc.using_memory) {
        setDb((d) => ({ ...d, host: dbc.host ?? d.host, port: dbc.port ?? d.port, user: dbc.user ?? d.user, dbname: dbc.dbname ?? d.dbname, sslmode: dbc.sslmode ?? d.sslmode }));
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to load setup state");
    }
  }, []);

  const testDb = async () => {
    setDbBusy("test");
    setError(null);
    setDbNotice(null);
    try {
      const r = await api.testDatabase(db);
      setDbNotice(r.db_exists ? "Connected — database exists." : r.can_create_db ? "Connected — the database will be created." : "Connected, but the role can't create the database; pre-create it.");
    } catch (e) {
      setError(e instanceof Error ? e.message : "connection failed");
    } finally {
      setDbBusy(null);
    }
  };

  const connectDb = async () => {
    setDbBusy("connect");
    setError(null);
    setDbNotice(null);
    try {
      await api.connectDatabase(db);
      setRestarting(true);
      // The panel exits ~400ms after responding; wait for it to come back, then
      // return to sign-in (the fresh Postgres reseeds the admin).
      await new Promise((r) => setTimeout(r, 1500));
      for (let i = 0; i < 25; i++) {
        try {
          const res = await fetch("/healthz");
          if (res.ok) break;
        } catch {
          /* down during restart */
        }
        await new Promise((r) => setTimeout(r, 1500));
      }
      clearToken();
      navigate("/login");
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not connect database");
      setDbBusy(null);
    }
  };

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // Auto-detect the bundled Docker Compose Postgres. On first mount, if we're
  // on the in-memory store and the operator hasn't touched the DB form, probe
  // localhost:5432 with the compose defaults (kraken/kraken@localhost/kraken).
  // Success → pre-fill the form and hint. Failure → silent no-op; the operator
  // types their own DSN as before. Non-compose operators see zero change.
  useEffect(() => {
    if (!dbCfg || !dbCfg.using_memory || autoProbed || db.host !== "") return;
    setAutoProbed(true);
    const defaults = { host: "localhost", port: 5432, user: "kraken", password: "kraken", dbname: "kraken", sslmode: "disable" };
    void (async () => {
      try {
        const r = await api.testDatabase(defaults);
        setDb(defaults);
        setDbNotice(
          r.db_exists
            ? "Detected the bundled Postgres — click Connect to persist."
            : r.can_create_db
              ? "Detected the bundled Postgres — the database will be created on Connect."
              : "Detected the bundled Postgres — click Connect to persist.",
        );
      } catch {
        /* not the compose stack — leave the form blank */
      }
    })();
  }, [dbCfg, autoProbed, db.host]);

  // Auto-import all catalog games the first time the operator lands on the
  // Add-a-game step. Prior behavior asked them to click Import on each row;
  // the mock treats this step as informational ("here's what's ready") so we
  // just pull everything in on entry. A per-session guard keeps re-visits from
  // re-firing the loop; the Import all button is the manual re-trigger.
  useEffect(() => {
    if (step !== 3 || autoImportedGames || catalog.length === 0) return;
    if (catalog.every((g) => g.already_imported)) {
      setAutoImportedGames(true);
      return;
    }
    setAutoImportedGames(true);
    void importAllGames();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [step, catalog, autoImportedGames]);

  // While waiting for the local node to come online, poll its live info so the
  // quickstart agent flips to "online" without the operator doing anything.
  const hasOnlineNode = nodes.some((n) => n.status === "online");
  useEffect(() => {
    if (step !== 2 || hasOnlineNode) return;
    const t = setInterval(() => void pingAll(), 4000);
    return () => clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [step, hasOnlineNode, nodes]);

  const pingAll = async () => {
    if (nodes.length === 0) return;
    setPinging(true);
    try {
      await Promise.allSettled(nodes.filter((n) => n.status !== "online").map((n) => api.nodeInfo(n.id)));
      await refresh();
    } finally {
      setPinging(false);
    }
  };

  const generateRemoteToken = async () => {
    setRemoteError(null);
    try {
      const t = await api.createBootstrapToken();
      setRemoteToken(t.token);
      setTokenExpiresAt(t.expires_at);
      setEnroll({ status: "pending", expires_at: t.expires_at });
      setRegisteredNodeId(null);
      setRegAddress("");
    } catch (e) {
      setRemoteError(e instanceof Error ? e.message : "could not create enrollment token");
    }
  };

  // Poll the token lifecycle while we're waiting for the agent to enroll, so
  // the console flips to "agent enrolled" the moment the CSR is exchanged.
  useEffect(() => {
    if (!remoteToken || enroll?.status !== "pending") return;
    const t = setInterval(() => {
      api.enrollStatus(remoteToken).then(setEnroll).catch(() => {/* transient; keep polling */});
    }, 3000);
    return () => clearInterval(t);
  }, [remoteToken, enroll?.status]);

  // Once enrolled, prefill the agent address from the hosts the agent baked
  // into its certificate (the -hosts flag it enrolled with).
  useEffect(() => {
    if (enroll?.status !== "redeemed" || regAddress !== "") return;
    const host = enroll.hosts?.[0];
    if (host) setRegAddress(`${host}:9090`);
  }, [enroll, regAddress]);

  // After registration, keep pinging the new node until it reports online.
  useEffect(() => {
    if (!registeredNodeId) return;
    const reg = nodes.find((n) => n.id === registeredNodeId);
    if (reg?.status === "online") return;
    const t = setInterval(() => {
      void api.nodeInfo(registeredNodeId).catch(() => {/* not up yet */}).then(() => refresh());
    }, 4000);
    return () => clearInterval(t);
  }, [registeredNodeId, nodes, refresh]);

  const registerRemote = async () => {
    const address = regAddress.trim();
    if (!address) return;
    setRegistering(true);
    setRemoteError(null);
    try {
      // Name/OS/Wine come from the agent itself (KRAKEN_NODE_ID + its runtime).
      const n = await api.registerNode({ address });
      setRegisteredNodeId(n.id);
      await api.nodeInfo(n.id).catch(() => {/* first contact may lag; the poller retries */});
      await refresh();
    } catch (e) {
      setRemoteError(e instanceof Error ? e.message : "could not register node");
    } finally {
      setRegistering(false);
    }
  };

  const importGame = async (id: string) => {
    setImporting(id);
    try {
      await api.importCatalog(id);
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "import failed");
    } finally {
      setImporting(null);
    }
  };

  // Serially import every not-yet-imported catalog entry. 409s from the panel
  // (spec slug already exists) are treated as success — they just mean the row
  // was imported by an earlier run.
  const importAllGames = async () => {
    setImportingAll(true);
    setError(null);
    try {
      for (const g of catalog) {
        if (g.already_imported) continue;
        setImporting(g.id);
        try {
          await api.importCatalog(g.id);
        } catch (e) {
          const msg = e instanceof Error ? e.message : "";
          if (!/already exists|409/i.test(msg)) throw e;
        }
      }
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "import failed");
    } finally {
      setImporting(null);
      setImportingAll(false);
    }
  };

  // Live console feed for the remote-node flow: one line per lifecycle stage.
  const registeredNode = registeredNodeId ? nodes.find((n) => n.id === registeredNodeId) : undefined;
  const consoleLines: ConsoleLine[] = [];
  if (remoteToken) {
    const expiry = tokenExpiresAt ? new Date(tokenExpiresAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : "";
    consoleLines.push({ state: "done", text: `one-time enrollment token generated${expiry ? ` — expires ${expiry}` : ""}` });
    if (enroll?.status === "expired") {
      consoleLines.push({ state: "error", text: "token expired (or the panel restarted) — generate a new one" });
    } else if (enroll?.status !== "redeemed") {
      consoleLines.push({ state: "active", text: "waiting for the agent to enroll — run the commands on the remote host…" });
    } else {
      consoleLines.push({
        state: "done",
        text: `agent enrolled${enroll.ip ? ` from ${enroll.ip}` : ""}${enroll.hosts?.length ? ` — advertised hosts: ${enroll.hosts.join(", ")}` : ""}`,
      });
      if (registeredNode?.status === "online") {
        consoleLines.push({ state: "done", text: `node "${registeredNode.name}" (${registeredNode.os}) is online — connection verified` });
      } else if (registeredNodeId) {
        consoleLines.push({ state: "active", text: "node registered — waiting for it to come online…" });
      } else if (registering) {
        consoleLines.push({ state: "active", text: "contacting the agent to register the node…" });
      } else {
        consoleLines.push({ state: "active", text: "confirm the agent address below and register the node" });
      }
    }
    if (remoteError) consoleLines.push({ state: "error", text: remoteError });
  }

  const done = [
    !!dbCfg && !dbCfg.using_memory, // Database: persisted on Postgres
    !!status && !status.admin_must_change_password, // Secure
    hasOnlineNode, // Connect a node
    specs.length > 0, // Add a game
    !!status?.has_server, // Deploy
  ];
  // The node + game steps gate Continue; Database/Secure/Deploy are skippable.
  const canNext = step === 2 ? hasOnlineNode : step === 3 ? specs.length > 0 : true;
  const panelOrigin = window.location.origin;

  if (restarting) {
    return (
      <main style={{ maxWidth: 560, margin: "0 auto", padding: "80px 30px" }}>
        <Card padding={28} style={{ textAlign: "center" }}>
          <div style={{ fontSize: 36, marginBottom: 12 }}>🗄️</div>
          <h2 style={{ fontWeight: 700, fontSize: 20, color: "var(--text-primary)", margin: "0 0 8px" }}>Connecting to Postgres…</h2>
          <p style={{ fontSize: 13.5, color: "var(--text-secondary)", margin: 0 }}>
            The panel is restarting onto your database. You'll return to sign-in shortly — log in again to continue setup.
          </p>
        </Card>
      </main>
    );
  }

  return (
    <main style={{ maxWidth: step === 3 ? 1080 : 760, margin: "0 auto", padding: "34px 30px 70px", transition: "max-width 220ms ease" }}>
      <div style={{ fontFamily: mono, fontSize: 12, letterSpacing: "3px", color: "var(--accent)", marginBottom: 10 }}>// FIRST RUN</div>
      <h1 style={{ fontFamily: "var(--font-display)", fontWeight: 800, fontSize: 30, letterSpacing: "-0.5px", margin: "0 0 28px", color: "var(--text-primary)" }}>
        Get Kraken ready
      </h1>

      <StepDots step={step} done={done} />

      {error && <div style={{ color: "var(--status-crashed)", fontFamily: mono, fontSize: 13, marginBottom: 16 }}>{error}</div>}

      <Card>
        {step === 0 && (
          <div>
            <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12 }}>
              <div style={SECTION_LABEL}>DATABASE</div>
              {dbCfg?.env_locked ? <Badge tone="neutral">ENV-MANAGED</Badge> : dbCfg && !dbCfg.using_memory ? <Badge tone="accent">POSTGRES</Badge> : <Badge tone="neutral">IN-MEMORY</Badge>}
            </div>
            {dbCfg?.env_locked ? (
              <p style={{ fontSize: 13.5, color: "var(--text-secondary)", lineHeight: 1.55 }}>
                The database is set via <span style={{ fontFamily: mono, color: "var(--text-primary)" }}>KRAKEN_DATABASE_URL</span> and managed outside the UI
                {dbCfg.host ? ` (${dbCfg.user}@${dbCfg.host}/${dbCfg.dbname}).` : "."}
              </p>
            ) : !dbCfg?.using_memory ? (
              <div style={{ display: "flex", alignItems: "center", gap: 12, fontSize: 14, color: "var(--text-secondary)" }}>
                <Icon name="check" size={18} style={{ color: "var(--status-running)" }} />
                Connected to Postgres — <span style={{ fontFamily: mono, color: "var(--text-primary)" }}>{dbCfg?.user}@{dbCfg?.host}/{dbCfg?.dbname}</span>.
              </div>
            ) : (
              <>
                <p style={{ fontSize: 13.5, color: "var(--text-secondary)", lineHeight: 1.55, margin: "0 0 14px" }}>
                  Kraken is on the built-in <strong>in-memory</strong> store — data resets when the panel restarts. Connect Postgres to
                  persist. We'll create the database if it doesn't exist, run migrations, then restart (you'll sign in again).
                </p>
                <div style={{ display: "flex", gap: 12, marginBottom: 12 }}>
                  <div style={{ flex: 2 }}>
                    <Input label="HOST" value={db.host} onChange={(e) => setDb({ ...db, host: e.target.value })} placeholder="db.internal or 192.168.1.20" mono />
                  </div>
                  <div style={{ flex: 1 }}>
                    <Input label="PORT" type="number" value={db.port} onChange={(e) => setDb({ ...db, port: +e.target.value })} mono />
                  </div>
                </div>
                <div style={{ display: "flex", gap: 12, marginBottom: 12 }}>
                  <div style={{ flex: 1 }}>
                    <Input label="USER" value={db.user} onChange={(e) => setDb({ ...db, user: e.target.value })} mono />
                  </div>
                  <div style={{ flex: 1 }}>
                    <Input label="PASSWORD" type="password" value={db.password} onChange={(e) => setDb({ ...db, password: e.target.value })} mono autoComplete="off" />
                  </div>
                </div>
                <div style={{ display: "flex", gap: 12 }}>
                  <div style={{ flex: 1 }}>
                    <Input label="DATABASE" value={db.dbname} onChange={(e) => setDb({ ...db, dbname: e.target.value })} mono />
                  </div>
                  <div style={{ flex: 1 }}>
                    <Select
                      label="SSL MODE"
                      mono
                      value={db.sslmode}
                      options={["disable", "require", "verify-ca", "verify-full"].map((m) => ({ value: m, label: m }))}
                      onChange={(v) => setDb({ ...db, sslmode: v })}
                    />
                  </div>
                </div>
                {dbNotice && (
                  <div style={{ display: "flex", alignItems: "center", gap: 8, marginTop: 14, color: "var(--status-running)", fontFamily: mono, fontSize: 12.5 }}>
                    <Icon name="check" size={14} /> {dbNotice}
                  </div>
                )}
                <div style={{ display: "flex", gap: 10, marginTop: 18 }}>
                  <Button variant="secondary" icon="refresh" disabled={dbBusy !== null || db.host.trim() === ""} onClick={() => void testDb()}>
                    {dbBusy === "test" ? "Testing…" : "Test connection"}
                  </Button>
                  <Button variant="primary" icon="postgresql" disabled={dbBusy !== null || db.host.trim() === ""} onClick={() => void connectDb()}>
                    {dbBusy === "connect" ? "Connecting…" : "Connect & restart"}
                  </Button>
                </div>
                <p style={{ fontSize: 12, color: "var(--text-faint)", marginTop: 14 }}>
                  Or continue on the in-memory store for now — fine for trying things out, not for production.
                </p>
              </>
            )}
          </div>
        )}

        {step === 1 && (
          <div>
            <div style={SECTION_LABEL}>SECURE THE ADMIN ACCOUNT</div>
            <div style={{ display: "flex", alignItems: "center", gap: 12, fontSize: 14, color: "var(--text-secondary)" }}>
              <Icon name="check" size={18} style={{ color: "var(--status-running)" }} />
              Admin password set — your account is secured.
            </div>
            <p style={{ fontSize: 13, color: "var(--text-muted)", marginTop: 14 }}>
              Next, connect a node (a host running the Kraken agent) so you have somewhere to deploy game servers.
            </p>
          </div>
        )}

        {step === 2 && (
          <div>
            <div style={SECTION_LABEL}>CONNECT A NODE</div>
            {nodes.length === 0 ? (
              <div style={{ fontFamily: mono, fontSize: 13, color: "var(--text-muted)", marginBottom: 14 }}>
                No nodes yet. Connect a remote node below, or start a co-located agent for an all-in-one install.
              </div>
            ) : (
              nodes.map((n) => (
                <div
                  key={n.id}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "space-between",
                    padding: "13px 16px",
                    borderRadius: "var(--radius-md)",
                    border: "1px solid var(--border-subtle)",
                    background: "rgba(7,23,29,.4)",
                    marginBottom: 10,
                  }}
                >
                  <div style={{ display: "flex", alignItems: "center", gap: 11 }}>
                    <OsIcon os="docker" size={16} style={{ color: "var(--accent)" }} />
                    <span style={{ fontFamily: mono, fontSize: 13, color: "var(--text-primary)" }}>{n.name}</span>
                    <span style={{ fontFamily: mono, fontSize: 11.5, color: "var(--text-muted)" }}>{n.address}</span>
                  </div>
                  <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                    {n.status === "online" ? (
                      <Badge tone="accent">ONLINE</Badge>
                    ) : (
                      <Badge tone="neutral">{n.status.toUpperCase()}</Badge>
                    )}
                  </div>
                </div>
              ))
            )}

            <div style={{ display: "flex", gap: 10, marginTop: 6, marginBottom: 18 }}>
              <Button size="sm" variant="secondary" icon="refresh" onClick={() => void pingAll()} disabled={pinging || nodes.length === 0}>
                {pinging ? "Checking…" : "Check node status"}
              </Button>
            </div>

            <div style={{ borderTop: "1px solid var(--border-subtle)", paddingTop: 16 }}>
              {!remoteOpen ? (
                <Button variant="ghost" icon="plus" onClick={() => setRemoteOpen(true)}>
                  Connect a remote node
                </Button>
              ) : (
                <div>
                  <div style={SECTION_LABEL}>CONNECT A REMOTE NODE</div>
                  <p style={{ fontSize: 13, color: "var(--text-secondary)", marginTop: 0, marginBottom: 12 }}>
                    Generate a one-time enrollment token, pick the target OS, and run the commands on the remote host.
                    The node names itself from its <span style={{ fontFamily: mono, color: "var(--text-primary)" }}>KRAKEN_NODE_ID</span>.
                  </p>
                  {!remoteToken ? (
                    <Button variant="secondary" icon="lock" onClick={() => void generateRemoteToken()}>
                      Generate enrollment token
                    </Button>
                  ) : (
                    <>
                      <div style={{ fontFamily: mono, fontSize: 11, color: "var(--text-faint)", margin: "10px 0 6px" }}>
                        ONE-TIME ENROLLMENT TOKEN — VALID 15 MINUTES
                      </div>
                      <div
                        style={{
                          display: "flex",
                          alignItems: "center",
                          justifyContent: "space-between",
                          gap: 10,
                          padding: "10px 12px",
                          borderRadius: "var(--radius-sm)",
                          border: "1px solid var(--border-subtle)",
                          background: "var(--bg-inset)",
                          marginBottom: 12,
                        }}
                      >
                        <code style={{ fontFamily: mono, fontSize: 12, color: "var(--accent)", wordBreak: "break-all", lineHeight: 1.5 }}>{remoteToken}</code>
                        <CopyButton text={remoteToken} />
                      </div>
                      <AgentInstallInstructions panelOrigin={panelOrigin} token={remoteToken} />
                      <EnrollConsole lines={consoleLines} />
                      {enroll?.status === "expired" && (
                        <Button variant="secondary" icon="refresh" onClick={() => void generateRemoteToken()}>
                          Generate a new token
                        </Button>
                      )}
                      {enroll?.status === "redeemed" && !(registeredNode && registeredNode.status === "online") && !registeredNodeId && (
                        <div style={{ display: "flex", gap: 10, alignItems: "flex-end", marginTop: 4 }}>
                          <div style={{ flex: 1 }}>
                            <Input
                              label="AGENT ADDRESS"
                              value={regAddress}
                              onChange={(e) => setRegAddress(e.target.value)}
                              placeholder="host:9090"
                              mono
                            />
                          </div>
                          <Button variant="primary" icon="check" disabled={registering || !regAddress.trim()} onClick={() => void registerRemote()}>
                            {registering ? "Registering…" : "Register node"}
                          </Button>
                        </div>
                      )}
                    </>
                  )}
                </div>
              )}
            </div>
          </div>
        )}

        {step === 3 && (
          <div>
            <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12, marginBottom: 12 }}>
              <div style={{ ...SECTION_LABEL, marginBottom: 0 }}>ADD A GAME</div>
              <div style={{ display: "flex", alignItems: "center", gap: 12 }}>
                <span style={{ fontFamily: mono, fontSize: 12, color: "var(--text-muted)" }}>
                  <b style={{ color: "var(--text-primary)" }}>{catalog.filter((g) => g.already_imported).length}</b> / {catalog.length} specs imported
                </span>
                <Button
                  size="sm"
                  variant="secondary"
                  icon="plus"
                  onClick={() => void importAllGames()}
                  disabled={importingAll || catalog.length === 0 || catalog.every((g) => g.already_imported)}
                >
                  {importingAll ? "Importing…" : "Import all"}
                </Button>
              </div>
            </div>
            {catalog.length === 0 ? (
              <div style={{ fontFamily: mono, fontSize: 13, color: "var(--text-muted)" }}>No catalog entries available.</div>
            ) : (
              <div style={{ overflow: "hidden", borderRadius: "var(--radius-md)", border: "1px solid var(--border-subtle)", background: "rgba(7,23,29,.4)" }}>
                <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 13 }}>
                  <thead>
                    <tr style={{ background: "rgba(3,16,21,.6)" }}>
                      <th style={CATALOG_TH}>Game</th>
                      <th style={{ ...CATALOG_TH, width: 70, textAlign: "center" }}>Platform</th>
                      <th style={{ ...CATALOG_TH, width: "48%" }}>Configuration</th>
                      <th style={{ ...CATALOG_TH, width: 130 }}>Status</th>
                    </tr>
                  </thead>
                  <tbody>
                    {catalog.map((g, i) => {
                      const appid = g.steam_app_ids?.linux ?? g.steam_app_ids?.windows;
                      const os = g.platforms.includes("linux-native") ? "linux" : "windows";
                      const isImporting = importing === g.id;
                      return (
                        <tr key={g.id} style={{ borderTop: i === 0 ? "none" : "1px solid var(--border-subtle)" }}>
                          <td style={CATALOG_TD}>
                            <div style={{ display: "flex", alignItems: "center", gap: 12 }}>
                              <div
                                style={{
                                  width: 96,
                                  height: 54,
                                  borderRadius: 4,
                                  flexShrink: 0,
                                  backgroundImage: g.banner_url ? `url(${g.banner_url})` : "repeating-linear-gradient(135deg,rgba(61,245,207,.06) 0 8px,transparent 8px 16px)",
                                  backgroundColor: "rgba(3,16,21,.7)",
                                  backgroundSize: "cover",
                                  backgroundPosition: "center",
                                  border: "1px solid var(--border-soft)",
                                }}
                              />
                              <div style={{ minWidth: 0 }}>
                                <div style={{ fontWeight: 600, fontSize: 13.5, color: "var(--text-primary)" }}>{g.name}</div>
                                <div style={{ fontFamily: mono, fontSize: 11, color: "var(--text-faint)" }}>
                                  {appid ? `SteamCMD · ${appid}` : g.slug}
                                </div>
                              </div>
                            </div>
                          </td>
                          <td style={{ ...CATALOG_TD, textAlign: "center" }}>
                            <OsIcon os={os} size={20} style={{ color: "var(--text-secondary)" }} />
                          </td>
                          <td style={{ ...CATALOG_TD, color: "var(--text-secondary)", fontSize: 12.5, lineHeight: 1.5 }}>
                            {g.description}
                          </td>
                          <td style={CATALOG_TD}>
                            {g.already_imported ? (
                              <span style={{ display: "inline-flex", alignItems: "center", gap: 6, fontFamily: mono, fontSize: 12, color: "var(--status-running)" }}>
                                <Icon name="check" size={13} /> Imported
                              </span>
                            ) : isImporting ? (
                              <span style={{ display: "inline-flex", alignItems: "center", gap: 6, fontFamily: mono, fontSize: 12, color: "var(--accent)" }}>
                                <Icon name="refresh" size={13} /> Importing…
                              </span>
                            ) : (
                              <Button size="sm" variant="secondary" icon="plus" onClick={() => void importGame(g.id)}>
                                Import
                              </Button>
                            )}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
            <p style={{ fontSize: 12.5, color: "var(--text-muted)", marginTop: 16 }}>
              Or{" "}
              <button onClick={() => navigate("/specs/new")} style={{ background: "none", border: "none", color: "var(--accent)", cursor: "pointer", padding: 0, font: "inherit" }}>
                author your own spec
              </button>
              .
            </p>
          </div>
        )}

        {step === 4 && (
          <div style={{ textAlign: "center", padding: "20px 0" }}>
            <div
              style={{
                width: 64,
                height: 64,
                borderRadius: "50%",
                margin: "0 auto 18px",
                background: "var(--gradient-iris)",
                boxShadow: "var(--elevation-glow)",
                display: "flex",
                alignItems: "center",
                justifyContent: "center",
              }}
            >
              <Icon name="check" size={30} style={{ color: "var(--text-on-accent)" }} />
            </div>
            <h2 style={{ fontWeight: 700, fontSize: 22, color: "var(--text-primary)", margin: "0 0 8px" }}>
              {status?.has_server ? "You're all set" : "Ready to deploy"}
            </h2>
            <p style={{ fontSize: 14, color: "var(--text-secondary)", margin: "0 auto 18px", maxWidth: 460 }}>
              {status?.has_server
                ? "Your first server is deployed. Head to the fleet to manage it."
                : "A node is online and a game is imported — deploy your first server to finish."}
            </p>
            <Button variant="primary" icon={status?.has_server ? "check" : "plus"} onClick={() => navigate("/", { state: { deploy: !status?.has_server } })}>
              {status?.has_server ? "Go to fleet" : "Deploy your first server"}
            </Button>
          </div>
        )}
      </Card>

      <div style={{ display: "flex", justifyContent: "space-between", marginTop: 20 }}>
        <Button variant="ghost" onClick={() => (step === 0 ? navigate("/") : setStep((s) => s - 1))}>
          {step === 0 ? "Skip for now" : "Back"}
        </Button>
        {step < STEPS.length - 1 ? (
          <div style={{ display: "flex", gap: 10 }}>
            {/* Adding a game is optional — let the user skip straight to deploy. */}
            {step === 3 && (
              <Button variant="ghost" onClick={() => setStep((s) => Math.min(STEPS.length - 1, s + 1))}>
                Skip
              </Button>
            )}
            <Button variant="primary" disabled={!canNext} onClick={() => setStep((s) => Math.min(STEPS.length - 1, s + 1))}>
              Continue
            </Button>
          </div>
        ) : (
          // Final step: let the user finish without deploying (deploy is the
          // primary call-to-action inside the card above).
          <Button variant="secondary" onClick={() => navigate("/")}>
            {status?.has_server ? "Finish" : "Skip & finish"}
          </Button>
        )}
      </div>
    </main>
  );
}
