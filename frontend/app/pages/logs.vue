<template>
  <main class="w-full min-w-0 max-w-full space-y-6">
    <PageHeader
      icon="i-lucide-scroll-text"
      section-label="Configuration"
      page-label="Logs"
      description="Browse server and node logs."
    >
      <template #actions>
        <div class="flex flex-wrap items-center gap-2">
          <UButton
            v-if="!atBottom"
            color="neutral"
            variant="outline"
            size="sm"
            icon="i-lucide-arrow-down"
            @click="scrollBottom"
          >
            Scroll to bottom
          </UButton>
          <UDropdownMenu :items="copyMenuItems" :modal="false">
            <UButton
              color="neutral"
              variant="outline"
              size="sm"
              icon="i-lucide-copy"
              trailing-icon="i-lucide-chevron-down"
            >
              Copy
            </UButton>
            <UButton
              color="neutral"
              variant="outline"
              size="sm"
              icon="i-lucide-refresh-cw"
              :loading="loading"
              :disabled="loadingOlder"
              @click="refresh"
            >
              Refresh
            </UButton>
          </UDropdownMenu>
        </div>
      </template>
    </PageHeader>

    <div class="space-y-3">
      <div class="app-card flex flex-wrap items-center gap-3 p-3 sm:p-4">
        <USelectMenu
          v-model="selectedLevels"
          :items="levelItems"
          icon="i-lucide-list-filter"
          value-key="value"
          label-key="label"
          multiple
          size="sm"
          class="w-full sm:w-48"
          aria-label="Filter log level"
          >{{ levelFilterLabel }}</USelectMenu
        >
        <USelect
          v-model="selectedNode"
          :items="nodeItems"
          icon="i-lucide-server"
          size="sm"
          class="w-full sm:w-60"
          aria-label="Filter log node"
        />
        <UInput
          v-model.lazy="query"
          icon="i-lucide-search"
          placeholder="Search logs..."
          size="sm"
          class="w-full sm:w-72"
          aria-label="Search logs"
        />
        <UButton
          color="neutral"
          variant="outline"
          size="sm"
          :icon="wrap ? 'i-lucide-wrap-text' : 'i-lucide-align-justify'"
          :aria-pressed="wrap"
          class="ml-auto"
          @click="wrap = !wrap"
        >
          Wrap
        </UButton>
      </div>
    </div>

    <UAlert v-if="error" color="error" variant="soft" title="Logs unavailable" :description="error">
      <template #actions>
        <UButton size="xs" color="error" variant="outline" @click="refresh">Retry</UButton>
      </template>
    </UAlert>
    <div class="app-card overflow-hidden rounded-none">
      <div
        ref="container"
        class="max-h-[65vh] min-h-96 overflow-y-auto font-mono text-sm"
        style="overflow-anchor: none"
        @scroll.passive="onScroll"
      >
        <div v-if="hasOlder" class="border-b border-default p-3 text-center">
          <UButton
            size="xs"
            color="neutral"
            variant="outline"
            :loading="loadingOlder"
            :disabled="loading"
            @click="loadOlder"
            >Load older</UButton
          >
        </div>
        <p v-if="truncated" class="border-b border-default px-4 py-3 text-sm text-warning">
          Older logs are no longer available.
        </p>
        <article
          v-for="(entry, index) in filtered"
          :key="entry.id"
          :data-log-id="entry.id"
          class="flex min-w-0 border-b border-default/40 bg-transparent transition-colors duration-150 last:border-b-0 hover:bg-elevated/70"
          :class="1 === index % 2 ? 'bg-elevated/40' : ''"
        >
          <div
            class="flex w-full min-w-0 flex-col gap-1 px-3 py-[0.65rem] leading-[1.6] md:flex-row md:items-start md:gap-2"
          >
            <span
              class="inline-flex max-w-full flex-wrap items-center gap-x-2 gap-y-1 align-middle md:shrink-0 md:flex-nowrap"
            >
              <UTooltip :text="formatDate(entry.timestamp, 'detail')">
                <span class="inline text-[11px] font-semibold text-toned cursor-pointer">{{
                  formatDate(entry.timestamp, 'clock')
                }}</span>
              </UTooltip>
              <UDropdownMenu
                :items="logMenuItems(entry)"
                :content="{ align: 'start' }"
                :modal="false"
              >
                <UButton
                  color="neutral"
                  variant="ghost"
                  size="xs"
                  icon="i-lucide-ellipsis-vertical"
                  trailing-icon="i-lucide-chevron-down"
                  aria-label="Log actions"
                  class="inline-flex h-5! align-[-0.2em] opacity-70 hover:opacity-100"
                  :ui="{ base: 'min-h-0 min-w-0', leadingIcon: 'size-3', trailingIcon: 'size-3' }"
                >
                  <span class="text-[10px] font-medium">Actions</span>
                </UButton>
              </UDropdownMenu>
              <button
                type="button"
                :class="logLevelBadgeClass(getLogLevel(entry.level))"
                class="inline-flex w-20 shrink-0 items-center justify-start gap-1.5 px-1.5 py-0.5 text-left text-[10px] font-bold uppercase tracking-wide cursor-pointer"
                :aria-label="`Show ${entry.level} log details`"
                @click="openDetails(entry)"
              >
                <UIcon
                  :name="logLevelIcon(getLogLevel(entry.level))"
                  class="size-3 shrink-0"
                  aria-hidden="true"
                />{{ getLogLevel(entry.level) }}
              </button>
              <UTooltip :text="entry.logger">
                <span
                  class="inline-block max-w-[46vw] truncate align-middle text-[11px] font-semibold text-toned sm:max-w-104"
                  >[{{ entry.logger }}]</span
                >
              </UTooltip>
            </span>
            <button
              type="button"
              class="block min-w-0 flex-1 cursor-pointer text-start"
              :aria-expanded="expanded.has(entry.id) || wrap"
              @click="toggleRow(entry.id)"
            >
              <span
                class="block text-sm text-default"
                :class="
                  expanded.has(entry.id) || wrap
                    ? 'whitespace-pre-wrap wrap-break-word'
                    : 'truncate'
                "
                >{{ entry.message
                }}<span v-if="entry.exception" class="text-error/90"
                  >: {{ entry.exception.type
                  }}{{ entry.exception.message ? `: ${entry.exception.message}` : '' }}</span
                ></span
              >
            </button>
          </div>
        </article>
        <p v-if="!loading && !filtered.length" class="p-10 text-center text-sm text-toned">
          {{
            !selectedLevels.length
              ? 'Select at least one level to view logs.'
              : error
                ? 'Log history could not be loaded. Retry to reconnect.'
                : 'No matching logs.'
          }}
        </p>
        <div v-if="loading && !entries.length" class="p-10 text-center" role="status">
          <UIcon name="i-lucide-loader-circle" class="size-6 animate-spin text-toned" />
        </div>
      </div>
    </div>
    <LogDetailModal v-model="detailsOpen" :log="selectedLog" />
  </main>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useHead, useRoute } from '#app'
import { useStorage } from '@vueuse/core'
import UAlert from '@nuxt/ui/components/Alert.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UDropdownMenu from '@nuxt/ui/components/DropdownMenu.vue'
import UIcon from '@nuxt/ui/components/Icon.vue'
import UInput from '@nuxt/ui/components/Input.vue'
import USelect from '@nuxt/ui/components/Select.vue'
import USelectMenu from '@nuxt/ui/components/SelectMenu.vue'
import UTooltip from '@nuxt/ui/components/Tooltip.vue'
import PageHeader from '~/components/PageHeader.vue'
import LogDetailModal from '~/components/LogDetailModal.vue'
import { useLogs } from '~/composables/useLogs'
import { useWorkspace } from '~/composables/useWorkspace'
import { copyText, notification } from '~/utils'
import type { LogEntry, LogLevel } from '~/types'
import {
  formatLogDate as formatDate,
  getLogLevel,
  logLevels,
  normalizeLogLevels,
  serializeRenderedLogs,
  serializeLogs,
} from '~/utils/logs'

useHead({ title: 'Logs - DCompose' })
const route = useRoute()
const {
  entries,
  observedNodes,
  loading,
  loadingOlder,
  error,
  truncated,
  hasOlder,
  refresh: refreshHistory,
  search: searchHistory,
  loadOlder: fetchOlder,
} = useLogs()
const { nodes } = useWorkspace()
const container = ref<HTMLElement | null>(null)
const query = ref('')
const selectedNode = ref('all')
const nodeItems = computed(() => {
  const known = new Map(observedNodes.value)
  for (const node of nodes.value) {
    known.set(node.id, node.name)
  }
  return [
    { label: 'Everything', value: 'all' },
    { label: 'Server', value: 'server' },
    ...[...known].map(([value, label]) => ({ value, label: `${label} (${value})` })),
  ]
})
const wrap = useStorage('logs_text_wrap', false)
const atBottom = ref(true)
const expanded = ref(new Set<number>())
let searchTimer: ReturnType<typeof setTimeout> | null = null
let mounted = false
const storedLevels = useStorage<Array<LogLevel>>('logs_levels', [...logLevels])
const linkedLevels = ref<Array<LogLevel> | null>(null)
const selectedLevels = computed<Array<LogLevel>>({
  get: () => linkedLevels.value ?? normalizeLogLevels(storedLevels.value),
  set: (value) => {
    if (null === linkedLevels.value) {
      storedLevels.value = normalizeLogLevels(value)
    } else {
      linkedLevels.value = normalizeLogLevels(value)
    }
  },
})
watch(
  () => route.query,
  (params) => {
    linkedLevels.value =
      'string' === typeof params.levels
        ? normalizeLogLevels(params.levels.split(',').map((level) => level.trim()))
        : null
    selectedNode.value = 'string' === typeof params.node && params.node ? params.node : 'all'
    query.value = 'string' === typeof params.search ? params.search : ''
  },
  { immediate: true },
)
const levelItems = logLevels.map((value) => ({
  label: value.charAt(0).toUpperCase() + value.slice(1),
  value,
}))
const levelFilterLabel = computed(() =>
  selectedLevels.value.length === logLevels.length
    ? 'Everything'
    : selectedLevels.value.length
      ? selectedLevels.value.join(', ')
      : 'No levels',
)
const detailsOpen = ref(false)
const selectedLog = ref<LogEntry | null>(null)
const filtered = computed(() => entries.value)
const logLevelIcon = (value: LogLevel): string =>
  ({
    error: 'i-lucide-circle-x',
    warning: 'i-lucide-triangle-alert',
    info: 'i-lucide-info',
    debug: 'i-lucide-terminal',
  })[value]
const logLevelBadgeClass = (value: LogLevel): string =>
  'error' === value
    ? 'bg-error/15 text-error ring-1 ring-inset ring-error/40'
    : 'warning' === value
      ? 'bg-warning/20 text-warning ring-1 ring-inset ring-warning/50'
      : 'info' === value
        ? 'bg-info/15 text-info ring-1 ring-inset ring-info/40'
        : 'bg-neutral/15 text-default ring-1 ring-inset ring-default'
const scrollBottom = (): void => {
  const element = container.value
  if (element) {
    element.scrollTo({ top: element.scrollHeight })
  }
  atBottom.value = true
}
const onScroll = (): void => {
  const element = container.value
  if (element) {
    atBottom.value = 24 > element.scrollHeight - element.scrollTop - element.clientHeight
  }
}
const captureAnchor = () => {
  const element = container.value
  if (!element) {
    return null
  }
  const top = element.getBoundingClientRect().top
  const anchor = [...element.querySelectorAll<HTMLElement>('[data-log-id]')].find(
    (row) => row.getBoundingClientRect().bottom > top,
  )
  return anchor
    ? {
        id: anchor.dataset.logId,
        offset: anchor.getBoundingClientRect().top,
        top: element.scrollTop,
      }
    : null
}
const restoreAnchor = (anchor: ReturnType<typeof captureAnchor>): void => {
  const element = container.value
  if (!element || !anchor) {
    return
  }
  const row = element.querySelector(`[data-log-id="${anchor.id}"]`)
  if (row) {
    element.scrollTo({ top: anchor.top + row.getBoundingClientRect().top - anchor.offset })
  }
}
const toggleRow = (id: number): void => {
  const next = new Set(expanded.value)
  if (next.has(id)) {
    next.delete(id)
  } else {
    next.add(id)
  }
  expanded.value = next
}
const loadOlder = async (): Promise<void> => {
  atBottom.value = false
  await fetchOlder()
}
const applyFilters = (): Promise<void> =>
  searchHistory({
    levels: selectedLevels.value,
    node: 'all' === selectedNode.value ? '' : selectedNode.value,
    search: query.value,
  })
const refresh = (): void => {
  if (null !== searchTimer) {
    clearTimeout(searchTimer)
    searchTimer = null
    void applyFilters()
  } else {
    void refreshHistory()
  }
}
const openDetails = (entry: LogEntry): void => {
  selectedLog.value = entry
  detailsOpen.value = true
}
const copyMenuItems = computed(() => [
  [
    {
      label: 'Copy rendered',
      icon: 'i-lucide-file-text',
      onSelect: () => copyText(serializeRenderedLogs(filtered.value)),
    },
    {
      label: 'Copy JSON',
      icon: 'i-lucide-braces',
      onSelect: () => copyText(JSON.stringify(filtered.value, null, 2)),
    },
  ],
  [
    {
      label: 'Download',
      icon: 'i-lucide-download',
      disabled: 0 === filtered.value.length,
      onSelect: () => downloadVisible(),
    },
  ],
])
const logMenuItems = (entry: LogEntry) => [
  [
    { label: 'Details', icon: 'i-lucide-panel-right-open', onSelect: () => openDetails(entry) },
    {
      label: 'Copy rendered',
      icon: 'i-lucide-file-text',
      onSelect: () => copyText(entry.rendered),
    },
    {
      label: 'Copy JSON',
      icon: 'i-lucide-braces',
      onSelect: () => copyText(JSON.stringify(entry, null, 2)),
    },
  ],
]
watch([query, selectedLevels, selectedNode], () => {
  if (null !== searchTimer) {
    clearTimeout(searchTimer)
  }
  searchTimer = setTimeout(() => {
    searchTimer = null
    void applyFilters()
  }, 300)
})
watch(entries, async () => {
  const follow = atBottom.value
  const anchor = !follow ? captureAnchor() : null
  const retained = new Set(entries.value.map((entry) => entry.id))
  expanded.value = new Set([...expanded.value].filter((id) => retained.has(id)))
  await nextTick()
  if (!mounted) {
    return
  }
  if (follow && atBottom.value) {
    scrollBottom()
  } else {
    restoreAnchor(anchor)
  }
})
watch(wrap, async () => {
  if (atBottom.value) {
    await nextTick()
    if (mounted && atBottom.value) {
      scrollBottom()
    }
  }
})
const downloadVisible = (): void => {
  let url: string | null = null
  try {
    url = URL.createObjectURL(
      new Blob([serializeLogs(filtered.value)], { type: 'application/x-ndjson' }),
    )
    const link = document.createElement('a')
    link.href = url
    link.download = 'dcompose-logs.jsonl'
    link.click()
  } catch {
    notification('error', 'Download failed', "Couldn't download visible logs.")
  } finally {
    if (url) {
      URL.revokeObjectURL(url)
    }
  }
}
onMounted(() => {
  mounted = true
  void applyFilters()
})
onBeforeUnmount(() => {
  mounted = false
  if (null !== searchTimer) {
    clearTimeout(searchTimer)
  }
})
</script>
