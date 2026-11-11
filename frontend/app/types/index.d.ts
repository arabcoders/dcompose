export interface GenericError {
  error: {
    code: number
    message: string
  }
}

export type JsonLogValue =
  | string
  | number
  | boolean
  | null
  | Array<JsonLogValue>
  | { [key: string]: JsonLogValue }

export type LogLevel = 'debug' | 'info' | 'warning' | 'error'

export interface LogSource {
  file?: string
  path?: string
  module?: string
  function?: string
  line?: number
}

export interface LogRuntime {
  id?: number | string
  name?: string
}

export interface LogException {
  type: string
  message?: string
  file?: string
  line?: number
  stack?: Array<LogSource>
}

export interface LogEntry {
  id: number
  timestamp: string
  level: string
  logger: string
  message: string
  rendered: string
  fields: Record<string, JsonLogValue>
  source?: LogSource
  process?: LogRuntime
  thread?: LogRuntime
  exception?: LogException
}

export interface LogDetailRow {
  label: string
  value: string
  icon: string
}

export interface LogFieldRow {
  key: string
  label: string
  value: string
  preview: string
  kind: 'scalar' | 'text' | 'json'
}

export interface LogsResponse {
  entries: Array<LogEntry>
  next_before: number | null
  latest_id: number
  oldest_id: number | null
  truncated: boolean
}

export interface LogFilters {
  levels: Array<LogLevel>
  node: string
  search: string
}

export interface LogStreamEvent {
  id: number | null
  event: string
  data: string
}

export interface LogStreamStatus {
  oldest_id: number | null
  truncated: boolean
}

export interface AuthAccount {
  username: string
}

export interface AuthUserResponse {
  account: AuthAccount
}

export interface AuthStatusResponse {
  setup_required: boolean
  authenticated: boolean
  oidc_available: boolean
}

export interface AppVersion {
  version: string
  api_version: number
  agent_api_version: number
}

export interface StatCardProps {
  label: string
  value: string | number
  icon: string
  hint?: string
  tooltip?: string
  color?: 'primary' | 'success' | 'error' | 'warning' | 'info' | 'neutral'
}

export interface PageHeaderProps {
  icon: string
  sectionLabel: string
  pageLabel: string
  description: string
}

export interface EditorProps {
  content: string
  mode?: 'yaml' | 'ini'
  height?: string
  readOnly?: boolean
}

export interface SettingsNode {
  id: string
  name: string
  url: string
  fingerprint: string
  public_host: string
  has_token: boolean
  token: string
  paused?: boolean
}

export interface SettingsResponse extends AppVersion {
  username: string
  nodes: Array<SettingsNode>
}

export type SettingsSection = 'nodes' | 'account' | 'registries'

export interface RegistryCredential {
  id: string
  registry: string
  label: string
  auth: Record<string, string>
  username: string
  background_checks: boolean
  has_auth: boolean
}

export interface RegistryCredentialInput {
  id?: string
  registry: string
  label: string
  auth: Record<string, string>
  background_checks: boolean
}

export interface RegistriesResponse {
  registries: Array<RegistryCredential>
}

export interface RegistryImportEntry extends RegistryCredentialInput {
  selected: boolean
  message: string
}

export interface RegistryImportResult {
  entries: Array<RegistryImportEntry>
  credentialStore: string
}

export interface PairNodeRequest {
  name: string
  url: string
  pairing_token: string
  fingerprint?: string
}

export interface DisconnectNodeResponse {
  status: 'ok'
  warning?: string
}

export interface NodeConnectionRequest {
  name: string
  url: string
  token: string
  fingerprint: string
  public_host: string
}

export interface NodeConnectionTest {
  version: string
  api_version: number
}

export interface UpdateAccountRequest {
  username: string
  password: string
  current_password: string
}

export interface Node {
  id: string
  name: string
  public_host: string
  paused?: boolean
}

export interface NodesResponse {
  nodes: Array<Node>
}

export interface UpdateNodeOrderRequest {
  nodes: Array<string>
}

export interface NodeHealth {
  status: string
  version: string
  api_version: number
  os?: string
  arch?: string
  started_at?: string
}

export interface RuntimeInfo {
  name: string
  version: string
  operating_system: string
  architecture: string
  kernel_version: string
  cpus: number
  memory_bytes: number
  containers: number
  containers_running: number
  containers_stopped: number
  containers_paused: number
  images: number
  storage_driver: string
}

export interface ContainerStats {
  id: string
  name: string
  cpu: string
  memory: string
  memory_percent: string
  network_io: string
  block_io: string
  pids: string
}

export interface StackStats {
  containers: Array<ContainerStats>
}

export type StackStatsTotals = Omit<ContainerStats, 'id' | 'name' | 'memory_percent'>

export interface NodeInfo {
  runtime: RuntimeInfo
  containers: Array<ContainerStats>
  checked_at: string
}

export type StackStatus = 'running' | 'stopped' | 'partial' | 'draft' | 'unknown'
export type StackFilter = StackStatus | 'all' | 'updates'

export interface StackSummary {
  name: string
  status: StackStatus
}

export interface WorkspaceNode extends Node {
  stacks: Array<WorkspaceStack>
  error: string
  health: NodeHealth | null
  connection: 'loading' | 'connected' | 'reconnecting' | 'unavailable' | 'paused'
  loading: boolean
}

export interface WorkspaceStack extends StackRuntime {
  loading: boolean
  error?: string
}

export type WorkspaceEvent =
  | StackEvent
  | { event: 'nodes.changed' }
  | {
      event: 'node.snapshot'
      node: string
      health: NodeHealth
      stacks: Array<WorkspaceStack>
    }
  | { event: 'stack.runtime'; node: string; runtime: StackRuntime & { error?: string } }
  | { event: 'stack.removed'; node: string; name: string }
  | {
      event: 'node.state'
      node: string
      state: 'connected' | 'unavailable' | 'paused'
      message?: string
    }

export interface WorkspaceStreamMessage {
  stream?: string
  event: string
  data: string
}

export interface WorkspaceStreamError {
  type: 'error'
  stream?: string
  status?: number
  error: string
}

export type RealtimeSubscription =
  | { type: 'runtime' }
  | { type: 'logs' | 'stats'; node: string; resource: string }
  | { type: 'shell'; node: string; shell: string }

export interface RealtimeCallbacks {
  onMessage: (message: WorkspaceStreamMessage | ShellServerEvent) => void
  onDisconnect: (message: string) => void
  onRejected: (status: number) => void
}

export interface RealtimeReader {
  stop: () => void
  send: (message: ShellClientMessage) => boolean
}

export type TerminalEvent =
  | { event: 'operation.snapshot' | 'operation.done'; operation: TerminalSnapshot }
  | { event: 'operation.output'; id: string; output: string }

export type StackEvent =
  | {
      event: 'stack.action'
      node: string
      name: string
      action: StackAction | 'delete'
      phase: 'start' | 'exit'
      exit_code?: number
      message?: string
    }
  | { event: 'stack.output'; node: string; name: string; output: string }

export interface CreateCopyModalProps {
  open: boolean
  node: string
  stack: string
}

export interface StackListProps {
  nodes: Array<WorkspaceNode>
  workspaceLoading: boolean
  search: string
  status: StackFilter
}

export interface Service {
  name: string
  service: string
  image: string
  state: string
  health: string
  exit_code?: number
  status?: string
  created_at?: string
  started_at?: string
  running_for?: string
  ports: string
  port_bindings?: Array<PortBinding>
  image_update?: ImageUpdate
  check_interval?: string
  auto_update?: boolean
}

export interface ImageUpdate {
  status: 'checking' | 'available' | 'current' | 'unsupported' | 'error'
  checked_at?: string
  reason?: string
}

export interface PortBinding {
  host_ip: string
  host_port: number
  container_port: number
  protocol: string
}

export interface StackRuntime extends StackSummary {
  services: Array<Service>
  busy?: boolean
}

export interface Stack extends StackRuntime {
  compose: string
  env: string
}

export type StackAction = 'deploy' | 'start' | 'stop' | 'restart' | 'pull' | 'update' | 'down'

export interface TerminalSnapshot {
  id: string
  stack: string
  action: string
  status: 'running' | 'succeeded' | 'failed'
  output: string
  exit_code: number | null
}

export interface ShellSession {
  id: string
  stack: string
  container: string
  shell: 'sh' | 'bash' | 'ash' | 'dash'
}

export type ShellClientMessage =
  | { type: 'shell.input'; data: string }
  | { type: 'shell.resize'; cols: number; rows: number }
  | { type: 'shell.close' }

export type ShellServerEvent =
  | { type: 'shell.event'; stream: string; shell: string; event: TerminalEvent }
  | { type: 'shell.error'; stream: string; shell: string; error: string }

export interface OutputTerminalProps {
  placeholder?: string
  interactive?: boolean
}

export interface StackLogsProps {
  node: string
  stack: string
  connected: boolean
  paused?: boolean
  draft?: boolean
  busy?: boolean
  actionError?: string
  showErrors?: boolean
}
