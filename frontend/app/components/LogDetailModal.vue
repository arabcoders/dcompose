<template>
  <UModal
    :open="open"
    :title="'Log details'"
    :ui="{
      content: 'max-w-5xl',
      body: 'max-h-[75vh] overflow-y-auto',
    }"
    @update:open="emit('update:modelValue', $event)"
  >
    <template #body>
      <div v-if="log" class="space-y-5">
        <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          <StatCard
            :label="'Level'"
            :value="getLogLevel(log.level)"
            :icon="LOG_LEVEL_ICON[getLogLevel(log.level)]"
            :color="LOG_LEVEL_COLOR[getLogLevel(log.level)]"
          />
          <div v-if="log.logger" dir="ltr">
            <StatCard :label="'Logger'" :value="log.logger" icon="i-lucide-tag" />
          </div>
          <StatCard
            :label="'Date'"
            :value="relativeTime(log.timestamp)"
            icon="i-lucide-clock"
            :tooltip="formatDate(log.timestamp, 'detail')"
          />
        </div>

        <div class="flex flex-wrap items-center justify-end gap-2">
          <UDropdownMenu :items="copyMenuItems" :content="{ align: 'end' }" :modal="false">
            <UButton color="neutral" variant="outline" size="sm" icon="i-lucide-copy">
              {{ 'Copy' }}
            </UButton>
          </UDropdownMenu>
        </div>

        <UAlert
          :color="LOG_LEVEL_COLOR[getLogLevel(log.level)]"
          variant="soft"
          :icon="LOG_LEVEL_ICON[getLogLevel(log.level)]"
          title=""
          class="min-w-0"
          dir="ltr"
        >
          <template #description>
            <p
              class="whitespace-pre-wrap wrap-break-word w-full font-mono text-sm text-default"
              dir="ltr"
            >
              {{ log.message }}
            </p>
          </template>
        </UAlert>

        <UAlert
          v-if="exceptionSummary(log)"
          color="error"
          variant="soft"
          icon="i-lucide-badge-alert"
          :title="exceptionSummary(log)"
          dir="ltr"
        />

        <section v-if="log.exception" class="space-y-3">
          <button
            type="button"
            class="flex w-full flex-wrap items-center justify-between gap-3 text-start"
            :aria-label="exceptionOpen ? 'Hide exception' : 'Show exception'"
            :aria-expanded="exceptionOpen"
            @click="exceptionOpen = !exceptionOpen"
          >
            <div class="flex items-center gap-3">
              <span
                class="inline-flex size-9 shrink-0 items-center justify-center rounded-md border border-error/30 bg-error/5 text-error"
              >
                <UIcon name="i-lucide-bug" class="size-4" />
              </span>
              <p class="text-base font-semibold text-highlighted">{{ 'Exception' }}</p>
            </div>
            <div class="flex items-center gap-2">
              <UButton
                size="sm"
                color="neutral"
                :variant="wrapException ? 'soft' : 'outline'"
                icon="i-lucide-wrap-text"
                @click.stop="wrapException = !wrapException"
              >
                <span class="hidden sm:inline">{{ 'Wrap' }}</span>
              </UButton>
              <UIcon
                :name="exceptionOpen ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'"
                class="size-4 text-toned"
              />
            </div>
          </button>

          <pre
            v-if="exceptionOpen"
            class="app-terminal max-h-96 overflow-auto"
            dir="ltr"
            :class="wrapException ? 'whitespace-pre-wrap wrap-break-word' : 'whitespace-pre'"
          ><code>{{ exceptionText(log) }}</code></pre>
        </section>

        <section v-if="detailRows.length > 0" class="space-y-3">
          <button
            type="button"
            class="flex w-full flex-wrap items-center justify-between gap-3 text-start"
            :aria-label="sourceOpen ? 'Hide source and runtime' : 'Show source and runtime'"
            :aria-expanded="sourceOpen"
            @click="sourceOpen = !sourceOpen"
          >
            <div class="flex items-center gap-3">
              <span class="app-detail-icon">
                <UIcon name="i-lucide-file-code" class="size-4" />
              </span>
              <p class="text-base font-semibold text-highlighted">{{ 'Source and runtime' }}</p>
            </div>
            <div class="flex items-center gap-2">
              <UIcon
                :name="sourceOpen ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'"
                class="size-4 text-toned"
              />
            </div>
          </button>

          <dl v-if="sourceOpen" class="grid gap-2 sm:grid-cols-2" dir="ltr">
            <div
              v-for="row in detailRows"
              :key="row.label"
              class="rounded-sm border border-default bg-elevated/40 p-3"
            >
              <dt
                class="inline-flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wide text-toned"
              >
                <UIcon :name="row.icon" class="size-3.5" />
                <span>{{ row.label }}</span>
              </dt>
              <dd class="mt-1 wrap-break-word font-mono text-xs text-default">{{ row.value }}</dd>
            </div>
          </dl>
        </section>

        <section v-if="fieldRows.length > 0" class="space-y-3">
          <button
            type="button"
            class="flex w-full flex-wrap items-center justify-between gap-3 text-start"
            :aria-label="fieldsOpen ? 'Hide fields' : 'Show fields'"
            :aria-expanded="fieldsOpen"
            @click="fieldsOpen = !fieldsOpen"
          >
            <div class="flex items-center gap-3">
              <span class="app-detail-icon">
                <UIcon name="i-lucide-tags" class="size-4" />
              </span>
              <p class="text-base font-semibold text-highlighted">{{ 'Fields' }}</p>
            </div>
            <div class="flex items-center gap-2">
              <UButton
                size="sm"
                color="neutral"
                :variant="wrapFields ? 'soft' : 'outline'"
                icon="i-lucide-wrap-text"
                @click.stop="wrapFields = !wrapFields"
              >
                <span class="hidden sm:inline">{{ 'Wrap' }}</span>
              </UButton>
              <UIcon
                :name="fieldsOpen ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'"
                class="size-4 text-toned"
              />
            </div>
          </button>

          <div v-if="fieldsOpen" class="space-y-2">
            <UInput
              v-model="fieldsQuery"
              type="search"
              icon="i-lucide-filter"
              :placeholder="'Filter fields'"
              size="sm"
              class="w-full"
              dir="ltr"
            />

            <UAlert
              v-if="fieldsQuery && 0 === visibleFieldRows.length"
              color="warning"
              variant="soft"
              icon="i-lucide-filter"
              :title="'No matching fields'"
            />

            <div
              v-for="field in visibleFieldRows"
              :key="field.key"
              class="rounded-sm border border-default bg-elevated/40"
            >
              <button
                type="button"
                class="grid w-full grid-cols-[minmax(7rem,0.45fr)_minmax(0,1fr)] items-center gap-3 px-3 py-2 text-start sm:grid-cols-[minmax(0,12rem)_minmax(0,1fr)]"
                dir="ltr"
                :aria-label="`${fieldOpen(field.key) ? 'Hide' : 'Show'} ${field.label}`"
                :aria-expanded="fieldOpen(field.key)"
                @click="toggleField(field.key)"
              >
                <span
                  class="min-w-0 truncate text-[11px] font-semibold uppercase tracking-wide text-toned"
                >
                  {{ field.label }}
                </span>
                <div class="flex min-w-0 items-center justify-end gap-2">
                  <span v-if="field.preview" class="min-w-0 truncate text-xs text-toned">
                    {{ field.preview }}
                  </span>
                  <UIcon
                    :name="fieldOpen(field.key) ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'"
                    class="size-4 shrink-0 text-toned"
                  />
                </div>
              </button>

              <div v-if="fieldOpen(field.key)" class="border-t border-default/70 px-3 py-3">
                <div
                  v-if="'scalar' !== field.kind"
                  class="mb-3 flex items-center justify-between gap-3"
                >
                  <div class="w-full">
                    <UInput
                      v-model="fieldFilters[field.key]"
                      type="search"
                      icon="i-lucide-filter"
                      :placeholder="'Filter lines'"
                      size="sm"
                      class="w-full"
                      dir="ltr"
                    />
                  </div>
                </div>

                <UAlert
                  v-if="fieldFilter(field.key) && 0 === filteredFieldLineCount(field)"
                  color="warning"
                  variant="soft"
                  icon="i-lucide-filter"
                  :title="'No matching lines'"
                  class="mb-3"
                />

                <pre
                  v-if="
                    'json' === field.kind &&
                    (!fieldFilter(field.key) || filteredFieldLineCount(field) > 0)
                  "
                  class="app-terminal max-h-96 overflow-auto"
                  dir="ltr"
                  :class="wrapFields ? 'whitespace-pre-wrap wrap-break-word' : 'whitespace-pre'"
                ><code>{{ displayedFieldValue(field) }}</code></pre>
                <pre
                  v-else-if="
                    'text' === field.kind &&
                    (!fieldFilter(field.key) || filteredFieldLineCount(field) > 0)
                  "
                  class="app-terminal max-h-96 overflow-auto"
                  dir="ltr"
                  :class="wrapFields ? 'whitespace-pre-wrap wrap-break-word' : 'whitespace-pre'"
                ><code>{{ displayedFieldValue(field) }}</code></pre>
                <p
                  v-else-if="'scalar' === field.kind"
                  class="wrap-break-word font-mono text-xs text-default"
                  dir="ltr"
                >
                  {{ field.value }}
                </p>
              </div>
            </div>
          </div>
        </section>

        <section class="space-y-3">
          <button
            type="button"
            class="flex w-full flex-wrap items-center justify-between gap-3 text-start"
            :aria-label="rawJsonOpen ? 'Hide raw data' : 'Show raw data'"
            :aria-expanded="rawJsonOpen"
            @click="rawJsonOpen = !rawJsonOpen"
          >
            <div class="flex items-center gap-3">
              <span class="app-detail-icon">
                <UIcon name="i-lucide-braces" class="size-4" />
              </span>
              <p class="text-base font-semibold text-highlighted">{{ 'Raw data' }}</p>
            </div>
            <div class="flex items-center gap-2">
              <UButton
                size="sm"
                color="neutral"
                :variant="wrapRaw ? 'soft' : 'outline'"
                icon="i-lucide-wrap-text"
                @click.stop="wrapRaw = !wrapRaw"
              >
                <span class="hidden sm:inline">{{ 'Wrap' }}</span>
              </UButton>
              <UIcon
                :name="rawJsonOpen ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'"
                class="size-4 text-toned"
              />
            </div>
          </button>

          <template v-if="rawJsonOpen">
            <UInput
              v-model="rawJsonFilter"
              type="search"
              icon="i-lucide-filter"
              :placeholder="'Filter raw data'"
              size="sm"
              class="w-full"
              dir="ltr"
            />

            <UAlert
              v-if="rawJsonFilter && 0 === filteredRawJsonLineCount"
              color="warning"
              variant="soft"
              icon="i-lucide-filter"
              :title="'No matching lines'"
            />

            <pre
              v-if="!rawJsonFilter || filteredRawJsonLineCount > 0"
              class="app-terminal max-h-96 overflow-auto"
              dir="ltr"
              :class="wrapRaw ? 'whitespace-pre-wrap wrap-break-word' : 'whitespace-pre'"
            ><code>{{ displayedRawJson }}</code></pre>
          </template>
        </section>
      </div>
    </template>
  </UModal>
</template>

<script setup lang="ts">
import { useStorage } from '@vueuse/core'
import { computed, ref, watch } from 'vue'
import UAlert from '@nuxt/ui/components/Alert.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UDropdownMenu from '@nuxt/ui/components/DropdownMenu.vue'
import UIcon from '@nuxt/ui/components/Icon.vue'
import UInput from '@nuxt/ui/components/Input.vue'
import UModal from '@nuxt/ui/components/Modal.vue'
import StatCard from '~/components/StatCard.vue'
import type { JsonLogValue, LogDetailRow, LogEntry, LogFieldRow, LogLevel } from '~/types'
import { copyText } from '~/utils'
import {
  filterLogTextLines,
  formatLogDate as formatDate,
  formatLogRelativeTime as relativeTime,
  getLogLevel,
  isJsonLogValue,
  logNodeLabel,
} from '~/utils/logs'

const props = withDefaults(defineProps<{ modelValue?: boolean; log?: LogEntry | null }>(), {
  modelValue: false,
  log: null,
})
const emit = defineEmits<{ (event: 'update:modelValue', value: boolean): void }>()
const open = computed({
  get: () => props.modelValue,
  set: (value: boolean) => emit('update:modelValue', value),
})
const LOG_LEVEL_COLOR: Record<LogLevel, 'neutral' | 'info' | 'warning' | 'error'> = {
  debug: 'neutral',
  info: 'info',
  warning: 'warning',
  error: 'error',
}
const LOG_LEVEL_ICON: Record<LogLevel, string> = {
  debug: 'i-lucide-terminal',
  info: 'i-lucide-info',
  warning: 'i-lucide-triangle-alert',
  error: 'i-lucide-circle-x',
}
const exceptionOpen = useStorage('logs_exception_open', false)
const fieldsOpen = useStorage('logs_fields_open', true)
const rawJsonOpen = useStorage('logs_raw_json_open', false)
const sourceOpen = useStorage('logs_source_open', true)
const wrapException = useStorage('logs_wrap_exception', false)
const wrapFields = useStorage('logs_wrap_fields', false)
const wrapRaw = useStorage('logs_wrap_raw', false)
const rawJsonFilter = ref('')
const fieldOpenState = ref<Record<string, boolean>>({})
const fieldFilters = ref<Record<string, string>>({})
const fieldsQuery = ref('')

const logRaw = (log: LogEntry): string => JSON.stringify(log, null, 2)
const exceptionSummary = (log: LogEntry): string => {
  const type = log.exception?.type?.trim() ?? ''
  const message = log.exception?.message?.trim() ?? ''
  return type && message ? `${type}: ${message}` : type || message
}
const exceptionText = (log: LogEntry): string =>
  log.exception ? JSON.stringify(log.exception, null, 2) : ''
const rawJson = computed(() => (props.log ? logRaw(props.log) : ''))
const copyMenuItems = computed(() => [
  [
    {
      label: 'Copy rendered',
      icon: 'i-lucide-file-text',
      onSelect: () => {
        if (props.log) {
          void copyText(props.log.rendered)
        }
      },
    },
    {
      label: 'Copy JSON',
      icon: 'i-lucide-braces',
      onSelect: () => {
        if (props.log) {
          void copyText(rawJson.value)
        }
      },
    },
  ],
])
const filteredRawJsonLines = computed(() => filterLogTextLines(rawJson.value, rawJsonFilter.value))
const filteredRawJsonLineCount = computed(() => filteredRawJsonLines.value.length)
const displayedRawJson = computed(() =>
  rawJsonFilter.value ? filteredRawJsonLines.value.join('\n') : rawJson.value,
)
const formatDetailValue = (value: JsonLogValue | undefined): string => {
  if (undefined === value || null === value || '' === value) {
    return ''
  }
  if ('string' === typeof value) {
    return value
  }
  if ('number' === typeof value || 'boolean' === typeof value) {
    return String(value)
  }
  return JSON.stringify(value)
}
const compactRows = (
  rows: Array<{ label: string; value: JsonLogValue | undefined; icon: string }>,
): Array<LogDetailRow> =>
  rows
    .map((row) => ({ label: row.label, value: formatDetailValue(row.value), icon: row.icon }))
    .filter((row) => Boolean(row.value))
const formatNameId = (name: string | undefined, id: string | number | undefined): string => {
  const nameValue = formatDetailValue(name)
  const idValue = formatDetailValue(id)
  return nameValue && idValue ? `${nameValue} / ${idValue}` : nameValue || idValue
}
const detailRows = computed((): Array<LogDetailRow> => {
  if (!props.log) {
    return []
  }
  return compactRows([
    { label: 'Timestamp (original)', value: props.log.timestamp, icon: 'i-lucide-clock' },
    { label: 'Severity (original)', value: props.log.level, icon: 'i-lucide-tag' },
    { label: 'Entry ID', value: props.log.id, icon: 'i-lucide-hash' },
    { label: 'Node', value: logNodeLabel(props.log), icon: 'i-lucide-server' },
    { label: 'Node ID', value: props.log.fields.node_id, icon: 'i-lucide-server' },
    { label: 'File', value: props.log.source?.file, icon: 'i-lucide-file' },
    { label: 'Line', value: props.log.source?.line, icon: 'i-lucide-hash' },
    { label: 'Function', value: props.log.source?.function, icon: 'i-lucide-code-2' },
    { label: 'Module', value: props.log.source?.module, icon: 'i-lucide-box' },
    { label: 'Path', value: props.log.source?.path, icon: 'i-lucide-folder-tree' },
    {
      label: 'Process',
      value: formatNameId(props.log.process?.name, props.log.process?.id),
      icon: 'i-lucide-cpu',
    },
    {
      label: 'Thread',
      value: formatNameId(props.log.thread?.name, props.log.thread?.id),
      icon: 'i-lucide-git-branch',
    },
  ])
})
const formatFieldValue = (value: JsonLogValue): string => {
  if ('string' === typeof value) {
    return value
  }
  if ('number' === typeof value || 'boolean' === typeof value) {
    return String(value)
  }
  return JSON.stringify(value, null, 2)
}
const isJsonLike = (value: string): boolean => {
  const trimmed = value.trim()
  return (
    ((trimmed.startsWith('{') && trimmed.endsWith('}')) ||
      (trimmed.startsWith('[') && trimmed.endsWith(']'))) &&
    1 < trimmed.length
  )
}
const isJsonContainer = (
  value: JsonLogValue,
): value is Record<string, JsonLogValue> | Array<JsonLogValue> =>
  Array.isArray(value) || (null !== value && 'object' === typeof value)
const parseJsonContainerString = (
  value: string,
): Record<string, JsonLogValue> | Array<JsonLogValue> | null => {
  if (!isJsonLike(value)) {
    return null
  }
  try {
    const parsed: unknown = JSON.parse(value)
    if (isJsonLogValue(parsed) && isJsonContainer(parsed)) {
      return parsed
    }
  } catch {
    return null
  }
  return null
}
const fieldRows = computed((): Array<LogFieldRow> => {
  if (!props.log) {
    return []
  }
  const rows: Array<LogFieldRow> = []
  for (const [key, rawValue] of Object.entries(props.log.fields)) {
    const jsonValue =
      'string' === typeof rawValue
        ? parseJsonContainerString(rawValue)
        : isJsonContainer(rawValue)
          ? rawValue
          : null
    const value = jsonValue ? JSON.stringify(jsonValue, null, 2) : formatFieldValue(rawValue)
    const kind: LogFieldRow['kind'] = jsonValue
      ? 'json'
      : 'string' === typeof rawValue && rawValue.includes('\n')
        ? 'text'
        : 'scalar'
    rows.push({
      key,
      label: key,
      value,
      preview: formatFieldValue(rawValue).replaceAll('\n', ' '),
      kind,
    })
  }
  return rows
})
const visibleFieldRows = computed((): Array<LogFieldRow> => {
  const query = fieldsQuery.value.trim().toLowerCase()
  if (!query) {
    return fieldRows.value
  }
  if (query.includes('.')) {
    const matches = fieldRows.value.filter((field) => field.key.toLowerCase().includes(query))
    if (0 < matches.length) {
      return matches
    }
  }
  return fieldRows.value.filter(
    (field) =>
      `${field.key}\n${field.preview}\n${field.value}`.toLowerCase().includes(query) ||
      0 < filterLogTextLines(field.value, query).length,
  )
})
const fieldOpen = (key: string): boolean => fieldOpenState.value[key] ?? false
const toggleField = (key: string): void => {
  fieldOpenState.value = { ...fieldOpenState.value, [key]: !fieldOpen(key) }
}
const fieldFilter = (key: string): string => fieldFilters.value[key] ?? ''
const displayedFieldValue = (field: LogFieldRow): string => {
  const query = fieldFilter(field.key)
  return query ? filterLogTextLines(field.value, query).join('\n') : field.value
}
const filteredFieldLineCount = (field: LogFieldRow): number =>
  filterLogTextLines(field.value, fieldFilter(field.key)).length
watch(
  () => props.log?.id,
  () => {
    rawJsonFilter.value = ''
    fieldsQuery.value = ''
    fieldOpenState.value = {}
    fieldFilters.value = {}
  },
)
</script>
