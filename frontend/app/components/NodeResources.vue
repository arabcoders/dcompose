<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { NuxtLink } from '#components'
import UBadge from '@nuxt/ui/components/Badge.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UIcon from '@nuxt/ui/components/Icon.vue'
import UTooltip from '@nuxt/ui/components/Tooltip.vue'
import { useWorkspaceStore } from '~/store/workspace'
import type { NodeResourceStatus, WorkspaceNode } from '~/types'
import { formatMemoryBytes } from '~/utils/node'
import { aggregateStackStats, parseStatBytes } from '~/utils/stacks'

const props = defineProps<{ node: WorkspaceNode }>()
const workspace = useWorkspaceStore()
const connected = computed(() => 'connected' === props.node.connection && !props.node.paused)
const cached = computed(() => workspace.nodeInfoCache[props.node.id])
const info = computed(() =>
  props.node.paused || 'paused' === props.node.connection ? null : (cached.value?.info ?? null),
)
const loading = ref(false)
const error = ref('')
const ranking = ref<'cpu' | 'memory'>('cpu')
let mounted = false
let timer: ReturnType<typeof setInterval> | undefined
let refreshToken = 0

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

const status = computed<NodeResourceStatus | null>(() => {
  if (props.node.paused || 'paused' === props.node.connection) {
    return {
      text: 'Paused. Resume this node to reconnect.',
      icon: 'i-lucide-pause',
      tone: 'muted',
    }
  }
  if (!connected.value) {
    return {
      text: `${props.node.error || connectionLabel.value}${info.value ? ' Showing previous sample.' : ''}`,
      icon: 'i-lucide-wifi-off',
      tone: 'warning',
    }
  }
  if (error.value) {
    return {
      text: info.value ? `${error.value} Showing previous sample.` : error.value,
      icon: 'i-lucide-circle-alert',
      tone: 'warning',
    }
  }
  if (!info.value) {
    return {
      text: 'Loading resource usage...',
      icon: 'i-lucide-loader-circle',
      tone: 'muted',
      spin: true,
    }
  }
  return null
})
const totals = computed(() =>
  aggregateStackStats(info.value?.containers ?? [], info.value?.runtime.cpus ?? 1),
)
const sampleUpdated = computed(() =>
  info.value ? new Date(info.value.checked_at).toLocaleString() : '',
)
const consumers = computed(() => {
  const sample = info.value
  if (!sample) {
    return []
  }
  return props.node.stacks
    .filter((stack) => !stack.error && 0 < stack.services.length)
    .map((stack) => {
      const names = new Set(stack.services.map((service) => service.name))
      const containers = sample.containers.filter((container) => names.has(container.name))
      return {
        name: stack.name,
        stale: stack.loading,
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

const refresh = async (force = false) => {
  if (!mounted || !connected.value || document.hidden) {
    return
  }
  const id = props.node.id
  const token = ++refreshToken
  loading.value = true
  try {
    const result = await workspace.loadNodeInfo(id, force)
    if (token === refreshToken && id === props.node.id && !result.aborted) {
      error.value = result.error
    }
  } finally {
    if (token === refreshToken) {
      loading.value = false
    }
  }
}

const stop = () => {
  refreshToken++
  loading.value = false
  clearInterval(timer)
  timer = undefined
}

const resume = () => {
  stop()
  if (!mounted || !connected.value || document.hidden) {
    return
  }
  void refresh()
  timer = setInterval(() => void refresh(true), 30_000)
}

watch([() => props.node.id, connected, () => props.node.paused], ([id], [previousId]) => {
  if (id !== previousId || props.node.paused || 'paused' === props.node.connection) {
    error.value = ''
  }
  resume()
})
watch(info, (sample) => {
  if (sample) {
    error.value = ''
  }
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
    <div class="space-y-4 p-4">
      <p
        v-if="status"
        role="status"
        class="inline-flex min-w-0 items-center gap-1.5 text-xs"
        :class="{
          'text-warning': 'warning' === status.tone,
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
              <span class="text-muted"> / {{ formatMemoryBytes(info.runtime.memory_bytes) }}</span>
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
              <span class="inline-flex min-w-0 items-center gap-1.5">
                <NuxtLink
                  :to="`/stacks/${encodeURIComponent(node.id)}/${encodeURIComponent(stack.name)}`"
                  class="truncate hover:text-primary"
                  >{{ stack.name }}</NuxtLink
                >
                <UTooltip v-if="stack.stale" :text="`Showing last sample from ${sampleUpdated}.`">
                  <UIcon
                    name="i-lucide-history"
                    class="size-3.5 shrink-0 text-muted"
                    role="img"
                    aria-label="Stale sample"
                    tabindex="0"
                  />
                </UTooltip>
              </span>
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
        <p class="text-xs text-muted">
          Updated
          <time :datetime="info.checked_at">{{ new Date(info.checked_at).toLocaleString() }}</time>
        </p>
      </template>
    </div>
  </article>
</template>
