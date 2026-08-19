import { useCallback, useEffect, useState } from "react";
import { NavLink, Outlet, useNavigate } from "react-router-dom";
import type { ReactNode } from "react";
import { Boxes, HardDrive, Layers, Users, ShieldCheck, ScrollText, BookOpen, LogOut, ChevronDown, Rocket, Settings as SettingsIcon } from "lucide-react";
import { api } from "@/api/client";
import { useAuth } from "@/auth";
import { memColor } from "@/lib/memory";
import { AmbientBackground } from "./AmbientBackground";

const mono = "var(--font-mono)";

const NAV = [
  { to: "/", label: "Fleet", icon: Boxes, end: true },
  { to: "/nodes", label: "Nodes", icon: HardDrive, end: false },
  { to: "/specs", label: "Game Specs", icon: Layers, end: false },
  { to: "/docs", label: "API Docs", icon: BookOpen, end: false },
];

// The three destinations that earn a spot in the bar itself. Docs and the admin
// pages stay under the user menu, which also keeps the full NAV for phones.
const BAR_NAV = NAV.slice(0, 3);

// How often the header's live cluster re-reads fleet state. Coarser than the
// fleet page's own poll — the cluster is a glance, not the dashboard.
const PULSE_MS = 15_000;

type Pulse = {
  running: number;
  crashed: number;
  nodesOnline: number;
  nodesTotal: number;
  memPct: number;
};

/** The live instrument cluster: running count, node health, fleet memory.
 *  Visible from every page so "is everything OK" never needs a navigation.
 *  Renders nothing until a fetch lands; dims (rather than lies) when polls
 *  start failing. */
function FleetPulse() {
  const [pulse, setPulse] = useState<Pulse | null>(null);
  const [stale, setStale] = useState(false);

  const read = useCallback(async () => {
    try {
      const [s, n] = await Promise.all([api.listServers(), api.listNodes()]);
      const servers = s.servers ?? [];
      const nodes = n.nodes ?? [];
      const totalMem = nodes.reduce((a, x) => a + x.total_memory_mb, 0);
      const usedMem = nodes.reduce((a, x) => a + x.allocated_memory_mb, 0);
      setPulse({
        running: servers.filter((x) => x.state === "running").length,
        crashed: servers.filter((x) => x.state === "crashed").length,
        nodesOnline: nodes.filter((x) => x.status === "online").length,
        nodesTotal: nodes.length,
        memPct: totalMem > 0 ? Math.round((usedMem / totalMem) * 100) : 0,
      });
      setStale(false);
    } catch {
      setStale(true); // keep the last known numbers, but say they're old
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
      timer = window.setInterval(() => void read(), PULSE_MS);
    };
    const onVisibility = () => {
      if (document.hidden) {
        stop();
      } else {
        void read();
        start();
      }
    };
    void read();
    if (!document.hidden) start();
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      stop();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [read]);

  if (!pulse) return null;

  const cell = {
    display: "flex",
    alignItems: "center",
    gap: 7,
    padding: "7px 13px",
    fontFamily: mono,
    fontSize: 11,
    letterSpacing: 1,
    color: "var(--text-secondary)",
  } as const;
  const divider = { borderLeft: "1px solid var(--border-soft)" } as const;
  const unit = { color: "var(--text-faint)" } as const;

  const healthy = pulse.crashed === 0;
  const nodesDegraded = pulse.nodesOnline < pulse.nodesTotal;

  return (
    <div
      className="nav-cluster"
      title={stale ? "Last known values — the Panel isn't answering right now." : undefined}
      style={{
        marginRight: 14,
        border: "1px solid var(--border-soft)",
        borderRadius: 10,
        background: "rgba(3,13,18,.55)",
        overflow: "hidden",
        opacity: stale ? 0.55 : 1,
        transition: "opacity var(--duration-base) var(--ease-out)",
      }}
    >
      <span style={cell} title={healthy ? `${pulse.running} running, none crashed` : `${pulse.crashed} crashed — open the fleet`}>
        <span
          style={{
            width: 7,
            height: 7,
            borderRadius: "50%",
            background: healthy ? "var(--status-running)" : "var(--status-crashed)",
            boxShadow: healthy ? "0 0 8px rgba(54,229,166,.8)" : "0 0 8px rgba(255,92,87,.8)",
            animation: stale ? "none" : "abyssalPulseDot 2.4s infinite",
          }}
        />
        <span style={{ color: healthy ? "var(--text-primary)" : "var(--status-crashed)", fontWeight: 600 }}>
          {healthy ? pulse.running : pulse.crashed}
        </span>
        <span style={healthy ? unit : { color: "var(--status-crashed)" }}>{healthy ? "RUNNING" : "CRASHED"}</span>
      </span>
      <span style={{ ...cell, ...divider }} title={nodesDegraded ? "A node is unreachable or degraded — open Nodes" : "All nodes online"}>
        <span style={{ color: nodesDegraded ? "var(--status-stopping)" : "var(--text-primary)", fontWeight: 600 }}>
          {pulse.nodesOnline}
          <span style={unit}>/{pulse.nodesTotal}</span>
        </span>
        <span style={unit}>NODES</span>
      </span>
      <span style={{ ...cell, ...divider }} title="Fleet memory allocated across all nodes">
        <span style={{ color: memColor(pulse.memPct), fontWeight: 600 }}>
          {pulse.memPct}
          <span style={unit}>%</span>
        </span>
        <span style={unit}>MEM</span>
      </span>
    </div>
  );
}

const ADMIN_NAV = [
  { to: "/admin/users", label: "Users", icon: Users, end: false },
  { to: "/admin/roles", label: "Roles", icon: ShieldCheck, end: false },
  { to: "/admin/audit", label: "Audit log", icon: ScrollText, end: false },
  { to: "/admin/settings", label: "Settings", icon: SettingsIcon, end: false },
];

function MenuLink({
  to,
  label,
  icon: Icon,
  end,
  onClick,
}: {
  to: string;
  label: string;
  icon: typeof Boxes;
  end: boolean;
  onClick: () => void;
}) {
  return (
    <NavLink
      to={to}
      end={end}
      onClick={onClick}
      style={({ isActive }) => ({
        display: "flex",
        alignItems: "center",
        gap: 11,
        padding: "10px 16px",
        fontSize: 14,
        fontFamily: "var(--font-sans)",
        color: isActive ? "var(--accent)" : "var(--text-secondary)",
        background: isActive ? "rgba(61,245,207,.08)" : "transparent",
      })}
    >
      <Icon size={16} strokeWidth={2} />
      {label}
    </NavLink>
  );
}

function UserMenu() {
  const { user, role, logout, hasPerm } = useAuth();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [setupComplete, setSetupComplete] = useState(true);
  const isAdmin = hasPerm("user.manage");
  const close = () => setOpen(false);

  // Surface a Setup shortcut until first-run onboarding is finished.
  useEffect(() => {
    api
      .setupStatus()
      .then((s) => setSetupComplete(s.setup_complete))
      .catch(() => setSetupComplete(true));
  }, []);

  return (
    <div style={{ position: "relative" }}>
      <button
        onClick={() => setOpen((o) => !o)}
        style={{
          display: "flex",
          alignItems: "center",
          gap: 9,
          padding: "8px 12px",
          borderRadius: "var(--radius-pill)",
          background: open ? "rgba(61,245,207,.12)" : "rgba(61,245,207,.08)",
          border: `1px solid ${open ? "var(--border-strong)" : "rgba(61,245,207,.16)"}`,
          fontFamily: mono,
          fontSize: 12.5,
          color: "var(--accent)",
          cursor: "pointer",
        }}
      >
        <span style={{ width: 7, height: 7, borderRadius: "50%", background: "var(--accent)", boxShadow: "0 0 8px var(--accent)" }} />
        {user?.username ?? "operator"}
        <ChevronDown size={14} style={{ transform: open ? "rotate(180deg)" : "none", transition: "transform var(--duration-fast) var(--ease-out)" }} />
      </button>

      {open && (
        <>
          <div onClick={close} style={{ position: "fixed", inset: 0, zIndex: 50 }} />
          <div
            style={{
              position: "absolute",
              top: 44,
              right: 0,
              zIndex: 51,
              minWidth: 220,
              background: "var(--bg-raised)",
              border: "1px solid var(--border-strong)",
              borderRadius: "var(--radius-md)",
              boxShadow: "var(--elevation-e2)",
              overflow: "hidden",
              paddingBottom: 6,
            }}
          >
            <div style={{ padding: "12px 16px 10px", borderBottom: "1px solid var(--border-subtle)" }}>
              <div style={{ fontFamily: mono, fontSize: 13, color: "var(--text-primary)" }}>{user?.username}</div>
              <div style={{ fontFamily: mono, fontSize: 10.5, letterSpacing: 1, color: "var(--text-faint)", marginTop: 3 }}>
                {(role?.name ?? user?.role_id ?? "").toUpperCase()}
              </div>
            </div>

            <div style={{ padding: "6px 0" }}>
              {!setupComplete && <MenuLink to="/setup" label="Setup" icon={Rocket} end={false} onClick={close} />}
              {NAV.map((n) => (
                <MenuLink key={n.to} {...n} onClick={close} />
              ))}
            </div>

            {isAdmin && (
              <div style={{ paddingTop: 6, borderTop: "1px solid var(--border-subtle)" }}>
                <div style={{ fontFamily: mono, fontSize: 10, letterSpacing: 1.5, color: "var(--text-faint)", padding: "8px 16px 4px" }}>ADMIN</div>
                {ADMIN_NAV.map((n) => (
                  <MenuLink key={n.to} {...n} onClick={close} />
                ))}
              </div>
            )}

            <div style={{ borderTop: "1px solid var(--border-subtle)", marginTop: 6, paddingTop: 6 }}>
              <button
                onClick={() => {
                  close();
                  void logout().then(() => navigate("/login"));
                }}
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: 11,
                  width: "100%",
                  padding: "10px 16px",
                  background: "transparent",
                  border: "none",
                  cursor: "pointer",
                  textAlign: "left",
                  fontFamily: "var(--font-sans)",
                  fontSize: 14,
                  color: "var(--text-secondary)",
                }}
              >
                <LogOut size={16} strokeWidth={2} />
                Log out
              </button>
            </div>
          </div>
        </>
      )}
    </div>
  );
}

/** Build stamp, pinned to the bottom-right corner of every authenticated screen.
 *  Deliberately quiet — it exists so a bug report can name an exact build without
 *  anyone digging. Sits under the toaster's z-index (a toast may briefly cover it)
 *  and ignores pointer events so it can never intercept a click. */
function VersionStamp() {
  const [v, setV] = useState<string>("");
  const [title, setTitle] = useState<string>("");

  useEffect(() => {
    let live = true;
    api
      .version()
      .then((info) => {
        if (!live) return;
        setV(info.version);
        setTitle(`Kraken Panel ${info.version}${info.commit && info.commit !== "none" ? ` · commit ${info.commit}` : ""}${info.date && info.date !== "unknown" ? ` · built ${info.date}` : ""}`);
      })
      .catch(() => {/* a missing version is not worth an error toast */});
    return () => { live = false; };
  }, []);

  if (!v) return null;
  return (
    <div
      title={title}
      style={{
        position: "fixed",
        right: 14,
        bottom: 10,
        zIndex: 20,
        pointerEvents: "none",
        fontFamily: mono,
        fontSize: 10.5,
        letterSpacing: 0.5,
        color: "var(--text-faint)",
      }}
    >
      v{v}
    </div>
  );
}

/** Authenticated app shell: ambient backdrop + top bar (with the username menu) +
 *  full-width routed content. */
export function Shell() {
  const navigate = useNavigate();

  return (
    <div style={{ position: "relative", minHeight: "100vh", overflowX: "hidden", background: "var(--bg-abyss)" }}>
      <AmbientBackground atmosphere="balanced" />

      <div style={{ position: "relative", zIndex: 10 }}>
        <nav
          style={{
            position: "sticky",
            top: 0,
            zIndex: 40,
            display: "flex",
            alignItems: "center",
            height: "var(--nav-height)",
            padding: "0 24px 0 28px",
            backdropFilter: "blur(var(--blur-nav))",
            WebkitBackdropFilter: "blur(var(--blur-nav))",
            background: "rgba(2,9,14,.66)",
            borderBottom: "1px solid var(--border-subtle)",
          }}
        >
          <div style={{ display: "flex", alignItems: "center", gap: 11, cursor: "pointer" }} onClick={() => navigate("/")}>
            <img
              src="/kraken-glyph-teal.png"
              alt="Kraken"
              style={{ width: 28, height: 28, objectFit: "contain", filter: "drop-shadow(0 0 7px rgba(61,245,207,.55))" }}
            />
            <span style={{ fontFamily: "var(--font-display)", fontWeight: 800, letterSpacing: 4, fontSize: 15, color: "var(--text-primary)" }}>
              KRAKEN
            </span>
          </div>

          <div className="top-nav-links">
            {BAR_NAV.map(({ to, label, icon: NavIcon, end }) => (
              <NavLink key={to} to={to} end={end} className={({ isActive }) => `top-nav-link${isActive ? " active" : ""}`}>
                <NavIcon size={15} strokeWidth={2} style={{ opacity: 0.85 }} />
                {label}
              </NavLink>
            ))}
          </div>

          <div style={{ display: "flex", alignItems: "center", marginLeft: "auto" }}>
            <FleetPulse />
            <UserMenu />
          </div>
        </nav>

        <Outlet />
      </div>

      <VersionStamp />
    </div>
  );
}

/** Shared page container so screens share consistent margins. */
export function Page({ children }: { children: ReactNode }) {
  return <main style={{ maxWidth: "var(--container-max)", margin: "0 auto", padding: "34px 30px 70px" }}>{children}</main>;
}
