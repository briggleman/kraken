import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { Page } from "@/components/Shell";
import { useDialog } from "@/components/Dialog";
import { Toaster } from "@ds/components/core/Toast";
import { api } from "@/api/client";
import type { Node, NodeConfig, NodeConfigUpdate } from "@/api/types";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { Input } from "@ds/components/core/Input";
import { Badge } from "@ds/components/core/Badge";
import { StatusPill } from "@ds/components/core/StatusPill";
import { IconButton } from "@ds/components/core/IconButton";
import { Toggle } from "@ds/components/core/Toggle";
import { Select } from "@ds/components/core/Select";
import { MetricBar } from "@ds/components/core/MetricCard";
import type { ServerStatus } from "@ds/components/core/StatusPill";

const mono = "var(--font-mono)";

const STATUS_MAP: Record<Node["status"], { status: ServerStatus; label: string }> = {
  online: { status: "running", label: "Online" },
  offline: { status: "offline", label: "Offline" },
  cordoned: { status: "stopping", label: "Cordoned" },
};

export function Nodes() {
  const { confirm } = useDialog();
  const [nodes, setNodes] = useState<Node[]>([]);
  const [adding, setAdding] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [configuring, setConfiguring] = useState<Node | null>(null);

  const refresh = () => {
    api.listNodes().then((n) => setNodes(n.nodes ?? [])).catch((e) => Toaster.error(msg(e)));
  };
  useEffect(refresh, []);

  const ping = async (id: string) => {
    setBusy(id);
    try {
      await api.nodeInfo(id);
      Toaster.success("Node reachable");
    } catch (e) {
      Toaster.error(msg(e));
    } finally {
      setBusy(null);
      refresh();
    }
  };

  const remove = async (id: string) => {
    if (!(await confirm({ title: "Remove node", message: "Remove this node? Servers on it will be orphaned.", confirmLabel: "Remove", danger: true }))) return;
    try {
      await api.deleteNode(id);
      Toaster.success("Node removed");
      refresh();
    } catch (e) {
      Toaster.error(msg(e));
    }
  };

  return (
    <Page>
      <Header onAdd={() => setAdding(true)} />

      {nodes.length === 0 ? (
        <Empty onAdd={() => setAdding(true)} />
      ) : (
        <div style={{ display: "grid", gap: 14 }}>
          {nodes.map((n) => {
            const s = STATUS_MAP[n.status];
            const pct = n.total_memory_mb > 0 ? Math.round((n.allocated_memory_mb / n.total_memory_mb) * 100) : 0;
            return (
              <Card key={n.id} padding={20}>
                <div style={{ display: "flex", alignItems: "center", gap: 18, flexWrap: "wrap" }}>
                  <div style={{ minWidth: 0, flex: "1 1 240px" }}>
                    <div style={{ display: "flex", alignItems: "center", gap: 10, flexWrap: "wrap" }}>
                      <span style={{ fontFamily: mono, fontWeight: 700, fontSize: 15, color: "var(--text-primary)" }}>{n.name}</span>
                      <Badge tone={n.os.toLowerCase() === "windows" ? "neutral" : "accent"}>{n.os.toUpperCase()}</Badge>
                      {n.wine_enabled && <Badge tone="coral">WINE</Badge>}
                    </div>
                    <div style={{ fontFamily: mono, fontSize: 11.5, color: "var(--text-muted)", marginTop: 6 }}>{n.address}</div>
                  </div>

                  <div style={{ flex: "0 1 200px", minWidth: 150 }}>
                    <div style={{ display: "flex", justifyContent: "space-between", fontFamily: mono, fontSize: 10, letterSpacing: "1.5px", color: "var(--text-faint)", marginBottom: 6 }}>
                      <span>MEMORY</span>
                      <span style={{ color: "var(--text-secondary)" }}>{n.allocated_memory_mb}/{n.total_memory_mb} MB</span>
                    </div>
                    <MetricBar pct={pct} />
                  </div>

                  <StatusPill status={s.status} label={s.label} />

                  <div style={{ marginLeft: "auto", display: "flex", alignItems: "center", gap: 8 }}>
                    <IconButton icon="refresh" variant="secondary" disabled={busy === n.id} onClick={() => ping(n.id)} title={busy === n.id ? "Pinging…" : "Ping"} />
                    <IconButton icon="gear" variant="ghost" onClick={() => setConfiguring(n)} title="Node settings" />
                    <IconButton icon="x" variant="ghost" onClick={() => remove(n.id)} title="Delete" />
                  </div>
                </div>
              </Card>
            );
          })}
        </div>
      )}

      {configuring && (
        <NodeConfigModal node={configuring} onClose={() => setConfiguring(null)} />
      )}

      {adding && (
        <AddNodeModal
          onClose={() => setAdding(false)}
          onSubmit={async (input) => {
            try {
              const n = await api.registerNode(input);
              setAdding(false);
              // Auto-ping so the node comes online immediately (no manual ping).
              try {
                await api.nodeInfo(n.id);
              } catch {
                /* unreachable for now — the operator can retry from the card */
              }
              Toaster.success("Node registered");
              refresh();
            } catch (e) {
              Toaster.error(msg(e));
              setAdding(false);
            }
          }}
        />
      )}
    </Page>
  );
}

function Header({ onAdd }: { onAdd: () => void }) {
  return (
    <div style={{ display: "flex", alignItems: "flex-end", justifyContent: "space-between", gap: 20, flexWrap: "wrap", marginBottom: 26 }}>
      <div>
        <div style={{ fontFamily: mono, fontSize: 12, letterSpacing: "3px", color: "var(--accent)", marginBottom: 10 }}>// NODES</div>
        <h1 style={{ fontFamily: "var(--font-display)", fontWeight: 800, fontSize: 34, letterSpacing: "-0.5px", margin: 0, color: "var(--text-primary)" }}>Nodes</h1>
      </div>
      <Button variant="primary" icon="plus" onClick={onAdd}>Add node</Button>
    </div>
  );
}

function Empty({ onAdd }: { onAdd: () => void }) {
  return (
    <Card dashed style={{ textAlign: "center", padding: "80px 20px" }}>
      <div style={{ fontSize: 40, marginBottom: 12 }}>🐙</div>
      <div style={{ fontFamily: mono, color: "var(--text-secondary)", marginBottom: 18 }}>
        No nodes registered. Add a node Agent to start hosting servers.
      </div>
      <Button variant="primary" icon="plus" onClick={onAdd}>Add node</Button>
    </Card>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div style={{ marginBottom: 14 }}>
      <label style={{ display: "block", fontFamily: mono, fontSize: 11, letterSpacing: "1.5px", color: "var(--text-faint)", marginBottom: 6 }}>{label}</label>
      {children}
    </div>
  );
}

const selectStyle: React.CSSProperties = {
  width: "100%",
  padding: "12px 14px",
  borderRadius: "var(--radius-md)",
  background: "rgba(3,15,20,.7)",
  color: "var(--text-primary)",
  border: "1px solid var(--border-subtle)",
  fontSize: 14,
  fontFamily: "var(--font-sans)",
  outline: "none",
};

function AddNodeModal(props: {
  onClose: () => void;
  onSubmit: (input: {
    name: string; os: string; wine_enabled: boolean; address: string; public_host: string;
    total_memory_mb: number; port_start: number; port_end: number;
  }) => void;
}) {
  const [name, setName] = useState("");
  const [os, setOs] = useState("linux");
  const [wine, setWine] = useState(true);
  const [address, setAddress] = useState("127.0.0.1:9090");
  const [publicHost, setPublicHost] = useState("");
  const [mem, setMem] = useState(16384);
  const [portStart, setPortStart] = useState(28000);
  const [portEnd, setPortEnd] = useState(28100);

  return (
    <div onClick={props.onClose} style={{ position: "fixed", inset: 0, zIndex: 100, background: "rgba(1,9,14,.78)", display: "flex", alignItems: "center", justifyContent: "center", padding: "48px 20px", overflowY: "auto" }}>
      <div onClick={(e) => e.stopPropagation()} style={{ width: "100%", maxWidth: 460 }}>
        <Card glow padding={24}>
          <div style={{ fontFamily: mono, fontSize: 12, letterSpacing: "3px", color: "var(--accent)", marginBottom: 8 }}>// REGISTER</div>
          <h2 style={{ fontFamily: "var(--font-display)", fontWeight: 800, fontSize: 22, letterSpacing: "-0.5px", margin: "0 0 18px", color: "var(--text-primary)" }}>Add a node</h2>

          <Input label="NAME" value={name} onChange={(e) => setName(e.target.value)} placeholder="abyss-node-01" mono style={{ marginBottom: 14 }} />

          <div style={{ display: "flex", gap: 12 }}>
            <div style={{ flex: 1 }}>
              <Select
                label="OS"
                mono
                value={os}
                options={[
                  { value: "linux", label: "linux", icon: "linux" },
                  { value: "windows", label: "windows", icon: "windows" },
                ]}
                onChange={setOs}
              />
            </div>
            <div style={{ flex: 1, display: "flex", alignItems: "flex-end", paddingBottom: 14 }}>
              <label style={{ display: "flex", alignItems: "center", gap: 8, color: "var(--text-secondary)", fontSize: 14, cursor: "pointer" }}>
                <input type="checkbox" checked={wine} onChange={(e) => setWine(e.target.checked)} /> Wine enabled
              </label>
            </div>
          </div>

          <Input label="AGENT ADDRESS" value={address} onChange={(e) => setAddress(e.target.value)} placeholder="host:port (gRPC control)" mono style={{ marginBottom: 14 }} />
          <Input label="PUBLIC HOST (OPTIONAL)" value={publicHost} onChange={(e) => setPublicHost(e.target.value)} placeholder="players' connect IP / DNS — auto-detected if blank" mono helper="auto-detected if blank" style={{ marginBottom: 14 }} />

          <div style={{ display: "flex", gap: 12 }}>
            <Input label="MEMORY (MB)" type="number" value={mem} onChange={(e) => setMem(+e.target.value)} mono style={{ flex: 1 }} />
            <Input label="PORT START" type="number" value={portStart} onChange={(e) => setPortStart(+e.target.value)} mono style={{ flex: 1 }} />
            <Input label="PORT END" type="number" value={portEnd} onChange={(e) => setPortEnd(+e.target.value)} mono style={{ flex: 1 }} />
          </div>

          <div style={{ display: "flex", gap: 10, justifyContent: "flex-end", marginTop: 18 }}>
            <Button variant="ghost" onClick={props.onClose}>Cancel</Button>
            <Button
              variant="primary"
              icon="check"
              disabled={!name || !address}
              onClick={() => props.onSubmit({ name, os, wine_enabled: wine, address, public_host: publicHost, total_memory_mb: mem, port_start: portStart, port_end: portEnd })}
            >
              Register
            </Button>
          </div>
        </Card>
      </div>
    </div>
  );
}

// NodeConfigModal edits a node's System settings: where backups are stored
// (local disk or SFTP remote) and whether they're mirrored to an SFTP remote.
// Secrets are write-only — blank inputs leave the stored value untouched.
function NodeConfigModal({ node, onClose }: { node: Node; onClose: () => void }) {
  const [cfg, setCfg] = useState<NodeConfig | null>(null);
  const [target, setTarget] = useState("local");
  const [backupDir, setBackupDir] = useState("");
  // SFTP
  const [sftpHost, setSftpHost] = useState("");
  const [sftpUser, setSftpUser] = useState("");
  const [sftpPassword, setSftpPassword] = useState("");
  const [sftpKey, setSftpKey] = useState("");
  const [sftpBase, setSftpBase] = useState("");
  const [replicate, setReplicate] = useState(false);
  // Steam account (for installing games whose server isn't anonymous-downloadable)
  const [steamUser, setSteamUser] = useState("");
  const [steamPass, setSteamPass] = useState("");

  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  useEffect(() => {
    api
      .getNodeConfig(node.id)
      .then((c) => {
        setCfg(c);
        setTarget(c.backup_target || "local");
        setBackupDir(c.backup_dir ?? "");
        setSftpHost(c.sftp_host ?? "");
        setSftpUser(c.sftp_user ?? "");
        setSftpBase(c.sftp_base_path ?? "");
        setReplicate(c.replicate_to_sftp);
        setSteamUser(c.steam_username ?? "");
      })
      .catch((e) => setError(msg(e)));
  }, [node.id]);

  const showSftp = target === "sftp" || replicate;

  const save = async () => {
    setBusy(true);
    setError(null);
    setNotice(null);
    // Only send secrets when the operator typed a new value (blank keeps stored).
    const input: NodeConfigUpdate = {
      backup_target: target,
      backup_dir: backupDir,
      sftp_host: sftpHost,
      sftp_user: sftpUser,
      sftp_base_path: sftpBase,
      replicate_to_sftp: replicate,
      steam_username: steamUser,
    };
    if (sftpPassword) input.sftp_password = sftpPassword;
    if (sftpKey) input.sftp_private_key = sftpKey;
    if (steamPass) input.steam_password = steamPass;
    try {
      const r = await api.updateNodeConfig(node.id, input);
      setCfg(r);
      setSftpPassword("");
      setSftpKey("");
      setSteamPass("");
      if (!r.applied) {
        setNotice(r.apply_detail || "Saved. Will apply when the node next checks in.");
      } else if (r.apply_ok) {
        setNotice(`Saved and applied. ${r.apply_detail}`);
      } else {
        setError(`Saved, but the target is unreachable: ${r.apply_detail}`);
      }
    } catch (e) {
      setError(msg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div onClick={onClose} style={{ position: "fixed", inset: 0, zIndex: 100, background: "rgba(1,9,14,.78)", display: "flex", alignItems: "center", justifyContent: "center", padding: "48px 20px", overflowY: "auto" }}>
      <div onClick={(e) => e.stopPropagation()} style={{ width: "100%", maxWidth: 520 }}>
        <Card glow padding={24}>
          <div style={{ fontFamily: mono, fontSize: 12, letterSpacing: "3px", color: "var(--accent)", marginBottom: 8 }}>// NODE SETTINGS</div>
          <h2 style={{ fontFamily: "var(--font-display)", fontWeight: 800, fontSize: 22, letterSpacing: "-0.5px", margin: "0 0 4px", color: "var(--text-primary)" }}>{node.name}</h2>
          <p style={{ margin: "0 0 18px", fontSize: 13, color: "var(--text-secondary)" }}>Where this node stores backups, and whether they're mirrored off-node.</p>

          <Select
            label="BACKUP TARGET"
            value={target}
            options={[
              { value: "local", label: "Local disk", icon: "database" },
              { value: "share", label: "Network share (SMB/NFS)", icon: "folder" },
              { value: "sftp", label: "SFTP remote", icon: "lock" },
            ]}
            onChange={setTarget}
          />

          {target === "local" && (
            <Input label="BACKUP DIR (OPTIONAL)" value={backupDir} onChange={(e) => setBackupDir(e.target.value)} mono placeholder="leave blank for the node default" helper="Supports {{SLUG}} (the game's slug) for per-game folders, e.g. /var/backups/{{SLUG}}." style={{ marginBottom: 14 }} />
          )}

          {target === "share" && (
            <Input
              label="SHARE MOUNT PATH"
              value={backupDir}
              onChange={(e) => setBackupDir(e.target.value)}
              mono
              placeholder="/mnt/unas/games/{{SLUG}}  or  Z:\kraken\{{SLUG}}"
              helper="Mount the NAS share on this node's host first (SMB/NFS); Kraken writes backups here. Supports {{SLUG}} (the game's slug) for per-game folders, e.g. /media/games/{{SLUG}}/backup. Save verifies the mount is writable."
              style={{ marginBottom: 14 }}
            />
          )}

          <label style={{ display: "flex", alignItems: "center", gap: 10, margin: "4px 0 14px", color: "var(--text-secondary)", fontSize: 14 }}>
            <Toggle checked={replicate} onChange={setReplicate} />
            Mirror backups to an SFTP remote
          </label>

          {showSftp && (
            <div style={{ borderTop: "1px solid var(--border-subtle)", paddingTop: 14 }}>
              <div style={{ fontFamily: mono, fontSize: 11, letterSpacing: "1.5px", color: "var(--text-faint)", marginBottom: 12 }}>SFTP REMOTE</div>
              <Input label="HOST" value={sftpHost} onChange={(e) => setSftpHost(e.target.value)} mono placeholder="host:port (default 22)" style={{ marginBottom: 14 }} />
              <Input label="USERNAME" value={sftpUser} onChange={(e) => setSftpUser(e.target.value)} mono autoComplete="off" style={{ marginBottom: 14 }} />
              <Input label={cfg?.sftp_password_configured ? "REPLACE PASSWORD" : "PASSWORD"} type="password" value={sftpPassword} onChange={(e) => setSftpPassword(e.target.value)} mono autoComplete="off" placeholder={cfg?.sftp_password_configured ? "•••••••• (leave blank to keep)" : "or use a private key"} style={{ marginBottom: 14 }} />
              <Field label={cfg?.sftp_key_configured ? "REPLACE PRIVATE KEY (PEM)" : "PRIVATE KEY (PEM, OPTIONAL)"}>
                <textarea
                  style={{ ...selectStyle, fontFamily: mono, fontSize: 12, minHeight: 90, resize: "vertical" }}
                  value={sftpKey}
                  onChange={(e) => setSftpKey(e.target.value)}
                  placeholder={cfg?.sftp_key_configured ? "(leave blank to keep stored key)" : "-----BEGIN OPENSSH PRIVATE KEY-----"}
                  autoComplete="off"
                />
              </Field>
              <Input label="BASE PATH" value={sftpBase} onChange={(e) => setSftpBase(e.target.value)} mono placeholder="/backups/kraken" helper="Supports {{SLUG}} (the game's slug), e.g. /backups/{{SLUG}}." style={{ marginBottom: 4 }} />
            </div>
          )}

          <div style={{ borderTop: "1px solid var(--border-subtle)", paddingTop: 14, marginTop: 14 }}>
            <div style={{ fontFamily: mono, fontSize: 11, letterSpacing: "1.5px", color: "var(--text-faint)", marginBottom: 6 }}>STEAM ACCOUNT</div>
            <p style={{ margin: "0 0 12px", fontSize: 12.5, color: "var(--text-muted)" }}>
              Needed only for games whose dedicated server isn't anonymous-downloadable (e.g. V Rising). Enter a Steam account that owns the game; provide the Steam Guard code at deploy time.
            </p>
            <Input label="USERNAME" value={steamUser} onChange={(e) => setSteamUser(e.target.value)} mono autoComplete="off" style={{ marginBottom: 14 }} />
            <Input label={cfg?.steam_configured ? "REPLACE PASSWORD" : "PASSWORD"} type="password" value={steamPass} onChange={(e) => setSteamPass(e.target.value)} mono autoComplete="off" placeholder={cfg?.steam_configured ? "•••••••• (leave blank to keep)" : ""} style={{ marginBottom: 4 }} />
          </div>

          {error && <div style={{ color: "var(--status-crashed)", fontFamily: mono, fontSize: 12.5, marginTop: 14 }}>{error}</div>}
          {notice && <div style={{ color: "var(--status-running)", fontFamily: mono, fontSize: 12.5, marginTop: 14 }}>{notice}</div>}

          <div style={{ display: "flex", gap: 10, justifyContent: "flex-end", marginTop: 18 }}>
            <Button variant="ghost" onClick={onClose}>Close</Button>
            <Button variant="primary" icon="check" disabled={busy} onClick={save}>{busy ? "Saving…" : "Save & apply"}</Button>
          </div>
        </Card>
      </div>
    </div>
  );
}

function msg(e: unknown): string {
  return e instanceof Error ? e.message : "request failed";
}
