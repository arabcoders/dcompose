<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { NuxtLink } from '#components'
import UBadge from '@nuxt/ui/components/Badge.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UAlert from '@nuxt/ui/components/Alert.vue'
import UIcon from '@nuxt/ui/components/Icon.vue'
import UTooltip from '@nuxt/ui/components/Tooltip.vue'
import type { NodeInfo, WorkspaceNode } from '~/types'
import { notification, parse_api_response, request } from '~/utils'
import { formatMemoryBytes } from '~/utils/node'
import { aggregateStackStats, parseStatBytes, workspaceErrorMessage } from '~/utils/stacks'

const props = defineProps<{ node: WorkspaceNode }>()
const connected = computed(() => 'connected' === props.node.connection && !props.node.paused)
const info = ref<NodeInfo | null>(null)
const loading = ref(false)
const error = ref('')
const ranking = ref<'cpu' | 'memory'>('cpu')
let mounted = false
let controller: AbortController | null = null
let timer: ReturnType<typeof setInterval> | undefined

const connectionLabel = computed(() => {
  if (props.node.paused) {
    return 'Paused'
  }
  return {
    connected: 'Connected',
    loading: 'Connecting',
    reconnecting: 'Reconnecting',
    unavailable: 'Unavailable',
    paused: 'Paused',
  }[props.node.connection]
})
const totals = computed(() =>
  aggregateStackStats(info.value?.containers ?? [], info.value?.runtime.cpus ?? 1),
)
const consumers = computed(() => {
  const sample = info.value
  if (!sample) {
    return []
  }
  return props.node.stacks
    .filter((stack) => !stack.loading && !stack.error && 0 < stack.services.length)
    .map((stack) => {
      const names = new Set(stack.services.map((service) => service.name))
      const containers = sample.containers.filter((container) => names.has(container.name))
      return {
        name: stack.name,
        count: containers.length,
        totals: aggregateStackStats(containers, sample.runtime.cpus),
        cpu:
          containers.reduce((total, container) => total + Number.parseFloat(container.cpu), 0) /
          sample.runtime.cpus,
        memory: containers.reduce(
          (total, container) => total + parseStatBytes(container.memory.split('/')[0] ?? ''),
          0,
        ),
      }
    })
    .filter((stack) => 0 < stack.count && Number.isFinite(stack[ranking.value]))
    .sort((a, b) => b[ranking.value] - a[ranking.value] || a.name.localeCompare(b.name))
    .slice(0, 3)
})

const refresh = async () => {
  if (!mounted || !connected.value || document.hidden || controller) {
    return
  }
  const current = new AbortController()
  controller = current
  loading.value = true
  try {
    const response = await request(`/api/nodes/${encodeURIComponent(props.node.id)}/info`, {
      signal: current.signal,
    })
    const json = await parse_api_response<NodeInfo>(response)
    if (current.signal.aborted) {
      return
    }
    if (!response.ok || 'error' in json) {
      throw new Error(
        workspaceErrorMessage(
          response.status,
          'error' in json ? json.error.message : 'Could not load resource usage.',
        ),
      )
    }
    info.value = json
    error.value = ''
  } catch (cause) {
    if (!current.signal.aborted) {
      const message =
        cause instanceof Error && !(cause instanceof TypeError)
          ? cause.message
          : 'Could not load resource usage.'
      if (!error.value) {
        notification('error', `Resource snapshot failed for ${props.node.name}`, message)
      }
      error.value = message
    }
  } finally {
    if (controller === current) {
      controller = null
      loading.value = false
    }
  }
}

const stop = () => {
  clearInterval(timer)
  timer = undefined
  controller?.abort()
  controller = null
  loading.value = false
}

const resume = () => {
  clearInterval(timer)
  timer = undefined
  if (!mounted || !connected.value || document.hidden) {
    return
  }
  void refresh()
  timer = setInterval(() => void refresh(), 30_000)
}

watch([() => props.node.id, connected], () => {
  stop()
  info.value = null
  error.value = ''
  resume()
})

onMounted(() => {
  mounted = true
  document.addEventListener('visibilitychange', resume)
  resume()
})
onBeforeUnmount(() => {
  mounted = false
  stop()
  document.removeEventListener('visibilitychange', resume)
})
</script>

<template>
  <article class="app-frame min-w-0 overflow-hidden" :aria-busy="loading">
    <header class="flex items-center justify-between gap-3 border-b border-default px-4 py-3">
      <slot name="leading" />
      <UTooltip :text="connectionLabel">
        <UIcon
          :name="
            node.paused || 'paused' === node.connection
              ? 'i-lucide-pause'
              : 'unavailable' === node.connection
                ? 'i-lucide-server-off'
                : 'i-lucide-server'
          "
          class="size-4 shrink-0"
          :class="
            connected
              ? 'text-success'
              : node.paused || 'paused' === node.connection
                ? 'text-muted'
                : 'unavailable' === node.connection
                  ? 'text-error'
                  : 'text-warning'
          "
          role="img"
          :aria-label="connectionLabel"
          tabindex="0"
        />
      </UTooltip>
      <NuxtLink
        :to="`/nodes/${encodeURIComponent(node.id)}`"
        class="min-w-0 flex-1 truncate font-semibold hover:text-primary"
        >{{ node.name }}</NuxtLink
      >
    </header>
    <div v-if="connected || node.error" class="space-y-4 p-4">
      <UAlert
        v-if="!connected && node.error"
        color="warning"
        variant="subtle"
        icon="i-lucide-wifi-off"
        :title="node.error"
      />
      <template v-else>
        <UAlert
          v-if="error"
          color="warning"
          variant="subtle"
          icon="i-lucide-circle-alert"
          :title="error"
          :description="info ? 'Showing previous sample' : undefined"
        >
          <template #actions>
            <UButton
              color="neutral"
              variant="outline"
              size="sm"
              icon="i-lucide-refresh-cw"
              :loading="loading"
              :disabled="loading"
              @click="refresh"
              >Retry</UButton
            >
          </template>
        </UAlert>
        <UAlert
          v-else-if="!info"
          color="neutral"
          variant="subtle"
          icon="i-lucide-loader-circle"
          title="Loading"
          :ui="{ icon: 'animate-spin' }"
        />
        <template v-if="info">
          <dl class="grid grid-cols-2 gap-4">
            <div>
              <dt class="text-xs text-muted">
                <UTooltip
                  text="Node CPU capacity used by running containers, including unmanaged ones. Excludes other host processes."
                >
                  <span class="inline-flex items-center gap-1.5" tabindex="0"
                    ><UIcon name="i-lucide-cpu" class="size-3.5" />CPU</span
                  >
                </UTooltip>
              </dt>
              <dd class="mt-1 text-base font-semibold tabular-nums">
                <span class="text-primary">{{ totals.cpu }}</span>
                <span class="text-muted"> / {{ info.runtime.cpus }} cores</span>
              </dd>
            </div>
            <div>
              <dt class="text-xs text-muted">
                <UTooltip
                  text="Memory across all running containers, including unmanaged ones. Excludes other host processes."
                >
                  <span class="inline-flex items-center gap-1.5" tabindex="0"
                    ><UIcon name="i-lucide-memory-stick" class="size-3.5" />Memory</span
                  >
                </UTooltip>
              </dt>
              <dd class="mt-1 text-base font-semibold tabular-nums">
                <span class="text-info">{{ totals.memory }}</span>
                <span class="text-muted">
                  / {{ formatMemoryBytes(info.runtime.memory_bytes) }}</span
                >
              </dd>
            </div>
          </dl>
          <div v-if="consumers.length" class="space-y-3 border-t border-default pt-3">
            <div class="flex flex-wrap items-center justify-between gap-2">
              <h3 class="inline-flex items-center gap-1.5 text-sm font-medium">
                <UIcon name="i-lucide-layers" class="size-4 text-primary" />Top stacks
              </h3>
              <div class="flex gap-1" role="group" aria-label="Rank stacks by resource">
                <UButton
                  v-for="metric in ['cpu', 'memory'] as const"
                  :key="metric"
                  size="xs"
                  :color="ranking === metric ? 'primary' : 'neutral'"
                  :icon="'cpu' === metric ? 'i-lucide-cpu' : 'i-lucide-memory-stick'"
                  :variant="ranking === metric ? 'soft' : 'ghost'"
                  :aria-pressed="ranking === metric"
                  :aria-label="'cpu' === metric ? 'Sort by CPU' : 'Sort by memory'"
                  @click="ranking = metric"
                  >{{ 'cpu' === metric ? 'CPU' : 'Memory' }}</UButton
                >
              </div>
            </div>
            <ol class="space-y-2">
              <li
                v-for="stack in consumers"
                :key="stack.name"
                class="flex flex-wrap items-center justify-between gap-2 text-sm"
              >
                <NuxtLink
                  :to="`/stacks/${encodeURIComponent(node.id)}/${encodeURIComponent(stack.name)}`"
                  class="truncate hover:text-primary"
                  >{{ stack.name }}</NuxtLink
                >
                <div class="flex shrink-0 gap-1.5">
                  <UTooltip text="Share of node CPU capacity"
                    ><UBadge
                      icon="i-lucide-cpu"
                      color="primary"
                      variant="subtle"
                      size="sm"
                      class="w-18 justify-center font-mono tabular-nums"
                      tabindex="0"
                      >{{ stack.totals.cpu }}</UBadge
                    ></UTooltip
                  >
                  <UTooltip text="Memory"
                    ><UBadge
                      icon="i-lucide-memory-stick"
                      color="info"
                      variant="subtle"
                      size="sm"
                      class="w-24 justify-center font-mono tabular-nums"
                      tabindex="0"
                      >{{ stack.totals.memory }}</UBadge
                    ></UTooltip
                  >
                </div>
              </li>
            </ol>
          </div>
        </template>
      </template>
    </div>
  </article>
</template>
