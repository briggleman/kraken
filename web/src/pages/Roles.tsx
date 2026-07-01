import { useEffect, useState } from "react";
import { Page } from "@/components/Shell";
import { api } from "@/api/client";
import type { Role } from "@/api/types";
import { Card } from "@ds/components/core/Card";
import { Badge } from "@ds/components/core/Badge";
import { Icon } from "@ds/components/core/Icon";

const mono = "var(--font-mono)";

export function Roles() {
  const [roles, setRoles] = useState<Role[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.listRoles().then((r) => setRoles(r.roles ?? [])).catch((e) => setError(e instanceof Error ? e.message : "failed"));
  }, []);

  return (
    <Page>
      <div style={{ marginBottom: 28 }}>
        <div style={{ fontFamily: mono, fontSize: 12, letterSpacing: 3, color: "var(--accent)", marginBottom: 12, textTransform: "uppercase" }}>// ADMIN · ROLES</div>
        <h1 style={{ fontFamily: "var(--font-display)", fontWeight: 800, fontSize: 34, margin: 0, color: "var(--text-primary)", letterSpacing: "-0.01em" }}>Roles</h1>
        <p style={{ margin: "10px 0 0", fontSize: 13.5, color: "var(--text-muted)", maxWidth: 560 }}>
          Built-in roles and the permissions they grant. Custom roles coming soon.
        </p>
      </div>

      {error && (
        <div style={{ display: "flex", alignItems: "center", gap: 8, color: "var(--status-crashed)", fontFamily: mono, fontSize: 13, marginBottom: 16 }}>
          <Icon name="crashed" size={15} />
          {error}
        </div>
      )}

      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit,minmax(340px,1fr))", gap: 16 }}>
        {roles.map((r) => (
          <Card key={r.id} padding={20}>
            <div style={{ display: "flex", alignItems: "center", gap: 10, marginBottom: 16 }}>
              <Icon name="lock" size={16} strokeWidth={2} />
              <Badge tone="accent">{r.name}</Badge>
              {r.builtin && (
                <span style={{ fontFamily: mono, fontSize: 10, letterSpacing: 1.5, color: "var(--text-faint)", textTransform: "uppercase", marginLeft: "auto" }}>
                  Built-in
                </span>
              )}
            </div>
            <div style={{ fontFamily: mono, fontSize: 11, letterSpacing: 1.5, color: "var(--text-faint)", textTransform: "uppercase", marginBottom: 10 }}>
              Permissions
            </div>
            <div style={{ display: "flex", flexWrap: "wrap", gap: 7 }}>
              {(r.permissions ?? []).map((p) => {
                const wildcard = p === "*" || p.endsWith(".*");
                return (
                  <Badge key={p} tone={wildcard ? "coral" : "neutral"}>
                    {p === "*" ? "all permissions" : p}
                  </Badge>
                );
              })}
            </div>
          </Card>
        ))}
      </div>
    </Page>
  );
}
