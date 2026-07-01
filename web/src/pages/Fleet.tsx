import { useEffect, useMemo, useState } from "react";
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
import { CreateWizard } from "./CreateServer";

const mono = "var(--font-mono)";
const GRID = "28px 1.5fr 1.1fr 1fr .6fr 44px .7fr .9fr";

export function Fleet() {
  const navigate = useNavigate();
  const location = useLocation();
  const [servers, setServers] = useState<Server[]>([]);
  const [specs, setSpecs] = useState<Spec[]>([]);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [deploying, setDeploying] = useState(false);
  const [selected, setSelected] = useState<Record<string, boolean>>({});
  const [filter, setFilter] = useState("");

  const refresh = () => {
    Promise.all([api.listServers(), api.listSpecs(), api.listNodes()])
      .then(([s, sp, n]) => {
        setServers(s.servers ?? []);
        setSpecs(sp.specs ?? []);
        setNodes(n.nodes ?? []);
      })
      .catch((e) => Toaster.error(e instanceof Error ? e.message : "failed to load fleet"));
  };
  useEffect(refresh, []);

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
  const attention = servers.filter((s) => s.state === "crashed").length;
  const onlineNodes = nodes.filter((n) => n.status === "online").length;
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

  const crashedName = servers.find((s) => s.state === "crashed")?.name;

  return (
    <main style={{ maxWidth: "var(--container-max)", margin: "0 auto", padding: "34px 30px 70px" }}>
      <div style={{ display: "flex", alignItems: "flex-end", justifyContent: "space-between", gap: 20, flexWrap: "wrap", marginBottom: 26 }}>
        <div>
          <div style={{ fontFamily: mono, fontSize: 12, letterSpacing: "3px", color: "var(--accent)", marginBottom: 10 }}>// YOUR FLEET</div>
          <h1 style={{ fontFamily: "var(--font-display)", fontWeight: 800, fontSize: 34, letterSpacing: "-0.5px", margin: 0, color: "var(--text-primary)" }}>
            Servers
          </h1>
        </div>
        <Button variant="primary" icon="plus" onClick={() => setDeploying(true)}>New server</Button>
      </div>

      {/* metric tiles */}
      <div style={{ display: "grid", gridTemplateColumns: "repeat(4,1fr)", gap: 14, marginBottom: 26 }}>
        <MetricCard label="RUNNING SERVERS" value={running} />
        <MetricCard label="NEEDS ATTENTION" value={attention} accent={attention ? "var(--status-crashed)" : undefined}>
          <div style={{ fontSize: 12, color: "var(--text-muted)", marginTop: 14 }}>
            {attention ? `${crashedName ?? "a server"} crashed` : "all healthy"}
          </div>
        </MetricCard>
        <MetricCard label="NODES ONLINE" value={onlineNodes} suffix={`/${nodes.length || 0}`}>
          <div style={{ display: "flex", gap: 5, marginTop: 16 }}>
            {nodes.length === 0 ? (
              <span style={{ fontSize: 12, color: "var(--text-faint)" }}>no nodes</span>
            ) : (
              nodes.map((n) => (
                <span
                  key={n.id}
                  title={`${n.name} · ${n.status}`}
                  style={{
                    flex: 1,
                    height: 6,
                    borderRadius: 3,
                    background: n.status === "online" ? "var(--status-running)" : "var(--status-offline)",
                    boxShadow: n.status === "online" ? "0 0 6px rgba(54,229,166,.6)" : "none",
                  }}
                />
              ))
            )}
          </div>
        </MetricCard>
        <MetricCard label="FLEET MEMORY" value={fleetMem} suffix="%">
          <MetricBar pct={fleetMem} />
        </MetricCard>
      </div>


      {servers.length === 0 ? (
        <EmptyState onDeploy={() => setDeploying(true)} hasSpecs={specs.length > 0} />
      ) : (
        <Card padding={0} style={{ overflow: "hidden", background: "rgba(5,19,24,.55)" }}>
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
                onClick={() => navigate(`/servers/${r.id}`)}
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
              onDeploy={async ({ spec_id, name, variables, steam_guard_code, install_bepinex }) => {
                try {
                  const sv = await api.createServer({ spec_id, name, variables, steam_guard_code, install_bepinex });
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

function EmptyState({ onDeploy, hasSpecs }: { onDeploy: () => void; hasSpecs: boolean }) {
  return (
    <Card dashed style={{ textAlign: "center", padding: "80px 20px" }}>
      <div style={{ fontSize: 40, marginBottom: 12 }}>🐙</div>
      <div style={{ fontFamily: mono, color: "var(--text-secondary)", marginBottom: 18 }}>
        {hasSpecs ? "The deep is quiet — deploy your first server." : "No game specs yet. Add a spec, then deploy."}
      </div>
      {hasSpecs && <Button variant="primary" icon="plus" onClick={onDeploy}>New server</Button>}
    </Card>
  );
}
