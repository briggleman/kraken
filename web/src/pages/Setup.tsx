import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api, clearToken } from "@/api/client";
import type { CatalogItem, DatabaseConfig, Node, SetupStatus, Spec } from "@/api/types";
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

// CodeBlock renders a copy-able shell command.
function CodeBlock({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  const copy = () => {
    void navigator.clipboard?.writeText(text).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };
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
      <Button size="sm" variant="ghost" onClick={copy}>
        {copied ? "Copied" : "Copy"}
      </Button>
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

  // Remote-node enrollment panel state.
  const [remoteOpen, setRemoteOpen] = useState(false);
  const [remoteName, setRemoteName] = useState("");
  const [remoteToken, setRemoteToken] = useState<string | null>(null);

  // Database step state.
  const [dbCfg, setDbCfg] = useState<DatabaseConfig | null>(null);
  const [db, setDb] = useState({ host: "", port: 5432, user: "kraken", password: "", dbname: "kraken", sslmode: "disable" });
  const [dbBusy, setDbBusy] = useState<"test" | "connect" | null>(null);
  const [dbNotice, setDbNotice] = useState<string | null>(null);
  const [restarting, setRestarting] = useState(false);

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
    const name = remoteName.trim();
    if (!name) return;
    try {
      const t = await api.createBootstrapToken({ node_name: name });
      setRemoteToken(t.token);
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not create enrollment token");
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
    <main style={{ maxWidth: 760, margin: "0 auto", padding: "34px 30px 70px" }}>
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
                  <Button variant="primary" icon="check" disabled={dbBusy !== null || db.host.trim() === ""} onClick={() => void connectDb()}>
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
                    Generate a one-time enrollment token, then run these on the remote host (it needs the{" "}
                    <code style={{ fontFamily: mono, color: "var(--text-primary)" }}>krakenctl</code> and{" "}
                    <code style={{ fontFamily: mono, color: "var(--text-primary)" }}>kraken-agent</code> binaries).
                  </p>
                  <div style={{ display: "flex", gap: 10, alignItems: "flex-end", marginBottom: 14 }}>
                    <div style={{ flex: 1 }}>
                      <Input label="NODE NAME" value={remoteName} onChange={(e) => setRemoteName(e.target.value)} placeholder="abyss-node-02" mono />
                    </div>
                    <Button variant="secondary" onClick={() => void generateRemoteToken()} disabled={!remoteName.trim()}>
                      Generate
                    </Button>
                  </div>
                  {remoteToken && (
                    <div>
                      <div style={{ fontFamily: mono, fontSize: 11, color: "var(--text-faint)", marginBottom: 6 }}>1 · ENROLL (writes ./certs)</div>
                      <CodeBlock text={`krakenctl enroll -panel ${panelOrigin} -token ${remoteToken} -hosts <this-host-ip> -out ./certs`} />
                      <div style={{ fontFamily: mono, fontSize: 11, color: "var(--text-faint)", margin: "8px 0 6px" }}>2 · START THE AGENT</div>
                      <CodeBlock text={`KRAKEN_TLS_CERT=certs/agent.pem KRAKEN_TLS_KEY=certs/agent-key.pem KRAKEN_TLS_CA=certs/ca.pem ./kraken-agent`} />
                      <p style={{ fontSize: 12.5, color: "var(--text-muted)", marginTop: 8 }}>
                        Then{" "}
                        <button onClick={() => navigate("/nodes")} style={{ background: "none", border: "none", color: "var(--accent)", cursor: "pointer", padding: 0, font: "inherit" }}>
                          register the node
                        </button>{" "}
                        with its agent address (host:9090). The token expires in 15 minutes.
                      </p>
                    </div>
                  )}
                </div>
              )}
            </div>
          </div>
        )}

        {step === 3 && (
          <div>
            <div style={SECTION_LABEL}>ADD A GAME</div>
            {catalog.length === 0 ? (
              <div style={{ fontFamily: mono, fontSize: 13, color: "var(--text-muted)" }}>No catalog entries available.</div>
            ) : (
              <div style={{ display: "grid", gridTemplateColumns: "repeat(2,1fr)", gap: 12 }}>
                {catalog.map((g) => (
                  <div
                    key={g.id}
                    style={{
                      borderRadius: "var(--radius-md)",
                      overflow: "hidden",
                      border: "1px solid var(--border-subtle)",
                      background: "rgba(7,23,29,.5)",
                    }}
                  >
                    <div
                      style={{
                        height: 70,
                        backgroundImage: g.banner_url ? `url(${g.banner_url})` : "repeating-linear-gradient(135deg,rgba(61,245,207,.05) 0 10px,transparent 10px 20px)",
                        backgroundColor: "rgba(3,16,21,.7)",
                        backgroundSize: "cover",
                        backgroundPosition: "center",
                        borderBottom: "1px solid var(--border-soft)",
                      }}
                    />
                    <div style={{ padding: 13 }}>
                      <div style={{ fontWeight: 600, fontSize: 14, color: "var(--text-primary)", marginBottom: 3 }}>{g.name}</div>
                      <div style={{ fontSize: 12, color: "var(--text-muted)", marginBottom: 10, minHeight: 32 }}>{g.description}</div>
                      {g.already_imported ? (
                        <div style={{ display: "flex", alignItems: "center", gap: 7, fontFamily: mono, fontSize: 12, color: "var(--status-running)" }}>
                          <Icon name="check" size={14} /> Imported
                        </div>
                      ) : (
                        <Button size="sm" variant="primary" icon="plus" disabled={importing === g.id} onClick={() => void importGame(g.id)}>
                          {importing === g.id ? "Importing…" : "Import"}
                        </Button>
                      )}
                    </div>
                  </div>
                ))}
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
