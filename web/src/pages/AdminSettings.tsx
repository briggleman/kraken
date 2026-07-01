import { useEffect, useState } from "react";
import { api } from "@/api/client";
import { Page } from "@/components/Shell";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { Input } from "@ds/components/core/Input";
import { Badge } from "@ds/components/core/Badge";
import { Icon } from "@ds/components/core/Icon";
import { Toggle } from "@ds/components/core/Toggle";
import { CloudflareIcon, UnifiIcon } from "@/components/BrandIcon";
import type { DatabaseConfig } from "@/api/types";

const mono = "var(--font-mono)";

const sectionLabel: React.CSSProperties = {
  fontFamily: mono,
  fontSize: 11,
  letterSpacing: "1.5px",
  color: "var(--text-faint)",
  marginBottom: 14,
};

function GroupHeading({ children }: { children: React.ReactNode }) {
  return (
    <div style={{ maxWidth: 640, margin: "0 0 14px", display: "flex", alignItems: "center", gap: 12 }}>
      <h2
        style={{
          fontFamily: "var(--font-display)",
          fontWeight: 700,
          fontSize: 18,
          letterSpacing: "-0.2px",
          color: "var(--text-primary)",
          margin: 0,
          whiteSpace: "nowrap",
        }}
      >
        {children}
      </h2>
      <div style={{ flex: 1, height: 1, background: "var(--border-subtle)" }} />
    </div>
  );
}

function Notice({ error, ok }: { error?: string | null; ok?: string | null }) {
  if (error) {
    return (
      <div style={{ display: "flex", alignItems: "center", gap: 8, marginTop: 14, color: "var(--status-crashed)", fontFamily: mono, fontSize: 12.5 }}>
        <Icon name="info" size={14} /> {error}
      </div>
    );
  }
  if (ok) {
    return (
      <div style={{ display: "flex", alignItems: "center", gap: 8, marginTop: 14, color: "var(--status-running)", fontFamily: mono, fontSize: 12.5 }}>
        <Icon name="check" size={14} /> {ok}
      </div>
    );
  }
  return null;
}

// ConfiguredBadges shows the status of a stored secret: when set, an "encrypted"
// pill (the value is sealed at rest, AES-256-GCM) sits to the left of CONFIGURED.
function ConfiguredBadges({ on }: { on: boolean }) {
  if (!on) return <Badge tone="neutral">NOT SET</Badge>;
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
      <Badge tone="neutral"><Icon name="lock" size={10} strokeWidth={2} /> encrypted</Badge>
      <Badge tone="accent">CONFIGURED</Badge>
    </div>
  );
}

export function AdminSettings() {
  // Cloudflare
  const [cfConfigured, setCfConfigured] = useState(false);
  const [cfToken, setCfToken] = useState("");
  const [cfBusy, setCfBusy] = useState<"save" | "test" | null>(null);
  const [cfError, setCfError] = useState<string | null>(null);
  const [cfNotice, setCfNotice] = useState<string | null>(null);
  const [zones, setZones] = useState<string[] | null>(null);

  // UniFi
  const [uConfigured, setUConfigured] = useState(false);
  const [uURL, setUURL] = useState("");
  const [uKey, setUKey] = useState("");
  const [uSite, setUSite] = useState("");
  const [uBusy, setUBusy] = useState<"save" | "test" | null>(null);
  const [uError, setUError] = useState<string | null>(null);
  const [uNotice, setUNotice] = useState<string | null>(null);

  const [dbCfg, setDbCfg] = useState<DatabaseConfig | null>(null);

  // Sessions & Security (global runtime settings)
  const [sessTTL, setSessTTL] = useState(86400);
  const [sessLocked, setSessLocked] = useState(false);
  const [origins, setOrigins] = useState("");
  const [originsLocked, setOriginsLocked] = useState(false);
  const [bootstrapEnabled, setBootstrapEnabled] = useState(true);
  const [bootstrapUser, setBootstrapUser] = useState("");
  const [bootstrapLocked, setBootstrapLocked] = useState(false);
  const [secBusy, setSecBusy] = useState(false);
  const [secError, setSecError] = useState<string | null>(null);
  const [secNotice, setSecNotice] = useState<string | null>(null);

  const load = () => {
    api
      .getPanelSettings()
      .then((s) => {
        setCfConfigured(s.cloudflare_configured);
        setUConfigured(s.unifi_configured);
        setUURL(s.unifi_url ?? "");
        setUSite(s.unifi_site ?? "");
        setSessTTL(s.session_ttl_seconds);
        setSessLocked(s.session_ttl_locked);
        setOrigins((s.allowed_origins ?? []).join(", "));
        setOriginsLocked(s.allowed_origins_locked);
        setBootstrapEnabled(!s.bootstrap_disabled);
        setBootstrapUser(s.bootstrap_user);
        setBootstrapLocked(s.bootstrap_locked);
      })
      .catch((e) => setCfError(e instanceof Error ? e.message : "failed to load settings"));
    api.getDatabaseConfig().then(setDbCfg).catch(() => {});
  };
  useEffect(load, []);

  const saveSecurity = async () => {
    setSecBusy(true);
    setSecError(null);
    setSecNotice(null);
    try {
      const s = await api.updatePanelSettings({
        session_ttl_seconds: sessLocked ? undefined : sessTTL,
        allowed_origins: originsLocked ? undefined : origins.split(",").map((o) => o.trim()).filter(Boolean),
      });
      setSessTTL(s.session_ttl_seconds);
      setOrigins((s.allowed_origins ?? []).join(", "));
      setBootstrapEnabled(!s.bootstrap_disabled);
      setSecNotice("Security settings saved.");
    } catch (e) {
      setSecError(e instanceof Error ? e.message : "could not save");
    } finally {
      setSecBusy(false);
    }
  };

  const saveCf = async () => {
    setCfBusy("save");
    setCfError(null);
    setCfNotice(null);
    setZones(null);
    try {
      const s = await api.updatePanelSettings({ cloudflare_api_token: cfToken.trim() });
      setCfConfigured(s.cloudflare_configured);
      setCfToken("");
      setCfNotice(s.cloudflare_configured ? "Cloudflare token saved." : "Cloudflare token cleared.");
    } catch (e) {
      setCfError(e instanceof Error ? e.message : "could not save");
    } finally {
      setCfBusy(null);
    }
  };
  const testCf = async () => {
    setCfBusy("test");
    setCfError(null);
    setCfNotice(null);
    setZones(null);
    try {
      const r = await api.testCloudflare();
      setZones(r.zones ?? []);
      setCfNotice(`Connected. ${(r.zones ?? []).length} zone(s) reachable.`);
    } catch (e) {
      setCfError(e instanceof Error ? e.message : "connection failed");
    } finally {
      setCfBusy(null);
    }
  };

  const saveUnifi = async () => {
    setUBusy("save");
    setUError(null);
    setUNotice(null);
    try {
      const s = await api.updatePanelSettings({ unifi_url: uURL.trim(), unifi_api_key: uKey.trim(), unifi_site: uSite.trim() });
      setUConfigured(s.unifi_configured);
      setUURL(s.unifi_url ?? "");
      setUSite(s.unifi_site ?? "");
      setUKey("");
      setUNotice(s.unifi_configured ? "UniFi settings saved." : "UniFi settings cleared.");
    } catch (e) {
      setUError(e instanceof Error ? e.message : "could not save");
    } finally {
      setUBusy(null);
    }
  };
  const testUnifi = async () => {
    setUBusy("test");
    setUError(null);
    setUNotice(null);
    try {
      const r = await api.testUnifi();
      setUNotice(`Connected. ${r.forward_count} forward(s)${r.wan_ip ? ` · WAN ${r.wan_ip}` : ""}.`);
    } catch (e) {
      setUError(e instanceof Error ? e.message : "connection failed");
    } finally {
      setUBusy(null);
    }
  };

  return (
    <Page>
      <div style={{ marginBottom: 26 }}>
        <div style={{ fontFamily: mono, fontSize: 12, letterSpacing: "3px", color: "var(--accent)", marginBottom: 10 }}>// ADMIN · SETTINGS</div>
        <h1 style={{ fontFamily: "var(--font-display)", fontWeight: 800, fontSize: 34, letterSpacing: "-0.5px", margin: 0, color: "var(--text-primary)" }}>
          Settings
        </h1>
      </div>

      {/* ── Database group ── */}
      <GroupHeading>Database</GroupHeading>
      {/* Database (view-only — configured during setup) */}
      <Card padding={24} style={{ maxWidth: 640, marginBottom: 32 }}>
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12, marginBottom: 4 }}>
          <div style={{ ...sectionLabel, display: "flex", alignItems: "center", gap: 8 }}>
            <Icon name="database" size={15} style={{ color: "var(--accent)" }} />
            DATABASE
          </div>
          {dbCfg?.env_locked ? <Badge tone="neutral">ENV-MANAGED</Badge> : dbCfg && !dbCfg.using_memory ? <Badge tone="accent">POSTGRES</Badge> : <Badge tone="warn">IN-MEMORY</Badge>}
        </div>
        <p style={{ margin: "0 0 4px", fontSize: 14, color: "var(--text-secondary)", fontFamily: mono }}>
          {dbCfg && !dbCfg.using_memory
            ? `${dbCfg.user}@${dbCfg.host}${dbCfg.port ? ":" + dbCfg.port : ""}/${dbCfg.dbname} · sslmode=${dbCfg.sslmode}`
            : "In-memory store — data is not persisted across restarts."}
        </p>
        <p style={{ margin: "8px 0 0", fontSize: 12.5, color: "var(--text-muted)" }}>
          {dbCfg?.env_locked
            ? "Managed via KRAKEN_DATABASE_URL."
            : "Configured during first-run setup."}
        </p>
      </Card>

      {/* ── Networking group ── */}
      <GroupHeading>Networking</GroupHeading>
      {/* Cloudflare */}
      <Card padding={24} style={{ maxWidth: 640, marginBottom: 16 }}>
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12, marginBottom: 4 }}>
          <div style={{ ...sectionLabel, display: "flex", alignItems: "center", gap: 8 }}>
            <CloudflareIcon size={15} style={{ color: "var(--accent)" }} />
            CLOUDFLARE DNS
          </div>
          <ConfiguredBadges on={cfConfigured} />
        </div>
        <p style={{ margin: "0 0 18px", fontSize: 13.5, color: "var(--text-secondary)", lineHeight: 1.55 }}>
          A scoped Cloudflare API token (DNS edit) lets servers publish a DNS name to your domains. Create one at
          Cloudflare → My Profile → API Tokens with <span style={{ fontFamily: mono, color: "var(--text-primary)" }}>Zone · DNS · Edit</span>.
        </p>
        <Input
          label={cfConfigured ? "REPLACE API TOKEN" : "API TOKEN"}
          type="password"
          value={cfToken}
          onChange={(e) => setCfToken(e.target.value)}
          mono
          placeholder={cfConfigured ? "•••••••••• (leave blank to keep)" : "paste token"}
          autoComplete="off"
          helper="Stored server-side and never shown again."
        />
        <Notice error={cfError} ok={cfNotice} />
        {zones && zones.length > 0 && (
          <div style={{ display: "flex", gap: 6, flexWrap: "wrap", marginTop: 12 }}>
            {zones.map((z) => (
              <Badge key={z} tone="neutral">{z}</Badge>
            ))}
          </div>
        )}
        <div style={{ display: "flex", gap: 10, marginTop: 20 }}>
          <Button variant="primary" icon="check" disabled={cfBusy !== null || cfToken.trim() === ""} onClick={saveCf}>
            {cfBusy === "save" ? "Saving…" : "Save token"}
          </Button>
          <Button variant="secondary" icon="refresh" disabled={cfBusy !== null || !cfConfigured} onClick={testCf}>
            {cfBusy === "test" ? "Testing…" : "Test connection"}
          </Button>
        </div>
      </Card>

      {/* UniFi */}
      <Card padding={24} style={{ maxWidth: 640 }}>
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12, marginBottom: 4 }}>
          <div style={{ ...sectionLabel, display: "flex", alignItems: "center", gap: 8 }}>
            <UnifiIcon size={15} style={{ color: "var(--accent)" }} />
            UNIFI GATEWAY
          </div>
          <ConfiguredBadges on={uConfigured} />
        </div>
        <p style={{ margin: "0 0 18px", fontSize: 13.5, color: "var(--text-secondary)", lineHeight: 1.55 }}>
          A UniFi OS API key lets servers open port forwards on your gateway. Create one in the UniFi console under
          <span style={{ fontFamily: mono, color: "var(--text-primary)" }}> Settings → Control Plane → Integrations</span>.
        </p>
        <Input
          label="CONTROLLER URL"
          value={uURL}
          onChange={(e) => setUURL(e.target.value)}
          mono
          placeholder="https://192.168.1.1"
          autoComplete="off"
          style={{ marginBottom: 14 }}
        />
        <div style={{ display: "flex", gap: 12 }}>
          <div style={{ flex: 2 }}>
            <Input
              label={uConfigured ? "REPLACE API KEY" : "API KEY"}
              type="password"
              value={uKey}
              onChange={(e) => setUKey(e.target.value)}
              mono
              placeholder={uConfigured ? "•••••••••• (leave blank to keep)" : "paste key"}
              autoComplete="off"
            />
          </div>
          <div style={{ flex: 1 }}>
            <Input label="SITE" value={uSite} onChange={(e) => setUSite(e.target.value)} mono placeholder="default" autoComplete="off" />
          </div>
        </div>
        <Notice error={uError} ok={uNotice} />
        <div style={{ display: "flex", gap: 10, marginTop: 20 }}>
          <Button variant="primary" icon="check" disabled={uBusy !== null || uURL.trim() === "" || (!uConfigured && uKey.trim() === "")} onClick={saveUnifi}>
            {uBusy === "save" ? "Saving…" : "Save"}
          </Button>
          <Button variant="secondary" icon="refresh" disabled={uBusy !== null || !uConfigured} onClick={testUnifi}>
            {uBusy === "test" ? "Testing…" : "Test connection"}
          </Button>
        </div>
      </Card>

      {/* ── Sessions & Security group ── */}
      <div style={{ marginTop: 32 }}>
        <GroupHeading>Sessions &amp; Security</GroupHeading>
      </div>
      <Card padding={24} style={{ maxWidth: 640 }}>
        {/* Session lifetime */}
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12, marginBottom: 4 }}>
          <div style={{ ...sectionLabel, display: "flex", alignItems: "center", gap: 8 }}>
            <Icon name="lock" size={15} style={{ color: "var(--accent)" }} />
            SESSION LIFETIME
          </div>
          {sessLocked && <Badge tone="neutral">ENV-MANAGED</Badge>}
        </div>
        <Input
          label="DURATION (SECONDS)"
          type="number"
          value={sessTTL}
          onChange={(e) => setSessTTL(+e.target.value)}
          mono
          disabled={sessLocked}
          helper={sessLocked ? "Managed via KRAKEN_SESSION_TTL." : `≈ ${(sessTTL / 3600).toFixed(1)}h — how long a login stays valid.`}
        />

        {/* Allowed WS origins */}
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12, margin: "24px 0 4px" }}>
          <div style={{ ...sectionLabel, marginBottom: 0 }}>ALLOWED WEBSOCKET ORIGINS</div>
          {originsLocked && <Badge tone="neutral">ENV-MANAGED</Badge>}
        </div>
        <Input
          label="ORIGINS (COMMA-SEPARATED)"
          value={origins}
          onChange={(e) => setOrigins(e.target.value)}
          mono
          disabled={originsLocked}
          placeholder="panel.example.com, *.example.com"
          helper={originsLocked ? "Managed via KRAKEN_ALLOWED_ORIGINS." : "Empty falls back to localhost dev origins. Same-origin is always allowed."}
        />

        {/* Bootstrap policy */}
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12, margin: "24px 0 10px" }}>
          <div style={{ ...sectionLabel, marginBottom: 0 }}>BOOTSTRAP ADMIN</div>
          {bootstrapLocked && <Badge tone="neutral">ENV-MANAGED</Badge>}
        </div>
        <div style={{ display: "flex", alignItems: "center", gap: 10, color: "var(--text-secondary)", fontSize: 14 }}>
          <Toggle checked={bootstrapEnabled} disabled />
          Auto-create the bootstrap admin (<span style={{ fontFamily: mono, color: "var(--text-primary)" }}>{bootstrapUser || "admin"}</span>) when no users exist
        </div>
        <p style={{ margin: "8px 0 0", fontSize: 12.5, color: "var(--text-muted)" }}>
          {bootstrapLocked
            ? "Read-only — pinned on via the KRAKEN_BOOTSTRAP_ADMIN_* env vars."
            : "Read-only — the bootstrap admin is created at first start when the instance has no users."}
        </p>

        <Notice error={secError} ok={secNotice} />
        <div style={{ display: "flex", gap: 10, marginTop: 20 }}>
          <Button variant="primary" icon="check" disabled={secBusy || (sessLocked && originsLocked)} onClick={saveSecurity}>
            {secBusy ? "Saving…" : "Save"}
          </Button>
        </div>
      </Card>
    </Page>
  );
}
