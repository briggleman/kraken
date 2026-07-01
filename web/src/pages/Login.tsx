import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useAuth } from "@/auth";
import { AmbientBackground } from "@/components/AmbientBackground";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { Input } from "@ds/components/core/Input";
import { Icon } from "@ds/components/core/Icon";

export function Login() {
  const { login } = useAuth();
  const navigate = useNavigate();
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [focused, setFocused] = useState<"username" | "password" | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await login(username, password);
      navigate("/");
    } catch (err) {
      setError(err instanceof Error ? err.message : "sign in failed");
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
        {/* brand mark */}
        <div
          style={{
            display: "flex",
            flexDirection: "column",
            alignItems: "center",
            gap: "var(--sp-4)",
            marginBottom: "var(--sp-7)",
          }}
        >
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
            <div
              style={{
                fontFamily: "var(--font-mono)",
                fontSize: 11.5,
                letterSpacing: "1.5px",
                color: "var(--accent)",
                marginBottom: "var(--sp-3)",
              }}
            >
              // CONTROL PANEL
            </div>
            <h1
              style={{
                fontFamily: "var(--font-sans)",
                fontWeight: 700,
                fontSize: 26,
                margin: "0 0 var(--sp-2)",
                color: "var(--text-primary)",
                letterSpacing: "-0.4px",
              }}
            >
              Descend to your fleet
            </h1>
            <p
              style={{
                margin: "0 0 var(--sp-6)",
                fontSize: 14,
                color: "var(--text-secondary)",
                lineHeight: 1.55,
              }}
            >
              Sign in to reach your servers in the deep.
            </p>

            <Input
              label="USERNAME"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              onFocus={() => setFocused("username")}
              onBlur={() => setFocused(null)}
              focused={focused === "username"}
              mono
              autoComplete="username"
              style={{ marginBottom: "var(--sp-4)" }}
            />
            <Input
              label="PASSWORD"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              onFocus={() => setFocused("password")}
              onBlur={() => setFocused(null)}
              focused={focused === "password"}
              autoComplete="current-password"
              style={{ marginBottom: "var(--sp-4)" }}
            />

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
              disabled={busy}
              icon={<Icon name="lock" size={14} />}
              style={{ width: "100%", marginTop: "var(--sp-2)" }}
            >
              {busy ? "Signing in…" : "Sign in"}
            </Button>
          </form>
        </Card>

        <div
          style={{
            marginTop: "var(--sp-5)",
            textAlign: "center",
            fontFamily: "var(--font-mono)",
            fontSize: 11,
            letterSpacing: "1.5px",
            color: "var(--text-faint)",
          }}
        >
          SELF-HOSTED GAME SERVER CONTROL
        </div>
      </div>
    </div>
  );
}
