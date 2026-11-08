import { computed, onScopeDispose, ref } from 'vue'
import type { GenericError, LogEntry, LogFilters, LogsResponse } from '~/types'
import { notification, parse_api_response, request } from '~/utils'
import { LogStreamParser } from '~/utils/logStream'
import {
  LOG_WINDOW,
  logLevels,
  logQuery,
  mergeLogs,
  parseLogEntry,
  parseLogStatus,
} from '~/utils/logs'

export const useLogs = () => {
  const entries = ref<Array<LogEntry>>([])
  const filters = ref<LogFilters>({ levels: [...logLevels], node: '', search: '' })
  const observedNodes = ref(new Map<string, string>())
  const loading = ref(false)
  const loadingOlder = ref(false)
  const error = ref('')
  const truncated = ref(false)
  const status = ref<'connecting' | 'live' | 'reconnecting'>('connecting')
  const olderCursor = ref<number | null>(null)
  const hasOlder = computed(() => null !== olderCursor.value)
  let historyController: AbortController | null = null
  let streamController: AbortController | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let reconnectAttempt = 0
  let disposed = false
  let version = 0
  let cursor = 0

  const reportError = (cause: unknown): void => {
    const message = cause instanceof Error ? cause.message : "Couldn't load logs."
    if (error.value !== message) {
      notification('error', 'Logs unavailable', message)
    }
    error.value = message
  }

  const observe = (incoming: Array<LogEntry>): void => {
    for (const entry of incoming) {
      const id = entry.fields.node_id
      const name = entry.fields.node_name
      if ('string' === typeof id) {
        observedNodes.value.set(id, 'string' === typeof name ? name : id)
      }
    }
  }

  const stopStream = (): void => {
    streamController?.abort()
    streamController = null
    if (null !== reconnectTimer) {
      clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
  }

  const fetchHistory = async (older = false): Promise<boolean> => {
    historyController?.abort()
    const controller = new AbortController()
    historyController = controller
    const requestVersion = version
    loading.value = !older
    loadingOlder.value = older
    if (!filters.value.levels.length) {
      entries.value = []
      olderCursor.value = null
      error.value = ''
      loading.value = false
      loadingOlder.value = false
      historyController = null
      return true
    }
    const params = logQuery(filters.value)
    params.set('limit', '100')
    if (older && null !== olderCursor.value) {
      params.set('before', String(olderCursor.value))
    }
    try {
      const response = await request(`/api/logs?${params}`, { signal: controller.signal })
      const body = await parse_api_response<LogsResponse>(response)
      if (disposed || controller.signal.aborted || requestVersion !== version) {
        return false
      }
      if ('error' in body) {
        throw new Error(body.error.message)
      }
      if (!response.ok) {
        throw new Error(`Couldn't load logs (${response.status}).`)
      }
      observe(body.entries)
      entries.value = mergeLogs(older ? entries.value : [], body.entries, older)
      truncated.value = older ? truncated.value || body.truncated : body.truncated
      olderCursor.value = body.next_before
      if (!older) {
        cursor = body.latest_id
      }
      error.value = ''
      return true
    } catch (cause) {
      if (!disposed && !controller.signal.aborted && requestVersion === version) {
        reportError(cause)
      }
      return false
    } finally {
      if (historyController === controller) {
        historyController = null
        loading.value = false
        loadingOlder.value = false
      }
    }
  }

  const connectStream = async (): Promise<void> => {
    if (disposed || streamController || !filters.value.levels.length) {
      return
    }
    const controller = new AbortController()
    const requestVersion = version
    streamController = controller
    status.value = reconnectAttempt ? 'reconnecting' : 'connecting'
    const params = logQuery(filters.value)
    params.set('after', String(cursor))
    let reader: ReadableStreamDefaultReader<Uint8Array> | null = null
    let retry = true
    try {
      const response = await request(`/api/logs/stream?${params}`, {
        signal: controller.signal,
        headers: { Accept: 'text/event-stream' },
      })
      if (controller.signal.aborted || disposed || requestVersion !== version) {
        return
      }
      if (!response.ok) {
        retry = 401 !== response.status && 403 !== response.status
        const body = await parse_api_response<GenericError>(response)
        throw new Error(
          'error' in body ? body.error.message : `Log stream unavailable (${response.status}).`,
        )
      }
      if (!response.body) {
        throw new Error('The log stream has no response body.')
      }
      status.value = 'live'
      error.value = ''
      reconnectAttempt = 0
      reader = response.body.getReader()
      const decoder = new TextDecoder()
      const parser = new LogStreamParser()
      while (!disposed && !controller.signal.aborted && requestVersion === version) {
        const result = await reader.read()
        if (controller.signal.aborted || disposed || requestVersion !== version) {
          return
        }
        if (result.done) {
          throw new Error('Log stream disconnected. Reconnecting with history backfill.')
        }
        for (const event of parser.feed(decoder.decode(result.value, { stream: true }))) {
          if ('log_status' === event.event) {
            truncated.value = parseLogStatus(event.data).truncated || truncated.value
            continue
          }
          if ('log_lines' !== event.event) {
            continue
          }
          const entry = parseLogEntry(event.data)
          if (entry.id <= cursor) {
            continue
          }
          cursor = entry.id
          observe([entry])
          const overflow = LOG_WINDOW <= entries.value.length
          entries.value = mergeLogs(entries.value, [entry])
          if (overflow) {
            olderCursor.value = entries.value[0]?.id ?? null
          }
        }
      }
    } catch (cause) {
      if (!disposed && !controller.signal.aborted && requestVersion === version) {
        reportError(cause)
        status.value = 'reconnecting'
      }
    } finally {
      if (reader) {
        try {
          await reader.cancel()
        } catch {
          // The connection may already have closed.
        }
        reader.releaseLock()
      }
      if (streamController === controller) {
        streamController = null
      }
      if (!disposed && !controller.signal.aborted && requestVersion === version && retry) {
        reconnectTimer = setTimeout(
          () => {
            reconnectTimer = null
            void connectStream()
          },
          Math.min(15_000, 500 * 2 ** reconnectAttempt++),
        )
      }
    }
  }

  const refresh = async (): Promise<void> => {
    version += 1
    stopStream()
    const requestVersion = version
    if (await fetchHistory()) {
      if (!disposed && requestVersion === version) {
        void connectStream()
      }
    }
  }

  const search = async (value: LogFilters): Promise<void> => {
    filters.value = { ...value, levels: [...value.levels] }
    entries.value = []
    olderCursor.value = null
    truncated.value = false
    cursor = 0
    reconnectAttempt = 0
    await refresh()
  }

  const loadOlder = async (): Promise<void> => {
    if (!hasOlder.value || loading.value || loadingOlder.value) {
      return
    }
    await fetchHistory(true)
  }

  onScopeDispose(() => {
    disposed = true
    version += 1
    historyController?.abort()
    stopStream()
  })

  return {
    entries,
    filters,
    observedNodes,
    loading,
    loadingOlder,
    error,
    status,
    truncated,
    hasOlder,
    refresh,
    search,
    loadOlder,
  }
}
