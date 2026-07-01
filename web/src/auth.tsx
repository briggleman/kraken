import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { api, clearToken, getToken } from "@/api/client";
import type { Role, User } from "@/api/types";

interface AuthState {
  user: User | null;
  role: Role | null;
  loading: boolean;
  mustChangePassword: boolean;
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  refresh: () => Promise<void>;
  hasPerm: (perm: string) => boolean;
}

const AuthContext = createContext<AuthState | null>(null);

// permits checks a permission against a role's grant list, honoring the "*"
// superuser wildcard and "<domain>.*" domain wildcards.
function permits(role: Role | null, perm: string): boolean {
  if (!role) return false;
  for (const p of role.permissions ?? []) {
    if (p === "*" || p === perm) return true;
    if (p.endsWith(".*") && perm.startsWith(p.slice(0, -1))) return true;
  }
  return false;
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [role, setRole] = useState<Role | null>(null);
  const [loading, setLoading] = useState(true);

  const refreshMe = async () => {
    const r = (await api.me()) as { user: User; role?: Role };
    setUser(r.user);
    setRole(r.role ?? null);
  };

  useEffect(() => {
    let active = true;
    if (!getToken()) {
      setLoading(false);
      return;
    }
    refreshMe()
      .catch(() => clearToken())
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, []);

  const login = async (username: string, password: string) => {
    await api.login(username, password);
    await refreshMe(); // pull the role + permissions
  };

  const logout = async () => {
    await api.logout();
    setUser(null);
    setRole(null);
  };

  return (
    <AuthContext.Provider
      value={{
        user,
        role,
        loading,
        mustChangePassword: !!user?.must_change_password,
        login,
        logout,
        refresh: refreshMe,
        hasPerm: (p) => permits(role, p),
      }}
    >
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
