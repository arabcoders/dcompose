import { defineStore } from 'pinia'
import { onScopeDispose, ref, watch } from 'vue'
import { useAuthStore } from '~/store/auth'
import type {
  ContainerStats,
  RealtimeCallbacks,
  RealtimeReader,
  RealtimeSubscription,
  StackStatsSample,
} from '~/types'
import { resolveWebSocketUrl } from '~/utils'
import { parseRealtimeMessage, workspaceErrorMessage } from '~/utils/stacks'

export const useRealtimeStore = defineStore('realtime', () => {
  const auth = useAuthStore()
  const error = ref('')
  const stackStats = ref(new Map<string, Map<string, StackStatsSample>>())
  const readers = new Map<
    string,
    { subscription: RealtimeSubscription; callbacks: RealtimeCallbacks }
  >()
  let socket: WebSocket | null = null
  let timer: ReturnType<typeof setTimeout> | undefined
  let streamTimer: ReturnType<typeof setTimeout> | undefined
  let active = false
  let paused = false
  let retry = 1000
  let streamRetry = 1000
  let nextID = 0
  const retryingStreams = new Set<string>()

  const transient = (status?: number): boolean =>
    undefined !== status && (429 === status || 500 <= status)

  const stackStatsSample = (node: string, stack: string): StackStatsSample | null =>
    stackStats.value.get(node)?.get(stack) ?? null

  const rememberStackStats = (
    node: string,
    stack: string,
    containers: Array<ContainerStats>,
    receivedAt = Date.now(),
  ): StackStatsSample => {
    const sample = { containers, receivedAt }
    let samples = stackStats.value.get(node)
    if (!samples) {
      samples = new Map()
      stackStats.value.set(node, samples)
    }
    samples.set(stack, sample)
    return sample
  }

  const forgetStackStats = (node: string, stack: string) => {
    const samples = stackStats.value.get(node)
    if (!samples) {
      return
    }
    samples.delete(stack)
    if (0 === samples.size) {
      stackStats.value.delete(node)
    }
  }

  const forgetNodeStackStats = (node: string) => {
    stackStats.value.delete(node)
  }

  const retainStackStats = (nodes: Array<string>) => {
    const keep = new Set(nodes)
    for (const node of [...stackStats.value.keys()]) {
      if (!keep.has(node)) {
        stackStats.value.delete(node)
      }
    }
  }

  const retainNodeStackStats = (node: string, stacks: Array<string>) => {
    const samples = stackStats.value.get(node)
    if (!samples) {
      return
    }
    const keep = new Set(stacks)
    for (const stack of [...samples.keys()]) {
      if (!keep.has(stack)) {
        samples.delete(stack)
      }
    }
    if (0 === samples.size) {
      stackStats.value.delete(node)
    }
  }

  const clearStackStats = () => {
    stackStats.value = new Map()
  }

  const send = (message: object): boolean => {
    if (!socket || WebSocket.OPEN !== socket.readyState) {
      return false
    }
    try {
      socket.send(JSON.stringify(message))
      return true
    } catch {
      return false
    }
  }

  const attach = (id: string, subscription: RealtimeSubscription) => {
    if ('runtime' === subscription.type) {
      return
    }
    if ('shell' === subscription.type) {
      send({ type: 'shell.attach', stream: id, node: subscription.node, shell: subscription.shell })
    } else {
      const container = 'logs' === subscription.type ? subscription.container : undefined
      send({
        type: `${subscription.type}.attach`,
        stream: id,
        node: subscription.node,
        resource: subscription.resource,
        ...(container ? { container } : {}),
      })
    }
  }

  const clearStreamRetries = () => {
    if (streamTimer) {
      clearTimeout(streamTimer)
      streamTimer = undefined
    }
    retryingStreams.clear()
    streamRetry = 1000
  }

  const scheduleStreamRetry = (id: string) => {
    retryingStreams.add(id)
    if (streamTimer) {
      return
    }
    const delay = streamRetry
    streamRetry = Math.min(streamRetry * 2, 15000)
    streamTimer = setTimeout(() => {
      streamTimer = undefined
      const ids = [...retryingStreams]
      retryingStreams.clear()
      if (!socket || WebSocket.OPEN !== socket.readyState) {
        return
      }
      for (const key of ids) {
        const reader = readers.get(key)
        if (reader) {
          attach(key, reader.subscription)
        }
      }
    }, delay)
  }

  const close = () => {
    clearTimeout(timer)
    timer = undefined
    clearStreamRetries()
    if (socket) {
      socket.onopen = null
      socket.onmessage = null
      socket.onerror = null
      socket.onclose = null
      socket.close()
      socket = null
    }
  }

  const stop = () => {
    active = false
    paused = false
    close()
    const previous = [...readers.values()]
    readers.clear()
    for (const reader of previous) {
      reader.callbacks.onDisconnect('Connection closed.')
    }
    clearStackStats()
    error.value = ''
    retry = 1000
  }

  const disconnectReaders = (message: string, manualMessage = message) => {
    for (const [id, reader] of [...readers]) {
      const manual = 'logs' === reader.subscription.type || 'shell' === reader.subscription.type
      if (manual) {
        readers.delete(id)
      }
      reader.callbacks.onDisconnect(manual ? manualMessage : message)
    }
  }

  const pause = () => {
    paused = true
    close()
    disconnectReaders('Connection closed. Reconnect to continue.')
  }

  const resume = () => {
    if (!paused) {
      return
    }
    paused = false
    if (active && auth.authenticated) {
      connect()
    }
  }

  const failed = () => {
    close()
    error.value = 'Connection lost. Reconnecting...'
    disconnectReaders(error.value, 'Connection lost. Try again.')
    if (active) {
      timer = setTimeout(connect, retry)
      retry = Math.min(retry * 2, 15000)
    }
  }

  const connect = () => {
    if (!active || paused || socket) {
      return
    }
    try {
      const current = new WebSocket(resolveWebSocketUrl('/api/events'))
      socket = current
      timer = setTimeout(failed, 10000)
      current.onopen = () => {
        if (socket !== current) {
          return
        }
        clearTimeout(timer)
        timer = undefined
        error.value = ''
        for (const [id, reader] of readers) {
          attach(id, reader.subscription)
        }
      }
      current.onmessage = (message: MessageEvent<unknown>) => {
        if (socket !== current) {
          return
        }
        let parsed: ReturnType<typeof parseRealtimeMessage>
        try {
          if ('string' !== typeof message.data) {
            throw new Error("Couldn't read updates.")
          }
          parsed = parseRealtimeMessage(message.data)
        } catch {
          failed()
          return
        }
        if ('type' in parsed && 'error' === parsed.type) {
          const failure = workspaceErrorMessage(parsed.status ?? 0, parsed.error)
          if (!parsed.stream) {
            if (transient(parsed.status)) {
              failed()
              return
            }
            const previous = [...readers.values()]
            stop()
            error.value = failure
            if (401 === parsed.status) {
              auth.invalidate()
            }
            for (const reader of previous) {
              reader.callbacks.onRejected(parsed.status ?? 0)
            }
          } else {
            const reader = readers.get(parsed.stream)
            if (reader) {
              reader.callbacks.onDisconnect(failure)
              if (transient(parsed.status) && 'stats' === reader.subscription.type) {
                scheduleStreamRetry(parsed.stream)
              } else {
                readers.delete(parsed.stream)
                if (!transient(parsed.status)) {
                  reader.callbacks.onRejected(parsed.status ?? 0)
                }
              }
            }
          }
          return
        }
        retry = 1000
        if ('event' in parsed && 'stack.stats' === parsed.event) {
          streamRetry = 1000
        }
        const targets = parsed.stream
          ? [readers.get(parsed.stream)].filter((reader) => undefined !== reader)
          : [...readers.values()].filter((reader) => 'runtime' === reader.subscription.type)
        for (const reader of targets) {
          try {
            reader.callbacks.onMessage(parsed)
          } catch (cause) {
            reader.callbacks.onDisconnect(
              cause instanceof Error ? cause.message : "Couldn't read updates.",
            )
          }
        }
      }
      current.onerror = current.onclose = () => {
        if (socket === current) {
          failed()
        }
      }
    } catch {
      failed()
    }
  }

  const subscribe = (
    subscription: RealtimeSubscription,
    callbacks: RealtimeCallbacks,
  ): RealtimeReader => {
    const id = `reader-${++nextID}`
    readers.set(id, { subscription, callbacks })
    active = true
    if (paused && ('logs' === subscription.type || 'shell' === subscription.type)) {
      readers.delete(id)
      callbacks.onDisconnect('Account update in progress. Reconnect after it finishes.')
    } else if (socket && WebSocket.OPEN === socket.readyState) {
      attach(id, subscription)
    } else if (!timer) {
      connect()
    }
    return {
      stop: () => {
        retryingStreams.delete(id)
        if (readers.delete(id) && 'runtime' !== subscription.type) {
          send({ type: 'stream.detach', stream: id })
        }
      },
      // Never queue terminal input, resize, or close commands for reconnect.
      send: (message) =>
        'shell' === subscription.type && readers.has(id) && send({ ...message, stream: id }),
    }
  }

  watch(
    () => auth.authenticated,
    (authenticated) => {
      if (!authenticated) {
        stop()
      }
    },
  )
  onScopeDispose(stop)
  const syncRuntime = () => send({ type: 'workspace.sync' })
  return {
    error,
    subscribe,
    stop,
    pause,
    resume,
    syncRuntime,
    stackStatsSample,
    rememberStackStats,
    forgetStackStats,
    forgetNodeStackStats,
    retainStackStats,
    retainNodeStackStats,
    clearStackStats,
  }
})
