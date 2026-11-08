import { defineStore } from 'pinia'
import { onScopeDispose, ref, watch } from 'vue'
import { useAuthStore } from '~/store/auth'
import type { RealtimeCallbacks, RealtimeReader, RealtimeSubscription } from '~/types'
import { resolveWebSocketUrl } from '~/utils'
import { parseRealtimeMessage, workspaceErrorMessage } from '~/utils/stacks'

export const useRealtimeStore = defineStore('realtime', () => {
  const auth = useAuthStore()
  const error = ref('')
  const readers = new Map<
    string,
    { subscription: RealtimeSubscription; callbacks: RealtimeCallbacks }
  >()
  let socket: WebSocket | null = null
  let timer: ReturnType<typeof setTimeout> | undefined
  let active = false
  let paused = false
  let retry = 1000
  let nextID = 0

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
      send({
        type: `${subscription.type}.attach`,
        stream: id,
        node: subscription.node,
        resource: subscription.resource,
      })
    }
  }

  const close = () => {
    clearTimeout(timer)
    timer = undefined
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
    error.value = ''
    retry = 1000
  }

  const disconnectReaders = (message: string, manualMessage = message) => {
    for (const [id, reader] of [...readers]) {
      // Only runtime updates resume after a connection loss.
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
        retry = 1000
        if ('type' in parsed && 'error' === parsed.type) {
          const failure = workspaceErrorMessage(parsed.status ?? 0, parsed.error)
          if (!parsed.stream) {
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
              const transient = 429 === parsed.status || 500 <= (parsed.status ?? 0)
              readers.delete(parsed.stream)
              reader.callbacks.onDisconnect(failure)
              if (!transient) {
                reader.callbacks.onRejected(parsed.status ?? 0)
              }
            }
          }
          return
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
  return { error, subscribe, stop, pause, resume, syncRuntime }
})
