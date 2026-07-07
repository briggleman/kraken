import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "@/api/client";
import type { CatalogItem } from "@/api/types";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { Badge } from "@ds/components/core/Badge";
import { Icon } from "@ds/components/core/Icon";

const mono = "var(--font-mono)";

const PLATFORM_BADGE: Record<string, { label: string; tone: "accent" | "coral" | "neutral" }> = {
  "linux-native": { label: "LINUX", tone: "accent" },
  "linux-wine": { label: "WINE", tone: "coral" },
  "windows-native": { label: "WINDOWS", tone: "neutral" },
};

// Catalog is the standalone browse-and-import view of the built-in starter game
// specs (reachable from the Game Specs page). Importing adds the spec to the live
// catalog so it can be deployed.
export function Catalog() {
  const navigate = useNavigate();
  const [items, setItems] = useState<CatalogItem[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [importing, setImporting] = useState<string | null>(null);

  const refresh = () => {
    api
      .listCatalog()
      .then((c) => setItems(c.catalog ?? []))
      .catch((e) => setError(e instanceof Error ? e.message : "failed to load catalog"));
  };
  useEffect(refresh, []);

  const importGame = async (id: string) => {
    setImporting(id);
    setError(null);
    try {
      await api.importCatalog(id);
      refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "import failed");
    } finally {
      setImporting(null);
    }
  };

  return (
    <main style={{ maxWidth: "var(--container-max)", margin: "0 auto", padding: "34px 30px 70px" }}>
      <div style={{ display: "flex", alignItems: "flex-end", justifyContent: "space-between", gap: 20, flexWrap: "wrap", marginBottom: 26 }}>
        <div>
          <div style={{ fontFamily: mono, fontSize: 12, letterSpacing: "3px", color: "var(--accent)", marginBottom: 10 }}>// CATALOG</div>
          <h1 style={{ fontFamily: "var(--font-display)", fontWeight: 800, fontSize: 34, letterSpacing: "-0.5px", margin: 0, color: "var(--text-primary)" }}>
            Starter games
          </h1>
        </div>
        <Button variant="ghost" icon="layers" onClick={() => navigate("/specs")}>Back to specs</Button>
      </div>

      {error && <div style={{ color: "var(--status-crashed)", fontFamily: mono, fontSize: 13, marginBottom: 16 }}>{error}</div>}

      {items.length === 0 ? (
        <Card dashed style={{ textAlign: "center", padding: "80px 20px" }}>
          <img
            src="/kraken-glyph-teal.png"
            alt="Kraken"
            style={{ display: "block", margin: "0 auto 12px", width: 40, height: 40, objectFit: "contain", filter: "drop-shadow(0 0 10px rgba(61,245,207,.35))" }}
          />
          <div style={{ fontFamily: mono, color: "var(--text-secondary)" }}>The catalog is empty.</div>
        </Card>
      ) : (
        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill,minmax(300px,1fr))", gap: 16 }}>
          {items.map((g) => (
            <Card key={g.id} padding={0} style={{ overflow: "hidden", display: "flex", flexDirection: "column" }}>
              <div
                style={{
                  height: 96,
                  backgroundImage: g.banner_url ? `url(${g.banner_url})` : "repeating-linear-gradient(135deg,rgba(61,245,207,.05) 0 10px,transparent 10px 20px)",
                  backgroundColor: "rgba(3,16,21,.7)",
                  backgroundSize: "cover",
                  backgroundPosition: "center",
                  borderBottom: "1px solid var(--border-soft)",
                }}
              />
              <div style={{ padding: 18, display: "flex", flexDirection: "column", flex: 1 }}>
                <div style={{ fontFamily: "var(--font-sans)", fontWeight: 700, fontSize: 16, color: "var(--text-primary)", marginBottom: 4 }}>{g.name}</div>
                <div style={{ fontSize: 12.5, color: "var(--text-muted)", marginBottom: 14, flex: 1 }}>{g.description}</div>
                <div style={{ display: "flex", gap: 6, flexWrap: "wrap", marginBottom: 16 }}>
                  {g.platforms.map((p) => {
                    const b = PLATFORM_BADGE[p] ?? { label: p.toUpperCase(), tone: "neutral" as const };
                    return <Badge key={p} tone={b.tone}>{b.label}</Badge>;
                  })}
                </div>
                {g.already_imported ? (
                  <div style={{ display: "flex", alignItems: "center", gap: 7, fontFamily: mono, fontSize: 12.5, color: "var(--status-running)" }}>
                    <Icon name="check" size={14} /> Imported
                  </div>
                ) : (
                  <Button size="sm" variant="primary" icon="plus" disabled={importing === g.id} onClick={() => void importGame(g.id)}>
                    {importing === g.id ? "Importing…" : "Import"}
                  </Button>
                )}
              </div>
            </Card>
          ))}
        </div>
      )}
    </main>
  );
}
