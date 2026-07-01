import { useCallback, useEffect, useState } from "react";
import { api } from "@/api/client";
import { useDialog } from "@/components/Dialog";
import type { Backup } from "@/api/types";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { Icon } from "@ds/components/core/Icon";

const mono = "var(--font-mono)";
const GRID = "minmax(120px,1fr) 80px 150px 175px 185px";
const ROW_GAP = "0 14px";

// Small status chip; tone carries meaning (green ready, amber in-flight, red failed).
function Chip({ label, tone, spin }: { label: string; tone: "ok" | "wait" | "bad" | "muted"; spin?: boolean }) {
  const color =
    tone === "ok" ? "var(--status-running)" : tone === "wait" ? "var(--status-starting, #F4C152)" : tone === "bad" ? "var(--status-crashed)" : "var(--text-faint)";
  return (
    <span
      style={{
        display: "inline-flex",
        alignItems: "center",
        gap: 6,
        fontFamily: mono,
        fontSize: 10.5,
        letterSpacing: ".4px",
        textTransform: "uppercase",
        padding: "2px 8px",
        borderRadius: 999,
        color,
        border: `1px solid ${color}`,
        background: "transparent",
      }}
    >
      <span style={{ width: 6, height: 6, borderRadius: "50%", background: color, animation: spin ? "abyssalPulseDot 1.2s ease-in-out infinite" : undefined }} />
      {label}
    </span>
  );
}

function BackupStatus({ b }: { b: Backup }) {
  return (
    <span style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap" }}>
      {b.state === "pending" && <Chip label="Archiving" tone="wait" spin />}
      {b.state === "ready" && <Chip label="Ready" tone="ok" />}
      {b.state === "failed" && <Chip label="Failed" tone="bad" />}
      {b.replication === "pending" && <Chip label="Replicating" tone="wait" spin />}
      {b.replication === "done" && <Chip label="Mirrored" tone="ok" />}
      {b.replication === "failed" && <Chip label="Mirror failed" tone="bad" />}
    </span>
  );
}

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
  gap: ROW_GAP,
  alignItems: "center",
  padding: "12px 18px",
  borderBottom: "1px solid var(--border-subtle)",
  fontFamily: mono,
  fontSize: 10.5,
  letterSpacing: 1,
  textTransform: "uppercase",
  color: "var(--text-faint)",
};

function human(size: number): string {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KiB`;
  if (size < 1024 * 1024 * 1024) return `${(size / 1024 / 1024).toFixed(1)} MiB`;
  return `${(size / 1024 / 1024 / 1024).toFixed(2)} GiB`;
}

export function ServerBackupsPanel({ id, onRequestRestart }: { id: string; onRequestRestart: () => void }) {
  const { confirm, prompt } = useDialog();
  const [backups, setBackups] = useState<Backup[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);

  const load = useCallback(() => {
    api.listBackups(id).then((r) => setBackups(r.backups ?? [])).catch((e) => setError(e instanceof Error ? e.message : "failed to load backups"));
  }, [id]);
  useEffect(load, [load]);

  // Backups run asynchronously: while any archive is still being written or
  // mirrored, poll for the state transition so the UI settles on its own.
  useEffect(() => {
    const inFlight = backups.some((b) => b.state === "pending" || b.replication === "pending");
    if (!inFlight) return;
    const t = setTimeout(load, 3000);
    return () => clearTimeout(t);
  }, [backups, load]);

  const act = async (fn: () => Promise<unknown>, msg?: string) => {
    setBusy(true); setError(null); setNotice(null);
    try { await fn(); if (msg) setNotice(msg); load(); }
    catch (e) { setError(e instanceof Error ? e.message : "action failed"); }
    finally { setBusy(false); }
  };

  const create = async () => {
    const name = await prompt({ title: "Create backup", message: "Backup name (optional):", placeholder: "pre-update" });
    if (name === null) return; // cancelled
    void act(() => api.createBackup(id, name), "Backup started — archiving in the background.");
  };
  const restore = async (b: Backup) => {
    if (await confirm({ title: "Restore backup", message: `Restore "${b.name}"? This overwrites the current data. Restart to load it.`, confirmLabel: "Restore" })) {
      void act(() => api.restoreBackup(id, b.id), "Restored. Restart the server to load the restored data.");
    }
  };
  const del = async (b: Backup) => {
    if (await confirm({ title: "Delete backup", message: `Delete backup "${b.name}"?`, confirmLabel: "Delete", danger: true })) void act(() => api.deleteBackup(id, b.id));
  };

  return (
    <div style={{ paddingBottom: 30 }}>
      <div style={{ display: "flex", alignItems: "flex-end", justifyContent: "space-between", marginBottom: 18, gap: 16, flexWrap: "wrap" }}>
        <div>
          <p style={{ ...groupLabel, marginBottom: 6 }}>Backups</p>
          <p style={{ margin: 0, fontSize: 13.5, color: "var(--text-muted)" }}>Snapshots of this server's data volume, stored on the node.</p>
        </div>
        <Button variant="primary" disabled={busy} icon="plus" onClick={create}>{busy ? "Working…" : "Create backup"}</Button>
      </div>

      {error && <div style={{ color: "var(--status-crashed)", fontFamily: mono, fontSize: 13, marginBottom: 12 }}>{error}</div>}
      {notice && (
        <div style={{ marginBottom: 16, padding: "11px 14px", borderRadius: "var(--radius-md)", border: "1px solid var(--border-strong)", background: "var(--accent-wash-12)", color: "var(--text-secondary)", fontSize: 13.5, display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12 }}>
          <span style={{ display: "inline-flex", alignItems: "center", gap: 9 }}>
            <Icon name="check" size={15} strokeWidth={2} style={{ color: "var(--accent)" }} />
            {notice}
          </span>
          {notice.includes("Restart") && <Button variant="primary" size="sm" icon="refresh" onClick={onRequestRestart}>Restart now</Button>}
        </div>
      )}

      {backups.length === 0 ? (
        <Card dashed padding={40}>
          <div style={{ display: "flex", flexDirection: "column", alignItems: "center", justifyContent: "center", gap: 12, textAlign: "center" }}>
            <Icon name="kraken" size={46} strokeWidth={1.5} style={{ color: "var(--accent)" }} />
            <div style={{ fontFamily: mono, fontSize: 13.5, color: "var(--text-muted)" }}>No backups yet. Create one to snapshot the current data.</div>
          </div>
        </Card>
      ) : (
        <div style={{ overflowX: "auto", border: "1px solid var(--border-subtle)", borderRadius: "var(--radius-lg)", background: "rgba(7,23,29,.55)" }}>
          <div style={{ minWidth: 760 }}>
          <div style={colHead}>
            <span>Name</span><span style={{ textAlign: "right" }}>Size</span><span>Status</span><span style={{ textAlign: "right" }}>Created</span><span />
          </div>
          {backups.map((b) => (
            <div key={b.id} style={{ display: "grid", gridTemplateColumns: GRID, gap: ROW_GAP, alignItems: "center", padding: "12px 18px", borderBottom: "1px solid var(--border-subtle)", fontSize: 13.5 }}>
              <span style={{ display: "flex", alignItems: "center", gap: 9, fontFamily: mono, color: "var(--text-primary)" }}>
                <Icon name="folder" size={15} strokeWidth={2} style={{ color: "var(--text-muted)" }} />
                {b.name}
              </span>
              <span style={{ textAlign: "right", fontFamily: mono, fontSize: 12, color: "var(--text-muted)" }}>{b.state === "ready" ? human(b.size) : "—"}</span>
              <BackupStatus b={b} />
              <span style={{ textAlign: "right", fontFamily: mono, fontSize: 12, color: "var(--text-faint)" }}>{b.created_ms ? new Date(b.created_ms).toLocaleString() : "—"}</span>
              <span style={{ display: "flex", justifyContent: "flex-end", gap: 8 }}>
                <Button size="sm" variant="secondary" icon="refresh" disabled={b.state !== "ready"} onClick={() => restore(b)}>Restore</Button>
                <Button size="sm" variant="danger" icon="x" onClick={() => del(b)}>Delete</Button>
              </span>
            </div>
          ))}
          </div>
        </div>
      )}
    </div>
  );
}
