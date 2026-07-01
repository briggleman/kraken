import { Navigate, Route, Routes } from "react-router-dom";
import { AuthProvider, useAuth } from "@/auth";
import { DialogProvider } from "@/components/Dialog";
import { Toaster } from "@ds/components/core/Toast";
import { Shell } from "@/components/Shell";
import { Login } from "@/pages/Login";
import { Fleet } from "@/pages/Fleet";
import { ServerDetail } from "@/pages/ServerDetail";
import { Specs } from "@/pages/Specs";
import { SpecEditor } from "@/pages/SpecEditor";
import { Nodes } from "@/pages/Nodes";
import { Users } from "@/pages/Users";
import { Roles } from "@/pages/Roles";
import { Audit } from "@/pages/Audit";
import { AdminSettings } from "@/pages/AdminSettings";
import { ApiDocs } from "@/pages/ApiDocs";
import { ChangePassword } from "@/pages/ChangePassword";
import { Setup } from "@/pages/Setup";
import { Catalog } from "@/pages/Catalog";
import type { ReactNode } from "react";

function Surfacing() {
  return (
    <div style={{ minHeight: "100vh", display: "grid", placeItems: "center", background: "var(--bg-abyss)", color: "var(--text-muted)", fontFamily: "var(--font-mono)" }}>
      surfacing…
    </div>
  );
}

function RequireAuth({ children }: { children: ReactNode }) {
  const { user, loading, mustChangePassword } = useAuth();
  if (loading) return <Surfacing />;
  if (!user) return <Navigate to="/login" replace />;
  // First-run security gate: until the bootstrap password is rotated, the only
  // place to go is the change-password screen.
  if (mustChangePassword) return <Navigate to="/change-password" replace />;
  return <>{children}</>;
}

// RequirePasswordChange guards the change-password screen: it requires a session
// but is only reachable while a rotation is actually pending.
function RequirePasswordChange({ children }: { children: ReactNode }) {
  const { user, loading, mustChangePassword } = useAuth();
  if (loading) return <Surfacing />;
  if (!user) return <Navigate to="/login" replace />;
  if (!mustChangePassword) return <Navigate to="/" replace />;
  return <>{children}</>;
}

export function App() {
  return (
    <AuthProvider>
      <Toaster position="bottom-right" />
      <DialogProvider>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route
          path="/change-password"
          element={
            <RequirePasswordChange>
              <ChangePassword />
            </RequirePasswordChange>
          }
        />
        <Route
          element={
            <RequireAuth>
              <Shell />
            </RequireAuth>
          }
        >
          <Route index element={<Fleet />} />
          <Route path="/setup" element={<Setup />} />
          <Route path="/servers/:id" element={<ServerDetail />} />
          <Route path="/specs" element={<Specs />} />
          <Route path="/catalog" element={<Catalog />} />
          <Route path="/specs/new" element={<SpecEditor />} />
          <Route path="/specs/:id" element={<SpecEditor />} />
          <Route path="/nodes" element={<Nodes />} />
          <Route path="/admin/users" element={<Users />} />
          <Route path="/admin/roles" element={<Roles />} />
          <Route path="/admin/audit" element={<Audit />} />
          <Route path="/admin/settings" element={<AdminSettings />} />
          <Route path="/docs" element={<ApiDocs />} />
        </Route>
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
      </DialogProvider>
    </AuthProvider>
  );
}
