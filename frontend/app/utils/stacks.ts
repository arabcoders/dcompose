import { formatTimeAgo } from '@vueuse/core'
import { formatMemoryBytes } from '~/utils/node'
import type {
  ImageUpdate,
  NodeHealth,
  TerminalSnapshot,
  TerminalEvent,
  PortBinding,
  Service,
  ShellServerEvent,
  StackAction,
  StackRuntime,
  StackStatus,
  StackStats,
  StackStatsTotals,
  ContainerStats,
  WorkspaceEvent,
  WorkspaceStreamError,
  WorkspaceStreamMessage,
  WorkspaceStack,
} from '~/types'

export const imageUpdateLabel = (update: ImageUpdate): string =>
  ({
    checking: 'Checking for updates',
    available: 'Updates available',
    current: 'Up to date',
    unsupported: 'Update checks unavailable',
    error: "Couldn't check for updates",
  })[update.status]

const imageUpdateReasons: Record<string, string> = {
  inspect_error: 'Could not inspect the local container or image. Check the node logs for details.',
  registry_error: 'Registry lookup failed. Check the node logs for details.',
  manifest_invalid: 'The registry returned an invalid manifest. Check the node logs for details.',
  pinned_digest: 'This image is pinned to a digest, not a tag.',
  local_image: 'This image has no registry tag that can be checked.',
  platform_unknown: 'The local image platform could not be determined.',
  platform_missing: 'The registry has no manifest matching the local image platform.',
  platform_ambiguous: 'The registry has multiple manifests matching the local image platform.',
}

export const imageUpdateTooltip = (update: ImageUpdate): string => {
  const reason = update.reason ? imageUpdateReasons[update.reason] : undefined
  const description = reason ?? imageUpdateLabel(update)
  return update.checked_at
    ? `${description}\n${new Date(update.checked_at).toLocaleString()}`
    : description
}

export const stackStatusLabel = (status: StackStatus): string =>
  ({
    running: 'Running',
    stopped: 'Stopped',
    partial: 'Partly running',
    draft: 'Draft',
    unknown: 'Unknown',
  })[status]

export const containerProblem = (service: Service): string | null => {
  if ('restarting' === service.state) {
    return 'Restarting'
  }
  if ('dead' === service.state) {
    return 'Dead'
  }
  if (
    ('exited' === service.state || 'stopped' === service.state) &&
    undefined !== service.exit_code &&
    0 !== service.exit_code
  ) {
    return `Exited (code ${service.exit_code})`
  }
  if ('running' === service.state && 'unhealthy' === service.health) {
    return 'Unhealthy'
  }
  return null
}

export const containerUptime = (service: Service, now: Date): string | null => {
  if ('running' !== service.state || !service.started_at) {
    return null
  }
  const started = new Date(service.started_at).getTime()
  if (Number.isNaN(started)) {
    return null
  }
  const elapsed = Math.max(0, Math.floor((now.getTime() - started) / 1000))
  const units: Array<[string, number]> = [
    ['day', 86400],
    ['hour', 3600],
    ['minute', 60],
    ['second', 1],
  ]
  for (const [unit, seconds] of units) {
    if (seconds <= elapsed || 'second' === unit) {
      const count = Math.floor(elapsed / seconds)
      return `${count} ${unit}${1 === count ? '' : 's'}`
    }
  }
  return null
}

export const containerCreatedAt = (value: string, now: Date): string => {
  const date = new Date(value)
  return Number.isNaN(date.getTime())
    ? value
    : formatTimeAgo(date, { showSecond: true, rounding: 'floor' }, now)
}

const stackStatusOrder: Record<StackStatus, number> = {
  running: 0,
  partial: 1,
  stopped: 2,
  draft: 3,
  unknown: 4,
}

export const compareStacks = (
  a: Pick<StackRuntime, 'name' | 'status'>,
  b: Pick<StackRuntime, 'name' | 'status'>,
): number => stackStatusOrder[a.status] - stackStatusOrder[b.status] || a.name.localeCompare(b.name)

export const workspaceErrorMessage = (status: number, fallback: string): string => {
  if (401 === status) {
    return 'Sign in to continue.'
  }
  if (403 === status) {
    return 'This request was rejected.'
  }
  if (409 === status) {
    return 'Another action is running. Wait for it to finish.'
  }
  if (413 === status) {
    return 'The request is too large.'
  }
  return fallback
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  'object' === typeof value && null !== value && !Array.isArray(value)

const isContainerStats = (value: unknown): value is ContainerStats =>
  isRecord(value) &&
  ['id', 'name', 'cpu', 'memory', 'memory_percent', 'network_io', 'block_io', 'pids'].every(
    (key) => 'string' === typeof value[key],
  )

export const parseStackStats = (data: string): StackStats => {
  const value: unknown = JSON.parse(data)
  if (
    isRecord(value) &&
    Array.isArray(value.containers) &&
    value.containers.every(isContainerStats)
  ) {
    return { containers: value.containers }
  }
  throw new Error("Couldn't read stack resource stats.")
}

export const parseStatBytes = (value: string): number => {
  const match = value.trim().match(/^(\d+(?:\.\d+)?)\s*([kmgtp]?i?b)$/i)
  if (!match) {
    return Number.NaN
  }
  const unit = match[2]!.toUpperCase()
  const power = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'].indexOf(unit.replace('I', ''))
  return Number(match[1]) * (unit.includes('I') ? 1024 : 1000) ** power
}

const sum = (values: Array<number>): number => values.reduce((total, value) => total + value, 0)
const formatBytes = (value: number): string =>
  Number.isFinite(value) ? formatMemoryBytes(value) : '-'
const formatNumber = (value: number): string =>
  Number.isFinite(value)
    ? new Intl.NumberFormat('en', { maximumFractionDigits: 2 }).format(value)
    : '-'

const sumIO = (values: Array<string>): string => {
  const pairs = values.map((value) => value.split('/'))
  const first = sum(pairs.map((pair) => parseStatBytes(pair[0] ?? '')))
  const second = sum(pairs.map((pair) => parseStatBytes(pair[1] ?? '')))
  return Number.isFinite(first) && Number.isFinite(second)
    ? `${formatBytes(first)} / ${formatBytes(second)}`
    : '-'
}

export const aggregateStackStats = (
  containers: Array<ContainerStats>,
  cpuCount = 1,
): StackStatsTotals => {
  const cpu = sum(containers.map((container) => Number.parseFloat(container.cpu))) / cpuCount
  return {
    cpu: Number.isFinite(cpu) ? `${formatNumber(cpu)}%` : '-',
    // Limits can refer to the same host memory, so sum usage, not limits or percentages.
    memory: formatBytes(
      sum(containers.map((container) => parseStatBytes(container.memory.split('/')[0] ?? ''))),
    ),
    network_io: sumIO(containers.map((container) => container.network_io)),
    block_io: sumIO(containers.map((container) => container.block_io)),
    pids: formatNumber(sum(containers.map((container) => Number.parseFloat(container.pids)))),
  }
}

export const parseStreamMessage = (data: string): WorkspaceStreamMessage | WorkspaceStreamError => {
  const value: unknown = JSON.parse(data)
  if (
    isRecord(value) &&
    'error' === value.type &&
    (undefined === value.stream || 'string' === typeof value.stream) &&
    'string' === typeof value.error &&
    (undefined === value.status ||
      ('number' === typeof value.status && Number.isInteger(value.status)))
  ) {
    return { type: value.type, stream: value.stream, status: value.status, error: value.error }
  }
  if (
    isRecord(value) &&
    'event' === value.type &&
    (undefined === value.stream || 'string' === typeof value.stream) &&
    'string' === typeof value.event &&
    isRecord(value.payload)
  ) {
    return { stream: value.stream, event: value.event, data: JSON.stringify(value.payload) }
  }
  throw new Error("Couldn't read updates.")
}

const isPort = (value: unknown): value is PortBinding =>
  isRecord(value) &&
  'string' === typeof value.host_ip &&
  'number' === typeof value.host_port &&
  'number' === typeof value.container_port &&
  'string' === typeof value.protocol

const isImageUpdate = (value: unknown): value is ImageUpdate =>
  isRecord(value) &&
  'string' === typeof value.status &&
  ['checking', 'available', 'current', 'unsupported', 'error'].includes(value.status) &&
  (undefined === value.checked_at || 'string' === typeof value.checked_at) &&
  (undefined === value.reason || 'string' === typeof value.reason)

const isService = (value: unknown): value is Service =>
  isRecord(value) &&
  'string' === typeof value.name &&
  'string' === typeof value.service &&
  'string' === typeof value.image &&
  'string' === typeof value.state &&
  'string' === typeof value.health &&
  (undefined === value.exit_code ||
    ('number' === typeof value.exit_code && Number.isInteger(value.exit_code))) &&
  'string' === typeof value.ports &&
  ['status', 'created_at', 'started_at', 'running_for', 'check_interval'].every(
    (key) => undefined === value[key] || 'string' === typeof value[key],
  ) &&
  (undefined === value.auto_update || 'boolean' === typeof value.auto_update) &&
  (undefined === value.image_update || isImageUpdate(value.image_update)) &&
  (undefined === value.port_bindings ||
    (Array.isArray(value.port_bindings) && value.port_bindings.every(isPort)))

const isHealth = (value: unknown): value is NodeHealth =>
  isRecord(value) &&
  'string' === typeof value.status &&
  'string' === typeof value.version &&
  'number' === typeof value.api_version &&
  ['os', 'arch', 'started_at'].every(
    (key) => undefined === value[key] || 'string' === typeof value[key],
  )

const isRuntime = (value: unknown): value is StackRuntime & { error?: string } =>
  isRecord(value) &&
  'string' === typeof value.name &&
  'string' === typeof value.status &&
  ['running', 'stopped', 'partial', 'draft', 'unknown'].includes(value.status) &&
  Array.isArray(value.services) &&
  value.services.every(isService) &&
  (undefined === value.error || 'string' === typeof value.error) &&
  (undefined === value.busy || 'boolean' === typeof value.busy)

const isWorkspaceStack = (value: unknown): value is WorkspaceStack =>
  isRuntime(value) && 'loading' in value && 'boolean' === typeof value.loading

const isTerminalSnapshot = (value: unknown): value is TerminalSnapshot =>
  isRecord(value) &&
  'string' === typeof value.id &&
  'string' === typeof value.stack &&
  'string' === typeof value.action &&
  'string' === typeof value.output &&
  'string' === typeof value.status &&
  ['running', 'succeeded', 'failed'].includes(value.status) &&
  (null === value.exit_code || 'number' === typeof value.exit_code)

export const parseWorkspaceEvent = (event: string, data: string): WorkspaceEvent | null => {
  const value: unknown = JSON.parse(data)
  if ('nodes.changed' === event && isRecord(value)) {
    return { event }
  }
  if (!isRecord(value) || 'string' !== typeof value.node) {
    throw new Error("Couldn't read stack updates.")
  }

  const node = value.node
  switch (event) {
    case 'stack.action':
      if (
        'string' === typeof value.name &&
        'string' === typeof value.action &&
        ['deploy', 'start', 'stop', 'restart', 'pull', 'update', 'down', 'delete'].includes(
          value.action,
        ) &&
        ('start' === value.phase || 'exit' === value.phase) &&
        (undefined === value.exit_code || 'number' === typeof value.exit_code) &&
        (undefined === value.message || 'string' === typeof value.message)
      ) {
        return {
          event,
          node,
          name: value.name,
          action: value.action as StackAction | 'delete',
          phase: value.phase,
          exit_code: value.exit_code,
          message: value.message,
        }
      }
      break
    case 'stack.output':
      if ('string' === typeof value.name && 'string' === typeof value.output) {
        return { event, node, name: value.name, output: value.output }
      }
      break
    case 'node.snapshot':
      if (
        isHealth(value.health) &&
        Array.isArray(value.stacks) &&
        value.stacks.every(isWorkspaceStack)
      ) {
        return { event, node, health: value.health, stacks: value.stacks }
      }
      break
    case 'stack.runtime':
      if (isRuntime(value)) {
        return { event, node, runtime: value }
      }
      break
    case 'stack.removed':
      if ('string' === typeof value.name) {
        return { event, node, name: value.name }
      }
      break
    case 'node.state':
      if (
        ('connected' === value.state ||
          'unavailable' === value.state ||
          'paused' === value.state) &&
        (undefined === value.message || 'string' === typeof value.message)
      ) {
        return { event, node, state: value.state, message: value.message }
      }
      break
    default:
      return null
  }
  throw new Error("Couldn't read stack updates.")
}

export const parseTerminalEvent = (event: string, data: string): TerminalEvent | null => {
  const value: unknown = JSON.parse(data)
  if ('operation.snapshot' === event || 'operation.done' === event) {
    if (isTerminalSnapshot(value)) {
      return { event, operation: value }
    }
  } else if ('operation.output' === event) {
    if (isRecord(value) && 'string' === typeof value.id && 'string' === typeof value.output) {
      return { event, id: value.id, output: value.output }
    }
  } else {
    return null
  }
  throw new Error("Couldn't read action output.")
}

export const parseShellMessage = (data: string): ShellServerEvent => {
  const value: unknown = JSON.parse(data)
  if (isRecord(value) && 'string' === typeof value.shell && 'string' === typeof value.stream) {
    if ('shell.error' === value.type && 'string' === typeof value.error) {
      return { type: value.type, stream: value.stream, shell: value.shell, error: value.error }
    }
    if (
      'shell.event' === value.type &&
      'string' === typeof value.event &&
      isRecord(value.payload)
    ) {
      const event = parseTerminalEvent(value.event, JSON.stringify(value.payload))
      if (event) {
        return { type: value.type, stream: value.stream, shell: value.shell, event }
      }
    }
  }
  throw new Error("Couldn't read shell output.")
}

export const parseRealtimeMessage = (
  data: string,
): WorkspaceStreamMessage | WorkspaceStreamError | ShellServerEvent => {
  const value: unknown = JSON.parse(data)
  if (isRecord(value) && ('shell.event' === value.type || 'shell.error' === value.type)) {
    return parseShellMessage(data)
  }
  return parseStreamMessage(data)
}
