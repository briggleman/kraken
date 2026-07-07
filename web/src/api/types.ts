// Types mirroring the Panel REST API JSON. Kept deliberately lean — only the
// fields the UI uses.

export type ServerState =
  | "installing"
  | "offline"
  | "starting"
  | "running"
  | "stopping"
  | "crashed";

export type PlatformKind = "linux-native" | "linux-wine" | "windows-native";

export interface User {
  id: string;
  username: string;
  email: string;
  role_id: string;
  must_change_password?: boolean;
}

export interface SetupStatus {
  admin_must_change_password: boolean;
  using_memory: boolean;
  has_node_online: boolean;
  has_spec: boolean;
  has_server: boolean;
  setup_complete: boolean;
}

export interface DatabaseConfig {
  using_memory: boolean;
  env_locked: boolean;
  host?: string;
  port?: number;
  user?: string;
  dbname?: string;
  sslmode?: string;
}

export interface DatabaseInput {
  host: string;
  port?: number;
  user: string;
  password?: string;
  dbname?: string;
  sslmode?: string;
}

export interface CatalogItem {
  id: string;
  name: string;
  slug: string;
  description?: string;
  icon_url?: string;
  banner_url?: string;
  platforms: string[];
  steam_app_ids?: Record<string, number>;
  already_imported: boolean;
}

export interface BootstrapToken {
  token: string;
  node_name: string;
  expires_at: string;
}

export interface PanelSettings {
  cloudflare_configured: boolean;
  unifi_configured: boolean;
  unifi_url?: string;
  unifi_site?: string;
  session_ttl_seconds: number;
  session_ttl_locked: boolean;
  allowed_origins: string[] | null;
  allowed_origins_locked: boolean;
  bootstrap_disabled: boolean;
  bootstrap_user: string;
  bootstrap_locked: boolean;
}

// NodeConfig is a node's System settings: where backups are stored and whether
// they are mirrored to an SFTP remote. Secret fields are never returned — only
// a "*_configured" flag.
export interface NodeConfig {
  backup_target: string; // "local" | "sftp"
  backup_dir?: string;
  sftp_host?: string;
  sftp_user?: string;
  sftp_password_configured: boolean;
  sftp_key_configured: boolean;
  sftp_base_path?: string;
  replicate_to_sftp: boolean;
  steam_username?: string;
  steam_configured: boolean; // a Steam password is stored
}

// NodeConfigUpdateResult is the response to saving node config: the saved view
// plus the result of delivering it to the (online) Agent.
export interface NodeConfigUpdateResult extends NodeConfig {
  applied: boolean;
  apply_ok: boolean;
  apply_detail: string;
}

// NodeConfigUpdate is the set of writable fields; omit a field to leave it
// unchanged, send "" to clear it.
export interface NodeConfigUpdate {
  backup_target?: string;
  backup_dir?: string;
  sftp_host?: string;
  sftp_user?: string;
  sftp_password?: string;
  sftp_private_key?: string;
  sftp_base_path?: string;
  replicate_to_sftp?: boolean;
  steam_username?: string;
  steam_password?: string;
}

export interface PortForward {
  rule_id: string;
  enabled: boolean;
}

export interface ServerDNS {
  name: string;
  zone_id: string;
  service?: string;
  port_name?: string;
  record_ids: string[];
}

export interface ServerDnsState {
  cloudflare_configured: boolean;
  unifi_configured: boolean;
  target_host: string;
  lan_host: string;
  ports: Record<string, number> | null;
  dns: ServerDNS | null;
  forwards: Record<string, PortForward> | null;
}

export interface Role {
  id: string;
  name: string;
  builtin: boolean;
  permissions: string[];
}

export interface AdminUser {
  id: string;
  username: string;
  email: string;
  role_id: string;
  disabled: boolean;
}

export interface Backup {
  id: string;
  name: string;
  size: number;
  created_ms: number;
  /** Archive lifecycle — backups run asynchronously. */
  state: "pending" | "ready" | "failed";
  /** Off-node (SFTP) mirror status; "" when replication isn't configured. */
  replication: "" | "pending" | "done" | "failed";
}

export interface AuditEntry {
  id: string;
  time: string;
  actor: string;
  actor_id?: string;
  action: string;
  method: string;
  path: string;
  target_type?: string;
  target_id?: string;
  status: number;
  ip?: string;
}

export type ScheduleAction = "restart" | "backup" | "command" | "replicate";

export interface ScheduledTask {
  id: string;
  server_id: string;
  name: string;
  action: ScheduleAction;
  cron: string;
  command?: string;
  enabled: boolean;
  last_run_at?: string;
  next_run_at?: string;
  last_error?: string;
  created_at: string;
}

export interface LoginResponse {
  token: string;
  expires_at: string;
  user: User;
}

export interface Server {
  id: string;
  name: string;
  spec_id: string;
  node_id: string;
  kind: PlatformKind;
  state: ServerState;
  vars: Record<string, string>;
  ports: Record<string, number>;
  memory_mb: number;
  players?: number;
  max_players?: number;
  players_known?: boolean;
  created_at: string;
}

export interface SftpStatus {
  enabled: boolean;
  username: string;
  host?: string;
  port?: number;
  has_password: boolean;
  keys: string[];
}

export interface SpecPlatform {
  kind: PlatformKind;
  image: string;
}

export interface SpecVariable {
  key: string;
  label?: string;
  default: string;
  rules?: string;
  user_editable: boolean;
}

export interface SpecPort {
  name: string;
  protocol: "tcp" | "udp";
  default: number;
  required?: boolean;
}

export interface Spec {
  id: string;
  name: string;
  slug: string;
  description?: string;
  banner_url?: string;
  icon_url?: string;
  version: number;
  steam_app_ids?: Record<string, number>;
  platforms: SpecPlatform[];
  variables?: SpecVariable[];
  ports?: SpecPort[];
  resources: { min_memory_mb: number; recommended_memory_mb?: number };
  install?: { requires_steam_login?: boolean; bepinex_compatible?: boolean };
}

export interface Node {
  id: string;
  name: string;
  os: "linux" | "windows";
  wine_enabled: boolean;
  status: "online" | "offline" | "cordoned";
  address: string;
  public_host: string;
  external_ip?: string;
  total_memory_mb: number;
  allocated_memory_mb: number;
}

export type PowerActionName = "start" | "stop" | "restart" | "kill";

export type FieldType = "string" | "text" | "int" | "float" | "bool" | "enum" | "password";

export interface SettingField {
  key: string;
  label?: string;
  type: FieldType;
  default?: string;
  help?: string;
  options?: string[];
  min?: number;
  max?: number;
  pattern?: string;
  read_only?: boolean;
}

export interface SettingGroup {
  id: string;
  label?: string;
  description?: string;
  fields: SettingField[];
}

export interface ServerSettings {
  groups: SettingGroup[];
  values: Record<string, string>;
}

export interface UpdateSettingsResult {
  values: Record<string, string>;
  applied: boolean;
  restart_needed: boolean;
}

export interface FileEntry {
  name: string;
  path: string;
  is_dir: boolean;
  size: number;
  modified_ms: number;
}

export interface FileListing {
  path: string;
  entries: FileEntry[] | null;
}

export interface FileContent {
  path: string;
  content: string;
  size: number;
  is_binary: boolean;
  too_large: boolean;
}
