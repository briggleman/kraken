import { useEffect, useState } from "react";
import { Page } from "@/components/Shell";
import { useAuth } from "@/auth";
import { useDialog } from "@/components/Dialog";
import { api } from "@/api/client";
import type { AdminUser, Role } from "@/api/types";
import { Button } from "@ds/components/core/Button";
import { Card } from "@ds/components/core/Card";
import { Input } from "@ds/components/core/Input";
import { Toggle } from "@ds/components/core/Toggle";
import { Icon } from "@ds/components/core/Icon";
import { IconButton } from "@ds/components/core/IconButton";
import { Select } from "@ds/components/core/Select";

const mono = "var(--font-mono)";
const GRID = "1.3fr 1.7fr 1.1fr 96px 240px";

const eyebrow: React.CSSProperties = {
  fontFamily: mono, fontSize: 12, letterSpacing: 3, color: "var(--accent)",
  marginBottom: 12, textTransform: "uppercase",
};
const h1: React.CSSProperties = {
  fontFamily: "var(--font-display)", fontWeight: 800, fontSize: 34, margin: 0,
  color: "var(--text-primary)", letterSpacing: "-0.01em",
};
const colLabel: React.CSSProperties = {
  fontFamily: mono, fontSize: 11, letterSpacing: 1.5, color: "var(--text-faint)",
  textTransform: "uppercase",
};
export function Users() {
  const { user: me } = useAuth();
  const { confirm, prompt } = useDialog();
  const [users, setUsers] = useState<AdminUser[]>([]);
  const [roles, setRoles] = useState<Role[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const refresh = () => {
    Promise.all([api.listUsers(), api.listRoles()])
      .then(([u, r]) => { setUsers(u.users ?? []); setRoles(r.roles ?? []); })
      .catch((e) => setError(e instanceof Error ? e.message : "failed to load"));
  };
  useEffect(refresh, []);

  const act = async (fn: () => Promise<unknown>) => {
    setError(null);
    try { await fn(); refresh(); } catch (e) { setError(e instanceof Error ? e.message : "action failed"); }
  };

  return (
    <Page>
      <div style={{ display: "flex", alignItems: "flex-end", justifyContent: "space-between", marginBottom: 28 }}>
        <div>
          <div style={eyebrow}>// ADMIN · USERS</div>
          <h1 style={h1}>Users</h1>
        </div>
        <Button icon="plus" onClick={() => setCreating(true)}>Create</Button>
      </div>

      {error && (
        <div style={{ display: "flex", alignItems: "center", gap: 8, color: "var(--status-crashed)", fontFamily: mono, fontSize: 13, marginBottom: 16 }}>
          <Icon name="crashed" size={15} />
          {error}
        </div>
      )}

      <Card padding={0}>
        <div style={{ display: "grid", gridTemplateColumns: GRID, padding: "14px 20px", borderBottom: "1px solid var(--border-subtle)", ...colLabel }}>
          <span>Username</span><span>Email</span><span>Role</span><span>Status</span><span style={{ textAlign: "right" }}>Actions</span>
        </div>
        {users.map((u) => {
          const self = me?.id === u.id;
          return (
            <div key={u.id} style={{ display: "grid", gridTemplateColumns: GRID, alignItems: "center", padding: "14px 20px", borderBottom: "1px solid var(--border-subtle)", fontSize: 13.5 }}>
              <span style={{ fontFamily: mono, color: "var(--text-primary)", fontWeight: 600 }}>
                {u.username}
                {self && <span style={{ color: "var(--text-faint)", fontWeight: 400 }}> (you)</span>}
              </span>
              <span style={{ fontFamily: mono, color: "var(--text-muted)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{u.email || "—"}</span>
              <span>
                <Select size="sm" value={u.role_id} options={roles.map((r) => ({ value: r.id, label: r.name }))} onChange={(v) => act(() => api.updateUser(u.id, { role_id: v }))} />
              </span>
              <span style={{ display: "flex", alignItems: "center", gap: 8 }}>
                <Toggle
                  checked={!u.disabled}
                  disabled={self}
                  onChange={() => act(() => api.updateUser(u.id, { disabled: !u.disabled }))}
                />
                <span style={{ fontFamily: mono, fontSize: 11, letterSpacing: 0.5, color: u.disabled ? "var(--text-faint)" : "var(--accent)" }}>
                  {u.disabled ? "disabled" : "active"}
                </span>
              </span>
              <span style={{ display: "flex", justifyContent: "flex-end", gap: 8 }}>
                <Button size="sm" variant="ghost" icon="refresh" onClick={async () => {
                  const pw = await prompt({ title: "Reset password", message: `New password for ${u.username}:`, placeholder: "new password" });
                  if (pw) act(() => api.resetUserPassword(u.id, pw));
                }}>Reset</Button>
                {!self && (
                  <IconButton
                    icon="x"
                    size="sm"
                    variant="ghost"
                    title={`Delete user ${u.username}`}
                    onClick={async () => { if (await confirm({ title: "Delete user", message: `Delete user ${u.username}?`, confirmLabel: "Delete", danger: true })) act(() => api.deleteUser(u.id)); }}
                  />
                )}
              </span>
            </div>
          );
        })}
        {users.length === 0 && (
          <div style={{ padding: "40px 22px", textAlign: "center", fontFamily: mono, color: "var(--text-muted)" }}>
            <div style={{ fontSize: 30, marginBottom: 10 }}>🐙</div>
            No users.
          </div>
        )}
      </Card>

      {creating && (
        <CreateUserModal
          roles={roles}
          onClose={() => setCreating(false)}
          onSubmit={async (input) => { await act(() => api.createUser(input)); setCreating(false); }}
        />
      )}
    </Page>
  );
}

function CreateUserModal(props: {
  roles: Role[];
  onClose: () => void;
  onSubmit: (input: { username: string; email: string; password: string; role_id: string }) => void;
}) {
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [roleId, setRoleId] = useState(props.roles.find((r) => r.id === "operator")?.id ?? props.roles[0]?.id ?? "");

  return (
    <div onClick={props.onClose} style={{ position: "fixed", inset: 0, zIndex: 100, background: "rgba(1,9,14,.78)", display: "flex", alignItems: "center", justifyContent: "center", padding: 20 }}>
      <div onClick={(e) => e.stopPropagation()} style={{ width: "100%", maxWidth: 460 }}>
        <Card glow padding={26} style={{ boxShadow: "var(--elevation-e3)" }}>
          <div style={{ ...eyebrow, marginBottom: 8 }}>// ADMIN · NEW USER</div>
          <h2 style={{ fontFamily: "var(--font-display)", fontWeight: 800, fontSize: 22, margin: "0 0 20px", color: "var(--text-primary)" }}>Create user</h2>

          <div style={{ marginBottom: 14 }}>
            <Input label="Username" mono value={username} onChange={(e) => setUsername(e.target.value)} placeholder="captain" />
          </div>
          <div style={{ marginBottom: 14 }}>
            <Input label="Email" mono value={email} onChange={(e) => setEmail(e.target.value)} placeholder="captain@kraken.sea" />
          </div>
          <div style={{ marginBottom: 14 }}>
            <Input label="Password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
          </div>
          <div style={{ marginBottom: 8 }}>
            <Select label="Role" value={roleId} options={props.roles.map((r) => ({ value: r.id, label: r.name }))} onChange={setRoleId} />
          </div>

          <div style={{ display: "flex", gap: 10, justifyContent: "flex-end", marginTop: 22 }}>
            <Button variant="ghost" onClick={props.onClose}>Cancel</Button>
            <Button disabled={!username || !password || !roleId} onClick={() => props.onSubmit({ username, email, password, role_id: roleId })}>Create</Button>
          </div>
        </Card>
      </div>
    </div>
  );
}
