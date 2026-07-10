import { useEffect, useState, useCallback, useRef } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { api } from "@/api/client";
import { useDialog } from "@/components/Dialog";
import { Toaster } from "@ds/components/core/Toast";
import { useServerStream } from "@/api/useServerStream";
import type { Node, PlatformKind, PowerActionName, Server, Spec } from "@/api/types";
import { StatusPill } from "@ds/components/core/StatusPill";
import { MetricCard, MetricBar } from "@ds/components/core/MetricCard";
import { Card } from "@ds/components/core/Card";
import { Badge } from "@ds/components/core/Badge";
import { Button } from "@ds/components/core/Button";
import { Icon } from "@ds/components/core/Icon";
import { OsIcon } from "@/components/OsIcon";
import { ServerSettingsPanel } from "./ServerSettings";
import { ServerNetworkingPanel } from "./ServerNetworking";
import { ServerFilesPanel } from "./ServerFiles";
import { ServerBackupsPanel } from "./ServerBackups";
import { ServerSchedulesPanel } from "./ServerSchedules";

const mono = "var(--font-mono)";
const LIVE_STATES = ["running", "starting", "stopping"];
const POWER_LABEL: Record<PowerActionName, string> = {
  start: "Starting server…",
  stop: "Stopping server…",
  restart: "Restarting server…",
  kill: "Killing server…",
};

// loadColor maps a 0..1 load fraction to a severity tint: green (OK) → amber → red.
function loadColor(frac: number): string {
  const f = Math.max(0, Math.min(1, frac));
  const green = [54, 229, 166];
  const amber = [244, 193, 82];
  const red = [255, 92, 87];
  const [a, b, t] = f <= 0.6 ? [green, amber, f / 0.6] : [amber, red, (f - 0.6) / 0.4];
  const c = (i: number) => Math.round(a[i] + (b[i] - a[i]) * t);
  return `rgb(${c(0)}, ${c(1)}, ${c(2)})`;
}

function lineColor(stream: string): string {
  if (stream === "stderr") return "var(--log-warn)";
  if (stream === "error") return "var(--log-error)";
  return "var(--log-text)";
}

// platformBadge maps a platform kind to an Abyssal Badge (tone + label + icon).
function platformBadge(kind: PlatformKind): { tone: "accent" | "coral" | "neutral"; label: string; icon: "linux" | "windows" | "wine" } {
  if (kind === "windows-native") return { tone: "neutral", label: "WINDOWS", icon: "windows" };
  if (kind === "linux-wine") return { tone: "coral", label: "WINE", icon: "wine" };
  return { tone: "accent", label: "LINUX", icon: "linux" };
}

// isPrivateHost reports whether a connect host is on a private network (RFC1918 /
// loopback / link-local / IPv6 ULA). Returns null when unknown. A public IP or a
// routable DNS hostname is treated as public.
function isPrivateHost(host: string): boolean | null {
  if (!host || host === "—") return null;
  const m = host.match(/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.\d{1,3}$/);
  if (m) {
    const a = Number(m[1]);
    const b = Number(m[2]);
    if (a === 10 || a === 127) return true;
    if (a === 169 && b === 254) return true;
    if (a === 172 && b >= 16 && b <= 31) return true;
    if (a === 192 && b === 168) return true;
    return false;
  }
  const h = host.toLowerCase();
  if (h === "::1" || h.startsWith("fc") || h.startsWith("fd")) return true;
  return false; // other IPv6 or a DNS hostname → routable/public
}

function formatUptime(sec: number): string {
  if (sec <= 0) return "—";
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = Math.floor(sec % 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

function formatDisk(mb: number): { value: string; unit: string } {
  if (mb >= 1024) return { value: (mb / 1024).toFixed(1), unit: " GB" };
  return { value: String(mb), unit: " MB" };
}

// ResourceChart draws CPU% and memory% history as two overlaid line plots.
function ResourceChart({ cpu, mem }: { cpu: number[]; mem: number[] }) {
  const W = 760, H = 120, pad = 6;
  const n = Math.max(cpu.length, mem.length);
  const path = (data: number[]) => {
    if (data.length < 2) return "";
    const step = (W - pad * 2) / Math.max(1, data.length - 1);
    return data
      .map((v, i) => {
        const x = pad + i * step;
        const y = H - pad - (Math.max(0, Math.min(100, v)) / 100) * (H - pad * 2);
        return `${i === 0 ? "M" : "L"}${x.toFixed(1)},${y.toFixed(1)}`;
      })
      .join(" ");
  };
  return (
    <div style={{ position: "relative" }}>
      <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" style={{ width: "100%", height: 120, display: "block" }}>
        {[0.25, 0.5, 0.75].map((g) => (
          <line key={g} x1={pad} x2={W - pad} y1={H - pad - g * (H - pad * 2)} y2={H - pad - g * (H - pad * 2)} stroke="var(--border-subtle)" strokeWidth={1} />
        ))}
        {n >= 2 && <path d={path(mem)} fill="none" stroke="var(--status-crashed)" strokeWidth={1.6} opacity={0.85} />}
        {n >= 2 && <path d={path(cpu)} fill="none" stroke="var(--accent)" strokeWidth={1.6} style={{ filter: "drop-shadow(0 0 5px rgba(61,245,207,.8))" }} />}
      </svg>
      <div style={{ position: "absolute", top: 6, right: 10, display: "flex", gap: 14, fontFamily: mono, fontSize: 10.5 }}>
        <span style={{ color: "var(--accent)" }}>■ CPU</span>
        <span style={{ color: "var(--status-crashed)" }}>■ MEM</span>
      </div>
    </div>
  );
}

export function ServerDetail() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const { confirm } = useDialog();
  const [server, setServer] = useState<Server | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [busy, setBusy] = useState<PowerActionName | null>(null);
  const [command, setCommand] = useState("");
  const [tab, setTab] = useState<"console" | "settings" | "files" | "backups" | "schedules" | "networking">("console");
  const [node, setNode] = useState<Node | null>(null);
  const [spec, setSpec] = useState<Spec | null>(null);
  const [copied, setCopied] = useState(false);

  const refresh = useCallback(() => {
    api.getServer(id).then(setServer).catch((e) => setLoadError(e instanceof Error ? e.message : "failed to load"));
  }, [id]);

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 3000);
    return () => clearInterval(t);
  }, [refresh]);

  useEffect(() => {
    if (server?.node_id) api.getNode(server.node_id).then(setNode).catch(() => {});
  }, [server?.node_id]);

  useEffect(() => {
    if (server?.spec_id) api.getSpec(server.spec_id).then(setSpec).catch(() => {});
  }, [server?.spec_id]);

  const live = !!server && LIVE_STATES.includes(server.state);
  const { lines, stats, cpuHistory, memHistory, connected, send } = useServerStream(id, live);

  const consoleRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    const el = consoleRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [lines]);

  const power = async (action: PowerActionName) => {
    setBusy(action);
    try {
      await api.powerServer(id, action);
      Toaster.info(POWER_LABEL[action]);
      refresh();
    } catch (e) {
      Toaster.error(e instanceof Error ? e.message : "power action failed");
    } finally {
      setBusy(null);
    }
  };

  const remove = async () => {
    if (!(await confirm({ title: "Delete server", message: "Delete this server and its data? This cannot be undone.", confirmLabel: "Delete", danger: true }))) return;
    try {
      await api.deleteServer(id);
      Toaster.success("Server deleted");
      navigate("/");
    } catch (e) {
      Toaster.error(e instanceof Error ? e.message : "delete failed");
    }
  };

  const submitCommand = () => {
    if (command.trim()) {
      send(command.trim());
      setCommand("");
    }
  };

  if (!server) {
    return (
      <main style={{ maxWidth: "var(--container-max)", margin: "0 auto", padding: "28px 30px" }}>
        <div style={{ fontFamily: mono, color: "var(--text-muted)" }}>{loadError ?? "Loading…"}</div>
      </main>
    );
  }

  const port = Object.entries(server.ports ?? {});
  const installing = server.state === "installing";
  const running = server.state === "running";
  // Up = the server is running or on its way up; the primary power button is a
  // Start↔Stop toggle keyed off this. Restart/Kill stay distinct.
  const up = running || server.state === "starting";
  const connectHost = node?.external_ip || node?.public_host || (node?.address ? node.address.split(":")[0] : "—");
  const primaryPort = port.length ? port[0][1] : undefined;
  const connectAddr = primaryPort != null ? `${connectHost}:${primaryPort}` : connectHost;
  const memPct = stats && stats.mem_limit_mb > 0 ? (stats.mem_used_mb / stats.mem_limit_mb) * 100 : undefined;
  const pb = platformBadge(server.kind);

  return (
    <main style={{ maxWidth: "var(--container-max)", margin: "0 auto", padding: "28px 30px 60px", minWidth: 0 }}>
      <div onClick={() => navigate("/")} style={{ display: "inline-flex", alignItems: "center", gap: 7, cursor: "pointer", marginBottom: 20, fontFamily: mono, fontSize: 12, color: "var(--text-muted)" }}>
        <span style={{ transform: "rotate(180deg)", display: "inline-flex" }}><Icon name="play" size={11} /></span> fleet
      </div>

      {/* title + power header */}
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 16, flexWrap: "wrap", marginBottom: 8 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 16 }}>
          {spec?.icon_url && (
            <img src={spec.icon_url} alt="" style={{ width: 40, height: 40, borderRadius: 10, border: "1px solid var(--border-strong)", objectFit: "cover", flex: "none" }} />
          )}
          <h1 style={{ fontFamily: "var(--font-display)", fontWeight: 800, fontSize: 32, letterSpacing: "-0.5px", margin: 0, color: "var(--text-primary)" }}>{server.name}</h1>
          <StatusPill status={server.state} />
        </div>
        <div style={{ display: "flex", gap: 9, flexWrap: "wrap" }}>
          {/* Restart + Kill only exist while the server is up; the Start↔Stop
              toggle is pinned to the far right. */}
          {up && (
            <>
              <Button variant="secondary" icon="refresh" disabled={!running || busy !== null} onClick={() => power("restart")}>Restart</Button>
              <Button variant="danger" icon="x" disabled={busy !== null} onClick={() => power("kill")}>Kill</Button>
            </>
          )}
          {up ? (
            <Button variant="danger" icon="stopping" disabled={busy !== null} onClick={() => power("stop")} style={{ marginLeft: 6 }}>Stop</Button>
          ) : (
            <Button variant="primary" icon="play" disabled={installing || server.state === "stopping" || busy !== null} onClick={() => power("start")}>Start</Button>
          )}
        </div>
      </div>

      {/* meta row */}
      <div style={{ display: "flex", gap: 10, alignItems: "center", marginBottom: 24, fontFamily: mono, fontSize: 12, color: "var(--text-muted)", flexWrap: "wrap" }}>
        <Badge tone={pb.tone}>
          {pb.icon === "wine" ? <Icon name="wine" size={11} /> : <OsIcon os={pb.icon} size={11} />}
          {pb.label}
        </Badge>
        <span>{spec?.name ?? server.spec_id.slice(0, 8)} · {node?.name ?? server.node_id.slice(0, 8)}</span>
        <span style={{ color: "var(--text-faint)" }}>·</span>
        <div style={{ display: "inline-flex", alignItems: "center", gap: 8, padding: "5px 11px", borderRadius: "var(--radius-sm)", border: "1px solid var(--border-subtle)", background: "var(--bg-inset)" }}>
          <span style={{ color: "#dff7f1" }}>{connectAddr}</span>
          <span
            onClick={() => {
              navigator.clipboard?.writeText(connectAddr);
              setCopied(true);
              setTimeout(() => setCopied(false), 1200);
            }}
            style={{ display: "inline-flex", alignItems: "center", gap: 5, cursor: "pointer", color: "var(--accent)" }}
          >
            <Icon name={copied ? "check" : "copy"} size={12} />{copied ? "copied" : "copy"}
          </span>
        </div>
        {(() => {
          const priv = isPrivateHost(connectHost);
          if (priv === null) return null;
          return <Badge tone={priv ? "neutral" : "accent"}>{priv ? "PRIVATE" : "PUBLIC"}</Badge>;
        })()}
      </div>


      {installing && (
        <div style={{ marginBottom: 20, padding: "13px 16px", borderRadius: "var(--radius-md)", border: "1px solid rgba(56,182,255,.3)", background: "rgba(56,182,255,.09)", color: "var(--text-secondary)", fontFamily: mono, fontSize: 13 }}>
          Installing — the agent is pulling and provisioning this server…
        </div>
      )}

      {/* metric tiles */}
      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit,minmax(180px,1fr))", gap: 14, marginBottom: 22 }}>
        {stats?.players_known && (
          <MetricCard label="PLAYERS" value={stats.players} suffix={stats.max_players > 0 ? `/ ${stats.max_players}` : undefined} accent={stats.max_players > 0 ? loadColor(stats.players / stats.max_players) : undefined} />
        )}
        <MetricCard label="CPU LOAD" value={stats ? stats.cpu_percent.toFixed(0) : "—"} suffix={stats ? "%" : undefined} accent={stats ? loadColor(stats.cpu_percent / 100) : undefined}>
          {memPct != null && <MetricBar pct={stats ? stats.cpu_percent : 0} />}
        </MetricCard>
        <MetricCard label="MEMORY" value={stats ? stats.mem_used_mb : "—"} suffix={stats ? `/${stats.mem_limit_mb} MB` : undefined} accent={stats && stats.mem_limit_mb > 0 ? loadColor(stats.mem_used_mb / stats.mem_limit_mb) : undefined}>
          {memPct != null && <MetricBar pct={memPct} />}
        </MetricCard>
        <MetricCard label="DISK" value={stats ? formatDisk(stats.disk_used_mb).value : "—"} suffix={stats ? formatDisk(stats.disk_used_mb).unit : undefined} />
        <MetricCard label="PORTS" value={port.length}>
          <div style={{ fontFamily: mono, fontSize: 11, color: "var(--text-muted)", marginTop: 12, lineHeight: 1.6, wordBreak: "break-word" }}>
            {port.length ? port.map(([n, p]) => `${n} → ${p}`).join("  ") : "none"}
          </div>
        </MetricCard>
        <MetricCard label="UPTIME" value={stats && stats.uptime_seconds > 0 ? formatUptime(stats.uptime_seconds) : "—"} />
      </div>

      {/* tab bar */}
      <div style={{ display: "flex", gap: 4, marginBottom: 16, borderBottom: "1px solid var(--border-subtle)" }}>
        {(["console", "settings", "networking", "files", "backups", "schedules"] as const).map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            style={{
              background: "transparent",
              border: "none",
              borderBottom: `2px solid ${tab === t ? "var(--accent)" : "transparent"}`,
              color: tab === t ? "var(--accent)" : "var(--text-secondary)",
              fontFamily: mono,
              fontSize: 13,
              padding: "10px 14px",
              cursor: "pointer",
              textTransform: "capitalize",
            }}
          >
            {t}
          </button>
        ))}
      </div>

      {tab === "settings" && <ServerSettingsPanel id={id} state={server.state} onRequestRestart={() => power("restart")} />}
      {tab === "networking" && <ServerNetworkingPanel id={id} />}
      {tab === "files" && <ServerFilesPanel id={id} name={server.name} />}
      {tab === "backups" && <ServerBackupsPanel id={id} onRequestRestart={() => power("restart")} />}
      {tab === "schedules" && <ServerSchedulesPanel id={id} />}

      {tab === "console" && cpuHistory.length > 1 && (
        <Card style={{ marginBottom: 16, padding: "14px 16px 6px" }}>
          <div style={{ fontFamily: mono, fontSize: 11, letterSpacing: "1.5px", color: "var(--text-faint)", marginBottom: 4 }}>RESOURCE HISTORY</div>
          <ResourceChart cpu={cpuHistory} mem={memHistory} />
        </Card>
      )}

      {/* live console */}
      <Card padding={0} glow={connected} style={{ display: tab === "console" ? "block" : "none", overflow: "hidden", background: "var(--bg-inset)" }}>
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", padding: "12px 16px", borderBottom: "1px solid var(--border-subtle)", background: "rgba(6,22,28,.6)" }}>
          <span style={{ fontFamily: mono, fontSize: 12, color: "var(--text-secondary)" }}>live console — {server.name}</span>
          <span style={{ display: "flex", alignItems: "center", gap: 7, fontFamily: mono, fontSize: 11, color: connected ? "var(--accent)" : "var(--text-muted)" }}>
            {connected ? (
              <>
                <span style={{ width: 7, height: 7, borderRadius: "50%", background: "var(--accent)", animation: "abyssalPulseDot 2.2s infinite" }} />
                live
              </>
            ) : (
              live ? "○ connecting…" : "○ offline"
            )}
          </span>
        </div>
        <div ref={consoleRef} style={{ padding: "15px 18px", fontFamily: mono, fontSize: 12.5, lineHeight: 1.85, height: 320, overflowY: "auto" }}>
          {lines.length === 0 ? (
            <div style={{ color: "var(--text-faint)" }}>{live ? "waiting for output…" : "server is offline — start it to stream the console"}</div>
          ) : (
            lines.map((l, i) => (
              <div key={i} style={{ color: lineColor(l.stream), whiteSpace: "pre-wrap", wordBreak: "break-word" }}>{l.text}</div>
            ))
          )}
        </div>
        <div style={{ display: "flex", gap: 10, padding: "12px 14px", borderTop: "1px solid var(--border-subtle)", background: "rgba(6,22,28,.5)" }}>
          <input
            value={command}
            onChange={(e) => setCommand(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && submitCommand()}
            placeholder={connected ? "type a command…" : "console offline"}
            disabled={!connected}
            style={{ flex: 1, padding: "10px 14px", borderRadius: "var(--radius-sm)", background: "rgba(2,12,16,.7)", border: "1px solid var(--border-subtle)", color: "var(--text-primary)", fontFamily: mono, fontSize: 12.5, outline: "none" }}
          />
          <Button size="sm" variant="secondary" disabled={!connected || !command.trim()} onClick={submitCommand}>send</Button>
        </div>
      </Card>

      <div style={{ marginTop: 24 }}>
        <Button variant="danger" onClick={remove}>Delete server</Button>
      </div>
    </main>
  );
}
