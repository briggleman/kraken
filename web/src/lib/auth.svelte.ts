// Session state — the Svelte port of the old AuthProvider. Token lives in
// localStorage (api/client.ts owns it); this module owns who is signed in,
// their role, and the must-change-password gate.

import { api, clearToken, getToken } from "@/api/client";
import type { Role, User } from "@/api/types";

export const auth = $state({
  user: null as User | null,
  role: null as Role | null,
  loading: true,
});

export const mustChangePassword = () => !!auth.user?.must_change_password;

// permits checks a permission against a role's grant list, honoring the "*"
// superuser wildcard and "<domain>.*" domain wildcards.
export function hasPerm(perm: string): boolean {
  const role = auth.role;
  if (!role) return false;
  for (const p of role.permissions ?? []) {
    if (p === "*" || p === perm) return true;
    if (p.endsWith(".*") && perm.startsWith(p.slice(0, -1))) return true;
  }
  return false;
}

export async function refreshMe(): Promise<void> {
  const r = (await api.me()) as { user: User; role?: Role };
  auth.user = r.user;
  auth.role = r.role ?? null;
}

/** Resolve the stored token into a session at boot; loading stays true until
 *  we know either way. */
export async function bootAuth(): Promise<void> {
  if (!getToken()) {
    auth.loading = false;
    return;
  }
  try {
    await refreshMe();
  } catch {
    clearToken();
  } finally {
    auth.loading = false;
  }
}

export async function login(username: string, password: string): Promise<void> {
  await api.login(username, password);
  await refreshMe(); // pull the role + permissions
}

export async function logout(): Promise<void> {
  try {
    await api.logout();
  } finally {
    auth.user = null;
    auth.role = null;
  }
}
