<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useHead, useRoute } from '#app'
import { definePageMeta } from '#imports'
import { onBeforeRouteLeave } from 'vue-router'
import UAlert from '@nuxt/ui/components/Alert.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UIcon from '@nuxt/ui/components/Icon.vue'
import StatCard from '~/components/StatCard.vue'
import { useWorkspaceStore } from '~/store/workspace'
import type { NodeInfo, NodeResourceStatus } from '~/types'
import { parse_api_response, request } from '~/utils'
import { formatAgentUptime, formatMemoryBytes } from '~/utils/node'
import { workspaceErrorMessage } from '~/utils/stacks'

definePageMeta({ layout: 'workspace' })

const workspace = useWorkspaceStore()
const { nodes } = storeToRefs(workspace)
const route = useRoute()
const nodeId = computed(() => String(route.params.node))
const node = computed(() => nodes.value.find((item) => nodeId.value === item.id))
const connected = computed(() => 'connected' === node.value?.connection)
const info = ref<NodeInfo | null>(null)
const loading = ref(false)
const error = ref('')
let controller: AbortController | null = null
let retryTimer: ReturnType<typeof setTimeout> | undefined
let retryDelay = 0

useHead(() => ({ title: node.value?.name ?? nodeId.value }))

const connectionLabel = computed(() => {
  switch (node.value?.connection) {
    case 'connected':
      return 'Connected'
    case 'loading':
      return 'Connecting'
    case 'reconnecting':
      return 'Reconnecting'
    case 'paused':
      return 'Paused'
    default:
      return 'Unavailable'
  }
})

const status = computed<NodeResourceStatus | null>(() => {
  const current = node.value
  if (!current) {
    return null
  }
  if ('paused' === current.connection) {
    return {
      text: 'Node is paused. Resume it from Nodes to reconnect.',
      icon: 'i-lucide-pause',
      tone: 'muted',
    }
  }
  if (!connected.value) {
    return {
      text: current.error || connectionLabel.value,
      icon: 'i-lucide-network',
      tone: 'warning',
    }
  }
  if (error.value) {
    return {
      text: info.value ? `${error.value} Showing previous sample.` : error.value,
      icon: 'i-lucide-circle-alert',
      tone: 'error',
    }
  }
  if (loading.value) {
    return {
      text: info.value ? 'Refreshing stats...' : 'Loading stats...',
      icon: 'i-lucide-loader-circle',
      tone: 'muted',
      spin: true,
    }
  }
  return null
})
const details = computed(() => [
  {
    label: 'Node version',
    value: node.value?.health?.version ? `v${node.value.health.version}` : 'Unavailable',
  },
  { label: 'Address', value: node.value?.public_host || 'Not configured' },
  {
    label: 'System',
    value:
      [
        info.value?.runtime.operating_system || node.value?.health?.os,
        info.value?.runtime.architecture || node.value?.health?.arch,
      ]
        .filter(Boolean)
        .join(' / ') || 'Unavailable',
  },
  {
    label: 'Node uptime',
    value: node.value?.health?.started_at
      ? formatAgentUptime(node.value.health.started_at)
      : 'Unavailable',
  },
  ...(info.value
    ? [
        { label: 'Host', value: info.value.runtime.name },
        { label: 'Runtime version', value: info.value.runtime.version },
        { label: 'Kernel', value: info.value.runtime.kernel_version },
        { label: 'Storage driver', value: info.value.runtime.storage_driver },
        {
          label: 'Containers',
          value: `${info.value.runtime.containers} total, ${info.value.runtime.containers_stopped} stopped, ${info.value.runtime.containers_paused} paused`,
        },
      ]
    : []),
])

const clearRetry = () => {
  clearTimeout(retryTimer)
  retryTimer = undefined
}

const stopStats = () => {
  clearRetry()
  controller?.abort()
  controller = null
  loading.value = false
  retryDelay = 0
}

const scheduleRetry = () => {
  if (retryTimer || !connected.value) {
    return
  }
  retryDelay = retryDelay ? Math.min(retryDelay * 2, 15000) : 1000
  retryTimer = setTimeout(() => {
    retryTimer = undefined
    void refreshStats()
  }, retryDelay)
}

const refreshStats = async () => {
  if (!connected.value || loading.value) {
    return
  }
  clearRetry()
  const current = new AbortController()
  controller = current
  loading.value = true
  let retryable = false
  try {
    const response = await request(`/api/nodes/${encodeURIComponent(nodeId.value)}/info`, {
      signal: current.signal,
    })
    retryable = 429 === response.status || 500 <= response.status
    const json = await parse_api_response<NodeInfo>(response)
    if (!response.ok || 'error' in json) {
      throw new Error(workspaceErrorMessage(response.status, 'Could not load stats.'))
    }
    if (!current.signal.aborted) {
      info.value = json
      error.value = ''
      retryDelay = 0
    }
  } catch (cause) {
    if (!current.signal.aborted) {
      error.value =
        cause instanceof Error && !(cause instanceof TypeError)
          ? cause.message
          : 'Could not load stats.'
      if (!(cause instanceof Error) || cause instanceof TypeError) {
        retryable = true
      }
      if (retryable) {
        scheduleRetry()
      }
    }
  } finally {
    if (controller === current) {
      controller = null
      loading.value = false
    }
  }
}

watch(
  [nodeId, connected],
  ([id, isConnected], previous) => {
    const idChanged = id !== previous?.[0]
    if (idChanged) {
      stopStats()
      info.value = null
      error.value = ''
    }
    if (!isConnected) {
      stopStats()
      if ('paused' === node.value?.connection) {
        info.value = null
        error.value = ''
      }
      return
    }
    const reconnected = false === previous?.[1]
    if (idChanged || reconnected || !info.value || error.value) {
      void refreshStats()
    }
  },
  { immediate: true },
)

onBeforeRouteLeave(() => {
  stopStats()
})
onBeforeUnmount(() => {
  stopStats()
})
</script>

<template>
  <div class="min-w-0 space-y-6">
    <UAlert
      v-if="!node && !workspace.error && !workspace.streamError"
      color="neutral"
      variant="soft"
      title="Node not found"
    />
    <template v-else-if="node">
      <div
        v-if="(status && !workspace.error && !workspace.streamError) || info"
        class="flex flex-wrap items-center justify-end gap-2"
      >
        <p
          v-if="status && !workspace.error && !workspace.streamError"
          role="status"
          class="mr-auto inline-flex min-w-0 items-center gap-1.5 text-xs"
          :class="{
            'text-warning': 'warning' === status.tone,
            'text-error': 'error' === status.tone,
            'text-muted': 'muted' === status.tone,
          }"
        >
          <UIcon
            :name="status.icon"
            class="size-3.5 shrink-0"
            :class="{ 'animate-spin': status.spin }"
          />
          <span class="min-w-0">{{ status.text }}</span>
        </p>
        <UButton
          v-if="info"
          icon="i-lucide-refresh-cw"
          color="neutral"
          variant="outline"
          size="sm"
          :loading="loading"
          :disabled="!connected || loading"
          @click="refreshStats"
          >Refresh stats</UButton
        >
      </div>

      <template v-if="info">
        <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <StatCard label="CPU cores" :value="info.runtime.cpus" icon="i-lucide-cpu" />
          <StatCard
            label="Memory capacity"
            :value="formatMemoryBytes(info.runtime.memory_bytes)"
            icon="i-lucide-memory-stick"
          />
          <StatCard
            label="Running containers"
            :value="info.runtime.containers_running"
            icon="i-lucide-container"
          />
          <StatCard label="Images" :value="info.runtime.images" icon="i-lucide-layers" />
        </div>

        <dl class="app-frame grid gap-4 p-4 sm:grid-cols-2 xl:grid-cols-4">
          <div v-for="detail in details" :key="detail.label" class="min-w-0 space-y-1">
            <dt class="text-xs text-muted">{{ detail.label }}</dt>
            <dd class="wrap-break-word text-sm">{{ detail.value || 'Unavailable' }}</dd>
          </div>
        </dl>

        <section class="app-frame overflow-hidden">
          <header class="border-b border-default px-4 py-3">
            <h2 class="text-sm font-semibold">Resource usage</h2>
          </header>
          <p v-if="0 === info.containers.length" class="p-4 text-sm text-muted">
            No container resource usage reported.
          </p>
          <div v-else class="overflow-x-auto">
            <table class="w-full text-left text-sm">
              <thead class="border-b border-default text-xs text-muted">
                <tr>
                  <th class="px-4 py-3 font-medium">Name</th>
                  <th class="px-4 py-3 font-medium">CPU</th>
                  <th class="px-4 py-3 font-medium">Memory</th>
                  <th class="px-4 py-3 font-medium">Network</th>
                  <th class="px-4 py-3 font-medium">Disk</th>
                  <th class="px-4 py-3 font-medium">PIDs</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-default">
                <tr v-for="container in info.containers" :key="container.id">
                  <td class="px-4 py-3">{{ container.name }}</td>
                  <td class="whitespace-nowrap px-4 py-3 tabular-nums">{{ container.cpu }}</td>
                  <td class="whitespace-nowrap px-4 py-3 tabular-nums">
                    {{ container.memory }}
                    <span class="text-xs text-muted">({{ container.memory_percent }})</span>
                  </td>
                  <td class="whitespace-nowrap px-4 py-3 tabular-nums">
                    {{ container.network_io }}
                  </td>
                  <td class="whitespace-nowrap px-4 py-3 tabular-nums">
                    {{ container.block_io }}
                  </td>
                  <td class="px-4 py-3 tabular-nums">{{ container.pids }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
        <p class="text-xs text-muted">Updated {{ new Date(info.checked_at).toLocaleString() }}</p>
      </template>
    </template>
  </div>
</template>
