import { defineStore } from 'pinia'
import { computed, onScopeDispose, ref, watch } from 'vue'
import { useWorkspaceStream } from '~/composables/useWorkspaceStream'
import { useAuthStore } from '~/store/auth'
import { useRealtimeStore } from '~/store/realtime'
import type {
  NodesResponse,
  StackEvent,
  SettingsSection,
  StackAction,
  StackFilter,
  UpdateNodeOrderRequest,
  WorkspaceNode,
} from '~/types'
import { notification, parse_api_response, request } from '~/utils'
import { workspaceErrorMessage, parseWorkspaceEvent } from '~/utils/stacks'

const stackKey = (node: string, stack: string) => JSON.stringify([node, stack])

export const useWorkspaceStore = defineStore('workspace', () => {
  const auth = useAuthStore()
  const realtime = useRealtimeStore()
  const nodes = ref<Array<WorkspaceNode>>([])
  const stackSearch = ref('')
  const stackFilter = ref<StackFilter>('all')
  const sidebarOpen = ref(false)
  const settingsOpen = ref(false)
  const settingsSection = ref<SettingsSection>('nodes')
  const ready = ref(false)
  const error = ref('')
  if (import.meta.client) {
    localStorage.removeItem('workspace_operations')
  }
  const stackListeners = new Set<(event: StackEvent) => void>()
  const submitting = ref<Record<string, boolean>>({})
  const imageCheckRequests = ref<Record<string, boolean>>({})
  const savingNodeOrder = ref(false)
  let controller: AbortController | null = null
  let jobsController = new AbortController()

  const workspaceStream = useWorkspaceStream(
    (message) => {
      const event = parseWorkspaceEvent(message.event, message.data)
      if (!event) {
        return
      }
      if ('nodes.changed' === event.event) {
        void load()
        return
      }
      const node = nodes.value.find((node) => event.node === node.id)
      if ('stack.action' === event.event || 'stack.output' === event.event) {
        if ('stack.action' === event.event) {
          const stack = node?.stacks.find((stack) => event.name === stack.name)
          if (stack) {
            stack.busy = 'start' === event.phase
          }
          if ('exit' === event.phase && undefined !== event.exit_code && 0 !== event.exit_code) {
            notification(
              'error',
              'Stack action failed',
              `${event.action} failed for stack "${event.name}" on node "${node?.name || event.node}": ${event.message || `exit code ${event.exit_code}`}`,
            )
          }
        }
        for (const listener of stackListeners) {
          listener(event)
        }
        return
      }
      if (!node) {
        return
      }
      switch (event.event) {
        case 'node.snapshot':
          node.health = event.health
          node.stacks = event.stacks
          node.loading = event.stacks.some((stack) => stack.loading)
          node.connection = 'connected'
          node.error = ''
          break
        case 'stack.runtime': {
          const runtime = { ...event.runtime, loading: false }
          const index = node.stacks.findIndex((stack) => runtime.name === stack.name)
          if (-1 === index) {
            node.stacks.push(runtime)
          } else {
            node.stacks[index] = runtime
          }
          node.loading = node.stacks.some((stack) => stack.loading)
          break
        }
        case 'stack.removed':
          node.stacks = node.stacks.filter((stack) => event.name !== stack.name)
          node.loading = node.stacks.some((stack) => stack.loading)
          break
        case 'node.state':
          node.connection = event.state
          node.paused = 'paused' === event.state
          node.error =
            'unavailable' === event.state ? event.message || "Couldn't connect to this node." : ''
          if ('connected' !== event.state) {
            node.loading = false
          }
          if (node.paused) {
            node.stacks = []
            node.health = null
          }
          break
      }
    },
    () => {
      for (const node of nodes.value) {
        if (node.paused) {
          continue
        }
        node.connection = 'reconnecting'
        node.loading = false
      }
    },
    (status) => {
      if (401 === status) {
        reset()
      } else {
        for (const node of nodes.value) {
          if (node.paused) {
            continue
          }
          node.connection = 'unavailable'
          node.loading = false
        }
      }
      error.value = workspaceErrorMessage(status, "Couldn't load nodes.")
      ready.value = true
    },
  )

  const loading = computed(() => !ready.value || nodes.value.some((node) => node.loading))
  const streamError = workspaceStream.error

  const onStackEvent = (listener: (event: StackEvent) => void) => {
    stackListeners.add(listener)
    return () => {
      stackListeners.delete(listener)
    }
  }
  const isSubmitting = (node: string, stack: string) =>
    submitting.value[stackKey(node, stack)] ?? false
  const isCheckingImages = (node?: string, stack?: string, container?: string): boolean =>
    nodes.value.some(
      (item) =>
        !item.paused &&
        (!node || node === item.id) &&
        (imageCheckRequests.value[item.id] ||
          ('connected' === item.connection &&
            item.stacks.some(
              (runtime) =>
                (!stack || stack === runtime.name) &&
                runtime.services.some(
                  (service) =>
                    (!container || container === service.name) &&
                    'checking' === service.image_update?.status,
                ),
            ))),
    )
  function reset() {
    controller?.abort()
    workspaceStream.stop()
    jobsController.abort()
    jobsController = new AbortController()
    submitting.value = {}
    realtime.stop()
    imageCheckRequests.value = {}
    nodes.value = []
    ready.value = false
    settingsOpen.value = false
    sidebarOpen.value = false
  }

  const openSettings = (section: SettingsSection) => {
    settingsSection.value = section
    settingsOpen.value = true
  }

  const load = async () => {
    controller?.abort()
    const current = new AbortController()
    controller = current
    error.value = ''
    try {
      const response = await request('/api/nodes', { signal: current.signal })
      const json = await parse_api_response<NodesResponse>(response)
      if (401 === response.status) {
        reset()
        error.value = workspaceErrorMessage(response.status, "Couldn't load nodes.")
        ready.value = true
        return
      }
      if (!response.ok || 'error' in json) {
        throw new Error(workspaceErrorMessage(response.status, "Couldn't load nodes."))
      }
      if (current.signal.aborted) {
        return
      }
      nodes.value = json.nodes.map((node): WorkspaceNode => {
        const previous = nodes.value.find((item) => node.id === item.id)
        if (node.paused) {
          return {
            ...node,
            stacks: [],
            health: null,
            error: '',
            loading: false,
            connection: 'paused',
          }
        }
        return {
          ...node,
          stacks: previous?.stacks ?? [],
          health: previous?.health ?? null,
          error: '',
          loading: previous?.loading ?? true,
          connection:
            'paused' === previous?.connection ? 'loading' : (previous?.connection ?? 'loading'),
        }
      })
      ready.value = true
      workspaceStream.start({ type: 'runtime' })
      realtime.syncRuntime()
    } catch (cause) {
      if (!current.signal.aborted) {
        error.value =
          cause instanceof Error && !(cause instanceof TypeError)
            ? cause.message
            : "Couldn't load nodes."
        ready.value = true
        for (const node of nodes.value) {
          if (node.paused) {
            continue
          }
          node.connection = 'unavailable'
          node.loading = false
        }
        notification('error', 'Stacks', error.value)
      }
    }
  }

  const moveNode = async (id: string, target: string) => {
    const from = nodes.value.findIndex((node) => id === node.id)
    const to = nodes.value.findIndex((node) => target === node.id)
    if (savingNodeOrder.value || -1 === from || -1 === to || from === to) {
      return
    }
    const order = nodes.value.map((node) => node.id)
    order.splice(from, 1)
    order.splice(to, 0, id)
    const input: UpdateNodeOrderRequest = { nodes: order }
    const signal = jobsController.signal
    savingNodeOrder.value = true
    try {
      const response = await request('/api/settings/node-order', {
        method: 'PUT',
        body: JSON.stringify(input),
        signal,
      })
      const json = await parse_api_response<{ status: 'ok' }>(response)
      if (401 === response.status) {
        reset()
      }
      if ('error' in json) {
        throw new Error(json.error.message)
      }
      if (!response.ok || 'ok' !== json.status) {
        throw new Error("Couldn't save node order.")
      }
      if (!signal.aborted) {
        const positions = new Map(order.map((node, index) => [node, index]))
        nodes.value.sort(
          (a, b) => (positions.get(a.id) ?? order.length) - (positions.get(b.id) ?? order.length),
        )
      }
    } catch (cause) {
      if (!signal.aborted) {
        notification(
          'error',
          'Node order',
          cause instanceof Error && !(cause instanceof TypeError)
            ? cause.message
            : "Couldn't save node order.",
        )
      }
    } finally {
      savingNodeOrder.value = false
    }
  }

  const runAction = async (
    node: string,
    stack: string,
    action: StackAction | 'delete',
    service?: string,
  ) => {
    if (nodes.value.find((item) => node === item.id)?.paused) {
      throw new Error('Node is paused. Resume it before running stack actions.')
    }
    const key = stackKey(node, stack)
    const signal = jobsController.signal
    const url = `/api/nodes/${encodeURIComponent(node)}/stacks/${encodeURIComponent(stack)}`
    submitting.value[key] = true
    try {
      const response = await request('delete' === action ? url : `${url}/actions`, {
        signal,
        method: 'delete' === action ? 'DELETE' : 'POST',
        ...('delete' !== action
          ? { body: JSON.stringify({ action, ...(service ? { service } : {}) }) }
          : {}),
      })
      const json = await parse_api_response<{ status: 'accepted' }>(response)
      if (401 === response.status) {
        reset()
      }
      if ('error' in json) {
        throw new Error(json.error.message)
      }
      if (!response.ok || 'accepted' !== json.status) {
        throw new Error(workspaceErrorMessage(response.status, "Couldn't run this action."))
      }
      return json
    } finally {
      submitting.value[key] = false
    }
  }

  const requestImageCheck = async (node: string, stack?: string, container?: string) => {
    imageCheckRequests.value[node] = true
    try {
      const base = `/api/nodes/${encodeURIComponent(node)}`
      const url = stack ? `${base}/stacks/${encodeURIComponent(stack)}` : base
      const response = await request(`${url}/images/check`, {
        method: 'POST',
        ...(container ? { body: JSON.stringify({ container }) } : {}),
      })
      const json = await parse_api_response<{ status: 'checking' }>(response)
      if (401 === response.status) {
        reset()
      }
      if ('error' in json) {
        throw new Error(workspaceErrorMessage(response.status, json.error.message))
      }
      if (202 !== response.status || 'checking' !== json.status) {
        throw new Error(workspaceErrorMessage(response.status, "Couldn't check for updates."))
      }
    } finally {
      imageCheckRequests.value[node] = false
    }
  }

  const checkImages = async (node?: string, stack?: string, container?: string) => {
    if (isCheckingImages(node, stack, container)) {
      return
    }
    // Capture every enabled node, including unavailable agents.
    const targets = nodes.value.filter((item) => !item.paused && (!node || node === item.id))
    if (0 === targets.length) {
      return
    }
    const results = await Promise.allSettled(
      targets.map((target) => requestImageCheck(target.id, stack, container)),
    )
    const failures = results.flatMap((result, index) => {
      if ('fulfilled' === result.status) {
        return []
      }
      const target = targets[index]!
      const message =
        result.reason instanceof Error && !(result.reason instanceof TypeError)
          ? result.reason.message
          : "Couldn't check for updates."
      return [`${target.name}: ${message}`]
    })
    const queued = results.length - failures.length
    if (0 < failures.length) {
      notification(
        0 < queued ? 'warning' : 'error',
        'Update checks',
        `${0 < queued ? `Requested checks on ${queued} of ${targets.length} nodes. ` : ''}${failures.join(' ')}`,
      )
    } else {
      notification(
        'info',
        'Checking for updates',
        container
          ? `Container ${container} on ${targets[0]!.name}.`
          : stack
            ? `Stack ${stack} on ${targets[0]!.name}.`
            : node
              ? `All containers on ${targets[0]!.name}.`
              : `All containers on ${targets.length} nodes.`,
      )
    }
  }

  watch(
    () => auth.authenticated,
    (authenticated) => {
      if (!authenticated) {
        reset()
      }
    },
  )
  onScopeDispose(reset)

  return {
    nodes,
    stackSearch,
    stackFilter,
    sidebarOpen,
    settingsOpen,
    settingsSection,
    openSettings,
    ready,
    error,
    loading,
    streamError,
    submitting,
    imageCheckRequests,
    savingNodeOrder,
    moveNode,
    load,
    onStackEvent,
    isSubmitting,
    runAction,
    checkImages,
    isCheckingImages,
  }
})
