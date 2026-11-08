import { afterEach, beforeEach, describe, expect, jest, mock, test } from 'bun:test'
import * as ts from 'typescript'
import * as vue from 'vue'
import type { EffectScope } from 'vue'
import type { GenericError, LogEntry, LogFilters, LogsResponse } from '../app/types'
import * as logUtils from '../app/utils/logs'
import { LogStreamParser } from '../app/utils/logStream'

const entry = (id: number, values: Partial<LogEntry> = {}): LogEntry => ({
  id,
  timestamp: '2026-10-30T12:34:56.123456789Z',
  level: 'error',
  logger: 'node.kitchen',
  message: 'Compose failed',
  rendered:
    '2026-10-30T12:34:56.123456789Z ERROR Compose failed node="Kitchen" error="native failure" stderr="line one\\nline two"\n',
  fields: {
    node_id: 'kitchen',
    node_name: 'Kitchen',
    stderr: 'line one\nline two',
    error: { cause: ['native', null, false, 0] },
  },
  source: { file: 'compose.go', function: 'Deploy', line: 17 },
  ...values,
})
const filters = (values: Partial<LogFilters> = {}): LogFilters => ({
  levels: [...logUtils.logLevels],
  node: '',
  search: '',
  ...values,
})
const page = (entries: Array<LogEntry>, values: Partial<LogsResponse> = {}): Response =>
  Response.json({
    entries,
    latest_id: entries.at(-1)?.id ?? 0,
    next_before: null,
    oldest_id: entries[0]?.id ?? null,
    truncated: false,
    ...values,
  })

describe('log data', () => {
  test('SSE chunks', () => {
    const parser = new LogStreamParser()
    expect(parser.feed(': heartbeat\r\nevent: log_li')).toEqual([])
    expect(parser.feed('nes\r\nid: 123\r\ndata: first\r\ndata: second\r')).toEqual([])
    expect(parser.feed('\n\r\n')).toEqual([{ event: 'log_lines', id: 123, data: 'first\nsecond' }])
    expect(parser.feed(': another heartbeat\n\nevent: other\ndata: {}\n\n')).toEqual([
      { event: 'other', id: null, data: '{}' },
    ])
  })

  test('native fields', () => {
    const value = entry(1, {
      fields: {
        nil: null,
        flag: false,
        count: 0,
        image: 'redis:7',
        nested: { lines: ['<script>', { code: 1 }] },
      },
    })
    expect(logUtils.parseLogEntry(JSON.stringify(value))).toEqual(value)
    expect(logUtils.isLogEntry({ ...value, id: Number.MAX_SAFE_INTEGER + 1 })).toBe(false)
    expect(logUtils.isLogEntry({ ...value, fields: [] })).toBe(false)
    expect(logUtils.isLogEntry({ ...value, rendered: undefined })).toBe(false)
    expect(logUtils.isLogEntry({ ...value, source: { line: '17' } })).toBe(false)
    expect(logUtils.isJsonLogValue({ invalid: undefined })).toBe(false)
    expect(() => logUtils.parseLogEntry('{"id":1}')).toThrow()
    expect(
      logUtils.parseLogEntry(
        JSON.stringify(
          entry(2, {
            exception: {
              type: 'RuntimeError',
              message: 'native failure',
              stack: [{ path: 'compose.go', line: 17 }],
            },
            process: { name: 'server', id: 3 },
            thread: { name: 'worker', id: 'job' },
          }),
        ),
      ),
    ).toHaveProperty('exception.type', 'RuntimeError')
    expect(logUtils.isLogEntry({ ...value, exception: { type: 23 } })).toBe(false)
  })

  test('detail text lines', () => {
    expect(logUtils.filterLogTextLines('first\nSecond\nthird', ' second ')).toEqual(['Second'])
    expect(logUtils.filterLogTextLines('first\nsecond', '')).toEqual(['first', 'second'])
    expect(logUtils.getLogLevel('WARN')).toBe('warning')
    expect(logUtils.getLogLevel('critical')).toBe('error')
    expect(logUtils.formatLogDate('2026-10-30T12:00:00Z', 'clock')).not.toBe('')
  })

  test('bounded merge', () => {
    const records = Array.from({ length: 600 }, (_, index) => entry(index + 1))
    const latest = logUtils.mergeLogs(records.slice(0, 400), records.slice(300))
    expect(latest).toHaveLength(logUtils.LOG_WINDOW)
    expect(latest[0]?.id).toBe(101)
    expect(latest.at(-1)?.id).toBe(600)
    const older = logUtils.mergeLogs(latest, records.slice(0, 200), true)
    expect(older).toHaveLength(logUtils.LOG_WINDOW)
    expect(older[0]?.id).toBe(1)
    expect(older.at(-1)?.id).toBe(500)
    expect(logUtils.mergeLogs([entry(1)], [entry(1, { message: 'updated' })])[0]?.message).toBe(
      'updated',
    )
  })

  test('filters', () => {
    expect(logUtils.normalizeLogLevels([])).toEqual([])
    expect(logUtils.normalizeLogLevels(['invalid', 'error'])).toEqual(['error'])
    const query = logUtils.logQuery(
      filters({
        levels: ['warning', 'error'],
        node: 'kitchen',
        search: ' native ',
      }),
    )
    expect(Object.fromEntries(query)).toEqual({
      levels: 'warning,error',
      node: 'kitchen',
      search: 'native',
    })
  })

  test('retention status', () => {
    expect(logUtils.parseLogStatus('{"oldest_id":null,"truncated":false}')).toEqual({
      oldest_id: null,
      truncated: false,
    })
    expect(logUtils.parseLogStatus('{"oldest_id":123,"truncated":true}')).toEqual({
      oldest_id: 123,
      truncated: true,
    })
    for (const value of [
      null,
      {},
      { oldest_id: '123', truncated: true },
      { oldest_id: 1, truncated: 'true' },
    ]) {
      expect(() => logUtils.parseLogStatus(JSON.stringify(value))).toThrow()
    }
  })

  test('export', () => {
    const value = entry(1)
    const jsonl = logUtils.serializeLogs([value, entry(2)])
    expect(jsonl.endsWith('\n')).toBe(true)
    expect(
      jsonl
        .trim()
        .split('\n')
        .map((line) => logUtils.parseLogEntry(line)),
    ).toEqual([value, entry(2)])
    expect(logUtils.serializeLogs([])).toBe('')
    expect(logUtils.serializeRenderedLogs([value])).toBe(value.rendered)
    expect(logUtils.serializeRenderedLogs([value, entry(2)])).toBe(
      value.rendered + entry(2).rendered,
    )
    expect(
      logUtils.serializeRenderedLogs([
        entry(3, { rendered: 'first' }),
        entry(4, { rendered: 'second\n' }),
      ]),
    ).toBe('first\nsecond\n')
    expect(logUtils.serializeRenderedLogs([])).toBe('')
    expect(logUtils.logNodeLabel(value)).toBe('Kitchen')
    expect(logUtils.logNodeLabel(entry(3, { logger: 'server', fields: {} }))).not.toBe('')
  })
})

const notifications = mock((_type: string, _title: string, _message: string) => {})
let history: (url: URL, options: RequestInit) => Promise<Response>
let requests: Array<{ url: URL; options: RequestInit }>
let streams: Array<{
  controller: ReadableStreamDefaultController<Uint8Array>
  signal: AbortSignal | null | undefined
}>
const request = async (path: string, options: RequestInit = {}): Promise<Response> => {
  const url = new URL(path, 'https://dash.test')
  requests.push({ url, options })
  if ('/api/logs/stream' !== url.pathname) {
    return history(url, options)
  }
  const body = new ReadableStream<Uint8Array>({
    start: (controller) => {
      streams.push({ controller, signal: options.signal })
      options.signal?.addEventListener(
        'abort',
        () => controller.error(new DOMException('Aborted', 'AbortError')),
        { once: true },
      )
    },
  })
  return new Response(body, { headers: { 'Content-Type': 'text/event-stream' } })
}
const filename = `${import.meta.dir}/../app/composables/useLogs.ts`
const { outputText } = ts.transpileModule(await Bun.file(filename).text(), {
  compilerOptions: { target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.CommonJS },
})
const imports: Record<string, unknown> = {
  vue,
  '~/utils': {
    request,
    notification: notifications,
    parse_api_response: async <T>(response: Response): Promise<T | GenericError> =>
      response.json() as Promise<T>,
  },
  '~/utils/logStream': { LogStreamParser },
  '~/utils/logs': logUtils,
}
const exports = {} as typeof import('../app/composables/useLogs')
const run = new Function('require', 'exports', outputText) as (
  require: (name: string) => unknown,
  exports: typeof import('../app/composables/useLogs'),
) => void
run((name) => imports[name], exports)
const scopes: Array<EffectScope> = []
const createLogs = () => {
  const scope = vue.effectScope()
  scopes.push(scope)
  return scope.run(() => exports.useLogs())!
}
const defer = globalThis.setImmediate
const flush = async () => {
  await new Promise<void>((resolve) => defer(resolve))
  await vue.nextTick()
}
const emit = (index: number, ...entries: Array<LogEntry>) => {
  streams[index]!.controller.enqueue(
    new TextEncoder().encode(
      entries
        .map((value) => `event: log_lines\nid: ${value.id}\ndata: ${JSON.stringify(value)}\n\n`)
        .join(''),
    ),
  )
}

beforeEach(() => {
  jest.useFakeTimers()
  requests = []
  streams = []
  notifications.mockClear()
  history = async () => page([entry(10)])
})
afterEach(async () => {
  for (const scope of scopes.splice(0)) {
    scope.stop()
  }
  await flush()
  jest.useRealTimers()
})

describe('log history', () => {
  test('live cursor', async () => {
    const logs = createLogs()
    await logs.search(filters())
    await flush()
    expect(requests[1]?.url.searchParams.get('after')).toBe('10')
    expect(new Headers(requests[1]?.options.headers).get('Accept')).toBe('text/event-stream')
    emit(0, entry(10), entry(11), entry(11), entry(12))
    await flush()
    expect(logs.entries.value.map((value) => value.id)).toEqual([10, 11, 12])
    streams[0]!.controller.close()
    await flush()
    expect(logs.status.value).toBe('reconnecting')
    jest.advanceTimersByTime(500)
    await flush()
    expect(requests.at(-1)?.url.searchParams.get('after')).toBe('12')
    emit(1, entry(12), entry(13), entry(14))
    await flush()
    expect(logs.entries.value.map((value) => value.id)).toEqual([10, 11, 12, 13, 14])
    expect(requests.filter(({ url }) => '/api/logs' === url.pathname)).toHaveLength(1)
  })

  test('quoted search', async () => {
    const message = 'service "web" failed'
    history = async () => page([entry(10, { message })])
    const logs = createLogs()
    await logs.search(filters({ search: 'service "web"' }))
    await flush()
    expect(requests[0]?.url.searchParams.get('search')).toBe('service "web"')
    expect(requests[1]?.url.searchParams.get('search')).toBe('service "web"')
    emit(0, entry(11, { message }))
    await flush()
    expect(logs.entries.value.map((value) => value.id)).toEqual([10, 11])
  })

  test('filter changes', async () => {
    history = async (url) =>
      url.searchParams.has('before') ? page([entry(1)]) : page([entry(10)], { next_before: 10 })
    const logs = createLogs()
    await logs.search(filters({ levels: ['error'], node: 'kitchen', search: 'failed' }))
    await flush()
    const changed = filters({ levels: ['warning'], node: 'server', search: 'retry' })
    await logs.search(changed)
    await flush()
    expect(streams[0]?.signal?.aborted).toBe(true)
    await logs.loadOlder()
    expect(streams[1]?.signal?.aborted).toBe(false)
    expect(logs.status.value).toBe('live')
    expect(requests.at(-1)?.url.searchParams.get('before')).toBe('10')
    await logs.refresh()
    await flush()
    expect(logs.filters.value).toEqual(changed)
    expect(streams[1]?.signal?.aborted).toBe(true)
    expect(logs.status.value).toBe('live')
    expect(requests.at(-1)?.url.pathname).toBe('/api/logs/stream')
    for (const { url } of requests.slice(2)) {
      expect(url.searchParams.get('levels')).toBe('warning')
      expect(url.searchParams.get('node')).toBe('server')
      expect(url.searchParams.get('search')).toBe('retry')
      expect(url.searchParams.has('logger')).toBe(false)
    }
  })

  test('retention reconnect', async () => {
    history = async (url) =>
      url.searchParams.has('before') ? page([entry(1)]) : page([entry(10)], { next_before: 10 })
    const logs = createLogs()
    await logs.search(filters())
    await flush()
    streams[0]!.controller.enqueue(
      new TextEncoder().encode(
        'event: log_status\nid: 999\ndata: {"oldest_id":11,"truncated":true}\n\n',
      ),
    )
    emit(0, entry(11))
    await flush()
    expect(logs.truncated.value).toBe(true)
    expect(logs.entries.value.map((value) => value.id)).toEqual([10, 11])
    streams[0]!.controller.close()
    await flush()
    jest.advanceTimersByTime(500)
    await flush()
    expect(requests.at(-1)?.url.searchParams.get('after')).toBe('11')
    streams[1]!.controller.enqueue(
      new TextEncoder().encode('event: log_status\ndata: {"oldest_id":11,"truncated":false}\n\n'),
    )
    await flush()
    expect(logs.truncated.value).toBe(true)
    await logs.loadOlder()
    expect(logs.truncated.value).toBe(true)
    await logs.refresh()
    expect(logs.truncated.value).toBe(false)
  })

  test('reconnect backfill', async () => {
    const logs = createLogs()
    await logs.search(filters())
    await flush()
    emit(0, entry(11))
    await flush()
    streams[0]!.controller.close()
    await flush()
    expect(logs.status.value).toBe('reconnecting')
    expect(logs.error.value).not.toBe('')
    expect(notifications).toHaveBeenCalled()
    jest.advanceTimersByTime(500)
    await flush()
    expect(requests.at(-1)?.url.searchParams.get('after')).toBe('11')
    emit(1, entry(11), entry(12))
    await flush()
    expect(logs.entries.value.map((value) => value.id)).toEqual([10, 11, 12])
    expect(logs.status.value).toBe('live')
    expect(logs.error.value).toBe('')
  })

  test('live window', async () => {
    const logs = createLogs()
    await logs.search(filters())
    await flush()
    emit(0, ...Array.from({ length: 550 }, (_, index) => entry(index + 11)))
    await flush()
    expect(logs.entries.value).toHaveLength(logUtils.LOG_WINDOW)
    expect(logs.entries.value[0]?.id).toBe(61)
    expect(logs.entries.value.at(-1)?.id).toBe(560)
    expect(logs.hasOlder.value).toBe(true)
  })

  test('UTF8 chunks', async () => {
    const logs = createLogs()
    await logs.search(filters())
    await flush()
    const value = entry(11, { message: '日本語 🙂' })
    const bytes = new TextEncoder().encode(`event: log_lines\ndata: ${JSON.stringify(value)}\n\n`)
    for (const byte of bytes) {
      streams[0]!.controller.enqueue(new Uint8Array([byte]))
    }
    await flush()
    expect(logs.entries.value.at(-1)?.message).toBe(value.message)
  })

  test('older cursor', async () => {
    history = async (url) =>
      url.searchParams.has('before')
        ? page([entry(1), entry(2)], { next_before: null, latest_id: 99, truncated: true })
        : page([entry(10)], { next_before: 10, latest_id: 99 })
    const logs = createLogs()
    await logs.search(filters({ levels: ['error'], node: 'kitchen', search: 'failed' }))
    await flush()
    expect(requests[1]?.url.searchParams.get('after')).toBe('99')
    await logs.loadOlder()
    expect(streams[0]?.signal?.aborted).toBe(false)
    expect(logs.status.value).toBe('live')
    expect(requests.at(-1)?.url.searchParams.get('before')).toBe('10')
    expect(requests.at(-1)?.url.searchParams.get('search')).toBe('failed')
    expect(logs.entries.value.map((value) => value.id)).toEqual([1, 2, 10])
    expect(logs.hasOlder.value).toBe(false)
    expect(logs.truncated.value).toBe(true)
    await logs.refresh()
    await flush()
    expect(logs.entries.value.map((value) => value.id)).toEqual([10])
    for (const { url } of requests) {
      expect(url.searchParams.get('levels')).toBe('error')
      expect(url.searchParams.get('node')).toBe('kitchen')
      expect(url.searchParams.get('search')).toBe('failed')
      expect(url.searchParams.has('logger')).toBe(false)
    }
  })

  test('older while live', async () => {
    const pending = Promise.withResolvers<Response>()
    history = async (url) =>
      url.searchParams.has('before')
        ? pending.promise
        : page([entry(100), entry(101)], { next_before: 100 })
    const logs = createLogs()
    await logs.search(filters())
    await flush()
    const older = logs.loadOlder()
    expect(logs.loadingOlder.value).toBe(true)
    expect(requests.at(-1)?.url.searchParams.get('before')).toBe('100')
    expect(streams[0]?.signal?.aborted).toBe(false)
    emit(0, entry(102), entry(102))
    await flush()
    expect(logs.entries.value.map((value) => value.id)).toEqual([100, 101, 102])
    pending.resolve(page([entry(98), entry(99), entry(100)], { next_before: 98, latest_id: 999 }))
    await older
    expect(logs.loadingOlder.value).toBe(false)
    expect(logs.entries.value.map((value) => value.id)).toEqual([98, 99, 100, 101, 102])
    expect(streams).toHaveLength(1)
    expect(logs.status.value).toBe('live')
    streams[0]!.controller.close()
    await flush()
    jest.advanceTimersByTime(500)
    await flush()
    expect(requests.at(-1)?.url.searchParams.get('after')).toBe('102')
    emit(1, entry(102), entry(103))
    await flush()
    expect(logs.entries.value.map((value) => value.id)).toEqual([98, 99, 100, 101, 102, 103])
    await logs.loadOlder()
    expect(requests.at(-1)?.url.searchParams.get('before')).toBe('98')
    expect(streams[1]?.signal?.aborted).toBe(false)
  })

  test('older window', async () => {
    history = async (url) =>
      url.searchParams.has('before')
        ? page(Array.from({ length: 100 }, (_, index) => entry(index + 1)))
        : page(
            Array.from({ length: 500 }, (_, index) => entry(index + 101)),
            { next_before: 101 },
          )
    const logs = createLogs()
    await logs.search(filters())
    await flush()
    await logs.loadOlder()
    expect(logs.entries.value).toHaveLength(logUtils.LOG_WINDOW)
    expect(logs.entries.value[0]?.id).toBe(1)
    expect(logs.entries.value.at(-1)?.id).toBe(500)
    emit(0, entry(601))
    await flush()
    expect(streams[0]?.signal?.aborted).toBe(false)
    expect(logs.entries.value).toHaveLength(logUtils.LOG_WINDOW)
    expect(logs.entries.value[0]?.id).toBe(2)
    expect(logs.entries.value.at(-1)?.id).toBe(601)
    await logs.loadOlder()
    expect(requests.at(-1)?.url.searchParams.get('before')).toBe('2')
  })

  test('older failure', async () => {
    history = async (url) =>
      url.searchParams.has('before')
        ? Response.json({ error: { code: 503, message: 'failure-sentinel' } }, { status: 503 })
        : page([entry(10)], { next_before: 10 })
    const logs = createLogs()
    await logs.search(filters())
    await flush()
    await logs.loadOlder()
    expect(logs.error.value).not.toBe('')
    expect(notifications).toHaveBeenCalled()
    expect(logs.loadingOlder.value).toBe(false)
    expect(logs.hasOlder.value).toBe(true)
    expect(streams[0]?.signal?.aborted).toBe(false)
    emit(0, entry(11))
    await flush()
    expect(logs.entries.value.map((value) => value.id)).toEqual([10, 11])
  })

  test('zero levels', async () => {
    const logs = createLogs()
    await logs.search(filters({ levels: [] }))
    expect(requests).toHaveLength(0)
    expect(logs.entries.value).toEqual([])
    expect(logs.hasOlder.value).toBe(false)
    await logs.search(filters({ levels: ['warning', 'error'] }))
    await flush()
    expect(requests[0]?.url.searchParams.get('levels')).toBe('warning,error')
    expect(requests[1]?.url.searchParams.get('levels')).toBe('warning,error')
    await logs.search(filters({ levels: [] }))
    expect(streams[0]?.signal?.aborted).toBe(true)
    jest.advanceTimersByTime(60_000)
    await flush()
    expect(requests).toHaveLength(2)
  })

  test('stale filters', async () => {
    const pending = Promise.withResolvers<Response>()
    history = async (url) =>
      'old' === url.searchParams.get('search') ? pending.promise : page([entry(22)])
    const logs = createLogs()
    const first = logs.search(filters({ search: 'old' }))
    await logs.search(filters({ search: 'new' }))
    pending.resolve(page([entry(1)]))
    await first
    await flush()
    expect(requests[0]?.options.signal?.aborted).toBe(true)
    expect(logs.entries.value.map((value) => value.id)).toEqual([22])
    expect(streams).toHaveLength(1)
  })

  test('retry', async () => {
    history = async () =>
      Response.json({ error: { code: 503, message: 'failure-sentinel' } }, { status: 503 })
    const logs = createLogs()
    await logs.search(filters())
    expect(logs.error.value).not.toBe('')
    expect(logs.loading.value).toBe(false)
    expect(streams).toHaveLength(0)
    history = async () => page([entry(10)])
    await logs.refresh()
    await flush()
    expect(logs.error.value).toBe('')
    expect(logs.status.value).toBe('live')
    scopes[0]!.stop()
    expect(streams[0]?.signal?.aborted).toBe(true)
    jest.advanceTimersByTime(60_000)
    await flush()
    expect(streams).toHaveLength(1)
  })
})
