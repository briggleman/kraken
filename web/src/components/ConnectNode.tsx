import { useEffect, useState } from "react";
import { api } from "@/api/client";
import type { EnrollStatus, Node } from "@/api/types";
import { Button } from "@ds/components/core/Button";
import { Input } from "@ds/components/core/Input";
import { Icon } from "@ds/components/core/Icon";
import { OsIcon } from "@/components/OsIcon";

const mono = "var(--font-mono)";

const SECTION_LABEL: React.CSSProperties = {
  fontFamily: mono,
  fontSize: 11,
  letterSpacing: "1.5px",
  color: "var(--text-faint)",
  marginBottom: 14,
};

// AgentInstallInstructions surfaces per-OS install commands for a remote
// Agent, gated behind Linux / Windows tabs so the operator sees a shell
// matching the host they're about to run against. The Linux flow uses the
// bare-metal install.sh wrapper (installs the systemd unit); the Windows
// flow downloads the release binaries and enrolls directly (matches
// deploy/windows/README.md — nssm service install is documented there).
// Registration happens inline once the agent enrolls, so these steps end
// at "agent running".
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
export function CopyButton({ text }: { text: string }) {
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

// EnrollConsole is the live status feed for the connect flow: one line per
// lifecycle stage, styled like a terminal readout so the operator can see
// exactly where the process is (and where it stalled).
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

/**
 * ConnectNode is the one true node-onboarding flow, shared by the setup
 * wizard and the Nodes page: generate a one-time enrollment token → per-OS
 * install instructions → live status console (token → enrolled → registered
 * → online) → inline registration (identity comes from the agent itself).
 *
 * Layout intentionally puts the console + register form ABOVE the token /
 * instructions block: it's the "where am I?" readout the eye returns to.
 */
export function ConnectNode({
  nodes,
  refresh,
  defaultOpen = false,
}: {
  /** Current node list — used to detect the registered node coming online. */
  nodes: Node[];
  /** Re-fetch the node list (called after registration + on the online poll). */
  refresh: () => void | Promise<void>;
  /** Skip the "Connect a remote node" reveal button (e.g. inside a modal). */
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const [token, setToken] = useState<string | null>(null);
  const [tokenExpiresAt, setTokenExpiresAt] = useState<string | null>(null);
  const [enroll, setEnroll] = useState<EnrollStatus | null>(null);
  const [regAddress, setRegAddress] = useState("");
  const [registering, setRegistering] = useState(false);
  const [registeredNodeId, setRegisteredNodeId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Advanced (optional) registration fields; blank = agent-derived / defaults.
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [advName, setAdvName] = useState("");
  const [advMem, setAdvMem] = useState("");
  const [advPortStart, setAdvPortStart] = useState("");
  const [advPortEnd, setAdvPortEnd] = useState("");

  const panelOrigin = window.location.origin;

  const generateToken = async () => {
    setError(null);
    try {
      const t = await api.createBootstrapToken();
      setToken(t.token);
      setTokenExpiresAt(t.expires_at);
      setEnroll({ status: "pending", expires_at: t.expires_at });
      setRegisteredNodeId(null);
      setRegAddress("");
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not create enrollment token");
    }
  };

  // Poll the token lifecycle while we're waiting for the agent to enroll, so
  // the console flips to "agent enrolled" the moment the CSR is exchanged.
  useEffect(() => {
    if (!token || enroll?.status !== "pending") return;
    const t = setInterval(() => {
      api.enrollStatus(token).then(setEnroll).catch(() => {/* transient; keep polling */});
    }, 3000);
    return () => clearInterval(t);
  }, [token, enroll?.status]);

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

  const register = async () => {
    const address = regAddress.trim();
    if (!address) return;
    setRegistering(true);
    setError(null);
    try {
      // Name/OS/Wine come from the agent itself (KRAKEN_NODE_ID + its runtime)
      // unless the advanced fields override them.
      const n = await api.registerNode({
        address,
        name: advName.trim() || undefined,
        total_memory_mb: advMem ? +advMem : undefined,
        port_start: advPortStart ? +advPortStart : undefined,
        port_end: advPortEnd ? +advPortEnd : undefined,
      });
      setRegisteredNodeId(n.id);
      await api.nodeInfo(n.id).catch(() => {/* first contact may lag; the poller retries */});
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not register node");
    } finally {
      setRegistering(false);
    }
  };

  // Console feed: one line per lifecycle stage.
  const registeredNode = registeredNodeId ? nodes.find((n) => n.id === registeredNodeId) : undefined;
  const lines: ConsoleLine[] = [];
  if (token) {
    const expiry = tokenExpiresAt ? new Date(tokenExpiresAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : "";
    lines.push({ state: "done", text: `one-time enrollment token generated${expiry ? ` — expires ${expiry}` : ""}` });
    if (enroll?.status === "expired") {
      lines.push({ state: "error", text: "token expired (or the panel restarted) — generate a new one" });
    } else if (enroll?.status !== "redeemed") {
      lines.push({ state: "active", text: "waiting for the agent to enroll — run the commands on the remote host…" });
    } else {
      lines.push({
        state: "done",
        text: `agent enrolled${enroll.ip ? ` from ${enroll.ip}` : ""}${enroll.hosts?.length ? ` — advertised hosts: ${enroll.hosts.join(", ")}` : ""}`,
      });
      if (registeredNode?.status === "online") {
        lines.push({ state: "done", text: `node "${registeredNode.name}" (${registeredNode.os}) is online — connection verified` });
      } else if (registeredNodeId) {
        lines.push({ state: "active", text: "node registered — waiting for it to come online…" });
      } else if (registering) {
        lines.push({ state: "active", text: "contacting the agent to register the node…" });
      } else {
        lines.push({ state: "active", text: "confirm the agent address below and register the node" });
      }
    }
    if (error) lines.push({ state: "error", text: error });
  }

  const showRegisterForm = enroll?.status === "redeemed" && !(registeredNode && registeredNode.status === "online") && !registeredNodeId;

  return (
    <div>
      <EnrollConsole lines={lines} />
      {enroll?.status === "expired" && (
        <div style={{ marginBottom: 14 }}>
          <Button variant="secondary" icon="refresh" onClick={() => void generateToken()}>
            Generate a new token
          </Button>
        </div>
      )}
      {showRegisterForm && (
        <div style={{ marginBottom: 16 }}>
          <div style={{ display: "flex", gap: 10, alignItems: "flex-end" }}>
            <div style={{ flex: 1 }}>
              <Input label="AGENT ADDRESS" value={regAddress} onChange={(e) => setRegAddress(e.target.value)} placeholder="host:9090" mono />
            </div>
            <Button variant="primary" icon="check" disabled={registering || !regAddress.trim()} onClick={() => void register()}>
              {registering ? "Registering…" : "Register node"}
            </Button>
          </div>
          <button
            onClick={() => setAdvancedOpen((v) => !v)}
            style={{ background: "none", border: "none", color: "var(--text-muted)", cursor: "pointer", padding: 0, marginTop: 10, fontFamily: mono, fontSize: 11, letterSpacing: "1px" }}
          >
            {advancedOpen ? "▾" : "▸"} ADVANCED (NAME · MEMORY · PORT RANGE)
          </button>
          {advancedOpen && (
            <div style={{ display: "flex", gap: 10, marginTop: 10 }}>
              <div style={{ flex: 2 }}>
                <Input label="NAME OVERRIDE" value={advName} onChange={(e) => setAdvName(e.target.value)} placeholder="blank = agent's KRAKEN_NODE_ID" mono />
              </div>
              <div style={{ flex: 1 }}>
                <Input label="MEMORY (MB)" type="number" value={advMem} onChange={(e) => setAdvMem(e.target.value)} placeholder="auto" mono />
              </div>
              <div style={{ flex: 1 }}>
                <Input label="PORT START" type="number" value={advPortStart} onChange={(e) => setAdvPortStart(e.target.value)} placeholder="28000" mono />
              </div>
              <div style={{ flex: 1 }}>
                <Input label="PORT END" type="number" value={advPortEnd} onChange={(e) => setAdvPortEnd(e.target.value)} placeholder="28999" mono />
              </div>
            </div>
          )}
        </div>
      )}

      <div style={{ borderTop: "1px solid var(--border-subtle)", paddingTop: 16 }}>
        {!open ? (
          <Button variant="ghost" icon="plus" onClick={() => setOpen(true)}>
            Connect a remote node
          </Button>
        ) : (
          <div>
            <div style={SECTION_LABEL}>CONNECT A REMOTE NODE</div>
            <p style={{ fontSize: 13, color: "var(--text-secondary)", marginTop: 0, marginBottom: 12 }}>
              Generate a one-time enrollment token, pick the target OS, and run the commands on the remote host.
              The node names itself from its <span style={{ fontFamily: mono, color: "var(--text-primary)" }}>KRAKEN_NODE_ID</span>.
            </p>
            {!token ? (
              <Button variant="secondary" icon="lock" onClick={() => void generateToken()}>
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
                  <code style={{ fontFamily: mono, fontSize: 12, color: "var(--accent)", wordBreak: "break-all", lineHeight: 1.5 }}>{token}</code>
                  <CopyButton text={token} />
                </div>
                <AgentInstallInstructions panelOrigin={panelOrigin} token={token} />
              </>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
