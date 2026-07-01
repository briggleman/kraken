import { useEffect, useState } from "react";
import { NavLink, Outlet, useNavigate } from "react-router-dom";
import type { ReactNode } from "react";
import { Boxes, HardDrive, Layers, Users, ShieldCheck, ScrollText, BookOpen, LogOut, ChevronDown, Rocket, Settings as SettingsIcon } from "lucide-react";
import { api } from "@/api/client";
import { useAuth } from "@/auth";
import { AmbientBackground } from "./AmbientBackground";

const mono = "var(--font-mono)";

const NAV = [
  { to: "/", label: "Fleet", icon: Boxes, end: true },
  { to: "/nodes", label: "Nodes", icon: HardDrive, end: false },
  { to: "/specs", label: "Game Specs", icon: Layers, end: false },
  { to: "/docs", label: "API Docs", icon: BookOpen, end: false },
];

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
            justifyContent: "space-between",
            height: "var(--nav-height)",
            padding: "0 28px",
            backdropFilter: "blur(var(--blur-nav))",
            WebkitBackdropFilter: "blur(var(--blur-nav))",
            background: "rgba(2,9,14,.66)",
            borderBottom: "1px solid var(--border-subtle)",
          }}
        >
          <div style={{ display: "flex", alignItems: "center", gap: 14, cursor: "pointer" }} onClick={() => navigate("/")}>
            <div style={{ display: "flex", alignItems: "center", gap: 11 }}>
              <img
                src="/kraken-glyph-teal.png"
                alt="Kraken"
                style={{ width: 30, height: 30, objectFit: "contain", filter: "drop-shadow(0 0 7px rgba(61,245,207,.55))" }}
              />
              <span style={{ fontFamily: "var(--font-display)", fontWeight: 800, letterSpacing: 4, fontSize: 15, color: "var(--text-primary)" }}>
                KRAKEN
              </span>
            </div>
            <span
              style={{
                fontFamily: mono,
                fontSize: 10.5,
                letterSpacing: 1,
                color: "var(--text-faint)",
                padding: "3px 8px",
                border: "1px solid var(--border-subtle)",
                borderRadius: 6,
              }}
            >
              control panel
            </span>
          </div>
          <UserMenu />
        </nav>

        <Outlet />
      </div>
    </div>
  );
}

/** Shared page container so screens share consistent margins. */
export function Page({ children }: { children: ReactNode }) {
  return <main style={{ maxWidth: "var(--container-max)", margin: "0 auto", padding: "34px 30px 70px" }}>{children}</main>;
}
