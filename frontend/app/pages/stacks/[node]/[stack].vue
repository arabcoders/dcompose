<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useClipboard, useNow, useStorage } from '@vueuse/core'
import { navigateTo, useHead, useRoute, useRouter } from '#app'
import { definePageMeta } from '#imports'
import { onBeforeRouteLeave, onBeforeRouteUpdate } from 'vue-router'
import type { DropdownMenuItem } from '@nuxt/ui'
import UAlert from '@nuxt/ui/components/Alert.vue'
import UBadge from '@nuxt/ui/components/Badge.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UCard from '@nuxt/ui/components/Card.vue'
import UDropdownMenu from '@nuxt/ui/components/DropdownMenu.vue'
import UIcon from '@nuxt/ui/components/Icon.vue'
import UTooltip from '@nuxt/ui/components/Tooltip.vue'
import Editor from '~/components/Editor.vue'
import CreateCopyModal from '~/components/CreateCopyModal.vue'
import StatCard from '~/components/StatCard.vue'
import ContainerShell from '~/components/stacks/ContainerShell.vue'
import StackLogs from '~/components/stacks/StackLogs.vue'
import { useDialog } from '~/composables/useDialog'
import { useWorkspaceStream } from '~/composables/useWorkspaceStream'
import { useWorkspaceStore } from '~/store/workspace'
import { useAuthStore } from '~/store/auth'
import type {
  ContainerStats,
  Service,
  Stack,
  StackAction,
  StackStatsTotals,
  StatCardProps,
} from '~/types'
import { notification, parse_api_response, request } from '~/utils'
import {
  workspaceErrorMessage,
  imageUpdateLabel,
  imageUpdateTooltip,
  stackStatusLabel,
  containerCreatedAt,
  containerUptime,
  parseStackStats,
  aggregateStackStats,
} from '~/utils/stacks'

definePageMeta({ layout: 'workspace' })

const route = useRoute()
const router = useRouter()
const { confirmDialog } = useDialog()
const workspace = useWorkspaceStore()
const { nodes } = storeToRefs(workspace)
const auth = useAuthStore()
const now = useNow({ interval: 1000 })
const node = computed(() => String(route.params.node))
const name = computed(() => String(route.params.stack))
const base = computed(() => `/api/nodes/${encodeURIComponent(node.value)}`)
const stackUrl = computed(() => `${base.value}/stacks/${encodeURIComponent(name.value)}`)
const selectedNode = computed(() => nodes.value.find((item) => node.value === item.id))
useHead(() => ({ title: `${name.value} @${selectedNode.value?.name ?? node.value}` }))

const publicHost = computed(() => selectedNode.value?.public_host ?? '')
const portLinks = (service: Service) =>
  (service.port_bindings ?? [])
    .filter(
      (port, index, ports) =>
        index ===
        ports.findIndex(
          (other) =>
            port.host_port === other.host_port &&
            port.container_port === other.container_port &&
            port.protocol === other.protocol,
        ),
    )
    .map((port) => {
      const host = publicHost.value
      const address = host.includes(':') && !host.startsWith('[') ? `[${host}]` : host
      const scheme = 443 === port.host_port || 443 === port.container_port ? 'https' : 'http'
      return {
        ...port,
        label:
          0 < port.host_port
            ? `${port.host_port}:${port.container_port}/${port.protocol}`
            : `${port.container_port}/${port.protocol}`,
        href:
          address && 'tcp' === port.protocol && 0 < port.host_port
            ? `${scheme}://${address}:${port.host_port}/`
            : null,
      }
    })
const connected = computed(() => 'connected' === selectedNode.value?.connection)
const loading = ref(true)
const saving = ref(false)
const shellContainer = ref<string | null>(null)
const showCreateCopy = ref(false)
const error = ref('')
const actionError = ref('')
const selectedRuntime = computed(() =>
  selectedNode.value?.stacks.find((stack) => name.value === stack.name),
)
const runtime = computed(() => {
  const state = selectedRuntime.value
  if (
    !state ||
    (state.loading && 'unknown' === state.status && 0 === state.services.length && !state.error)
  ) {
    return null
  }
  return state
})
const runtimePending = computed(
  () =>
    !isNew.value &&
    (selectedRuntime.value?.loading ?? (!runtime.value && selectedNode.value?.loading)),
)
const connectionError = computed(() => {
  if ('paused' === selectedNode.value?.connection) {
    return 'Node is paused. Resume it from Nodes to reconnect.'
  }
  if ('connected' !== selectedNode.value?.connection) {
    return selectedNode.value?.error || 'Status may be out of date.'
  }
  if (!selectedNode.value.loading && !selectedRuntime.value && !isNew.value && !saving.value) {
    return 'Stack not found on this node.'
  }
  return selectedRuntime.value?.error || ''
})
const compose = ref('')
const env = ref('')
const savedCompose = ref('')
const savedEnv = ref('')
const { copy, copyPending } = useClipboard({ legacy: true })
const copyConfiguration = async (content: string, label: string) => {
  try {
    await copy(content)
    notification('success', `${label} copied`, '')
  } catch {
    notification('error', label, "Couldn't copy. Select the content and copy it manually.")
  }
}

const showCompose = useStorage('stacks_show_compose', false)
const showEnv = ref(false)
const configurationLoaded = ref(false)
const configurationLoading = ref(false)
const isNew = ref(false)
const editing = ref(false)
const dirty = computed(() => compose.value !== savedCompose.value || env.value !== savedEnv.value)
const busy = computed(
  () => saving.value || workspace.isSubmitting(node.value, name.value) || !!runtime.value?.busy,
)
const active = computed(() =>
  runtime.value?.services.some((service) => 'running' === service.state),
)
const stats = ref<Array<ContainerStats> | null>(null)
const statsStream = useWorkspaceStream(
  (message) => {
    if ('stack.stats' === message.event) {
      stats.value = parseStackStats(message.data).containers
    }
  },
  () => {
    stats.value = null
  },
)
const statsError = statsStream.error
const startStats = () => {
  statsStream.stop()
  stats.value = null
  if (connected.value && active.value && !isNew.value) {
    statsStream.start({ type: 'stats', node: node.value, resource: name.value })
  }
}
const servicesWithStats = computed(() =>
  (runtime.value?.services ?? []).map((service) => ({
    ...service,
    stats: stats.value?.find((item) => service.name === item.name),
  })),
)
const stackTotals = computed(() => {
  if (connected.value && !active.value) {
    return aggregateStackStats([])
  }
  return stats.value ? aggregateStackStats(stats.value) : undefined
})
const resourceCards = (
  stats?: StackStatsTotals & { memory_percent?: string },
): Array<StatCardProps> => [
  { label: 'CPU', value: stats?.cpu || '-', icon: 'i-lucide-cpu' },
  {
    label: 'Memory',
    value: stats?.memory || '-',
    hint: stats?.memory_percent,
    icon: 'i-lucide-memory-stick',
  },
  { label: 'Network I/O', value: stats?.network_io || '-', icon: 'i-lucide-network' },
  { label: 'Block I/O', value: stats?.block_io || '-', icon: 'i-lucide-hard-drive' },
  { label: 'PIDs', value: stats?.pids || '-', icon: 'i-lucide-list-tree' },
]
const checkingImages = computed(() => workspace.isCheckingImages(node.value, name.value))
const unavailable = computed(
  () =>
    busy.value ||
    loading.value ||
    configurationLoading.value ||
    runtimePending.value ||
    !!error.value ||
    !!connectionError.value,
)
const moreActions = computed<Array<Array<DropdownMenuItem>>>(() => {
  const checkUpdates: DropdownMenuItem = {
    label: 'Check for updates',
    icon: checkingImages.value ? 'i-lucide-loader-circle' : 'i-lucide-refresh-cw',
    ui: { itemLeadingIcon: checkingImages.value ? 'animate-spin' : '' },
    disabled:
      unavailable.value || checkingImages.value || isNew.value || !runtime.value?.services.length,
    onSelect: () => checkImages(),
  }

  if (editing.value) {
    return [[checkUpdates]]
  }

  return [
    [
      {
        label: 'Restart',
        icon: 'i-lucide-rotate-cw',
        disabled: unavailable.value || isNew.value || !active.value,
        onSelect: () => run('restart'),
      },
    ],
    [
      checkUpdates,
      {
        label: 'Create copy',
        icon: 'i-lucide-copy',
        disabled:
          unavailable.value ||
          isNew.value ||
          dirty.value ||
          !nodes.value.some(
            (target) => node.value !== target.id && 'connected' === target.connection,
          ),
        onSelect: () => {
          showCreateCopy.value = true
        },
      },
      {
        label: 'Pull images',
        icon: 'i-lucide-download',
        disabled: unavailable.value || isNew.value || dirty.value,
        onSelect: () => run('pull'),
      },
      {
        label: 'Bring down',
        icon: 'i-lucide-square',
        disabled: unavailable.value || isNew.value,
        onSelect: () => confirmAction('down'),
      },
    ],
    [
      {
        label: 'Delete stack',
        icon: 'i-lucide-trash-2',
        color: 'error',
        disabled: unavailable.value || isNew.value,
        onSelect: () => confirmAction('delete'),
      },
    ],
  ]
})
const containerActions = (service: Service): Array<DropdownMenuItem> => {
  const checking = workspace.isCheckingImages(node.value, name.value, service.name)
  const items: Array<DropdownMenuItem> = [
    {
      label: 'Check for updates',
      icon: checking ? 'i-lucide-loader-circle' : 'i-lucide-refresh-cw',
      ui: { itemLeadingIcon: checking ? 'animate-spin' : '' },
      disabled: unavailable.value || checking,
      onSelect: () => checkImages(service.name),
    },
  ]

  if (!editing.value) {
    if ('running' === service.state) {
      items.push({
        label: 'Stop',
        icon: 'i-lucide-square',
        disabled: unavailable.value,
        onSelect: () => run('stop', service.service),
      })
    }

    items.push({
      label: 'Restart',
      icon: 'i-lucide-rotate-cw',
      disabled: unavailable.value || 'running' !== service.state,
      onSelect: () => run('restart', service.service),
    })
  }

  return items
}
let controller: AbortController | null = null

const api = async <T extends object>(
  url: string,
  options: RequestInit = {},
  failureMessage = "Couldn't run this action.",
): Promise<T> => {
  const response = await request(url, { signal: controller?.signal, ...options })
  const json = await parse_api_response<T>(response)
  if ('error' in json) {
    // Keep validation output so errors in the Compose file can be corrected.
    throw new Error(
      400 === response.status
        ? json.error.message
        : workspaceErrorMessage(response.status, failureMessage),
    )
  }
  if (!response.ok) {
    throw new Error(workspaceErrorMessage(response.status, failureMessage))
  }
  return json
}

const report = (cause: unknown, fallback = "Couldn't run this action.") => {
  const message = cause instanceof Error && !(cause instanceof TypeError) ? cause.message : fallback
  notification('error', 'Stacks', message)
  return message
}

const checkImages = async (container?: string) => {
  if (
    unavailable.value ||
    isNew.value ||
    workspace.isCheckingImages(node.value, name.value, container) ||
    !runtime.value?.services.length
  ) {
    return
  }
  await workspace.checkImages(node.value, name.value, container)
}

const revealConfiguration = async (edit = false) => {
  if (unavailable.value) {
    return
  }
  const signal = controller?.signal
  showCompose.value = true
  configurationLoading.value = true
  try {
    if (!configurationLoaded.value) {
      const json = await api<Stack>(stackUrl.value, {}, "Couldn't load stack files.")
      if (signal?.aborted) {
        return
      }
      compose.value = json.compose
      env.value = json.env
      savedCompose.value = json.compose
      savedEnv.value = json.env
      configurationLoaded.value = true
    }
    if (edit) {
      showEnv.value = false
      editing.value = true
    }
  } catch (cause) {
    if (!signal?.aborted) {
      report(cause, "Couldn't load stack files.")
    }
  } finally {
    if (!signal?.aborted) {
      configurationLoading.value = false
    }
  }
}

const toggleCompose = () => {
  if (showCompose.value) {
    showEnv.value = false
    showCompose.value = false
  } else {
    void revealConfiguration()
  }
}

const load = () => {
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  saving.value = false
  error.value = ''
  actionError.value = ''
  compose.value = ''
  showCreateCopy.value = false
  env.value = ''
  savedCompose.value = ''
  savedEnv.value = ''
  showEnv.value = false
  configurationLoaded.value = false
  configurationLoading.value = false
  isNew.value = '1' === route.query.new
  editing.value = isNew.value

  try {
    if (!selectedNode.value) {
      throw new Error('Node not found.')
    }
    if ('op' in route.query) {
      const { op: _legacyOp, ...query } = route.query
      void router.replace({ query })
    }

    if (isNew.value) {
      if (selectedNode.value.stacks.some((stack) => name.value === stack.name)) {
        throw new Error('A stack with this name already exists. Choose another name.')
      }
      compose.value =
        'services:\n  app:\n    image: nginx:alpine\n    ports:\n      - "8080:80"\n    restart: unless-stopped\n'
      savedCompose.value = compose.value
      configurationLoaded.value = true
      showCompose.value = true
    }
  } catch (cause) {
    if (!current.signal.aborted) {
      isNew.value = false
      editing.value = false
      error.value = report(cause)
    }
  } finally {
    if (!current.signal.aborted) {
      loading.value = false
    }
  }
}

const save = async (): Promise<boolean> => {
  if (unavailable.value) {
    return false
  }
  const signal = controller?.signal
  const yaml = compose.value
  const environment = env.value
  saving.value = true
  try {
    await api<{ name: string }>(
      stackUrl.value,
      {
        method: 'PUT',
        body: JSON.stringify({ compose: yaml, env: environment }),
      },
      "Couldn't save stack files.",
    )
    if (signal?.aborted) {
      return false
    }
    savedCompose.value = yaml
    savedEnv.value = environment
    isNew.value = false
    editing.value = false
    await router.replace({ query: { ...route.query, new: undefined } })
    if (signal?.aborted) {
      return false
    }
    notification('success', 'Stack saved', '')
    return !signal?.aborted
  } catch (cause) {
    if (!signal?.aborted) {
      report(cause, "Couldn't save stack files.")
    }
    return false
  } finally {
    if (!signal?.aborted) {
      saving.value = false
    }
  }
}

const run = async (action: StackAction | 'delete', service?: string) => {
  const signal = controller?.signal
  const targetNode = node.value
  const targetStack = name.value
  if (unavailable.value) {
    return
  }
  if ('deploy' === action && (dirty.value || isNew.value) && !(await save())) {
    return
  }
  if (signal?.aborted) {
    return
  }
  actionError.value = ''
  saving.value = true
  try {
    await workspace.runAction(targetNode, targetStack, action, service)
    if (signal?.aborted || targetNode !== node.value || targetStack !== name.value) {
      return
    }
    if ('deploy' === action) {
      editing.value = false
    }
  } catch (cause) {
    if (!signal?.aborted) {
      const target = service ? `service "${service}" in ` : ''
      const message =
        cause instanceof Error && !(cause instanceof TypeError)
          ? cause.message
          : "Couldn't run this action."
      actionError.value = `Could not ${action} ${target}stack "${targetStack}" on node "${selectedNode.value?.name ?? targetNode}": ${message}`
      notification('error', 'Stacks', actionError.value)
    }
  } finally {
    if (!signal?.aborted) {
      saving.value = false
    }
  }
}

const discard = async () => {
  compose.value = savedCompose.value
  env.value = savedEnv.value
  editing.value = false
  if (isNew.value) {
    await navigateTo('/')
  }
}

const confirmAction = async (action: 'down' | 'delete') => {
  const signal = controller?.signal
  const { status } = await confirmDialog({
    title: 'delete' === action ? 'Delete stack?' : 'Bring stack down?',
    message:
      'delete' === action
        ? `Deletes containers, the Compose file and .env file for ${name.value}. Keeps volumes and other files.`
        : `Removes containers and networks for ${name.value}. Keeps files and volumes.`,
    confirmText: 'delete' === action ? 'Delete' : 'Bring down',
    confirmColor: 'delete' === action ? 'error' : 'warning',
    cancelText: 'Cancel',
  })
  if (!status || signal?.aborted) {
    return
  }

  await run(action)
}

const allowLeave = async () =>
  !auth.authenticated ||
  !dirty.value ||
  (
    await confirmDialog({
      title: 'Unsaved changes',
      message: 'Discard unsaved changes?',
      confirmText: 'Discard',
      confirmColor: 'warning',
    })
  ).status
onBeforeRouteLeave(allowLeave)
onBeforeRouteUpdate((to, from) => {
  if (to.path !== from.path) {
    return allowLeave()
  }
})
const beforeUnload = (event: BeforeUnloadEvent) => {
  if (auth.authenticated && dirty.value) {
    event.preventDefault()
  }
}
onMounted(() => window.addEventListener('beforeunload', beforeUnload))
watch(
  () => [node.value, name.value, connected.value],
  () => {
    shellContainer.value = null
  },
)
watch(() => [node.value, name.value], load, { immediate: true })
watch([node, name, connected, active, isNew], startStats, { immediate: true })
const actionCompleted = (action: StackAction | 'delete', exitCode?: number) => {
  if ('delete' === action && 0 === exitCode) {
    void navigateTo('/')
  }
}
watch(
  () => [
    node.value,
    name.value,
    showCompose.value,
    busy.value,
    runtimePending.value,
    selectedRuntime.value?.error,
    selectedNode.value?.connection,
  ],
  () => {
    if (showCompose.value && !isNew.value && !configurationLoaded.value && !unavailable.value) {
      void revealConfiguration()
    }
  },
  { immediate: true },
)
onBeforeUnmount(() => {
  controller?.abort()
  window.removeEventListener('beforeunload', beforeUnload)
})
</script>

<template>
  <div class="min-w-0 space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex flex-wrap items-center gap-2.5">
        <UBadge
          size="sm"
          variant="subtle"
          icon="i-lucide-layers"
          :color="
            'running' === runtime?.status
              ? 'success'
              : 'partial' === runtime?.status
                ? 'warning'
                : 'neutral'
          "
          >{{ isNew ? 'New draft' : stackStatusLabel(runtime?.status ?? 'unknown') }}</UBadge
        >
        <UBadge v-if="dirty" color="warning" variant="subtle" icon="i-lucide-pencil"
          >Unsaved</UBadge
        >
        <UBadge
          v-if="!loading && (runtime || isNew)"
          color="neutral"
          variant="soft"
          size="sm"
          icon="i-lucide-container"
          class="tabular-nums"
        >
          {{ runtime?.services.length ?? 0 }}
          {{ 1 === runtime?.services.length ? 'container' : 'containers' }}
        </UBadge>
      </div>
      <div class="ml-auto flex flex-wrap items-center justify-end gap-2">
        <template v-if="!loading && (runtime || isNew)">
          <UButton
            v-if="active"
            size="sm"
            icon="i-lucide-square"
            color="neutral"
            variant="outline"
            :disabled="unavailable || isNew || editing"
            @click="run('stop')"
            >Stop</UButton
          >
          <UButton
            v-else
            size="sm"
            icon="i-lucide-play"
            :disabled="unavailable || isNew || editing"
            @click="run('start')"
            >Start</UButton
          >
          <UButton
            size="sm"
            icon="i-lucide-cloud-download"
            color="neutral"
            variant="outline"
            :disabled="unavailable || isNew || editing"
            @click="run('update')"
            >Update</UButton
          >
        </template>
        <UDropdownMenu
          v-if="!loading && (runtime || isNew)"
          :items="moreActions"
          :content="{ align: 'end' }"
          :modal="false"
        >
          <UButton
            size="sm"
            color="neutral"
            variant="outline"
            icon="i-lucide-settings-2"
            trailing-icon="i-lucide-chevron-down"
            aria-label="Stack actions"
            :disabled="unavailable"
            >Actions</UButton
          >
        </UDropdownMenu>
        <UButton
          color="neutral"
          variant="outline"
          icon="i-lucide-refresh-cw"
          size="sm"
          :loading="loading"
          :disabled="isNew || busy"
          @click="error ? load() : workspace.load()"
          >Refresh</UButton
        >
      </div>
    </div>

    <div v-if="loading || runtimePending" class="flex items-center gap-2 py-8 text-sm text-muted">
      <UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" />
      {{ runtime ? 'Refreshing stack...' : 'Loading stack...' }}
    </div>
    <template v-if="!workspace.error && !workspace.streamError">
      <UAlert v-if="error" color="error" :title="error" />
      <UAlert
        v-else-if="connectionError"
        :color="'paused' === selectedNode?.connection ? 'neutral' : 'warning'"
        :icon="'paused' === selectedNode?.connection ? 'i-lucide-pause' : undefined"
        :title="connectionError"
      />
    </template>
    <template v-if="!loading">
      <div v-if="runtime && !isNew" class="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
        <StatCard v-for="stat in resourceCards(stackTotals)" :key="stat.icon" v-bind="stat" />
      </div>
      <UAlert
        v-if="statsError && connected && active"
        color="warning"
        icon="i-lucide-chart-no-axes-combined"
        :title="statsError"
      >
        <template #actions>
          <UButton color="neutral" variant="outline" icon="i-lucide-refresh-cw" @click="startStats"
            >Retry stats</UButton
          >
        </template>
      </UAlert>
      <StackLogs
        v-if="!isNew"
        :key="`${node}/${name}`"
        :node="node"
        :stack="name"
        :connected="connected"
        :paused="'paused' === selectedNode?.connection"
        :draft="'draft' === runtime?.status"
        :busy="runtime?.busy"
        :action-error="actionError"
        :show-errors="!error && !connectionError"
        @completed="actionCompleted"
      />

      <div v-if="runtime || isNew" class="stack-workspace grid min-w-0 items-start gap-4">
        <section class="min-w-0 space-y-4" aria-label="Containers">
          <UAlert
            v-if="!runtime?.services.length"
            color="neutral"
            variant="soft"
            icon="i-lucide-container"
            title="No containers"
          />
          <template v-else>
            <StatCard
              v-for="service in servicesWithStats"
              :key="service.name"
              :label="service.service"
              :value="service.image"
              icon="i-lucide-container"
              :aria-label="service.name"
            >
              <template #label>
                <h3
                  class="break-all text-sm font-semibold tracking-normal text-highlighted normal-case"
                >
                  {{ service.service }}
                  <span v-if="service.service !== service.name">({{ service.name }})</span>
                </h3>
              </template>
              <template #value>
                <span class="break-all font-mono text-xs text-muted">{{ service.image }}</span>
              </template>
              <template #actions>
                <span
                  v-if="service.created_at"
                  class="mr-auto inline-flex items-center gap-1.5 text-xs text-muted"
                >
                  <UIcon name="i-lucide-calendar" class="size-3.5 shrink-0" />
                  Created
                  <UTooltip :text="service.created_at">
                    <time :datetime="service.created_at" tabindex="0">
                      {{ containerCreatedAt(service.created_at, now) }}
                    </time>
                  </UTooltip>
                </span>
                <template v-if="!editing">
                  <UButton
                    v-if="'running' === service.state"
                    size="sm"
                    icon="i-lucide-terminal"
                    :aria-label="`Open shell in ${service.name}`"
                    :disabled="unavailable"
                    @click="shellContainer = service.name"
                    >Shell</UButton
                  >
                  <UButton
                    v-else
                    size="sm"
                    icon="i-lucide-play"
                    :aria-label="`Start ${service.service}`"
                    :disabled="unavailable"
                    @click="run('start', service.service)"
                    >Start</UButton
                  >
                </template>
                <UDropdownMenu
                  :items="containerActions(service)"
                  :content="{ align: 'end' }"
                  :modal="false"
                >
                  <UButton
                    size="sm"
                    color="neutral"
                    variant="outline"
                    icon="i-lucide-settings-2"
                    trailing-icon="i-lucide-chevron-down"
                    :aria-label="`Actions for ${service.name}`"
                    :loading="workspace.isCheckingImages(node, name, service.name)"
                    :disabled="unavailable"
                    >Actions</UButton
                  >
                </UDropdownMenu>
              </template>
              <template #status>
                <UBadge
                  v-if="containerUptime(service, now)"
                  icon="i-lucide-clock"
                  color="neutral"
                  size="sm"
                  variant="subtle"
                >
                  Up {{ containerUptime(service, now) }}
                </UBadge>
                <UBadge
                  icon="i-lucide-container"
                  :color="
                    'unhealthy' === service.health || 'dead' === service.state
                      ? 'error'
                      : 'running' === service.state
                        ? 'starting' === service.health
                          ? 'warning'
                          : 'success'
                        : 'neutral'
                  "
                  size="sm"
                  variant="subtle"
                  >{{ `${service.state}${service.health ? ` / ${service.health}` : ''}` }}</UBadge
                >
                <UTooltip
                  v-if="service.image_update"
                  :text="imageUpdateTooltip(service.image_update)"
                  :ui="{ text: 'whitespace-pre-line' }"
                >
                  <UBadge
                    icon="i-lucide-package"
                    size="sm"
                    variant="subtle"
                    tabindex="0"
                    :color="
                      'available' === service.image_update.status
                        ? 'warning'
                        : 'current' === service.image_update.status
                          ? 'success'
                          : 'error' === service.image_update.status
                            ? 'error'
                            : 'neutral'
                    "
                    >{{ imageUpdateLabel(service.image_update) }}</UBadge
                  >
                </UTooltip>
              </template>
              <div v-if="'running' === service.state" class="flex flex-wrap gap-1.5">
                <UTooltip
                  v-for="stat in resourceCards(service.stats)"
                  :key="stat.icon"
                  :text="stat.label"
                >
                  <UBadge
                    :icon="stat.icon"
                    color="neutral"
                    variant="subtle"
                    size="sm"
                    tabindex="0"
                    class="tabular-nums"
                  >
                    {{ stat.value }}<span v-if="stat.hint">({{ stat.hint }})</span>
                  </UBadge>
                </UTooltip>
              </div>
              <div
                v-if="service.port_bindings?.length || service.ports"
                class="flex flex-wrap items-center gap-1.5"
                :class="{ 'mt-3': 'running' === service.state }"
              >
                <template v-if="service.port_bindings?.length">
                  <template v-for="port in portLinks(service)" :key="port.label">
                    <UTooltip v-if="port.href" :text="port.href">
                      <UBadge
                        as="a"
                        :href="port.href"
                        target="_blank"
                        rel="noopener noreferrer"
                        color="primary"
                        variant="subtle"
                        size="sm"
                        icon="i-lucide-network"
                        trailing-icon="i-lucide-external-link"
                        class="font-mono hover:bg-elevated focus-visible:outline-2 focus-visible:outline-primary"
                        >{{ port.label }}</UBadge
                      >
                    </UTooltip>
                    <UBadge
                      v-else
                      color="neutral"
                      variant="subtle"
                      size="sm"
                      icon="i-lucide-network"
                      class="font-mono"
                      >{{ port.label }}</UBadge
                    >
                  </template>
                </template>
                <template v-else>
                  <UBadge
                    v-for="port in service.ports
                      .split(',')
                      .map((port) => port.trim())
                      .filter(Boolean)"
                    :key="port"
                    color="neutral"
                    variant="subtle"
                    size="sm"
                    icon="i-lucide-network"
                    class="font-mono"
                    >{{ port }}</UBadge
                  >
                </template>
              </div>
            </StatCard>
          </template>
        </section>
        <div class="min-w-0 space-y-4">
          <UCard
            as="section"
            class="compose-panel min-w-0 rounded-none"
            aria-labelledby="stack-compose"
            :ui="{
              header: 'flex flex-wrap items-center justify-between gap-2 px-4 py-3 sm:px-4',
              body: 'p-0 sm:p-0',
            }"
          >
            <template #header>
              <h2 id="stack-compose">
                <UButton
                  color="neutral"
                  variant="ghost"
                  size="sm"
                  icon="i-lucide-file-code-2"
                  class="p-0 text-sm font-semibold text-highlighted hover:bg-transparent"
                  :trailing-icon="showCompose ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'"
                  :loading="configurationLoading"
                  :disabled="!showCompose && unavailable"
                  aria-label="Toggle Compose file"
                  :aria-expanded="showCompose"
                  aria-controls="stack-compose-content"
                  @click="toggleCompose"
                  >Compose file</UButton
                >
              </h2>
              <div class="ml-auto flex flex-wrap items-center justify-end gap-2">
                <UButton
                  size="sm"
                  color="neutral"
                  variant="outline"
                  icon="i-lucide-copy"
                  aria-label="Copy Compose file"
                  :disabled="copyPending || configurationLoading || !configurationLoaded"
                  @click="copyConfiguration(compose, 'Compose file')"
                  >Copy</UButton
                >
                <template v-if="editing">
                  <UButton
                    size="sm"
                    icon="i-lucide-rocket"
                    :disabled="unavailable"
                    @click="run('deploy')"
                  >
                    {{ dirty || isNew ? 'Save and deploy' : 'Deploy' }}
                  </UButton>
                  <UButton
                    size="sm"
                    color="neutral"
                    variant="outline"
                    icon="i-lucide-save"
                    :loading="saving"
                    :disabled="unavailable || (!dirty && !isNew)"
                    @click="save"
                    >Save</UButton
                  >
                  <UButton
                    size="sm"
                    color="neutral"
                    variant="outline"
                    icon="i-lucide-undo-2"
                    :disabled="busy"
                    @click="discard"
                    >Discard</UButton
                  >
                </template>
                <UButton
                  v-else
                  size="sm"
                  icon="i-lucide-pencil"
                  color="neutral"
                  variant="outline"
                  :disabled="unavailable"
                  :loading="configurationLoading"
                  @click="revealConfiguration(true)"
                  >Edit</UButton
                >
              </div>
            </template>
            <div v-if="showCompose" id="stack-compose-content">
              <Editor
                :key="`${node}/${name}/compose`"
                :content="compose"
                :read-only="!editing || busy"
                height="clamp(26rem, 65vh, 48rem)"
                @updated="editing && !busy && (compose = $event)"
              />
            </div>
          </UCard>
          <UCard
            v-if="editing && showCompose"
            as="section"
            class="min-w-0 rounded-none"
            aria-labelledby="stack-env"
            :ui="{
              header: 'flex items-center justify-between gap-2 px-4 py-3 sm:px-4',
              body: 'p-0 sm:p-0',
            }"
          >
            <template #header>
              <h2 id="stack-env">
                <UButton
                  color="neutral"
                  variant="ghost"
                  size="sm"
                  icon="i-lucide-file-key"
                  class="p-0 text-sm font-semibold text-highlighted hover:bg-transparent"
                  :trailing-icon="showEnv ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'"
                  aria-label="Toggle .env"
                  :aria-expanded="showEnv"
                  aria-controls="stack-env-content"
                  @click="showEnv = !showEnv"
                  >.env</UButton
                >
              </h2>
              <div class="flex items-center gap-2">
                <UButton
                  size="sm"
                  color="neutral"
                  variant="outline"
                  icon="i-lucide-copy"
                  aria-label="Copy .env file"
                  :disabled="copyPending || configurationLoading || !configurationLoaded"
                  @click="copyConfiguration(env, '.env file')"
                  >Copy</UButton
                >
              </div>
            </template>
            <div v-if="showEnv" id="stack-env-content" class="compose-panel">
              <Editor
                :key="`${node}/${name}/env`"
                :content="env"
                mode="ini"
                :read-only="busy"
                height="14rem"
                @updated="!busy && (env = $event)"
              />
            </div>
          </UCard>
        </div>
      </div>
    </template>

    <CreateCopyModal v-model:open="showCreateCopy" :node="node" :stack="name" />
    <ContainerShell
      v-if="shellContainer && connected"
      :key="`${node}/${name}/${shellContainer}`"
      :node="node"
      :stack="name"
      :container="shellContainer"
      @close="shellContainer = null"
    />
  </div>
</template>

<style scoped>
@media (min-width: 1280px) {
  .stack-workspace {
    grid-template-columns: minmax(0, 0.95fr) minmax(0, 1.05fr);
  }
}

.compose-panel :deep(.editor-host) {
  font-size: 14px;
}
</style>
