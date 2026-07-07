import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "@/api/client";
import type { PlatformKind, Spec } from "@/api/types";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { Badge } from "@ds/components/core/Badge";
import { Icon } from "@ds/components/core/Icon";

const mono = "var(--font-mono)";

// Platform kind → badge label + tone. Convention: linux→accent, wine→coral,
// windows→neutral (one teal accent; coral is the wine counterpoint).
const platformBadge: Record<PlatformKind, { label: string; tone: "accent" | "coral" | "neutral" }> = {
  "linux-native": { label: "LINUX", tone: "accent" },
  "linux-wine": { label: "WINE", tone: "coral" },
  "windows-native": { label: "WINDOWS", tone: "neutral" },
};

export function Specs() {
  const navigate = useNavigate();
  const [specs, setSpecs] = useState<Spec[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.listSpecs().then((s) => setSpecs(s.specs ?? [])).catch((e) => setError(e instanceof Error ? e.message : "failed"));
  }, []);

  return (
    <main style={{ maxWidth: "var(--container-max)", margin: "0 auto", padding: "34px 30px 70px" }}>
      <div style={{ display: "flex", alignItems: "flex-end", justifyContent: "space-between", gap: 20, flexWrap: "wrap", marginBottom: 26 }}>
        <div>
          <div style={{ fontFamily: mono, fontSize: 12, letterSpacing: "3px", color: "var(--accent)", marginBottom: 10 }}>// GAME SPECS</div>
          <h1 style={{ fontFamily: "var(--font-display)", fontWeight: 800, fontSize: 34, letterSpacing: "-0.5px", margin: 0, color: "var(--text-primary)" }}>
            Game specs
          </h1>
        </div>
        <div style={{ display: "flex", gap: 10 }}>
          <Button variant="secondary" icon="layers" onClick={() => navigate("/catalog")}>Browse catalog</Button>
          <Button variant="primary" icon="plus" onClick={() => navigate("/specs/new")}>New spec</Button>
        </div>
      </div>

      {error && <div style={{ color: "var(--status-crashed)", fontFamily: mono, fontSize: 13, marginBottom: 16 }}>{error}</div>}

      {specs.length === 0 ? (
        <Card dashed style={{ textAlign: "center", padding: "80px 20px" }}>
          <img
            src="/kraken-glyph-teal.png"
            alt="Kraken"
            style={{ display: "block", margin: "0 auto 12px", width: 40, height: 40, objectFit: "contain", filter: "drop-shadow(0 0 10px rgba(61,245,207,.35))" }}
          />
          <div style={{ fontFamily: mono, color: "var(--text-secondary)", marginBottom: 18 }}>
            No game specs yet. Author one to make a game deployable.
          </div>
          <Button variant="primary" icon="plus" onClick={() => navigate("/specs/new")}>New spec</Button>
        </Card>
      ) : (
        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill,minmax(300px,1fr))", gap: 16 }}>
          {specs.map((s) => (
            <Card
              key={s.id}
              padding={18}
              onClick={() => navigate(`/specs/${s.id}`)}
              style={{ cursor: "pointer", display: "flex", flexDirection: "column" }}
            >
              {s.banner_url && (
                <img
                  src={s.banner_url}
                  alt=""
                  style={{ width: "calc(100% + 36px)", margin: "-18px -18px 14px", height: 96, objectFit: "cover", borderTopLeftRadius: "var(--radius-lg)", borderTopRightRadius: "var(--radius-lg)" }}
                />
              )}
              <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 10, marginBottom: 6 }}>
                <span style={{ display: "flex", alignItems: "center", gap: 10, minWidth: 0 }}>
                  {s.icon_url && (
                    <img src={s.icon_url} alt="" style={{ width: 28, height: 28, borderRadius: "var(--radius-sm)", border: "1px solid var(--border-strong)", objectFit: "cover", flex: "none" }} />
                  )}
                  <span style={{ fontFamily: "var(--font-sans)", fontWeight: 700, fontSize: 16, color: "var(--text-primary)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{s.name}</span>
                </span>
                <Badge tone="neutral">v{s.version}</Badge>
              </div>
              <div style={{ fontFamily: mono, fontSize: 11.5, color: "var(--text-muted)", marginBottom: 14, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{s.slug}</div>
              <div style={{ display: "flex", gap: 6, flexWrap: "wrap", marginBottom: 16 }}>
                {s.platforms.map((p) => {
                  const b = platformBadge[p.kind];
                  return <Badge key={p.kind} tone={b.tone}>{b.label}</Badge>;
                })}
              </div>
              <span style={{ marginTop: "auto", display: "inline-flex", alignItems: "center", gap: 6, fontFamily: mono, fontSize: 11.5, letterSpacing: "1px", color: "var(--accent)" }}>
                Manage <Icon name="play" size={13} />
              </span>
            </Card>
          ))}
        </div>
      )}
    </main>
  );
}
