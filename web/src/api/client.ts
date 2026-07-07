import type {
  AdminUser,
  AuditEntry,
  Backup,
  BootstrapToken,
  CatalogItem,
  DatabaseConfig,
  DatabaseInput,
  PanelSettings,
  PortForward,
  ServerDnsState,
  ServerDNS,
  FileContent,
  FileListing,
  LoginResponse,
  Node,
  NodeConfig,
  NodeConfigUpdate,
  NodeConfigUpdateResult,
  PowerActionName,
  Role,
  ScheduleAction,
  ScheduledTask,
  Server,
  ServerSettings,
  SftpStatus,
  SetupStatus,
  Spec,
  UpdateSettingsResult,
  User,
} from "./types";

/** Payload for creating/updating a scheduled task. */
export interface ScheduleInput {
  name: string;
  action: ScheduleAction;
  cron: string;
  command?: string;
  enabled: boolean;
}

const TOKEN_KEY = "kraken_token";

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY);
}
export function setToken(token: string) {
  localStorage.setItem(TOKEN_KEY, token);
}
export function clearToken() {
  localStorage.removeItem(TOKEN_KEY);
}

/** Raised on a non-2xx response; carries the HTTP status and optional machine code. */
export class ApiError extends Error {
  status: number;
  code?: string;
  constructor(status: number, message: string, code?: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  const token = getToken();
  if (token) headers["Authorization"] = `Bearer ${token}`;
  if (body !== undefined) headers["Content-Type"] = "application/json";

  const res = await fetch(`/api/v1${path}`, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });

  if (res.status === 401) {
    clearToken();
  }
  if (!res.ok) {
    let msg = res.statusText;
    let code: string | undefined;
    try {
      const data = await res.json();
      if (data?.error) msg = data.error;
      if (data?.code) code = data.code;
    } catch {
      /* ignore parse errors */
    }
    throw new ApiError(res.status, msg, code);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

// requestRaw sends a raw (non-JSON) body — used for YAML/JSON spec uploads.
async function requestRaw<T>(method: string, path: string, body: string): Promise<T> {
  const headers: Record<string, string> = { "Content-Type": "application/yaml" };
  const token = getToken();
  if (token) headers["Authorization"] = `Bearer ${token}`;
  const res = await fetch(`/api/v1${path}`, { method, headers, body });
  if (res.status === 401) clearToken();
  if (!res.ok) {
    let msg = res.statusText;
    try {
      const data = await res.json();
      if (data?.error) msg = data.error;
    } catch {
      /* ignore */
    }
    throw new ApiError(res.status, msg);
  }
  return (await res.json()) as T;
}

export const api = {
  async login(username: string, password: string): Promise<LoginResponse> {
    const resp = await request<LoginResponse>("POST", "/auth/login", { username, password });
    setToken(resp.token);
    return resp;
  },
  async logout(): Promise<void> {
    try {
      await request<void>("POST", "/auth/logout");
    } finally {
      clearToken();
    }
  },
  me(): Promise<{ user: User; role?: unknown }> {
    return request("GET", "/auth/me");
  },
  async changePassword(currentPassword: string, newPassword: string): Promise<LoginResponse> {
    const resp = await request<LoginResponse>("POST", "/auth/change-password", {
      current_password: currentPassword,
      new_password: newPassword,
    });
    setToken(resp.token); // the server rotates the session; adopt the new token
    return resp;
  },

  // --- first-run setup & catalog ---
  setupStatus(): Promise<SetupStatus> {
    return request("GET", "/setup/status");
  },
  getDatabaseConfig(): Promise<DatabaseConfig> {
    return request("GET", "/setup/database");
  },
  testDatabase(input: DatabaseInput): Promise<{ ok: boolean; db_exists: boolean; can_create_db: boolean }> {
    return request("POST", "/setup/database/test", input);
  },
  connectDatabase(input: DatabaseInput): Promise<{ restarting: boolean }> {
    return request("POST", "/setup/database", input);
  },
  listCatalog(): Promise<{ catalog: CatalogItem[] | null }> {
    return request("GET", "/catalog");
  },
  importCatalog(id: string): Promise<Spec> {
    return request("POST", `/catalog/${id}/import`);
  },
  createBootstrapToken(input: { node_name: string; ttl_seconds?: number }): Promise<BootstrapToken> {
    return request("POST", "/agents/bootstrap-tokens", input);
  },

  listServers(): Promise<{ servers: Server[] | null }> {
    return request("GET", "/servers");
  },
  getServer(id: string): Promise<Server> {
    return request("GET", `/servers/${id}`);
  },
  createServer(input: { spec_id: string; name: string; variables?: Record<string, string>; steam_guard_code?: string; install_bepinex?: boolean }): Promise<Server> {
    return request("POST", "/servers", input);
  },
  powerServer(id: string, action: PowerActionName): Promise<{ state: string }> {
    return request("POST", `/servers/${id}/power`, { action });
  },
  deleteServer(id: string): Promise<void> {
    return request("DELETE", `/servers/${id}`);
  },

  getServerSftp(id: string): Promise<SftpStatus> {
    return request("GET", `/servers/${id}/sftp`);
  },
  resetServerSftpPassword(id: string): Promise<{ password: string; status: SftpStatus }> {
    return request("POST", `/servers/${id}/sftp/password`);
  },
  setServerSftpKeys(id: string, keys: string[]): Promise<SftpStatus> {
    return request("PUT", `/servers/${id}/sftp/keys`, { keys });
  },
  disableServerSftp(id: string): Promise<SftpStatus> {
    return request("POST", `/servers/${id}/sftp/disable`);
  },
  getServerSettings(id: string): Promise<ServerSettings> {
    return request("GET", `/servers/${id}/settings`);
  },
  listFiles(id: string, path: string): Promise<FileListing> {
    return request("GET", `/servers/${id}/files?path=${encodeURIComponent(path)}`);
  },
  readFile(id: string, path: string): Promise<FileContent> {
    return request("GET", `/servers/${id}/files/content?path=${encodeURIComponent(path)}`);
  },
  mkdir(id: string, path: string): Promise<{ status: string }> {
    return request("POST", `/servers/${id}/files/mkdir`, { path });
  },
  writeFile(id: string, path: string, content: string): Promise<{ status: string }> {
    return request("POST", `/servers/${id}/files/write`, { path, content });
  },
  deleteFiles(id: string, paths: string[]): Promise<{ status: string }> {
    return request("POST", `/servers/${id}/files/delete`, { paths });
  },
  moveFile(id: string, src: string, dst: string): Promise<{ status: string }> {
    return request("POST", `/servers/${id}/files/move`, { src, dst });
  },
  copyFile(id: string, src: string, dst: string): Promise<{ status: string }> {
    return request("POST", `/servers/${id}/files/copy`, { src, dst });
  },
  async uploadFiles(id: string, dir: string, files: File[]): Promise<void> {
    const fd = new FormData();
    for (const f of files) fd.append("files", f);
    const headers: Record<string, string> = {};
    const token = getToken();
    if (token) headers["Authorization"] = `Bearer ${token}`;
    const res = await fetch(`/api/v1/servers/${id}/files/upload?path=${encodeURIComponent(dir)}`, {
      method: "POST",
      headers, // browser sets multipart Content-Type with boundary
      body: fd,
    });
    if (!res.ok) {
      let msg = res.statusText;
      try {
        const d = await res.json();
        if (d?.error) msg = d.error;
      } catch {
        /* ignore */
      }
      throw new ApiError(res.status, msg);
    }
  },
  async downloadFileRaw(id: string, path: string): Promise<Blob> {
    const headers: Record<string, string> = {};
    const token = getToken();
    if (token) headers["Authorization"] = `Bearer ${token}`;
    const res = await fetch(`/api/v1/servers/${id}/files/raw?path=${encodeURIComponent(path)}`, { headers });
    if (!res.ok) {
      let msg = res.statusText;
      try {
        const d = await res.json();
        if (d?.error) msg = d.error;
      } catch {
        /* ignore */
      }
      throw new ApiError(res.status, msg);
    }
    return res.blob();
  },
  async downloadFilesZip(id: string, paths: string[]): Promise<Blob> {
    const headers: Record<string, string> = { "Content-Type": "application/json" };
    const token = getToken();
    if (token) headers["Authorization"] = `Bearer ${token}`;
    const res = await fetch(`/api/v1/servers/${id}/files/download`, {
      method: "POST",
      headers,
      body: JSON.stringify({ paths }),
    });
    if (!res.ok) {
      let msg = res.statusText;
      try {
        const d = await res.json();
        if (d?.error) msg = d.error;
      } catch {
        /* ignore */
      }
      throw new ApiError(res.status, msg);
    }
    return res.blob();
  },
  updateServerSettings(id: string, values: Record<string, string>): Promise<UpdateSettingsResult> {
    return request("PUT", `/servers/${id}/settings`, { values });
  },

  listSpecs(): Promise<{ specs: Spec[] | null }> {
    return request("GET", "/specs");
  },
  getSpec(id: string): Promise<Spec> {
    return request("GET", `/specs/${id}`);
  },
  createSpec(spec: unknown): Promise<Spec> {
    return request("POST", "/specs", spec);
  },
  updateSpec(id: string, spec: unknown): Promise<Spec> {
    return request("PUT", `/specs/${id}`, spec);
  },
  // Raw-body variants: the Panel accepts YAML or JSON for specs, so the editor
  // can post the user's text verbatim in either format.
  createSpecRaw(body: string): Promise<Spec> {
    return requestRaw("POST", "/specs", body);
  },
  updateSpecRaw(id: string, body: string): Promise<Spec> {
    return requestRaw("PUT", `/specs/${id}`, body);
  },
  deleteSpec(id: string): Promise<void> {
    return request("DELETE", `/specs/${id}`);
  },

  // --- admin: users & roles ---
  listUsers(): Promise<{ users: AdminUser[] | null }> {
    return request("GET", "/users");
  },
  createUser(input: { username: string; email: string; password: string; role_id: string }): Promise<AdminUser> {
    return request("POST", "/users", input);
  },
  updateUser(id: string, patch: { email?: string; role_id?: string; disabled?: boolean }): Promise<AdminUser> {
    return request("PUT", `/users/${id}`, patch);
  },
  resetUserPassword(id: string, password: string): Promise<{ status: string }> {
    return request("POST", `/users/${id}/password`, { password });
  },
  deleteUser(id: string): Promise<void> {
    return request("DELETE", `/users/${id}`);
  },
  listRoles(): Promise<{ roles: Role[] | null }> {
    return request("GET", "/roles");
  },
  listAudit(): Promise<{ entries: AuditEntry[] | null }> {
    return request("GET", "/audit");
  },
  async fetchOpenAPISpec(): Promise<string> {
    const headers: Record<string, string> = {};
    const token = getToken();
    if (token) headers["Authorization"] = `Bearer ${token}`;
    const res = await fetch("/api/v1/openapi.yaml", { headers });
    if (res.status === 401) clearToken();
    if (!res.ok) throw new ApiError(res.status, res.statusText);
    return res.text();
  },

  // --- backups ---
  listBackups(id: string): Promise<{ backups: Backup[] | null }> {
    return request("GET", `/servers/${id}/backups`);
  },
  createBackup(id: string, name: string): Promise<Backup> {
    return request("POST", `/servers/${id}/backups`, { name });
  },
  restoreBackup(id: string, backupId: string): Promise<{ status: string }> {
    return request("POST", `/servers/${id}/backups/${backupId}/restore`);
  },
  deleteBackup(id: string, backupId: string): Promise<void> {
    return request("DELETE", `/servers/${id}/backups/${backupId}`);
  },

  // --- scheduled tasks ---
  listSchedules(id: string): Promise<{ schedules: ScheduledTask[] | null }> {
    return request("GET", `/servers/${id}/schedules`);
  },
  createSchedule(id: string, input: ScheduleInput): Promise<ScheduledTask> {
    return request("POST", `/servers/${id}/schedules`, input);
  },
  updateSchedule(id: string, scheduleId: string, input: ScheduleInput): Promise<ScheduledTask> {
    return request("PUT", `/servers/${id}/schedules/${scheduleId}`, input);
  },
  deleteSchedule(id: string, scheduleId: string): Promise<void> {
    return request("DELETE", `/servers/${id}/schedules/${scheduleId}`);
  },

  listNodes(): Promise<{ nodes: Node[] | null }> {
    return request("GET", "/nodes");
  },
  getNode(id: string): Promise<Node> {
    return request("GET", `/nodes/${id}`);
  },
  registerNode(input: {
    name: string;
    os: string;
    wine_enabled: boolean;
    address: string;
    public_host?: string;
    total_memory_mb: number;
    port_start: number;
    port_end: number;
  }): Promise<Node> {
    return request("POST", "/nodes", input);
  },
  nodeInfo(id: string): Promise<Record<string, unknown>> {
    return request("GET", `/nodes/${id}/info`);
  },
  deleteNode(id: string): Promise<void> {
    return request("DELETE", `/nodes/${id}`);
  },
  getNodeConfig(id: string): Promise<NodeConfig> {
    return request("GET", `/nodes/${id}/config`);
  },
  updateNodeConfig(id: string, input: NodeConfigUpdate): Promise<NodeConfigUpdateResult> {
    return request("PUT", `/nodes/${id}/config`, input);
  },

  // --- panel settings (admin) ---
  getPanelSettings(): Promise<PanelSettings> {
    return request("GET", "/settings");
  },
  updatePanelSettings(input: {
    cloudflare_api_token?: string;
    unifi_url?: string;
    unifi_api_key?: string;
    unifi_site?: string;
    unifi_verify_tls?: boolean;
    session_ttl_seconds?: number;
    allowed_origins?: string[];
    bootstrap_disabled?: boolean;
  }): Promise<PanelSettings> {
    return request("PUT", "/settings", input);
  },
  testCloudflare(): Promise<{ zones: string[] | null }> {
    return request("POST", "/settings/cloudflare/test");
  },
  testUnifi(): Promise<{ forward_count: number; wan_ip: string }> {
    return request("POST", "/settings/unifi/test");
  },

  // --- per-server DNS ---
  getServerDns(id: string): Promise<ServerDnsState> {
    return request("GET", `/servers/${id}/dns`);
  },
  setServerDns(id: string, input: { name: string; service?: string; port_name?: string }): Promise<{ dns: ServerDNS }> {
    return request("PUT", `/servers/${id}/dns`, input);
  },
  deleteServerDns(id: string): Promise<{ dns: null }> {
    return request("DELETE", `/servers/${id}/dns`);
  },
  setServerForward(id: string, portName: string, open: boolean): Promise<{ forwards: Record<string, PortForward> }> {
    return request("POST", `/servers/${id}/forwards/${encodeURIComponent(portName)}`, { open });
  },
};
