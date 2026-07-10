import { useEffect, useState } from "react";
import { api } from "@/api/client";
import type { SettingField, ServerSettings as Settings } from "@/api/types";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { Toggle } from "@ds/components/core/Toggle";
import { Badge } from "@ds/components/core/Badge";
import { Icon } from "@ds/components/core/Icon";
import { Select } from "@ds/components/core/Select";

const mono = "var(--font-mono)";

const groupLabel: React.CSSProperties = {
  fontFamily: mono,
  fontSize: 11,
  letterSpacing: "1.5px",
  textTransform: "uppercase",
  margin: 0,
  color: "var(--text-faint)",
};

// Recessed control shared by every settings field. Entered values render in the
// teal accent (matching the active "Settings" tab) to read as live, editable state.
const ctrlBase: React.CSSProperties = {
  width: "100%",
  padding: "10px 13px",
  borderRadius: "var(--radius-md)",
  background: "var(--bg-inset)",
  color: "var(--accent)",
  border: "1px solid var(--border-subtle)",
  fontSize: 14,
  fontFamily: mono,
  outline: "none",
};

/** One setting rendered as its own card: title + help on top, control below — or,
 *  for booleans, the toggle right-aligned next to the title. */
function SettingCard({ field, value, onChange }: { field: SettingField; value: string; onChange: (v: string) => void }) {
  const label = field.label || field.key;
  const isBool = field.type === "bool";
  const ro = !!field.read_only;
  const isPassword = field.type === "password";
  const [reveal, setReveal] = useState(false);

  const help = field.help && <div style={{ fontSize: 12.5, color: "var(--text-muted)", marginTop: 4 }}>{field.help}</div>;
  const defaultHint = field.default !== undefined && field.default !== "" && !isBool && (
    <div style={{ fontFamily: mono, fontSize: 10.5, letterSpacing: 0.5, color: "var(--text-faint)", marginTop: 8 }}>
      DEFAULT: <span style={{ color: "var(--text-muted)" }}>{field.default}</span>
    </div>
  );

  const ctrlStyle: React.CSSProperties = ro ? { ...ctrlBase, opacity: 0.55, cursor: "not-allowed" } : ctrlBase;
  let control: React.ReactNode = null;
  if (field.type === "enum") {
    control = (
      <Select mono value={value} disabled={ro} options={(field.options ?? []).map((o) => ({ value: o, label: o }))} onChange={onChange} />
    );
  } else if (field.type === "text") {
    control = <textarea style={{ ...ctrlStyle, minHeight: 80, resize: "vertical" }} value={value} disabled={ro} onChange={(e) => onChange(e.target.value)} />;
  } else if (isPassword) {
    // Password field with a reveal (eye) toggle so operators can verify what they typed.
    control = (
      <div style={{ position: "relative" }}>
        <input
          type={reveal ? "text" : "password"}
          style={{ ...ctrlStyle, paddingRight: 40 }}
          value={value}
          disabled={ro}
          onChange={(e) => onChange(e.target.value)}
        />
        <button
          type="button"
          onClick={() => setReveal((r) => !r)}
          title={reveal ? "Hide" : "Show"}
          aria-label={reveal ? "Hide password" : "Show password"}
          style={{
            position: "absolute", top: 0, right: 0, height: "100%", width: 38,
            display: "flex", alignItems: "center", justifyContent: "center",
            background: "none", border: "none", cursor: "pointer",
            color: reveal ? "var(--accent)" : "var(--text-muted)",
          }}
        >
          <Icon name={reveal ? "eye-off" : "eye"} size={16} />
        </button>
      </div>
    );
  } else if (!isBool) {
    control = (
      <input
        type={field.type === "int" || field.type === "float" ? "number" : "text"}
        style={ctrlStyle}
        value={value}
        disabled={ro}
        onChange={(e) => onChange(e.target.value)}
      />
    );
  }

  return (
    <Card padding={18}>
      {/* title row — toggle sits to the right for booleans */}
      <div style={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: 14 }}>
        <div style={{ minWidth: 0 }}>
          <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
            <span style={{ fontSize: 14, fontWeight: 600, color: "var(--text-primary)" }}>{label}</span>
            {ro && (
              <Badge tone="neutral">
                <Icon name="lock" size={11} strokeWidth={2} />
                read only
              </Badge>
            )}
          </div>
          {help}
          {field.type === "int" || field.type === "float" ? (
            (field.min != null || field.max != null) && (
              <div style={{ fontFamily: mono, fontSize: 10.5, color: "var(--text-faint)", marginTop: 4 }}>
                {field.min != null ? `min ${field.min}` : ""}{field.min != null && field.max != null ? " · " : ""}{field.max != null ? `max ${field.max}` : ""}
              </div>
            )
          ) : null}
        </div>
        {isBool && <Toggle checked={value === "true"} disabled={ro} onChange={(v) => onChange(v ? "true" : "false")} />}
      </div>

      {control && <div style={{ marginTop: 14 }}>{control}</div>}
      {defaultHint}
    </Card>
  );
}

export function ServerSettingsPanel({ id, running, onRequestRestart }: { id: string; running: boolean; onRequestRestart: () => void }) {
  const [data, setData] = useState<Settings | null>(null);
  const [values, setValues] = useState<Record<string, string>>({});
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    api.getServerSettings(id)
      .then((d) => { setData(d); setValues(d.values ?? {}); })
      .catch((e) => setError(e instanceof Error ? e.message : "failed to load settings"));
  }, [id]);

  const set = (k: string, v: string) => { setValues((prev) => ({ ...prev, [k]: v })); setDirty(true); setNotice(null); };

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      const res = await api.updateServerSettings(id, values);
      setValues(res.values);
      setDirty(false);
      setNotice(res.restart_needed ? "Saved. Restart the server to apply the new settings." : "Settings saved.");
    } catch (e) {
      setError(e instanceof Error ? e.message : "save failed");
    } finally {
      setBusy(false);
    }
  };

  if (error && !data) return <div style={{ fontFamily: mono, color: "var(--status-crashed)", padding: 20 }}>{error}</div>;
  if (!data) return <div style={{ fontFamily: mono, color: "var(--text-muted)", padding: 20 }}>Loading settings…</div>;
  // Older panels serialize a spec without settings as groups: null.
  const groups = data.groups ?? [];
  if (groups.length === 0) {
    return (
      <div style={{ fontFamily: mono, color: "var(--text-muted)", padding: 20 }}>
        This game has no Settings-tab options — its configuration (if any) is set via launch
        variables on the deploy screen.
      </div>
    );
  }

  return (
    <div style={{ paddingBottom: 30 }}>
      <div style={{ display: "flex", alignItems: "flex-end", justifyContent: "space-between", marginBottom: 22, gap: 16, flexWrap: "wrap" }}>
        <div>
          <p style={{ ...groupLabel, marginBottom: 6 }}>Configuration</p>
          <p style={{ margin: 0, fontSize: 13.5, color: "var(--text-muted)" }}>
            Adjust this server's game settings. Changes are written to the config and take effect after a restart.
          </p>
        </div>
        <Button variant="primary" disabled={!dirty || busy} icon="check" onClick={save}>{busy ? "Saving…" : "Save"}</Button>
      </div>

      {error && <div style={{ color: "var(--status-crashed)", fontFamily: mono, fontSize: 13, marginBottom: 14 }}>{error}</div>}
      {notice && (
        <div style={{ marginBottom: 18, padding: "11px 14px", borderRadius: "var(--radius-md)", border: "1px solid var(--border-strong)", background: "var(--accent-wash-12)", color: "var(--text-secondary)", fontSize: 13.5, display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12 }}>
          <span style={{ display: "inline-flex", alignItems: "center", gap: 9 }}>
            <Icon name="check" size={15} strokeWidth={2} style={{ color: "var(--accent)" }} />
            {notice}
          </span>
          {running && notice.includes("Restart") && <Button variant="primary" size="sm" icon="refresh" onClick={onRequestRestart}>Restart now</Button>}
        </div>
      )}

      {groups.map((g) => (
        <section key={g.id} style={{ marginBottom: 32 }}>
          <div style={{ marginBottom: 14 }}>
            <h3 style={groupLabel}>{g.label || g.id}</h3>
            {g.description && <div style={{ fontSize: 12.5, color: "var(--text-muted)", marginTop: 6 }}>{g.description}</div>}
            <div style={{ height: 1, background: "var(--border-subtle)", marginTop: 12 }} />
          </div>
          <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit,minmax(380px,1fr))", gap: 16 }}>
            {(g.fields ?? []).map((f) => (
              <SettingCard key={f.key} field={f} value={values[f.key] ?? f.default ?? ""} onChange={(v) => set(f.key, v)} />
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}
