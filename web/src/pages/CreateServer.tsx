import { useMemo, useState } from "react";
import type { Node, Spec, SpecVariable } from "@/api/types";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { Input } from "@ds/components/core/Input";
import { Badge } from "@ds/components/core/Badge";
import { Toggle } from "@ds/components/core/Toggle";
import { Icon } from "@ds/components/core/Icon";
import { OsIcon } from "@/components/OsIcon";

const mono = "var(--font-mono)";
const STEPS = ["Game", "Placement", "Configure", "Deploy"] as const;

const SECTION_LABEL: React.CSSProperties = {
  fontFamily: mono,
  fontSize: 11,
  letterSpacing: "1.5px",
  color: "var(--text-faint)",
  marginBottom: 14,
};

// Platform badges derived from a spec's declared platforms.
function platformBadges(spec: Spec) {
  const kinds = (spec.platforms ?? []).map((p) => p.kind);
  const out: { tone: "accent" | "coral" | "neutral"; label: string }[] = [];
  if (kinds.some((k) => k.startsWith("linux"))) out.push({ tone: "accent", label: "LINUX" });
  if (kinds.some((k) => k === "windows-native")) out.push({ tone: "neutral", label: "WINDOWS" });
  if (kinds.some((k) => k === "linux-wine")) out.push({ tone: "coral", label: "WINE" });
  return out;
}

function appId(spec: Spec): string | null {
  const ids = Object.values(spec.steam_app_ids ?? {});
  return ids.length ? `app ${ids[0]}` : spec.slug;
}

function StepDots({ step }: { step: number }) {
  return (
    <div style={{ display: "flex", alignItems: "center", marginBottom: 28 }}>
      {STEPS.map((label, i) => (
        <div key={label} style={{ display: "contents" }}>
          <div style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 8 }}>
            <div
              style={{
                width: 32,
                height: 32,
                borderRadius: "50%",
                border: i <= step ? "1px solid var(--border-strong)" : "1px solid var(--border-subtle)",
                color: i < step ? "var(--text-on-accent)" : i === step ? "var(--accent)" : "var(--text-faint)",
                display: "flex",
                alignItems: "center",
                justifyContent: "center",
                fontWeight: 700,
                fontSize: 13,
                fontFamily: mono,
                boxShadow: i < step ? "var(--elevation-glow-soft)" : "none",
                background:
                  i === step ? "var(--accent-wash-12)" : i < step ? "var(--gradient-accent)" : "transparent",
              }}
            >
              {i < step ? <Icon name="check" size={15} /> : i + 1}
            </div>
            <span style={{ fontSize: 10.5, fontFamily: mono, color: i === step ? "var(--text-primary)" : "var(--text-faint)" }}>
              {label}
            </span>
          </div>
          {i < STEPS.length - 1 && (
            <div
              style={{
                flex: 1,
                height: 2,
                margin: "0 6px 22px",
                background: i < step ? "var(--accent)" : "var(--border-subtle)",
              }}
            />
          )}
        </div>
      ))}
    </div>
  );
}

export function CreateWizard({
  specs,
  nodes,
  onCancel,
  onDeploy,
}: {
  specs: Spec[];
  nodes: Node[];
  onCancel: () => void;
  onDeploy: (input: { spec_id: string; name: string; variables: Record<string, string>; steam_guard_code?: string; install_bepinex?: boolean }) => Promise<void>;
}) {
  const [step, setStep] = useState(0);
  const [specId, setSpecId] = useState<string | null>(null);
  const [nodeId, setNodeId] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [overrides, setOverrides] = useState<Record<string, string>>({});
  const [steamGuard, setSteamGuard] = useState("");
  const [installBepInEx, setInstallBepInEx] = useState(false);
  const [busy, setBusy] = useState(false);

  const spec = useMemo(() => specs.find((s) => s.id === specId) ?? null, [specs, specId]);
  const editable: SpecVariable[] = (spec?.variables ?? []).filter((v) => v.user_editable);
  // Mirrors the scheduler's eligibility: only show nodes the selected game can
  // actually be placed on — online, platform match, enough unreserved memory,
  // and enough free game ports.
  const eligibleNodes = useMemo(() => {
    const specKinds = (spec?.platforms ?? []).map((p) => p.kind);
    const nodeKinds = (n: Node): string[] =>
      n.os === "linux" ? (n.wine_enabled ? ["linux-native", "linux-wine"] : ["linux-native"]) : ["windows-native"];
    const freePorts = (n: Node): number | null => {
      if (n.ports == null) return null; // panel predates port info — don't rule out
      const total = (n.ports.ranges ?? []).reduce(
        (sum, r) => sum + (r.end >= r.start ? r.end - r.start + 1 : 0), 0);
      return total - (n.ports.allocated?.length ?? 0);
    };
    return nodes.filter((n) => {
      if (n.status !== "online") return false;
      if (!spec) return true;
      if (!specKinds.some((k) => nodeKinds(n).includes(k))) return false;
      if (n.total_memory_mb - n.allocated_memory_mb < spec.resources.min_memory_mb) return false;
      const free = freePorts(n);
      return free === null || free >= (spec.ports?.length ?? 0);
    });
  }, [nodes, spec]);
  const placedNode = eligibleNodes.find((n) => n.id === nodeId) ?? eligibleNodes[0] ?? null;

  const nameErr = name.trim() === "" || name.length > 64;
  const canNext =
    step === 0 ? !!specId : step === 1 ? eligibleNodes.length > 0 : step === 2 ? !nameErr : true;

  const next = () => setStep((s) => Math.min(STEPS.length - 1, s + 1));
  const back = () => (step === 0 ? onCancel() : setStep((s) => s - 1));

  const loadPct = (n: Node) =>
    n.total_memory_mb > 0 ? Math.round((n.allocated_memory_mb / n.total_memory_mb) * 100) : 0;

  const deploy = async () => {
    if (!specId) return;
    setBusy(true);
    try {
      await onDeploy({ spec_id: specId, name: name.trim(), variables: overrides, steam_guard_code: steamGuard.trim() || undefined, install_bepinex: spec?.install?.bepinex_compatible ? installBepInEx : undefined });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div style={{ maxWidth: 760, margin: "0 auto" }}>
      <div style={{ fontFamily: mono, fontSize: 12, letterSpacing: "3px", color: "var(--accent)", marginBottom: 10 }}>
        // CREATE SERVER
      </div>
      <h1
        style={{
          fontFamily: "var(--font-display)",
          fontWeight: 800,
          fontSize: 30,
          letterSpacing: "-0.5px",
          margin: "0 0 28px",
          color: "var(--text-primary)",
        }}
      >
        Summon a new server
      </h1>

      <StepDots step={step} />

      <Card>
        {step === 0 && (
          <div>
            <div style={SECTION_LABEL}>PICK A GAME</div>
            {specs.length === 0 ? (
              <div style={{ fontFamily: mono, fontSize: 13, color: "var(--text-muted)" }}>
                No game specs yet — add one under Game Specs first.
              </div>
            ) : (
              <div style={{ display: "grid", gridTemplateColumns: "repeat(3,1fr)", gap: 12 }}>
                {specs.map((g) => {
                  const sel = specId === g.id;
                  return (
                    <div
                      key={g.id}
                      onClick={() => setSpecId(g.id)}
                      style={{
                        borderRadius: "var(--radius-md)",
                        overflow: "hidden",
                        cursor: "pointer",
                        border: `1px solid ${sel ? "var(--border-strong)" : "var(--border-subtle)"}`,
                        background: sel ? "var(--accent-wash-08)" : "rgba(7,23,29,.5)",
                        boxShadow: sel ? "var(--elevation-glow-soft)" : "none",
                      }}
                    >
                      <div
                        style={{
                          height: 74,
                          backgroundImage: g.banner_url
                            ? `url(${g.banner_url})`
                            : "repeating-linear-gradient(135deg,rgba(61,245,207,.05) 0 10px,transparent 10px 20px)",
                          backgroundColor: "rgba(3,16,21,.7)",
                          backgroundSize: "cover",
                          backgroundPosition: "center",
                          display: "flex",
                          alignItems: "center",
                          justifyContent: "center",
                          borderBottom: "1px solid var(--border-soft)",
                        }}
                      >
                        {!g.banner_url && (
                          <span style={{ fontFamily: mono, fontSize: 10, letterSpacing: 2, color: "var(--text-faint)" }}>
                            {g.slug.toUpperCase()}
                          </span>
                        )}
                      </div>
                      <div style={{ padding: 13 }}>
                        <div style={{ fontWeight: 600, fontSize: 14, color: "var(--text-primary)", marginBottom: 3 }}>{g.name}</div>
                        <div style={{ fontFamily: mono, fontSize: 10, color: "var(--text-muted)", marginBottom: 9 }}>{appId(g)}</div>
                        <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
                          {platformBadges(g).map((b) => (
                            <Badge key={b.label} tone={b.tone}>{b.label}</Badge>
                          ))}
                        </div>
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        )}

        {step === 1 && (
          <div>
            <div style={SECTION_LABEL}>NODE PLACEMENT</div>
            {eligibleNodes.length === 0 && (
              <div style={{ fontFamily: mono, fontSize: 12.5, color: "var(--text-muted)", marginBottom: 12 }}>
                No node can host {spec?.name ?? "this game"} right now — it needs an online node
                matching its platform ({(spec?.platforms ?? []).map((p) => p.kind).join(", ") || "any"})
                with {spec ? `${spec.resources.min_memory_mb}MB of` : "enough"} free memory and free game ports.
              </div>
            )}
            {eligibleNodes.map((n, i) => {
              const sel = placedNode ? placedNode.id === n.id : i === 0;
              return (
                <div
                  key={n.id}
                  onClick={() => setNodeId(n.id)}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "space-between",
                    padding: "14px 16px",
                    borderRadius: "var(--radius-md)",
                    cursor: "pointer",
                    border: `1px solid ${sel ? "var(--border-strong)" : "var(--border-subtle)"}`,
                    background: sel ? "var(--accent-wash-08)" : "rgba(7,23,29,.4)",
                    marginBottom: 10,
                  }}
                >
                  <div style={{ display: "flex", alignItems: "center", gap: 11 }}>
                    <OsIcon os={n.os} size={16} style={{ color: "var(--accent)" }} />
                    <span style={{ fontFamily: mono, fontSize: 13, color: "var(--text-primary)" }}>{n.name}</span>
                    <span style={{ fontSize: 12, color: "var(--text-muted)" }}>
                      {n.os}
                      {n.wine_enabled ? " · wine" : ""} · {loadPct(n)}% load
                    </span>
                  </div>
                  {sel && <Icon name="check" size={16} style={{ color: "var(--accent)" }} />}
                </div>
              );
            })}
            <div style={{ fontSize: 11.5, color: "var(--text-faint)", marginTop: 4 }}>
              The scheduler makes the final placement (it prefers a native Linux node when one is eligible).
            </div>
          </div>
        )}

        {step === 2 && (
          <div>
            <div style={SECTION_LABEL}>CONFIGURE · {spec ? spec.name.toUpperCase() : ""}</div>
            <Input
              label="SERVER NAME"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="leviathan-01"
              focused={!nameErr && name.length > 0}
              error={name.length > 64}
              mono
              helper={name.length > 64 ? "Must be 64 or fewer characters." : "A unique name for your server."}
            />
            {editable.map((v) => (
              <div key={v.key} style={{ marginTop: 14 }}>
                <Input
                  label={(v.label || v.key).toUpperCase()}
                  value={overrides[v.key] ?? v.default}
                  onChange={(e) => setOverrides((o) => ({ ...o, [v.key]: e.target.value }))}
                  mono
                  helper={v.rules || undefined}
                />
              </div>
            ))}
            {spec?.install?.bepinex_compatible && (
              <label style={{ display: "flex", alignItems: "center", gap: 10, marginTop: 18, cursor: "pointer", fontFamily: "var(--font-sans)", fontSize: 13.5, color: "var(--text-secondary)" }}>
                <Toggle checked={installBepInEx} onChange={setInstallBepInEx} />
                <span>
                  Install BepInEx mod support
                  <span style={{ display: "block", fontSize: 12, color: "var(--text-muted)" }}>Adds the BepInEx loader so plugins under <code>/data/BepInEx/plugins</code> run. Leave off for a vanilla server.</span>
                </span>
              </label>
            )}
            {spec?.install?.requires_steam_login && (
              <div style={{ marginTop: 14 }}>
                <Input
                  label="STEAM GUARD CODE"
                  value={steamGuard}
                  onChange={(e) => setSteamGuard(e.target.value)}
                  mono
                  autoComplete="off"
                  placeholder="e.g. 2FXY7"
                  helper="This game needs the node's Steam account (set in node settings). Enter a fresh Steam Guard code; leave blank if the account has no Steam Guard."
                />
              </div>
            )}
          </div>
        )}

        {step === 3 && (
          <div style={{ textAlign: "center", padding: "20px 0" }}>
            <div
              style={{
                width: 64,
                height: 64,
                borderRadius: "50%",
                margin: "0 auto 18px",
                background: "var(--gradient-iris)",
                boxShadow: "var(--elevation-glow)",
                display: "flex",
                alignItems: "center",
                justifyContent: "center",
              }}
            >
              <Icon name="check" size={30} style={{ color: "var(--text-on-accent)" }} />
            </div>
            <h2 style={{ fontWeight: 700, fontSize: 22, color: "var(--text-primary)", margin: "0 0 8px" }}>Ready to deploy</h2>
            <p style={{ fontSize: 14, color: "var(--text-secondary)", margin: "0 auto 6px", maxWidth: 460 }}>
              <span style={{ fontFamily: mono, color: "var(--text-primary)" }}>{name.trim() || "new-server"}</span>
              {" · "}
              {spec?.name ?? ""}
              {placedNode ? ` · ${placedNode.name}` : ""}
            </p>
            <p style={{ fontSize: 12.5, color: "var(--text-muted)", margin: 0 }}>The installer will stream to the console.</p>
          </div>
        )}
      </Card>

      <div style={{ display: "flex", justifyContent: "space-between", marginTop: 20 }}>
        <Button variant="ghost" onClick={back} disabled={busy}>
          {step === 0 ? "Cancel" : "Back"}
        </Button>
        {step < STEPS.length - 1 ? (
          <Button variant="primary" disabled={!canNext} onClick={next}>
            Continue
          </Button>
        ) : (
          <Button variant="primary" icon="installing" disabled={busy} onClick={deploy}>
            {busy ? "Deploying…" : "Deploy server"}
          </Button>
        )}
      </div>
    </div>
  );
}
