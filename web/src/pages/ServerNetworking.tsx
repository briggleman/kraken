import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "@/api/client";
import { useDialog } from "@/components/Dialog";
import type { ServerDnsState } from "@/api/types";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { Input } from "@ds/components/core/Input";
import { Icon } from "@ds/components/core/Icon";
import { Toggle } from "@ds/components/core/Toggle";
import { Select } from "@ds/components/core/Select";

const mono = "var(--font-mono)";

const sectionLabel: React.CSSProperties = {
  fontFamily: mono,
  fontSize: 11,
  letterSpacing: "1.5px",
  color: "var(--text-faint)",
  marginBottom: 14,
};

// A green "✓ <thing> Connected" chip (circle-check) shown when an integration is
// configured — matches the running status tint.
const connectedLabel: React.CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  gap: 6,
  color: "var(--status-running)",
  fontFamily: mono,
  fontSize: 11.5,
  letterSpacing: ".5px",
};

export function ServerNetworkingPanel({ id }: { id: string }) {
  const navigate = useNavigate();
  const { confirm } = useDialog();
  const [data, setData] = useState<ServerDnsState | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [fwdBusy, setFwdBusy] = useState<string | null>(null);

  const [name, setName] = useState("");
  const [portName, setPortName] = useState("");
  const [service, setService] = useState("");

  const load = useCallback(() => {
    api
      .getServerDns(id)
      .then((d) => {
        setData(d);
        if (d.dns) {
          setName(d.dns.name);
          setService(d.dns.service ?? "");
          setPortName(d.dns.port_name ?? "");
        } else if (d.ports) {
          setPortName(Object.keys(d.ports)[0] ?? "");
        }
      })
      .catch((e) => setError(e instanceof Error ? e.message : "failed to load networking"));
  }, [id]);
  useEffect(load, [load]);

  if (error && !data) return <div style={{ fontFamily: mono, color: "var(--status-crashed)", padding: 20 }}>{error}</div>;
  if (!data) return <div style={{ fontFamily: mono, color: "var(--text-muted)", padding: 20 }}>Loading…</div>;

  const ports = data.ports ?? {};
  const portNames = Object.keys(ports);
  const primaryPort = ports[portName] ?? Object.values(ports)[0];
  const target = data.target_host ? `${data.target_host}${primaryPort ? ":" + primaryPort : ""}` : "—";

  const saveDns = async () => {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      await api.setServerDns(id, { name: name.trim(), service: service.trim() || undefined, port_name: portName || undefined });
      setNotice("DNS records published to Cloudflare.");
      load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not publish DNS");
    } finally {
      setBusy(false);
    }
  };

  const removeDns = async () => {
    if (!(await confirm({ title: "Remove DNS", message: `Delete the Cloudflare records for ${data.dns?.name}?`, confirmLabel: "Remove", danger: true }))) return;
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      await api.deleteServerDns(id);
      setName("");
      setService("");
      setNotice("DNS records removed.");
      load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not remove DNS");
    } finally {
      setBusy(false);
    }
  };

  const toggleForward = async (port: string, open: boolean) => {
    setFwdBusy(port);
    setError(null);
    setNotice(null);
    try {
      await api.setServerForward(id, port, open);
      setNotice(open ? `Opened ${port}.` : `Closed ${port}.`);
      load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not update port forward");
    } finally {
      setFwdBusy(null);
    }
  };

  const errBlock = error && (
    <div style={{ display: "flex", alignItems: "center", gap: 8, marginTop: 14, color: "var(--status-crashed)", fontFamily: mono, fontSize: 12.5 }}>
      <Icon name="info" size={14} /> {error}
    </div>
  );
  const okBlock = notice && !error && (
    <div style={{ display: "flex", alignItems: "center", gap: 8, marginTop: 14, color: "var(--status-running)", fontFamily: mono, fontSize: 12.5 }}>
      <Icon name="check" size={14} /> {notice}
    </div>
  );

  return (
    <div style={{ paddingBottom: 30, maxWidth: 640 }}>
      <Card padding={20} style={{ marginBottom: 16 }}>
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12 }}>
          <div style={sectionLabel}>CONNECTION</div>
          {data.target_host && (
            <span style={connectedLabel}><Icon name="running" size={14} /> Connected</span>
          )}
        </div>
        <div style={{ fontFamily: mono, fontSize: 14, color: "var(--accent)" }}>{target}</div>
        <div style={{ fontSize: 12.5, color: "var(--text-muted)", marginTop: 8 }}>
          The address players use{data.lan_host ? ` — forwarded to ${data.lan_host} on the LAN` : ""}.
        </div>
      </Card>

      {/* DNS */}
      <Card padding={20} style={{ marginBottom: 16 }}>
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12, marginBottom: 4 }}>
          <div style={sectionLabel}>DNS NAME</div>
          {data.cloudflare_configured && (
            <span style={connectedLabel}><Icon name="running" size={14} /> Cloudflare Connected</span>
          )}
        </div>
        {!data.cloudflare_configured ? (
          <div>
            <p style={{ fontSize: 13, color: "var(--text-muted)", margin: "0 0 14px" }}>Configure a Cloudflare token in Settings to publish a DNS name.</p>
            <Button variant="ghost" icon="plus" onClick={() => navigate("/admin/settings")}>Open Settings</Button>
          </div>
        ) : (
          <>
            <Input
              label="HOSTNAME"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="play.example.com"
              mono
              helper="A fully-qualified name in a Cloudflare zone your token can edit."
            />
            <div style={{ display: "flex", gap: 12, marginTop: 14 }}>
              <div style={{ flex: 1 }}>
                <Select
                  label="PORT"
                  mono
                  value={portName}
                  placeholder="—"
                  options={portNames.map((p) => ({ value: p, label: `${p} · ${ports[p]}` }))}
                  onChange={setPortName}
                />
              </div>
              <div style={{ flex: 1 }}>
                <Input
                  label="SRV SERVICE (OPTIONAL)"
                  value={service}
                  onChange={(e) => setService(e.target.value)}
                  placeholder="e.g. minecraft"
                  mono
                  helper={service.trim() ? `_${service.trim().replace(/^_/, "")}._<proto>.${name || "<name>"}` : "Leave blank to skip the SRV record."}
                />
              </div>
            </div>
            <div style={{ display: "flex", gap: 10, marginTop: 20 }}>
              <Button variant="primary" icon="check" disabled={busy || name.trim() === ""} onClick={saveDns}>
                {busy ? "Publishing…" : data.dns ? "Update DNS" : "Publish DNS"}
              </Button>
              {data.dns && <Button variant="danger" icon="x" disabled={busy} onClick={removeDns}>Remove</Button>}
            </div>
          </>
        )}
      </Card>

      {/* UniFi port forwards */}
      <Card padding={20}>
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12, marginBottom: 4 }}>
          <div style={sectionLabel}>PORT FORWARDS</div>
          {data.unifi_configured && (
            <span style={connectedLabel}><Icon name="running" size={14} /> Unifi Connected</span>
          )}
        </div>
        {!data.unifi_configured ? (
          <div>
            <p style={{ fontSize: 13, color: "var(--text-muted)", margin: "0 0 14px" }}>Configure a UniFi gateway in Settings to open ports for this server.</p>
            <Button variant="ghost" icon="plus" onClick={() => navigate("/admin/settings")}>Open Settings</Button>
          </div>
        ) : portNames.length === 0 ? (
          <div style={{ fontFamily: mono, fontSize: 13, color: "var(--text-muted)" }}>This server has no allocated ports.</div>
        ) : (
          <div>
            <p style={{ fontSize: 12.5, color: "var(--text-muted)", margin: "0 0 6px" }}>
              Open a port to forward WAN traffic to {data.lan_host || "the node"} on the gateway.
            </p>
            {portNames.map((p) => {
              const open = !!data.forwards?.[p]?.enabled;
              return (
                <div key={p} style={{ display: "flex", alignItems: "center", justifyContent: "space-between", padding: "12px 0", borderTop: "1px solid var(--border-subtle)" }}>
                  <span style={{ fontFamily: mono, fontSize: 13, color: "var(--text-primary)" }}>
                    {p} <span style={{ color: "var(--text-muted)" }}>· {ports[p]}</span>
                  </span>
                  <Toggle checked={open} disabled={fwdBusy === p} onChange={(v) => toggleForward(p, v)} />
                </div>
              );
            })}
          </div>
        )}
        {errBlock}
        {okBlock}
      </Card>
    </div>
  );
}
