import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "@/api/client";
import type { Node, PlatformKind, Spec } from "@/api/types";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { Badge } from "@ds/components/core/Badge";
import { Icon } from "@ds/components/core/Icon";
import { Toaster } from "@ds/components/core/Toast";
import { OsIcon } from "@/components/OsIcon";
import { CreateWizard } from "./CreateServer";

const mono = "var(--font-mono)";

/** Rows per page — the list is a browse surface, so it stays scannable at a glance. */
const PAGE_SIZE = 8;

/** Hatch used behind specs that ship without banner art. */
const NO_ART = "repeating-linear-gradient(135deg,rgba(61,245,207,.05) 0 10px,transparent 10px 20px)";

// Platform kind → mark + tone. Native platforms are neutral brand glyphs (Tux,
// Windows); wine is the same Windows glyph in coral, marking a Windows game
// running on Linux. The tooltip carries the distinction so it never rests on
// color alone.
const PLATFORM: Record<PlatformKind, { label: string; color: string; os: string }> = {
  "linux-native": { label: "Linux", color: "var(--text-muted)", os: "linux" },
  "windows-native": { label: "Windows", color: "var(--text-muted)", os: "windows" },
  "linux-wine": { label: "Wine (Windows game on Linux)", color: "var(--coral-soft)", os: "windows" },
};

const FILTERS: { key: "all" | PlatformKind; label: string }[] = [
  { key: "all", label: "ALL" },
  { key: "linux-native", label: "LINUX" },
  { key: "windows-native", label: "WINDOWS" },
  { key: "linux-wine", label: "WINE" },
];

function PlatformMark({ kind }: { kind: PlatformKind }) {
  const p = PLATFORM[kind];
  if (!p) return null;
  return (
    <span style={{ display: "inline-flex", color: p.color }}>
      <OsIcon os={p.os} size={15} label={p.label} />
    </span>
  );
}

/** Mono segmented control — one active pill, the rest are quiet labels. */
function Segmented<T extends string>({
  value,
  options,
  onChange,
}: {
  value: T;
  options: { key: T; label: string }[];
  onChange: (key: T) => void;
}) {
  return (
    <div
      style={{
        display: "inline-flex",
        alignItems: "center",
        gap: 2,
        padding: 3,
        borderRadius: 999,
        border: "1px solid var(--border-subtle)",
        background: "var(--bg-inset)",
      }}
    >
      {options.map((o) => {
        const on = o.key === value;
        return (
          <button
            key={o.key}
            onClick={() => onChange(o.key)}
            style={{
              padding: "6px 15px",
              borderRadius: 999,
              border: `1px solid ${on ? "var(--border-strong)" : "transparent"}`,
              background: on ? "var(--accent-wash-12)" : "transparent",
              color: on ? "var(--accent)" : "var(--text-muted)",
              fontFamily: mono,
              fontSize: 11,
              letterSpacing: "1.5px",
              cursor: "pointer",
              transition: "background var(--duration-base) var(--ease-standard), color var(--duration-base) var(--ease-standard)",
            }}
          >
            {o.label}
          </button>
        );
      })}
    </div>
  );
}

/** Page numbers with an ellipsis once the run gets long: 1 2 3 … 15 */
function pageWindow(page: number, count: number): (number | "…")[] {
  if (count <= 5) return Array.from({ length: count }, (_, i) => i + 1);
  const near = [page - 1, page, page + 1].filter((p) => p > 1 && p < count);
  const out: (number | "…")[] = [1];
  if (near[0] !== undefined && near[0] > 2) out.push("…");
  out.push(...near);
  if (near[near.length - 1] !== undefined && near[near.length - 1] < count - 1) out.push("…");
  out.push(count);
  return out;
}

export function Specs() {
  const navigate = useNavigate();
  const [specs, setSpecs] = useState<Spec[]>([]);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [platform, setPlatform] = useState<"all" | PlatformKind>("all");
  const [page, setPage] = useState(1);
  const [hovered, setHovered] = useState<string | null>(null);
  const [deploySpec, setDeploySpec] = useState<Spec | null>(null);

  useEffect(() => {
    api
      .listSpecs()
      .then((s) => setSpecs(s.specs ?? []))
      .catch((e) => setError(e instanceof Error ? e.message : "failed"));
    // Nodes are only needed for the per-row Deploy hand-off; a failure here must
    // not blank the list, so it's reported but not fatal.
    api.listNodes().then((n) => setNodes(n.nodes ?? [])).catch(() => undefined);
  }, []);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return specs.filter((s) => {
      if (platform !== "all" && !s.platforms.some((p) => p.kind === platform)) return false;
      if (!q) return true;
      return s.name.toLowerCase().includes(q) || s.slug.toLowerCase().includes(q);
    });
  }, [specs, query, platform]);

  // Keep the page in range as the filters narrow the list.
  const pageCount = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE));
  const current = Math.min(page, pageCount);
  const visible = filtered.slice((current - 1) * PAGE_SIZE, current * PAGE_SIZE);
  const narrowed = () => setPage(1);

  return (
    <main style={{ maxWidth: "var(--container-max)", margin: "0 auto", padding: "34px 30px 70px" }}>
      <div style={{ display: "flex", alignItems: "flex-end", justifyContent: "space-between", gap: 20, flexWrap: "wrap", marginBottom: 22 }}>
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
        <>
          {/* toolbar — search on the left, platform filter + count on the right */}
          <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 16, flexWrap: "wrap", marginBottom: 16 }}>
            <div
              style={{
                display: "flex",
                alignItems: "center",
                gap: 10,
                padding: "11px 16px",
                minWidth: 300,
                borderRadius: "var(--radius-md)",
                border: "1px solid var(--border-subtle)",
                background: "var(--bg-inset)",
              }}
            >
              <input
                value={query}
                onChange={(e) => { setQuery(e.target.value); narrowed(); }}
                placeholder="search name or slug"
                style={{ flex: 1, background: "transparent", border: "none", outline: "none", fontFamily: mono, fontSize: 12.5, color: "var(--text-primary)" }}
              />
              <Icon name="search" size={14} style={{ color: "var(--text-muted)", flex: "none" }} />
            </div>
            <div style={{ display: "flex", alignItems: "center", gap: 14, flexWrap: "wrap" }}>
              <span style={{ fontFamily: mono, fontSize: 11, letterSpacing: "1.5px", color: "var(--text-faint)" }}>PLATFORM</span>
              <Segmented
                value={platform}
                options={FILTERS}
                onChange={(k) => { setPlatform(k); narrowed(); }}
              />
              <span style={{ fontFamily: mono, fontSize: 11, letterSpacing: "1.5px", color: "var(--text-faint)" }}>
                {filtered.length === specs.length
                  ? `${specs.length} SPEC${specs.length === 1 ? "" : "S"}`
                  : `${filtered.length} OF ${specs.length} SPECS`}
              </span>
            </div>
          </div>

          <Card padding={0} style={{ overflow: "hidden", background: "rgba(5,19,24,.55)" }}>
            {visible.map((s, i) => {
              const on = hovered === s.id;
              return (
                <div
                  key={s.id}
                  onMouseEnter={() => setHovered(s.id)}
                  onMouseLeave={() => setHovered((h) => (h === s.id ? null : h))}
                  onClick={() => navigate(`/specs/${s.id}`)}
                  style={{
                    position: "relative",
                    display: "flex",
                    alignItems: "center",
                    gap: 18,
                    minHeight: 106,
                    padding: "0 24px",
                    cursor: "pointer",
                    overflow: "hidden",
                    borderBottom: i === visible.length - 1 ? "none" : "1px solid var(--border-soft)",
                  }}
                >
                  {/* banner art bleeds edge to edge behind the row */}
                  <div
                    aria-hidden
                    style={{
                      position: "absolute",
                      inset: 0,
                      backgroundImage: s.banner_url ? `url(${s.banner_url})` : NO_ART,
                      backgroundSize: "cover",
                      backgroundPosition: "center",
                      opacity: on ? 1 : 0.78,
                      transform: on ? "scale(1.03)" : "none",
                      transition: "opacity var(--duration-base) var(--ease-standard), transform var(--duration-slow) var(--ease-standard)",
                    }}
                  />
                  {/* scrim keeps the title legible on the left and the controls legible on the right */}
                  <div
                    aria-hidden
                    style={{
                      position: "absolute",
                      inset: 0,
                      background:
                        "linear-gradient(90deg, rgba(3,14,19,.88) 0%, rgba(3,14,19,.58) 26%, rgba(3,14,19,.42) 48%, rgba(3,14,19,.82) 72%, rgba(3,14,19,.95) 100%)",
                    }}
                  />

                  {/* Name only — the art carries the identity, so no icon or slug. */}
                  <div
                    style={{
                      position: "relative",
                      flex: 1,
                      minWidth: 0,
                      fontFamily: "var(--font-sans)",
                      fontWeight: 700,
                      fontSize: 20,
                      letterSpacing: "-0.2px",
                      color: "var(--text-primary)",
                      textShadow: "0 2px 16px rgba(2,9,14,.95)",
                      overflow: "hidden",
                      textOverflow: "ellipsis",
                      whiteSpace: "nowrap",
                    }}
                  >
                    {s.name}
                  </div>

                  <div style={{ position: "relative", display: "flex", alignItems: "center", gap: 16, flex: "none" }}>
                    <div style={{ display: "flex", alignItems: "center", gap: 9 }}>
                      {s.platforms.map((p) => <PlatformMark key={p.kind} kind={p.kind} />)}
                    </div>
                    <Badge tone="neutral">v{s.version}</Badge>
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={(e) => { e.stopPropagation(); navigate(`/specs/${s.id}`); }}
                      style={{ color: on ? "var(--text-primary)" : "var(--text-secondary)" }}
                    >
                      Manage
                    </Button>
                    <Button
                      size="sm"
                      variant="primary"
                      icon="play"
                      onClick={(e) => { e.stopPropagation(); setDeploySpec(s); }}
                    >
                      Deploy
                    </Button>
                  </div>
                </div>
              );
            })}

            {visible.length === 0 && (
              <div style={{ padding: "60px 20px", textAlign: "center", fontFamily: mono, fontSize: 12.5, color: "var(--text-muted)" }}>
                No specs match {query.trim() ? `“${query.trim()}”` : "this filter"}.
              </div>
            )}
          </Card>

          {/* footer — range on the left, pager on the right (pager only when it pages) */}
          <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 16, flexWrap: "wrap", marginTop: 18 }}>
            <span style={{ fontFamily: mono, fontSize: 11, letterSpacing: "1.5px", color: "var(--text-faint)" }}>
              {filtered.length === 0
                ? "SHOWING 0 OF 0"
                : `SHOWING ${(current - 1) * PAGE_SIZE + 1}–${(current - 1) * PAGE_SIZE + visible.length} OF ${filtered.length}`}
            </span>
            {pageCount > 1 && (
              <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                <Button size="sm" variant="ghost" disabled={current === 1} onClick={() => setPage(current - 1)}>Previous</Button>
                {pageWindow(current, pageCount).map((p, i) =>
                  p === "…" ? (
                    <span key={`gap${i}`} style={{ fontFamily: mono, fontSize: 12, color: "var(--text-faint)", padding: "0 2px" }}>…</span>
                  ) : (
                    <button
                      key={p}
                      onClick={() => setPage(p)}
                      style={{
                        minWidth: 32,
                        padding: "7px 8px",
                        borderRadius: "var(--radius-sm)",
                        border: `1px solid ${p === current ? "var(--border-strong)" : "var(--border-subtle)"}`,
                        background: p === current ? "var(--accent-wash-12)" : "transparent",
                        color: p === current ? "var(--accent)" : "var(--text-muted)",
                        fontFamily: mono,
                        fontSize: 12,
                        cursor: "pointer",
                      }}
                    >
                      {p}
                    </button>
                  ),
                )}
                <Button size="sm" variant="secondary" disabled={current === pageCount} onClick={() => setPage(current + 1)}>Next</Button>
              </div>
            )}
          </div>
        </>
      )}

      {deploySpec && (
        <div
          onClick={() => setDeploySpec(null)}
          style={{ position: "fixed", inset: 0, zIndex: 100, background: "rgba(1,9,14,.78)", overflowY: "auto", padding: "48px 20px" }}
        >
          <div onClick={(e) => e.stopPropagation()}>
            <CreateWizard
              specs={specs}
              nodes={nodes}
              initialSpecId={deploySpec.id}
              onCancel={() => setDeploySpec(null)}
              onDeploy={async ({ spec_id, name, variables, steam_guard_code, install_bepinex, node_id }) => {
                try {
                  const sv = await api.createServer({ spec_id, name, variables, steam_guard_code, install_bepinex, node_id });
                  setDeploySpec(null);
                  Toaster.success(`Deploying ${name}…`);
                  navigate(`/servers/${sv.id}`);
                } catch (e) {
                  Toaster.error(e instanceof Error ? e.message : "deploy failed");
                  setDeploySpec(null);
                }
              }}
            />
          </div>
        </div>
      )}
    </main>
  );
}
