import { useEffect, useState } from "react";
import { api } from "@/api/client";
import type { AuditEntry } from "@/api/types";
import { Card } from "@ds/components/core/Card";

const mono = "var(--font-mono)";
const GRID = "150px 1fr 1.4fr 70px 120px";

const colHead: React.CSSProperties = {
  display: "grid",
  gridTemplateColumns: GRID,
  gap: 10,
  padding: "11px 18px",
  borderBottom: "1px solid var(--border-soft)",
  fontFamily: mono,
  fontSize: 10,
  letterSpacing: "1.5px",
  color: "var(--text-faint)",
};

function statusColor(code: number): string {
  if (code >= 500) return "var(--status-crashed)";
  if (code >= 400) return "var(--status-starting)";
  return "var(--status-running)";
}

export function Audit() {
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.listAudit().then((r) => setEntries(r.entries ?? [])).catch((e) => setError(e instanceof Error ? e.message : "failed to load audit log"));
  }, []);

  return (
    <main style={{ maxWidth: "var(--container-max)", margin: "0 auto", padding: "34px 30px 70px" }}>
      <div style={{ marginBottom: 26 }}>
        <div style={{ fontFamily: mono, fontSize: 12, letterSpacing: "3px", color: "var(--accent)", marginBottom: 10 }}>// ADMIN</div>
        <h1 style={{ fontFamily: "var(--font-display)", fontWeight: 800, fontSize: 34, letterSpacing: "-0.5px", margin: 0, color: "var(--text-primary)" }}>
          Audit log
        </h1>
        <p style={{ margin: "10px 0 0", fontSize: 14, color: "var(--text-muted)" }}>
          Recent security-relevant actions: authentication, power, and CRUD across servers, specs, nodes, and users.
        </p>
      </div>

      {error && <div style={{ color: "var(--status-crashed)", fontFamily: mono, fontSize: 13, marginBottom: 16 }}>{error}</div>}

      {entries.length === 0 ? (
        <Card dashed style={{ textAlign: "center", padding: "70px 20px", fontFamily: mono, color: "var(--text-muted)" }}>
          No audit entries yet.
        </Card>
      ) : (
        <Card padding={0} style={{ overflow: "hidden", background: "rgba(5,19,24,.55)" }}>
          <div style={colHead}>
            <span>TIME</span><span>ACTOR</span><span>ACTION</span><span>STATUS</span><span>IP</span>
          </div>
          {entries.map((e) => (
            <div key={e.id} style={{ display: "grid", gridTemplateColumns: GRID, gap: 10, padding: "11px 18px", alignItems: "center", borderBottom: "1px solid var(--border-soft)", fontSize: 12.5, color: "#cfe7e2" }}>
              <span style={{ fontFamily: mono, fontSize: 11.5, color: "var(--text-faint)" }}>{new Date(e.time).toLocaleString()}</span>
              <span style={{ fontFamily: mono, color: "var(--text-primary)" }}>{e.actor}</span>
              <span style={{ fontFamily: mono, fontSize: 12, color: "var(--text-secondary)", whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}>{e.action}</span>
              <span style={{ fontFamily: mono, fontSize: 12, fontWeight: 600, color: statusColor(e.status) }}>{e.status}</span>
              <span style={{ fontFamily: mono, fontSize: 11.5, color: "var(--text-faint)" }}>{e.ip || "—"}</span>
            </div>
          ))}
        </Card>
      )}
    </main>
  );
}
