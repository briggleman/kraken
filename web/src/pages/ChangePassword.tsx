import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "@/api/client";
import { useAuth } from "@/auth";
import { AmbientBackground } from "@/components/AmbientBackground";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { Input } from "@ds/components/core/Input";
import { Icon } from "@ds/components/core/Icon";

const MIN_LEN = 8;

// scorePassword grades a password 0–4 from length + character-class variety.
function scorePassword(pw: string): { score: number; label: string } {
  if (!pw) return { score: 0, label: "" };
  let s = 0;
  if (pw.length >= 8) s++;
  if (pw.length >= 12) s++;
  if (/[a-z]/.test(pw) && /[A-Z]/.test(pw)) s++;
  if (/\d/.test(pw)) s++;
  if (/[^A-Za-z0-9]/.test(pw)) s++;
  const score = Math.min(4, s);
  return { score, label: ["Too weak", "Weak", "Fair", "Good", "Strong"][score] };
}

// Strength bar colors (red → amber → teal → green) keyed to the score.
const STRENGTH_COLORS = ["var(--status-crashed)", "var(--status-crashed)", "var(--status-starting)", "var(--accent)", "var(--status-running)"];

function StrengthBar({ password }: { password: string }) {
  const { score, label } = scorePassword(password);
  const color = STRENGTH_COLORS[score];
  return (
    <div style={{ margin: "0 0 var(--sp-4)" }}>
      <div style={{ display: "flex", gap: 5 }}>
        {[0, 1, 2, 3].map((i) => (
          <span
            key={i}
            style={{
              flex: 1,
              height: 4,
              borderRadius: 2,
              background: password && i < score ? color : "var(--border-subtle)",
              transition: "background var(--duration-fast) var(--ease-out)",
            }}
          />
        ))}
      </div>
      {password && (
        <div style={{ marginTop: 6, fontFamily: "var(--font-mono)", fontSize: 11, letterSpacing: "0.5px", color }}>{label}</div>
      )}
    </div>
  );
}

// ChangePassword is the forced first-run password rotation. The bootstrap admin
// lands here before reaching anything else; on success the session is rotated and
// the operator continues into the setup wizard.
export function ChangePassword() {
  const { refresh } = useAuth();
  const navigate = useNavigate();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [focused, setFocused] = useState<string | null>(null);

  const tooShort = next.length > 0 && next.length < MIN_LEN;
  const mismatch = confirm.length > 0 && confirm !== next;
  const canSubmit = current.length > 0 && next.length >= MIN_LEN && next === confirm && !busy;

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!canSubmit) return;
    setBusy(true);
    setError(null);
    try {
      await api.changePassword(current, next);
      await refresh(); // clears must_change_password from auth state
      navigate("/setup");
    } catch (err) {
      setError(err instanceof Error ? err.message : "could not change password");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      style={{
        position: "relative",
        minHeight: "100vh",
        overflow: "hidden",
        background: "var(--bg-abyss)",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        padding: "var(--sp-8)",
      }}
    >
      <AmbientBackground atmosphere="heavy" />

      <div style={{ position: "relative", zIndex: 10, width: "100%", maxWidth: 408 }}>
        <div style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: "var(--sp-4)", marginBottom: "var(--sp-7)" }}>
          <img
            src="/kraken-glyph-teal.png"
            alt="Kraken"
            style={{ width: 84, height: 84, objectFit: "contain", filter: "drop-shadow(0 0 18px rgba(61,245,207,.5))" }}
          />
          <div
            style={{
              fontFamily: "var(--font-display)",
              fontWeight: 800,
              letterSpacing: 4,
              fontSize: 26,
              color: "var(--text-primary)",
              textShadow: "0 0 32px var(--accent-wash-16)",
            }}
          >
            KRAKEN
          </div>
        </div>

        <Card padding="var(--sp-7)" style={{ border: "none", background: "transparent" }}>
          <form onSubmit={submit}>
            <div style={{ fontFamily: "var(--font-mono)", fontSize: 11.5, letterSpacing: "1.5px", color: "var(--accent)", marginBottom: "var(--sp-3)" }}>
              // SECURE YOUR ACCOUNT
            </div>
            <h1 style={{ fontFamily: "var(--font-sans)", fontWeight: 700, fontSize: 26, margin: "0 0 var(--sp-2)", color: "var(--text-primary)", letterSpacing: "-0.4px" }}>
              Set a new password
            </h1>
            <p style={{ margin: "0 0 var(--sp-6)", fontSize: 14, color: "var(--text-secondary)", lineHeight: 1.55 }}>
              You're signed in with a temporary password. Choose a new one to continue.
            </p>

            <Input
              label="CURRENT PASSWORD"
              type="password"
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
              onFocus={() => setFocused("current")}
              onBlur={() => setFocused(null)}
              focused={focused === "current"}
              autoComplete="current-password"
              style={{ marginBottom: "var(--sp-4)" }}
            />
            <Input
              label="NEW PASSWORD"
              type="password"
              value={next}
              onChange={(e) => setNext(e.target.value)}
              onFocus={() => setFocused("next")}
              onBlur={() => setFocused(null)}
              focused={focused === "next"}
              error={tooShort}
              helper={tooShort ? `At least ${MIN_LEN} characters.` : undefined}
              autoComplete="new-password"
              style={{ marginBottom: "var(--sp-4)" }}
            />
            <Input
              label="CONFIRM NEW PASSWORD"
              type="password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              onFocus={() => setFocused("confirm")}
              onBlur={() => setFocused(null)}
              focused={focused === "confirm"}
              error={mismatch}
              helper={mismatch ? "Passwords don't match." : undefined}
              autoComplete="new-password"
              style={{ marginBottom: "var(--sp-3)" }}
            />

            <StrengthBar password={next} />

            {error && (
              <div
                style={{
                  display: "flex",
                  alignItems: "center",
                  gap: "var(--sp-2)",
                  margin: "0 0 var(--sp-4)",
                  color: "var(--status-crashed)",
                  fontSize: 12.5,
                  fontFamily: "var(--font-mono)",
                  lineHeight: 1.5,
                }}
              >
                <Icon name="info" size={14} />
                {error}
              </div>
            )}

            <Button
              type="submit"
              variant="primary"
              disabled={!canSubmit}
              icon={<Icon name="lock" size={14} />}
              style={{ width: "100%", marginTop: "var(--sp-2)" }}
            >
              {busy ? "Saving…" : "Set password & continue"}
            </Button>
          </form>
        </Card>
      </div>
    </div>
  );
}
