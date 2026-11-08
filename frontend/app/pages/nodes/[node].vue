<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useHead, useRoute } from '#app'
import { definePageMeta } from '#imports'
import { onBeforeRouteLeave } from 'vue-router'
import UAlert from '@nuxt/ui/components/Alert.vue'
import UBadge from '@nuxt/ui/components/Badge.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UIcon from '@nuxt/ui/components/Icon.vue'
import StatCard from '~/components/StatCard.vue'
import { useWorkspaceStore } from '~/store/workspace'
import type { NodeInfo } from '~/types'
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

const refreshStats = async () => {
  if (!connected.value || loading.value) {
    return
  }
  const current = new AbortController()
  controller = current
  loading.value = true
  error.value = ''
  try {
    const response = await request(`/api/nodes/${encodeURIComponent(nodeId.value)}/info`, {
      signal: current.signal,
    })
    const json = await parse_api_response<NodeInfo>(response)
    if (!response.ok || 'error' in json) {
      throw new Error(workspaceErrorMessage(response.status, 'Could not load stats.'))
    }
    if (!current.signal.aborted) {
      info.value = json
    }
  } catch (cause) {
    if (!current.signal.aborted) {
      error.value =
        cause instanceof Error && !(cause instanceof TypeError)
          ? cause.message
          : 'Could not load stats.'
    }
  } finally {
    if (controller === current) {
      loading.value = false
    }
  }
}

watch(
  [nodeId, connected],
  ([id, isConnected], previous) => {
    if (id !== previous?.[0]) {
      controller?.abort()
      loading.value = false
      info.value = null
      error.value = ''
    }
    if (!isConnected) {
      controller?.abort()
      loading.value = false
      if ('paused' === node.value?.connection) {
        info.value = null
        error.value = ''
      }
      return
    }
    if (!info.value) {
      void refreshStats()
    }
  },
  { immediate: true },
)

onBeforeRouteLeave(() => {
  controller?.abort()
})
onBeforeUnmount(() => {
  controller?.abort()
})
</script>

<template>
  <div class="min-w-0 space-y-6">
    <UAlert v-if="!node" color="neutral" variant="soft" title="Node not found" />
    <template v-else>
      <template v-if="!workspace.error && !workspace.streamError">
        <UAlert
          v-if="'paused' === node.connection"
          color="neutral"
          variant="soft"
          icon="i-lucide-pause"
          title="Node is paused"
          description="Resume it from Nodes to reconnect."
        />
        <UAlert
          v-else-if="!connected"
          color="warning"
          variant="soft"
          icon="i-lucide-network"
          :title="node.error || 'Waiting for the node to connect.'"
        />
        <UAlert v-else-if="loading && !info" color="info" variant="soft" title="Loading stats...">
          <template #leading>
            <UIcon name="i-lucide-loader-circle" class="size-5 shrink-0 animate-spin" />
          </template>
        </UAlert>
        <UAlert
          v-else-if="error"
          color="error"
          variant="soft"
          icon="i-lucide-circle-alert"
          :title="error"
        >
          <template v-if="!info" #actions>
            <UButton
              color="neutral"
              variant="outline"
              icon="i-lucide-refresh-cw"
              @click="refreshStats"
              >Retry</UButton
            >
          </template>
        </UAlert>
      </template>

      <template v-if="info">
        <div class="flex flex-wrap items-center justify-end gap-2">
          <UBadge
            :color="connected ? 'success' : 'unavailable' === node.connection ? 'error' : 'warning'"
            variant="soft"
            size="sm"
            icon="i-lucide-network"
            >{{ connectionLabel }}</UBadge
          >
          <UButton
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
