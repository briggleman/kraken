import { useEffect, useState } from "react";
import { api } from "@/api/client";
import type { SftpStatus } from "@/api/types";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { Icon } from "@ds/components/core/Icon";

const mono = "var(--font-mono)";
const label: React.CSSProperties = { fontFamily: mono, fontSize: 11, letterSpacing: "1.5px", color: "var(--text-faint)" };
const row: React.CSSProperties = { display: "flex", alignItems: "center", gap: 10, marginTop: 12, flexWrap: "wrap" };

// SftpCard manages a server's SFTP access from the Files tab: connection details,
// password reset (shown once), and authorized public keys.
export function SftpCard({ id }: { id: string }) {
  const [st, setSt] = useState<SftpStatus | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [revealed, setRevealed] = useState<string | null>(null); // one-time password
  const [keysText, setKeysText] = useState("");

  const apply = (s: SftpStatus) => { setSt(s); setKeysText((s.keys || []).join("\n")); };
  useEffect(() => {
    api.getServerSftp(id).then(apply).catch((e) => setErr(e instanceof Error ? e.message : "failed to load SFTP"));
  }, [id]);

  const run = async (fn: () => Promise<void>) => {
    setBusy(true); setErr(null);
    try { await fn(); } catch (e) { setErr(e instanceof Error ? e.message : "SFTP request failed"); } finally { setBusy(false); }
  };
  const resetPassword = () => run(async () => { const r = await api.resetServerSftpPassword(id); setRevealed(r.password); apply(r.status); });
  const saveKeys = () => run(async () => {
    const keys = keysText.split("\n").map((k) => k.trim()).filter(Boolean);
    apply(await api.setServerSftpKeys(id, keys));
  });
  const disable = () => run(async () => { setRevealed(null); apply(await api.disableServerSftp(id)); });

  if (!st) return null;

  const connectCmd = st.host && st.port ? `sftp -P ${st.port} ${st.username}@${st.host}` : "connect once the node reports its SFTP port";
  const copy = (text: string) => navigator.clipboard?.writeText(text);

  return (
    <Card padding={18} style={{ marginBottom: 16 }}>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12 }}>
        <div style={label}>SFTP ACCESS</div>
        {st.enabled ? (
          <span style={{ display: "inline-flex", alignItems: "center", gap: 6, color: "var(--status-running)", fontFamily: mono, fontSize: 11.5 }}>
            <Icon name="running" size={14} /> Enabled
          </span>
        ) : (
          <span style={{ fontFamily: mono, fontSize: 11.5, color: "var(--text-faint)" }}>OFF</span>
        )}
      </div>

      {!st.enabled ? (
        <div style={{ marginTop: 10 }}>
          <p style={{ fontSize: 13, color: "var(--text-muted)", margin: "0 0 14px" }}>
            Direct file access over SFTP (WinSCP, FileZilla, <code>sftp</code>), jailed to this server's data dir. Generate a password to turn it on.
          </p>
          <Button variant="primary" icon="lock" disabled={busy} onClick={resetPassword}>Enable SFTP</Button>
        </div>
      ) : (
        <>
          <div style={{ ...row, marginTop: 14 }}>
            <span style={{ fontFamily: mono, fontSize: 13, color: "var(--accent)", wordBreak: "break-all" }}>{connectCmd}</span>
            {st.host && st.port && (
              <span onClick={() => copy(connectCmd)} style={{ display: "inline-flex", alignItems: "center", gap: 5, cursor: "pointer", color: "var(--accent)", fontSize: 12 }}>
                <Icon name="copy" size={12} /> copy
              </span>
            )}
          </div>
          <div style={{ fontSize: 12, color: "var(--text-muted)", marginTop: 4 }}>Username <code>{st.username}</code> — chrooted to <code>/data</code>.</div>

          {revealed && (
            <div style={{ marginTop: 14, padding: "11px 14px", borderRadius: "var(--radius-md)", border: "1px solid var(--border-strong)", background: "var(--accent-wash-12)" }}>
              <div style={{ fontSize: 12, color: "var(--text-secondary)", marginBottom: 6 }}>New password — copy it now, it won't be shown again:</div>
              <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
                <code style={{ fontFamily: mono, fontSize: 14, color: "var(--text-primary)", wordBreak: "break-all" }}>{revealed}</code>
                <span onClick={() => copy(revealed)} style={{ display: "inline-flex", alignItems: "center", gap: 5, cursor: "pointer", color: "var(--accent)", fontSize: 12 }}>
                  <Icon name="copy" size={12} /> copy
                </span>
              </div>
            </div>
          )}

          <div style={row}>
            <span style={{ fontSize: 13, color: "var(--text-secondary)" }}>Password: {st.has_password ? "set" : "none"}</span>
            <Button variant="secondary" size="sm" icon="refresh" disabled={busy} onClick={resetPassword}>Reset password</Button>
          </div>

          <div style={{ marginTop: 16 }}>
            <div style={label}>AUTHORIZED KEYS (one per line)</div>
            <textarea
              value={keysText}
              onChange={(e) => setKeysText(e.target.value)}
              placeholder="ssh-ed25519 AAAA… user@host"
              spellCheck={false}
              style={{ width: "100%", minHeight: 70, marginTop: 8, resize: "vertical", fontFamily: mono, fontSize: 12, color: "var(--text-primary)", background: "var(--bg-inset)", border: "1px solid var(--border-subtle)", borderRadius: "var(--radius-md)", padding: 10 }}
            />
            <div style={{ display: "flex", gap: 10, marginTop: 10 }}>
              <Button variant="secondary" size="sm" icon="check" disabled={busy} onClick={saveKeys}>Save keys</Button>
              <Button variant="danger" size="sm" icon="x" disabled={busy} onClick={disable}>Disable SFTP</Button>
            </div>
          </div>
        </>
      )}
      {err && <div style={{ marginTop: 12, color: "var(--status-crashed)", fontFamily: mono, fontSize: 12.5 }}>{err}</div>}
    </Card>
  );
}
