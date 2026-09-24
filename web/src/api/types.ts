// Types mirroring the Panel REST API JSON. Kept deliberately lean — only the
// fields the UI uses.

export type ServerState =
  | "installing"
  /** Provisioning failed — see Server.last_error; recover via reinstall. */
  | "install_failed"
  | "offline"
  | "starting"
  | "running"
  | "stopping"
  | "crashed"
  /** A backup restore is swapping save files; start waits for it (#361). */
  | "restoring";

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
  /** Full SHA-256 fingerprint of the Panel CA — embedded in the generated
   *  install command so the agent can pin the CA it enrolls under. */
  ca_fingerprint: string;
}

/** Lifecycle of a bootstrap token, polled by the setup wizard. */
export interface EnrollStatus {
  status: "pending" | "redeemed" | "expired";
  node_name?: string;
  ip?: string;
  /** Hosts the agent baked into its cert — its reachable IPs/DNS names (IPs first). */
  hosts?: string[];
  /** The gRPC port the agent reports it serves on (prefill; 9090 default). */
  agent_port?: number;
  /** Panel-minted identity in the issued cert — binds a tunnel-mode registration. */
  tunnel_id?: string;
  expires_at?: string;
  redeemed_at?: string;
}

export interface PanelSettings {
  cloudflare_configured: boolean;
  unifi_configured: boolean;
  unifi_url?: string;
  unifi_site?: string;
  unifi_verify_tls: boolean;
  session_ttl_seconds: number;
  session_ttl_locked: boolean;
  allowed_origins: string[] | null;
  allowed_origins_locked: boolean;
  bootstrap_disabled: boolean;
  bootstrap_user: string;
  bootstrap_locked: boolean;
}

// NodeConfig is a node's System settings: where backups are stored and whether
// they are mirrored to an off-node remote. Secret fields are never returned —
// only a "*_configured" flag.
export interface NodeConfig {
  backup_target: string; // "local" | "share" | "sftp" | "smb"
  backup_dir?: string;
  sftp_host?: string;
  sftp_user?: string;
  sftp_password_configured: boolean;
  sftp_key_configured: boolean;
  sftp_base_path?: string;
  sftp_known_host_key?: string;
  replicate_to_sftp: boolean;
  smb_host?: string;
  smb_share?: string;
  smb_user?: string;
  smb_password_configured: boolean;
  smb_domain?: string;
  smb_base_path?: string;
  replicate_to_smb: boolean;
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
  sftp_known_host_key?: string;
  replicate_to_sftp?: boolean;
  smb_host?: string;
  smb_share?: string;
  smb_user?: string;
  smb_password?: string;
  smb_domain?: string;
  smb_base_path?: string;
  replicate_to_smb?: boolean;
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
  /** Why the backup failed (state=failed), or a degraded-capture note; absent when clean. */
  error?: string;
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
  /**
   * Raw X-Forwarded-For chain, present only when `ip` is an address that
   * identifies nobody (a NAT gateway, or one exempted from per-IP limiting).
   * The caller wrote it: show it as forensics, never treat it as the source.
   */
  forwarded_for?: string;
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
  /** Why the most recent provisioning attempt failed, verbatim — set with
   *  state install_failed, cleared when a (re)install succeeds or begins. */
  last_error?: string;
  /** The exit status of the container's last run, carried from the agent's
   *  watchdog. Held only while the state is crashed; last_exit_code_known
   *  separates "exited 0" from "no exit was observed". */
  last_exit_code?: number;
  last_exit_code_known?: boolean;
  /** Build pin: the panel skips the install/update pass it otherwise runs
   *  before every start or restart, so this server stays on the build now on
   *  disk. Reinstall is then the explicit "update now". */
  pin_build?: boolean;
  /** Present only while a backup restore runs (state `restoring`, #361). */
  restore?: RestoreProgress;
  restore_result?: RestoreResult;
  created_at: string;
}

/** A running backup restore, as the Panel's restore job reports it. Progress
 *  is compressed bytes read against the archive's size; `bytes_total` is 0 when
 *  the size is unknown (an agent too old to report progress, or a target that
 *  cannot size the archive) — progress unknown, never progress zero. */
export interface RestoreProgress {
  backup_id: string;
  /** opening · extracting · applying · done, or `restoring` for an old agent
   *  restoring without progress. */
  phase: string;
  bytes_done: number;
  bytes_total: number;
  /** Server clock. */
  started_at: string;
  /** Where the row goes back to when the restore ends. */
  prev_state?: ServerState;
}

/** How the most recent restore ended (#361). It stays on the row until the
 *  next restore replaces it; `finished_at` is the server's clock. */
export interface RestoreResult {
  backup_id: string;
  ok: boolean;
  /** The agent's reason, which says whether the files were rolled back. */
  error?: string;
  finished_at: string;
}

export interface SftpStatus {
  enabled: boolean;
  username: string;
  host?: string;
  port?: number;
  has_password: boolean;
  keys: string[];
  /** The hosting node is tunnel-mode — its SFTP listener is LAN-local only,
   *  unless proxied (below). */
  tunneled?: boolean;
  /** host/port address the Panel-side SFTP proxy, reachable wherever the Panel
   *  is, instead of the node's LAN-local listener. */
  proxied?: boolean;
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
  install?: { requires_steam_login?: boolean; bepinex_compatible?: boolean; skip_update_on_start?: boolean };
  /** The game's editable settings. The list endpoint has always sent them; the
   *  deploy form reads them to know whether a fresh server can start at once. */
  settings?: { groups?: SettingGroup[]; hot_reload?: boolean };
  /**
   * What a backup of this game captures, as doublestar globs. Omitted means the
   * whole data dir (minus what's unambiguously ephemeral). Drives the backup
   * detail's "captured" line — the spec's CURRENT policy, so it describes new
   * backups; an archive predating a glob change reflects the policy as it is now.
   */
  backup?: { include?: string[]; exclude?: string[] };
}

/** The Panel's build, from GET /version. */
export interface PanelVersion {
  version: string;
  commit: string;
  date: string;
}

export interface NodePortRange {
  start: number;
  end: number;
}

/**
 * One node's live host vitals, as sampled by its agent and cached by the panel.
 *
 * Every metric group carries a *_known flag. False means the host cannot supply
 * that metric — a Windows node has no temperature source, and cpu/net are
 * unknown until the agent has taken two samples — so the value must be rendered
 * as "no data", never as 0. A node the panel has no fresh reading for is absent
 * from the telemetry map entirely.
 */
export interface NodeTelemetry {
  /** When the agent sampled this reading (unix ms). */
  ts_unix_ms: number;
  uptime_seconds?: number;

  /** 0-100 across all cores. Meaningful only when cpu_known. */
  cpu_percent: number;
  cpu_cores?: number;
  cpu_known: boolean;

  mem_total_mb: number;
  /** Physical memory in use — total minus available, so reclaimable cache is not counted. */
  mem_used_mb: number;
  mem_known: boolean;

  /** The filesystem measured: the one holding the agent's data dir. */
  disk_path?: string;
  disk_total_mb: number;
  disk_used_mb: number;
  disk_known: boolean;

  /** Bytes per second across the host's physical interfaces. */
  net_rx_bps: number;
  net_tx_bps: number;
  net_known: boolean;

  /**
   * The Panel's own timing of the gRPC round trip that fetched this reading,
   * in milliseconds. No *_known flag: unlike the host groups it is not
   * reported by the agent — if this entry exists, the round trip happened.
   */
  link_rtt_ms: number;
}

/**
 * One agent-binary push, as the Panel sees it. Tracked in memory by the Panel
 * process that started it — so a 404 from the status endpoint means "this Panel
 * knows nothing", and the node's agent_version is then the truth.
 *
 * There is no "done" phase on purpose: the job ends at `restarting`, when the
 * agent has acked the binary and is swapping itself. Whether it came back on
 * the new build is the node record's story — the drift line clearing is the
 * success signal.
 */
export interface AgentUpdateJob {
  job_id: string;
  node_id: string;
  from_version: string;
  to_version: string;
  phase: "pushing" | "restarting" | "failed";
  /** The agent's verbatim refusal. Present only when phase is "failed". */
  error?: string;
  /** Bytes handed to the stream. gRPC gives no send-side ack, so this is
   *  "sent", not "received" — label it as such if it is ever shown as progress. */
  bytes_sent: number;
  /** 0 when the push failed before the binary was sized: treat progress as
   *  unknown rather than zero. */
  bytes_total: number;
  started_at: string;
  finished_at?: string;
}

/** One removal a node owes (see Node.pending_removals). */
export interface PendingRemoval {
  server_id: string;
  /** Whether the operator's delete also removes the world and config. */
  delete_data: boolean;
  requested_at: string;
  /** Failed tries so far, the one made at delete time included. */
  attempts: number;
  /** The most recent failure, verbatim. */
  last_error?: string;
}

export interface Node {
  id: string;
  name: string;
  os: "linux" | "windows";
  wine_enabled: boolean;
  /**
   * online — ready for work. partial — the agent answers but can't reach its
   * container runtime (see runtime_error); nothing can start there, so the
   * scheduler skips it. offline — the agent is unreachable. cordoned — reachable
   * but held back from new placements.
   */
  status: "online" | "partial" | "offline" | "cordoned";
  /** Why the container runtime is unreachable; set while status is "partial". */
  runtime_error?: string;
  /** Agent build seen on last contact — compare with the list's panel_version. */
  agent_version?: string;
  /** SHA-256 of the agent's running binary; compared with the panel's embedded
   *  build for artifact-identity skew (empty from agents predating the field). */
  agent_sha?: string;
  /** The AGENT's count of its own `kraken.managed` containers, observed on last
   *  contact. Compared against the servers the panel placed here it surfaces
   *  containers the panel has lost track of — see containerDrift(). */
  running_servers?: number;
  /** The same containers running_servers counts, named — one entry per
   *  `kraken.managed` container the agent reported running. Absent from an agent
   *  older than 0.54.0, which reports only the count, so an empty list means
   *  "this agent did not say", never "nothing is running" — see containerDrift(). */
  managed_containers?: { server_id: string; container_name: string }[];
  /** Server removals this node owes: servers deleted in the panel whose removal
   *  the node never confirmed (it was unreachable, or its agent failed it). The
   *  panel's node reconciler replays each one every time the node answers and
   *  drops it once the agent confirms — see pendingRemovalsNote(). */
  pending_removals?: PendingRemoval[];
  /** Operator hold: excluded from new placements while its servers keep running. */
  cordoned?: boolean;
  /** Registered before its agent answered; name is a placeholder until first contact. */
  identity_pending?: boolean;
  /** Agent build architecture ("amd64" | "arm64") seen on last contact. */
  arch?: string;
  /** Most recent self-update failure, verbatim from the agent ("" / absent = none). */
  last_update_error?: string;
  /** Why the agent's inbound listeners are down ("grpc :9090: bind: …"), verbatim.
   *  NOT a health state: a tunnel-mode node answers over its reverse tunnel and
   *  keeps hosting everything, so it stays "online" — this is the only place the
   *  port conflict is visible outside the node's own agent.log. */
  listen_error?: string;
  address: string;
  public_host: string;
  /** The node's LAN address as the Panel last observed it (tunnel source IP /
   *  dial address / agent report) — what forwards and LAN SFTP actually target. */
  lan_host?: string;
  /** Agent-reported interface/IP pairs ("eth0 192.168.0.88") for display. */
  lan_candidates?: string[];
  external_ip?: string;
  /**
   * How the Panel reaches this node's agent: "direct" (Panel dials address;
   * the default — absent means direct) or "tunnel" (the agent dials out and
   * keeps a reverse tunnel open; no inbound port on the node).
   */
  connection_mode?: "direct" | "tunnel";
  /** Panel-minted agent identity a tunnel-mode node is bound to. */
  tunnel_id?: string;
  total_memory_mb: number;
  allocated_memory_mb: number;
  /** Game-port pool: configured ranges (null when none) + allocated ports. */
  ports?: { ranges: NodePortRange[] | null; allocated?: number[] } | null;
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
  /** The game will not start without a value. The panel refuses to start or
   *  restart a server while one is empty (409, code required_settings_missing);
   *  saving settings is never blocked by it. */
  required?: boolean;
}

export interface SettingGroup {
  id: string;
  label?: string;
  description?: string;
  fields: SettingField[];
}

// ServerVariable is a launch variable (rendered into the startup command /
// container env at start) with this server's current value. Editable any
// time; changes apply on the next start.
export interface ServerVariable {
  key: string;
  label?: string;
  value: string;
  rules?: string;
  user_editable: boolean;
}

export interface ServerSettings {
  groups: SettingGroup[];
  /** Effective values: what the start gate judges and the config renders. */
  values: Record<string, string>;
  /** Keys whose value is the spec's current default, not one stored on the
   *  server: a field the spec added later, or a required field stored blank
   *  that yields to the spec's default (#367). */
  from_spec?: string[];
  variables?: ServerVariable[];
  /** The game re-reads config files live — saved settings apply without a restart. */
  hot_reload?: boolean;
  /** This server's build pin — true = no update pass before a start. */
  pin_build?: boolean;
  /** False when the spec itself opted out of update-on-start, so the pin is
   *  moot and the toggle says so instead of promising updates. */
  updates_on_start?: boolean;
  /** What the next operator start or restart through the Panel would do right
   *  now: false when the spec opted out, the build is pinned, the server was
   *  installed within the last 30 minutes, or its Steam-login install has no
   *  stored credentials. Scheduled restarts and the node-scoped power endpoint
   *  never run the pass, and it means nothing while installing or
   *  install_failed, where a start is refused outright. */
  next_start_updates?: boolean;
  /** Why the next start skips the pass; absent when it runs. */
  update_skip_reason?: "spec" | "pinned" | "fresh_install" | "steam_login";
}

export interface UpdateSettingsResult {
  values: Record<string, string>;
  from_spec?: string[];
  variables?: ServerVariable[];
  applied: boolean;
  restart_needed: boolean;
  hot_reload?: boolean;
  variables_changed?: boolean;
  pin_build?: boolean;
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

/** One line of install output, as the console stream writes it: stream is
 *  "install" for ordinary output and "error" for a failure note. */
export interface InstallLogLine {
  ts: number;
  stream: string;
  text: string;
}

/** The retained output of a server's most recent install. The buffer lives in
 *  the Panel's memory, so `retained` is false when the Panel has restarted since
 *  the attempt — which is NOT the same as an install that printed nothing, and
 *  the console pane says which of the two it is looking at. */
export interface InstallLog {
  server_id: string;
  lines: InstallLogLine[];
  done: boolean;
  retained: boolean;
  started_ms?: number;
  finished_ms?: number;
}

export interface FileContent {
  path: string;
  content: string;
  size: number;
  is_binary: boolean;
  too_large: boolean;
}
