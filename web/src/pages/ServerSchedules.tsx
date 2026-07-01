import { useCallback, useEffect, useState } from "react";
import { api, type ScheduleInput } from "@/api/client";
import { useDialog } from "@/components/Dialog";
import type { ScheduleAction, ScheduledTask } from "@/api/types";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { Input } from "@ds/components/core/Input";
import { Icon } from "@ds/components/core/Icon";
import { Select } from "@ds/components/core/Select";

const mono = "var(--font-mono)";
const GRID = "1.3fr 0.8fr 1fr 1.1fr 150px";

const groupLabel: React.CSSProperties = {
  fontFamily: mono,
  fontSize: 11,
  letterSpacing: "1.5px",
  textTransform: "uppercase",
  margin: 0,
  color: "var(--text-faint)",
};

const colHead: React.CSSProperties = {
  display: "grid",
  gridTemplateColumns: GRID,
  padding: "12px 18px",
  borderBottom: "1px solid var(--border-subtle)",
  fontFamily: mono,
  fontSize: 10.5,
  letterSpacing: 1,
  textTransform: "uppercase",
  color: "var(--text-faint)",
};

const ACTIONS: { value: ScheduleAction; label: string }[] = [
  { value: "restart", label: "Restart" },
  { value: "backup", label: "Backup" },
  { value: "command", label: "Console command" },
  { value: "replicate", label: "Replicate backups" },
];

// A few friendly presets so users don't have to remember cron syntax.
const CRON_PRESETS: { label: string; expr: string }[] = [
  { label: "Every hour", expr: "0 * * * *" },
  { label: "Every 6 hours", expr: "0 */6 * * *" },
  { label: "Daily at 04:00", expr: "0 4 * * *" },
  { label: "Weekly (Sun 04:00)", expr: "0 4 * * 0" },
];

const emptyForm: ScheduleInput = { name: "", action: "restart", cron: "0 4 * * *", command: "", enabled: true };

function fmtTime(s?: string): string {
  if (!s) return "—";
  const d = new Date(s);
  return isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

export function ServerSchedulesPanel({ id }: { id: string }) {
  const { confirm } = useDialog();
  const [schedules, setSchedules] = useState<ScheduledTask[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<ScheduledTask | null>(null);
  const [form, setForm] = useState<ScheduleInput>(emptyForm);
  const [showForm, setShowForm] = useState(false);

  const load = useCallback(() => {
    api.listSchedules(id).then((r) => setSchedules(r.schedules ?? [])).catch((e) => setError(e instanceof Error ? e.message : "failed to load schedules"));
  }, [id]);
  useEffect(load, [load]);

  const openCreate = () => {
    setEditing(null);
    setForm(emptyForm);
    setShowForm(true);
    setError(null);
  };
  const openEdit = (t: ScheduledTask) => {
    setEditing(t);
    setForm({ name: t.name, action: t.action, cron: t.cron, command: t.command ?? "", enabled: t.enabled });
    setShowForm(true);
    setError(null);
  };

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      if (editing) await api.updateSchedule(id, editing.id, form);
      else await api.createSchedule(id, form);
      setShowForm(false);
      load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not save schedule");
    } finally {
      setBusy(false);
    }
  };

  const toggle = async (t: ScheduledTask) => {
    setError(null);
    try {
      await api.updateSchedule(id, t.id, { name: t.name, action: t.action, cron: t.cron, command: t.command, enabled: !t.enabled });
      load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not update schedule");
    }
  };

  const del = async (t: ScheduledTask) => {
    if (!(await confirm({ title: "Delete schedule", message: `Delete schedule "${t.name || t.action}"?`, confirmLabel: "Delete", danger: true }))) return;
    try {
      await api.deleteSchedule(id, t.id);
      load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "could not delete schedule");
    }
  };

  return (
    <div style={{ paddingBottom: 30 }}>
      <div style={{ display: "flex", alignItems: "flex-end", justifyContent: "space-between", marginBottom: 18, gap: 16, flexWrap: "wrap" }}>
        <div>
          <p style={{ ...groupLabel, marginBottom: 6 }}>Scheduled tasks</p>
          <p style={{ margin: 0, fontSize: 13.5, color: "var(--text-muted)" }}>Cron-scheduled restarts, backups, and console commands for this server.</p>
        </div>
        {!showForm && <Button variant="primary" icon="plus" onClick={openCreate}>New schedule</Button>}
      </div>

      {error && <div style={{ color: "var(--status-crashed)", fontFamily: mono, fontSize: 13, marginBottom: 12 }}>{error}</div>}

      {showForm && (
        <Card padding={20} style={{ marginBottom: 18 }}>
          <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 14, marginBottom: 14 }}>
            <Input label="NAME" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Nightly restart" />
            <Select
              label="ACTION"
              value={form.action}
              options={ACTIONS.map((a) => ({ value: a.value, label: a.label }))}
              onChange={(v) => setForm({ ...form, action: v as ScheduleAction })}
            />
          </div>

          <div style={{ marginBottom: 8 }}>
            <Input label="CRON (min hour dom month dow)" value={form.cron} mono onChange={(e) => setForm({ ...form, cron: e.target.value })} placeholder="0 4 * * *" />
          </div>
          <div style={{ display: "flex", gap: 8, flexWrap: "wrap", marginBottom: 14 }}>
            {CRON_PRESETS.map((p) => (
              <button
                key={p.expr}
                type="button"
                onClick={() => setForm({ ...form, cron: p.expr })}
                style={{
                  fontFamily: mono, fontSize: 11, padding: "5px 10px", borderRadius: "var(--radius-sm)", cursor: "pointer",
                  border: `1px solid ${form.cron === p.expr ? "var(--border-strong)" : "var(--border-subtle)"}`,
                  background: form.cron === p.expr ? "var(--accent-wash-12)" : "transparent",
                  color: form.cron === p.expr ? "var(--accent)" : "var(--text-muted)",
                }}
              >
                {p.label}
              </button>
            ))}
          </div>

          {form.action === "command" && (
            <div style={{ marginBottom: 14 }}>
              <Input label="COMMAND" value={form.command ?? ""} mono onChange={(e) => setForm({ ...form, command: e.target.value })} placeholder="save-all" />
            </div>
          )}

          <label style={{ display: "flex", alignItems: "center", gap: 9, marginBottom: 18, fontSize: 13.5, color: "var(--text-secondary)", cursor: "pointer" }}>
            <input type="checkbox" checked={form.enabled} onChange={(e) => setForm({ ...form, enabled: e.target.checked })} />
            Enabled
          </label>

          <div style={{ display: "flex", gap: 9 }}>
            <Button variant="primary" disabled={busy} onClick={save}>{busy ? "Saving…" : editing ? "Save changes" : "Create schedule"}</Button>
            <Button variant="ghost" disabled={busy} onClick={() => setShowForm(false)}>Cancel</Button>
          </div>
        </Card>
      )}

      {schedules.length === 0 && !showForm ? (
        <Card dashed padding={40}>
          <div style={{ display: "flex", flexDirection: "column", alignItems: "center", justifyContent: "center", gap: 12, textAlign: "center" }}>
            <Icon name="clock" size={34} strokeWidth={1.6} style={{ color: "var(--text-muted)" }} />
            <div style={{ fontFamily: mono, fontSize: 13.5, color: "var(--text-muted)" }}>No schedules yet. Automate a restart, backup, or command.</div>
          </div>
        </Card>
      ) : schedules.length > 0 ? (
        <div style={{ border: "1px solid var(--border-subtle)", borderRadius: "var(--radius-lg)", background: "rgba(7,23,29,.55)" }}>
          <div style={colHead}>
            <span>Name / Action</span><span>Cron</span><span>Next run</span><span>Last run</span><span />
          </div>
          {schedules.map((t) => (
            <div key={t.id} style={{ display: "grid", gridTemplateColumns: GRID, alignItems: "center", padding: "12px 18px", borderBottom: "1px solid var(--border-subtle)", fontSize: 13, opacity: t.enabled ? 1 : 0.55 }}>
              <span style={{ display: "flex", flexDirection: "column", gap: 3 }}>
                <span style={{ color: "var(--text-primary)" }}>{t.name || <em style={{ color: "var(--text-faint)" }}>untitled</em>}</span>
                <span style={{ fontFamily: mono, fontSize: 11, color: "var(--accent)" }}>
                  {t.action}{t.action === "command" && t.command ? `: ${t.command}` : ""}
                </span>
                {t.last_error && <span style={{ fontSize: 11, color: "var(--status-crashed)" }} title={t.last_error}>last run failed</span>}
              </span>
              <span style={{ fontFamily: mono, fontSize: 12, color: "var(--text-secondary)" }}>{t.cron}</span>
              <span style={{ fontFamily: mono, fontSize: 11.5, color: "var(--text-muted)" }}>{t.enabled ? fmtTime(t.next_run_at) : "paused"}</span>
              <span style={{ fontFamily: mono, fontSize: 11.5, color: "var(--text-faint)" }}>{fmtTime(t.last_run_at)}</span>
              <span style={{ display: "flex", justifyContent: "flex-end", gap: 6 }}>
                <Button size="sm" variant="secondary" onClick={() => toggle(t)}>{t.enabled ? "Pause" : "Resume"}</Button>
                <Button size="sm" variant="ghost" icon="info" onClick={() => openEdit(t)}>Edit</Button>
                <Button size="sm" variant="danger" icon="x" onClick={() => del(t)} aria-label="Delete" />
              </span>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}

