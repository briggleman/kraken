import { useCallback, useEffect, useMemo, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { api } from "@/api/client";
import type { Node, PowerActionName, Server, Spec } from "@/api/types";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { MetricCard, MetricBar } from "@ds/components/core/MetricCard";
import { StatusPill } from "@ds/components/core/StatusPill";
import { Icon } from "@ds/components/core/Icon";
import { OsIcon } from "@/components/OsIcon";
import { Toaster } from "@ds/components/core/Toast";
import { memColor } from "@/lib/memory";
import { CreateWizard } from "./CreateServer";

const mono = "var(--font-mono)";
const GRID = "28px 1.5fr 1.1fr 1fr .6fr 44px .7fr .9fr";

// The fleet is meant to sit open on a second monitor, so it polls instead of
// rendering one snapshot forever. Paused while the tab is hidden — a backgrounded
// dashboard nobody is reading should not keep three endpoints warm.
const POLL_MS = 10_000;
// How often the "updated Ns ago" stamp re-renders. Coarser than POLL_MS on
// purpose: the stamp only needs to look alive, not tick like a clock.
const CLOCK_MS = 5_000;

function agoLabel(sinceMs: number): string {
  const s = Math.max(0, Math.round(sinceMs / 1000));
  if (s < 10) return "just now";
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  return m < 60 ? `${m}m ago` : `${Math.floor(m / 60)}h ago`;
}

export function Fleet() {
  const navigate = useNavigate();
  const location = useLocation();
  const [servers, setServers] = useState<Server[]>([]);
  const [specs, setSpecs] = useState<Spec[]>([]);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [deploying, setDeploying] = useState(false);
  const [selected, setSelected] = useState<Record<string, boolean>>({});
  const [filter, setFilter] = useState("");
  // `loaded` gates every derived number below. Without it an unanswered fetch
  // renders as a healthy empty fleet — zero running, zero needing attention,
  // "all healthy" — which is the one thing this panel must never claim.
  const [loaded, setLoaded] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [lastLoaded, setLastLoaded] = useState<number | null>(null);
  const [nowTs, setNowTs] = useState(() => Date.now());

  const refresh = useCallback(async () => {
    try {
      const [s, sp, n] = await Promise.all([api.listServers(), api.listSpecs(), api.listNodes()]);
      setServers(s.servers ?? []);
      setSpecs(sp.specs ?? []);
      setNodes(n.nodes ?? []);
      setLoadError(null);
      setLoaded(true);
      setLastLoaded(Date.now());
    } catch (e) {
      // Deliberately keep the last good data. A failed poll means "these numbers
      // are old", not "the fleet is empty" — the banner says which, and the
      // timestamp says how old. Errors surface on the page, not in a toast that
      // dismisses itself while the condition persists.
      setLoadError(e instanceof Error ? e.message : "failed to load fleet");
    }
  }, []);

  useEffect(() => {
    let timer: number | undefined;
    const stop = () => {
      if (timer !== undefined) {
        window.clearInterval(timer);
        timer = undefined;
      }
    };
    const start = () => {
      stop();
      timer = window.setInterval(() => void refresh(), POLL_MS);
    };
    const onVisibility = () => {
      if (document.hidden) {
        stop();
      } else {
        void refresh(); // catch up immediately on return, then resume the cadence
        start();
      }
    };
    void refresh();
    if (!document.hidden) start();
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      stop();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [refresh]);

  useEffect(() => {
    const id = window.setInterval(() => setNowTs(Date.now()), CLOCK_MS);
    return () => window.clearInterval(id);
  }, []);

  // Honor a deploy hand-off from the setup wizard ("Deploy your first server").
  useEffect(() => {
    if ((location.state as { deploy?: boolean } | null)?.deploy) {
      setDeploying(true);
      navigate(".", { replace: true, state: null }); // clear so refresh doesn't reopen
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [location.state]);

  const specName = (id: string) => specs.find((s) => s.id === id)?.name ?? id.slice(0, 8);
  const nodeFor = (id: string) => nodes.find((n) => n.id === id);
  const nodeName = (id: string) => nodeFor(id)?.name ?? id.slice(0, 8);
  const addressFor = (s: Server) => {
    const node = nodeFor(s.node_id);
    const host = node?.external_ip || node?.public_host || (node?.address ? node.address.split(":")[0] : "—");
    const port = Object.values(s.ports ?? {})[0];
    return port != null ? `${host}:${port}` : host;
  };

  const running = servers.filter((s) => s.state === "running").length;
  // Attention = anything a human must act on: crashes AND failed installs — a
  // server that never provisioned is at least as broken as one that fell over.
  const attention = servers.filter((s) => s.state === "crashed" || s.state === "install_failed").length;
  const onlineNodes = nodes.filter((n) => n.status === "online").length;
  const partialNodes = nodes.filter((n) => n.status === "partial").length;
  const fleetMem = useMemo(() => {
    const total = nodes.reduce((a, n) => a + n.total_memory_mb, 0);
    const used = nodes.reduce((a, n) => a + n.allocated_memory_mb, 0);
    return total > 0 ? Math.round((used / total) * 100) : 0;
  }, [nodes]);

  const visible = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return servers;
    return servers.filter(
      (s) => s.name.toLowerCase().includes(q) || specName(s.spec_id).toLowerCase().includes(q),
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [servers, specs, filter]);

  const selCount = Object.values(selected).filter(Boolean).length;
  const toggle = (id: string) => setSelected((m) => ({ ...m, [id]: !m[id] }));

  const bulkPower = async (action: PowerActionName) => {
    const ids = Object.keys(selected).filter((id) => selected[id]);
    await Promise.allSettled(ids.map((id) => api.powerServer(id, action)));
    setSelected({});
    refresh();
  };

  const crashed = servers.find((s) => s.state === "crashed");
  const failedInstall = servers.find((s) => s.state === "install_failed");
  const attentionDetail = crashed
    ? `${crashed.name} crashed`
    : failedInstall
      ? `${failedInstall.name} install failed`
      : "a server needs attention";

  // Every tile reads "—" until a fetch has actually landed, so a pending or
  // failed load can never be mistaken for a quiet, healthy fleet.
  const stat = (n: number) => (loaded ? n : "—");
  const freshness = lastLoaded == null ? null : agoLabel(nowTs - lastLoaded);
  const stale = loaded && loadError !== null;

  return (
    <main style={{ maxWidth: "var(--container-max)", margin: "0 auto", padding: "34px 30px 70px" }}>
      <div style={{ display: "flex", alignItems: "flex-end", justifyContent: "space-between", gap: 20, flexWrap: "wrap", marginBottom: 26 }}>
        <div>
          <div style={{ fontFamily: mono, fontSize: 12, letterSpacing: "3px", color: "var(--accent)", marginBottom: 10 }}>// YOUR FLEET</div>
          <h1 style={{ fontFamily: "var(--font-display)", fontWeight: 800, fontSize: 34, letterSpacing: "-0.5px", margin: 0, color: "var(--text-primary)" }}>
            Servers
          </h1>
        </div>
        <div style={{ display: "flex", alignItems: "center", gap: 14 }}>
          {freshness && (
            <span
              style={{
                display: "inline-flex",
                alignItems: "center",
                gap: 6,
                fontFamily: mono,
                fontSize: 11,
                letterSpacing: "1px",
                color: stale ? "var(--coral-soft)" : "var(--text-muted)",
              }}
            >
              {stale && <Icon name="octagon" size={12} />}
              {stale ? `stale — last updated ${freshness}` : `updated ${freshness}`}
            </span>
          )}
          <Button variant="primary" icon="plus" onClick={() => setDeploying(true)}>New server</Button>
        </div>
      </div>

      {/* metric tiles */}
      <div style={{ display: "grid", gridTemplateColumns: "repeat(4,1fr)", gap: 14, marginBottom: 26 }}>
        <MetricCard label="RUNNING SERVERS" value={stat(running)} />
        <MetricCard label="NEEDS ATTENTION" value={stat(attention)} accent={loaded && attention ? "var(--status-crashed)" : undefined}>
          <div style={{ fontSize: 12, color: "var(--text-muted)", marginTop: 14 }}>
            {!loaded ? "awaiting fleet state" : attention ? attentionDetail : "all healthy"}
          </div>
        </MetricCard>
        <MetricCard label="NODES ONLINE" value={stat(onlineNodes)} suffix={loaded ? `/${nodes.length || 0}` : undefined}>
          <div style={{ display: "flex", gap: 5, marginTop: 16 }}>
            {!loaded ? (
              <span style={{ fontSize: 12, color: "var(--text-muted)" }}>—</span>
            ) : nodes.length === 0 ? (
              <span style={{ fontSize: 12, color: "var(--text-muted)" }}>no nodes</span>
            ) : (
              nodes.map((n) => (
                <span
                  key={n.id}
                  title={n.status === "partial"
                    ? `${n.name} · partial — agent up, container runtime unreachable${n.runtime_error ? `: ${n.runtime_error}` : ""}`
                    : `${n.name} · ${n.status}`}
                  style={{
                    flex: 1,
                    height: 6,
                    borderRadius: 3,
                    background: nodeBarColor(n.status),
                    boxShadow: n.status === "online" ? "0 0 6px rgba(54,229,166,.6)" : "none",
                  }}
                />
              ))
            )}
          </div>
          {/* A partial node reads as up on every other surface, so say it here:
              its servers still report state, but nothing new can start on it. */}
          {partialNodes > 0 && (
            <div style={{ fontSize: 12, color: "var(--status-stopping)", marginTop: 10 }}>
              {partialNodes} degraded — container runtime down
            </div>
          )}
        </MetricCard>
        <MetricCard label="FLEET MEMORY" value={stat(fleetMem)} suffix={loaded ? "%" : undefined} accent={loaded ? memColor(fleetMem) : undefined}>
          <MetricBar pct={loaded ? fleetMem : 0} color={loaded ? memColor(fleetMem) : undefined} />
        </MetricCard>
      </div>

      {/* A failed refresh on top of good data keeps the table — the numbers are
          real, just old. The banner says so and offers the retry. */}
      {stale && (
        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: 12,
            flexWrap: "wrap",
            padding: "12px 18px",
            marginBottom: 16,
            borderRadius: "var(--radius-md)",
            border: "1px solid var(--border-danger)",
            background: "var(--danger-wash)",
          }}
        >
          <Icon name="octagon" size={16} style={{ color: "var(--status-crashed)", flex: "none" }} />
          <span style={{ fontSize: 13.5, color: "var(--text-secondary)", flex: 1, minWidth: 240 }}>
            Showing the last known state from {freshness}. Refreshing is failing:{" "}
            <span style={{ fontFamily: mono, fontSize: 12.5, color: "var(--coral-soft)" }}>{loadError}</span>
          </span>
          <Button size="sm" variant="secondary" icon="refresh" onClick={() => void refresh()}>Retry</Button>
        </div>
      )}

      {!loaded && loadError ? (
        <LoadFailed error={loadError} onRetry={() => void refresh()} />
      ) : !loaded ? (
        <LoadingState />
      ) : servers.length === 0 ? (
        <EmptyState onDeploy={() => setDeploying(true)} hasSpecs={specs.length > 0} />
      ) : (
        <Card
          padding={0}
          style={{
            overflow: "hidden",
            background: "linear-gradient(180deg, rgba(10,30,37,.94), rgba(6,20,26,.97))",
            border: "1px solid rgba(61,245,207,.18)",
            boxShadow: "inset 0 1px 0 rgba(234,255,247,.05), 0 16px 36px rgba(1,6,9,.5)",
          }}
        >
          {/* table header / selection bar */}
          <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", padding: "13px 18px", borderBottom: "1px solid var(--border-subtle)" }}>
            {selCount > 0 ? (
              <>
                <span style={{ fontFamily: mono, fontSize: 12.5, color: "#dff7f1" }}>{selCount} selected</span>
                <div style={{ display: "flex", gap: 8 }}>
                  <Button size="sm" variant="secondary" icon="play" onClick={() => bulkPower("start")}>Start</Button>
                  <Button size="sm" variant="secondary" icon="stopping" onClick={() => bulkPower("stop")}>Stop</Button>
                  <Button size="sm" variant="secondary" icon="refresh" onClick={() => bulkPower("restart")}>Restart</Button>
                </div>
              </>
            ) : (
              <>
                <span style={{ fontFamily: mono, fontSize: 11, letterSpacing: "1.5px", color: "var(--text-faint)" }}>
                  {servers.length} SERVER{servers.length === 1 ? "" : "S"}
                </span>
                <div style={{ display: "flex", alignItems: "center", gap: 8, padding: "7px 12px", borderRadius: "var(--radius-sm)", border: "1px solid var(--border-subtle)", background: "var(--bg-inset)" }}>
                  <Icon name="search" size={13} style={{ color: "var(--text-muted)" }} />
                  <input
                    value={filter}
                    onChange={(e) => setFilter(e.target.value)}
                    placeholder="filter…"
                    style={{ background: "transparent", border: "none", outline: "none", fontFamily: mono, fontSize: 12, color: "var(--text-primary)", width: 140 }}
                  />
                </div>
              </>
            )}
          </div>

          {/* column headers */}
          <div style={{ display: "grid", gridTemplateColumns: GRID, gap: 10, padding: "11px 18px", fontFamily: mono, fontSize: 10, letterSpacing: "1.5px", color: "var(--text-faint)", borderBottom: "1px solid var(--border-soft)" }}>
            <span />
            <span>SERVER</span>
            <span>GAME / NODE</span>
            <span>ADDRESS</span>
            <span>MEM</span>
            <span>OS</span>
            <span>ONLINE</span>
            <span>STATUS</span>
          </div>

          {visible.map((r) => {
            const sel = !!selected[r.id];
            return (
              <div
                key={r.id}
                className="fleet-row"
                role="link"
                tabIndex={0}
                onClick={() => navigate(`/servers/${r.id}`)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === " ") {
                    e.preventDefault();
                    navigate(`/servers/${r.id}`);
                  }
                }}
                style={{ display: "grid", gridTemplateColumns: GRID, gap: 10, padding: "13px 18px", alignItems: "center", borderBottom: "1px solid var(--border-soft)", fontSize: 13, color: "#cfe7e2", cursor: "pointer" }}
              >
                <span
                  onClick={(e) => { e.stopPropagation(); toggle(r.id); }}
                  style={{
                    width: 16,
                    height: 16,
                    borderRadius: 4,
                    border: `1px solid ${sel ? "var(--border-strong)" : "rgba(61,245,207,.25)"}`,
                    background: sel ? "var(--accent-wash-16)" : "transparent",
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "center",
                  }}
                >
                  {sel && <Icon name="check" size={11} style={{ color: "var(--accent)" }} />}
                </span>
                <span style={{ fontFamily: mono, color: "var(--text-primary)", fontWeight: 500, whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}>{r.name}</span>
                <span style={{ fontSize: 12, color: "var(--text-secondary)", whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}>
                  {specName(r.spec_id)} · {nodeName(r.node_id)}
                </span>
                <span style={{ fontFamily: mono, fontSize: 11.5, color: "var(--text-secondary)", whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}>{addressFor(r)}</span>
                <span style={{ fontFamily: mono, fontSize: 12, color: "var(--text-secondary)" }}>{r.memory_mb}MB</span>
                <span title={nodeFor(r.node_id)?.os ?? "unknown"} style={{ display: "flex", color: "var(--text-secondary)" }}>
                  <OsIcon os={nodeFor(r.node_id)?.os ?? ""} />
                </span>
                <span style={{ fontFamily: mono, fontSize: 12, color: r.players_known ? "var(--text-secondary)" : "var(--text-faint)", whiteSpace: "nowrap" }}>
                  {r.players_known ? `${r.players ?? 0}${r.max_players ? `/${r.max_players}` : ""}` : "—"}
                </span>
                <StatusPill status={r.state} style={{ justifySelf: "start" }} />
              </div>
            );
          })}
          {visible.length === 0 && (
            <div style={{ padding: "26px 18px", textAlign: "center", fontFamily: mono, fontSize: 12.5, color: "var(--text-muted)" }}>
              No servers match “{filter}”.
            </div>
          )}
        </Card>
      )}

      {deploying && (
        <div
          onClick={() => setDeploying(false)}
          style={{ position: "fixed", inset: 0, zIndex: 100, background: "rgba(1,9,14,.78)", overflowY: "auto", padding: "48px 20px" }}
        >
          <div onClick={(e) => e.stopPropagation()}>
            <CreateWizard
              specs={specs}
              nodes={nodes}
              onCancel={() => setDeploying(false)}
              onDeploy={async ({ spec_id, name, variables, steam_guard_code, install_bepinex, node_id }) => {
                try {
                  const sv = await api.createServer({ spec_id, name, variables, steam_guard_code, install_bepinex, node_id });
                  setDeploying(false);
                  Toaster.success(`Deploying ${name}…`);
                  navigate(`/servers/${sv.id}`);
                } catch (e) {
                  Toaster.error(e instanceof Error ? e.message : "deploy failed");
                  setDeploying(false);
                }
              }}
            />
          </div>
        </div>
      )}
    </main>
  );
}

// nodeBarColor tints one node's health bar. Partial gets its own amber: it is
// neither serving nor gone, and grouping it with offline hides the one status
// whose fix (start Docker) is different from the others.
function nodeBarColor(status: Node["status"]): string {
  if (status === "online") return "var(--status-running)";
  if (status === "partial") return "var(--status-stopping)";
  return "var(--status-offline)";
}

// Distinct from EmptyState on purpose: "we haven't looked yet" and "there is
// nothing there" are different facts, and the old code showed the second for both.
function LoadingState() {
  return (
    <Card dashed style={{ textAlign: "center", padding: "80px 20px" }}>
      <span
        style={{
          display: "inline-block",
          width: 8,
          height: 8,
          borderRadius: "50%",
          background: "var(--accent)",
          boxShadow: "var(--glow-accent-dot)",
          animation: "abyssalPulseDot 2.2s infinite",
          marginBottom: 14,
        }}
      />
      <div style={{ fontFamily: mono, fontSize: 13, color: "var(--text-secondary)" }}>Reading fleet state…</div>
    </Card>
  );
}

// The first load failed, so there is no data to fall back on. Say what broke and
// what it does not mean — an operator seeing red on a control panel needs to know
// whether their servers are affected before anything else.
function LoadFailed({ error, onRetry }: { error: string; onRetry: () => void }) {
  return (
    <Card style={{ textAlign: "center", padding: "64px 20px", border: "1px solid var(--border-danger)", background: "var(--danger-wash)" }}>
      <Icon name="octagon" size={26} style={{ color: "var(--status-crashed)" }} />
      <div style={{ fontSize: 16, color: "var(--text-primary)", margin: "12px 0 8px" }}>Can't read the fleet.</div>
      <div style={{ fontFamily: mono, fontSize: 12.5, color: "var(--coral-soft)", marginBottom: 10, wordBreak: "break-word" }}>{error}</div>
      <div style={{ fontSize: 13, color: "var(--text-secondary)", maxWidth: 460, margin: "0 auto 20px", lineHeight: 1.6 }}>
        The Panel is unreachable or your session has expired. Your servers are unaffected — this is the Panel's view of them, not their state.
      </div>
      <Button variant="secondary" icon="refresh" onClick={onRetry}>Retry</Button>
    </Card>
  );
}

function EmptyState({ onDeploy, hasSpecs }: { onDeploy: () => void; hasSpecs: boolean }) {
  return (
    <Card dashed style={{ textAlign: "center", padding: "80px 20px" }}>
      <img
        src="/kraken-glyph-teal.png"
        alt="Kraken"
        style={{ display: "block", margin: "0 auto 12px", width: 40, height: 40, objectFit: "contain", filter: "drop-shadow(0 0 10px rgba(61,245,207,.35))" }}
      />
      <div style={{ fontFamily: mono, color: "var(--text-secondary)", marginBottom: 18 }}>
        {hasSpecs ? "The deep is quiet — deploy your first server." : "No game specs yet. Add a spec, then deploy."}
      </div>
      {hasSpecs && <Button variant="primary" icon="plus" onClick={onDeploy}>New server</Button>}
    </Card>
  );
}
