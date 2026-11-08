import type {
  JsonLogValue,
  LogEntry,
  LogException,
  LogFilters,
  LogLevel,
  LogRuntime,
  LogSource,
  LogStreamStatus,
} from '../types'

export const LOG_WINDOW = 500
export const logLevels: Array<LogLevel> = ['debug', 'info', 'warning', 'error']

export const normalizeLogLevels = (value: unknown): Array<LogLevel> => {
  if (!Array.isArray(value)) {
    return [...logLevels]
  }
  return logLevels.filter((level) => value.includes(level))
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  null !== value && 'object' === typeof value && !Array.isArray(value)

const isLogSource = (value: unknown): value is LogSource =>
  isRecord(value) &&
  ['file', 'path', 'module', 'function'].every(
    (key) => undefined === value[key] || 'string' === typeof value[key],
  ) &&
  (undefined === value.line || 'number' === typeof value.line)

const isLogRuntime = (value: unknown): value is LogRuntime =>
  isRecord(value) &&
  (undefined === value.id || 'number' === typeof value.id || 'string' === typeof value.id) &&
  (undefined === value.name || 'string' === typeof value.name)

const isLogException = (value: unknown): value is LogException =>
  isRecord(value) &&
  'string' === typeof value.type &&
  (undefined === value.message || 'string' === typeof value.message) &&
  (undefined === value.file || 'string' === typeof value.file) &&
  (undefined === value.line || 'number' === typeof value.line) &&
  (undefined === value.stack || (Array.isArray(value.stack) && value.stack.every(isLogSource)))

export const isJsonLogValue = (value: unknown): value is JsonLogValue => {
  if (null === value || 'string' === typeof value || 'boolean' === typeof value) {
    return true
  }
  if ('number' === typeof value) {
    return Number.isFinite(value)
  }
  if (Array.isArray(value)) {
    return value.every(isJsonLogValue)
  }
  return isRecord(value) && Object.values(value).every(isJsonLogValue)
}

export const isLogEntry = (value: unknown): value is LogEntry => {
  if (!isRecord(value)) {
    return false
  }
  return (
    'number' === typeof value.id &&
    Number.isSafeInteger(value.id) &&
    0 < value.id &&
    'string' === typeof value.timestamp &&
    'string' === typeof value.level &&
    'string' === typeof value.logger &&
    'string' === typeof value.message &&
    'string' === typeof value.rendered &&
    isRecord(value.fields) &&
    Object.values(value.fields).every(isJsonLogValue) &&
    (undefined === value.source || isLogSource(value.source)) &&
    (undefined === value.process || isLogRuntime(value.process)) &&
    (undefined === value.thread || isLogRuntime(value.thread)) &&
    (undefined === value.exception || isLogException(value.exception))
  )
}

export const parseLogEntry = (data: string): LogEntry => {
  const value: unknown = JSON.parse(data)
  if (!isLogEntry(value)) {
    throw new Error("Couldn't read a log entry from the server.")
  }
  return value
}

export const parseLogStatus = (data: string): LogStreamStatus => {
  const value: unknown = JSON.parse(data)
  if (
    !isRecord(value) ||
    'boolean' !== typeof value.truncated ||
    (null !== value.oldest_id &&
      ('number' !== typeof value.oldest_id ||
        !Number.isSafeInteger(value.oldest_id) ||
        0 >= value.oldest_id))
  ) {
    throw new Error("Couldn't read log retention status from the server.")
  }
  return { oldest_id: value.oldest_id, truncated: value.truncated }
}

export const logQuery = (filters: LogFilters): URLSearchParams => {
  const params = new URLSearchParams()
  if (filters.levels.length !== logLevels.length) {
    params.set('levels', filters.levels.join(','))
  }
  for (const key of ['node', 'search'] as const) {
    const value = filters[key].trim()
    if (value) {
      params.set(key, value)
    }
  }
  return params
}

export const mergeLogs = (
  current: Array<LogEntry>,
  incoming: Array<LogEntry>,
  older = false,
): Array<LogEntry> => {
  const merged = [
    ...new Map([...current, ...incoming].map((entry) => [entry.id, entry])).values(),
  ].sort((left, right) => left.id - right.id)
  return older ? merged.slice(0, LOG_WINDOW) : merged.slice(-LOG_WINDOW)
}

export const filterLogTextLines = (value: string, filter: string): Array<string> => {
  const query = filter.trim().toLowerCase()
  const lines = value.split('\n')
  return query ? lines.filter((line) => line.toLowerCase().includes(query)) : lines
}

export const getLogLevel = (value: string): LogLevel => {
  switch (value.toLowerCase()) {
    case 'info':
      return 'info'
    case 'warning':
    case 'warn':
      return 'warning'
    case 'error':
    case 'critical':
    case 'fatal':
      return 'error'
    default:
      return 'debug'
  }
}

export const formatLogDate = (value: string, format: 'detail' | 'timestamp' | 'clock'): string => {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return value
  }
  const time: Intl.DateTimeFormatOptions = { hour: '2-digit', minute: '2-digit', second: '2-digit' }
  return new Intl.DateTimeFormat('en', {
    ...('clock' === format ? {} : ({ year: 'numeric', month: '2-digit', day: '2-digit' } as const)),
    ...time,
    ...('timestamp' === format ? ({ timeZoneName: 'longOffset' } as const) : {}),
  }).format(date)
}

export const formatLogRelativeTime = (value: string): string => {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return ''
  }
  const units: Array<[Intl.RelativeTimeFormatUnit, number]> = [
    ['year', 365 * 24 * 60 * 60],
    ['month', 30 * 24 * 60 * 60],
    ['week', 7 * 24 * 60 * 60],
    ['day', 24 * 60 * 60],
    ['hour', 60 * 60],
    ['minute', 60],
    ['second', 1],
  ]
  const diff = Math.round((date.getTime() - Date.now()) / 1000)
  const formatter = new Intl.RelativeTimeFormat('en', { numeric: 'auto' })
  for (const [unit, seconds] of units) {
    if (Math.abs(diff) >= seconds || 'second' === unit) {
      return formatter.format(Math.round(diff / seconds), unit)
    }
  }
  return formatter.format(0, 'second')
}

export const logNodeLabel = (entry: LogEntry): string => {
  if ('server' === entry.logger) {
    return 'Server'
  }
  const name = entry.fields.node_name
  const id = entry.fields.node_id
  return 'string' === typeof name ? name : 'string' === typeof id ? id : entry.logger
}

export const serializeRenderedLogs = (entries: Array<LogEntry>): string =>
  entries.reduce(
    (text, entry, index) =>
      0 === index || text.endsWith('\n') ? text + entry.rendered : text + '\n' + entry.rendered,
    '',
  )

export const serializeLogs = (entries: Array<LogEntry>): string =>
  entries.map((entry) => JSON.stringify(entry)).join('\n') + (entries.length ? '\n' : '')
