<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useHead } from '#app'
import { definePageMeta } from '#imports'
import { NuxtLink } from '#components'
import UAlert from '@nuxt/ui/components/Alert.vue'
import UBadge from '@nuxt/ui/components/Badge.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UIcon from '@nuxt/ui/components/Icon.vue'
import UTooltip from '@nuxt/ui/components/Tooltip.vue'
import StatCard from '~/components/StatCard.vue'
import NodeResources from '~/components/NodeResources.vue'
import { useDialog } from '~/composables/useDialog'
import { useLogs } from '~/composables/useLogs'
import { useWorkspaceStore } from '~/store/workspace'
import type { LogEntry, StatCardProps } from '~/types'
import { containerProblem, imageUpdateTooltip } from '~/utils/stacks'
import { formatLogDate, getLogLevel, logNodeLabel } from '~/utils/logs'

definePageMeta({ layout: 'workspace' })

const workspace = useWorkspaceStore()
const { nodes, loading: workspaceLoading } = storeToRefs(workspace)
const { confirmDialog } = useDialog()
const {
  entries: logEntries,
  error: logsError,
  truncated: logsTruncated,
  search: searchLogs,
  refresh: refreshLogs,
} = useLogs()
const recentErrors = computed(() => logEntries.value.slice(-5).reverse())
const logsLink = (entry?: LogEntry) => ({
  path: '/logs',
  query: {
    levels: 'warning,error',
    ...('string' === typeof entry?.fields.node_id ? { node: entry.fields.node_id } : {}),
  },
})
onMounted(() => {
  void searchLogs({ levels: ['warning', 'error'], node: '', search: '' })
})

const enabledNodes = computed(() => nodes.value.filter((node) => !node.paused))
const reportingNodes = computed(() =>
  enabledNodes.value.filter((node) => 'connected' === node.connection),
)
const unavailableNodes = computed(() =>
  enabledNodes.value.filter(
    (node) => 'unavailable' === node.connection || 'reconnecting' === node.connection,
  ),
)
const currentStacks = computed(() =>
  reportingNodes.value.flatMap((node) =>
    node.stacks.filter((stack) => !stack.loading).map((stack) => ({ node, stack })),
  ),
)
const issues = computed(() =>
  currentStacks.value
    .flatMap(({ node, stack }) => {
      const messages = stack.error
        ? [stack.error]
        : stack.services.flatMap((service) => {
            const problem = containerProblem(service)
            const check = service.image_update
            return [
              ...(problem ? [`${service.name}: ${problem}`] : []),
              ...('error' === check?.status ? [`${service.name}: update check failed`] : []),
            ]
          })
      const priority = stack.error
        ? 0
        : stack.services.some((service) => containerProblem(service))
          ? 1
          : 2
      return messages.length ? [{ node, stack, messages, priority }] : []
    })
    .sort(
      (a, b) =>
        a.priority - b.priority ||
        a.node.name.localeCompare(b.node.name) ||
        a.stack.name.localeCompare(b.stack.name),
    ),
)
const busyStacks = computed(() => currentStacks.value.filter(({ stack }) => stack.busy))
const updates = computed(() =>
  currentStacks.value.flatMap(({ node, stack }) => {
    if (stack.error) {
      return []
    }
    const services = stack.services.filter((service) =>
      ['available', 'checking', 'error'].includes(service.image_update?.status ?? ''),
    )
    return services.length ? [{ node, stack, services }] : []
  }),
)
const availableUpdates = computed(
  () =>
    updates.value.filter((item) =>
      item.services.some((service) => 'available' === service.image_update?.status),
    ).length,
)
const counts = computed<Array<StatCardProps & { href: string }>>(() => [
  {
    label: 'Nodes',
    value: `${reportingNodes.value.length} / ${nodes.value.length}`,
    tooltip: `${unavailableNodes.value.length} offline, ${nodes.value.length - enabledNodes.value.length} paused`,
    icon: 'i-lucide-server',
    href: '#resources-heading',
  },
  {
    label: 'Attention',
    value: issues.value.length,
    icon: 'i-lucide-triangle-alert',
    href:
      unavailableNodes.value.length || issues.value.length
        ? '#attention-heading'
        : '#resources-heading',
  },
  {
    label: 'Updates',
    value: availableUpdates.value,
    icon: 'i-lucide-circle-arrow-up',
    href: updates.value.length ? '#updates-heading' : '#resources-heading',
  },
  {
    label: 'Busy',
    value: busyStacks.value.length,
    icon: 'i-lucide-loader-circle',
    href: busyStacks.value.length ? '#busy-heading' : '#resources-heading',
  },
])

const checkUpdates = async () => {
  const { status } = await confirmDialog({
    title: 'Check for updates?',
    message: 'Check all configured nodes for container image updates?',
    confirmText: 'Check for updates',
  })
  if (status) {
    await workspace.checkImages()
  }
}

const draggedNode = ref('')
const dropTarget = ref('')
const startDrag = (event: PointerEvent, id: string) => {
  if (
    0 !== event.button ||
    workspace.savingNodeOrder ||
    !(event.currentTarget instanceof HTMLElement)
  ) {
    return
  }
  event.currentTarget.setPointerCapture(event.pointerId)
  draggedNode.value = id
}
const dragNode = (event: PointerEvent) => {
  if (!draggedNode.value) {
    return
  }
  const target = document
    .elementFromPoint(event.clientX, event.clientY)
    ?.closest<HTMLElement>('[data-node-id]')
  dropTarget.value = target?.dataset.nodeId ?? ''
}
const endDrag = () => {
  draggedNode.value = ''
  dropTarget.value = ''
}
const dropNode = () => {
  const id = draggedNode.value
  const target = dropTarget.value
  endDrag()
  if (id && target) {
    void workspace.moveNode(id, target)
  }
}
const moveByKeyboard = async (event: KeyboardEvent, id: string, direction: number) => {
  const index = nodes.value.findIndex((node) => id === node.id)
  const target = nodes.value[index + direction]
  if (target && event.currentTarget instanceof HTMLElement) {
    const handle = event.currentTarget
    await workspace.moveNode(id, target.id)
    await nextTick()
    handle.focus()
  }
}

useHead({ title: 'Overview' })
</script>

<template>
  <div class="min-w-0 space-y-6">
    <div class="flex flex-wrap items-center justify-end gap-2">
      <UButton
        color="neutral"
        variant="outline"
        icon="i-lucide-refresh-cw"
        aria-label="Check for updates on all nodes"
        :loading="workspace.isCheckingImages()"
        :disabled="workspace.isCheckingImages() || !reportingNodes.length"
        @click="checkUpdates"
        >Check for updates</UButton
      >
      <UButton
        color="neutral"
        variant="outline"
        icon="i-lucide-refresh-cw"
        :loading="workspaceLoading"
        @click="workspace.load"
        >Refresh</UButton
      >
    </div>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <a
        v-for="count in counts"
        :key="count.href + count.icon"
        :href="count.href"
        class="min-w-0 rounded-lg focus-visible:outline-2 focus-visible:outline-primary"
      >
        <StatCard
          :label="count.label"
          :value="count.value"
          :icon="count.icon"
          color="neutral"
          :hint="count.hint"
          :tooltip="count.tooltip"
        />
      </a>
    </div>

    <section
      v-if="unavailableNodes.length || issues.length"
      class="app-card overflow-hidden"
      aria-labelledby="attention-heading"
    >
      <div class="flex items-center gap-2 border-b border-default px-4 py-3">
        <UIcon name="i-lucide-triangle-alert" class="size-4 text-warning" />
        <h2 id="attention-heading" class="text-sm font-semibold text-highlighted">
          Needs attention
        </h2>
        <UBadge color="warning" variant="subtle" size="sm">{{
          unavailableNodes.length + issues.length
        }}</UBadge>
      </div>
      <ul class="divide-y divide-default">
        <li v-for="node in unavailableNodes" :key="node.id">
          <NuxtLink
            :to="`/nodes/${encodeURIComponent(node.id)}`"
            class="flex min-w-0 items-start gap-3 px-4 py-3 hover:bg-elevated"
          >
            <UIcon name="i-lucide-server-off" class="mt-0.5 size-4 shrink-0 text-error" />
            <div class="min-w-0 flex-1 space-y-1">
              <p class="text-sm font-medium text-highlighted">{{ node.name }}</p>
              <p v-if="node.error" class="text-xs text-muted">
                {{ node.error }}
              </p>
              <UTooltip text="Cached stack inventory. Current status is unknown.">
                <UBadge
                  icon="i-lucide-layers"
                  color="neutral"
                  variant="subtle"
                  size="sm"
                  tabindex="0"
                  >{{ node.stacks.length }} unverified</UBadge
                >
              </UTooltip>
            </div>
            <UBadge
              :color="'reconnecting' === node.connection ? 'warning' : 'error'"
              :icon="
                'reconnecting' === node.connection ? 'i-lucide-loader-circle' : 'i-lucide-wifi-off'
              "
              variant="subtle"
              size="sm"
              >{{ 'reconnecting' === node.connection ? 'Reconnecting' : 'Unavailable' }}</UBadge
            >
          </NuxtLink>
        </li>
        <li v-for="item in issues" :key="`${item.node.id}/${item.stack.name}`">
          <NuxtLink
            :to="`/stacks/${encodeURIComponent(item.node.id)}/${encodeURIComponent(item.stack.name)}`"
            class="flex min-w-0 items-start gap-3 px-4 py-3 hover:bg-elevated"
          >
            <UIcon name="i-lucide-triangle-alert" class="mt-0.5 size-4 shrink-0 text-warning" />
            <div class="min-w-0 flex-1 space-y-1">
              <p class="text-xs text-muted">{{ item.node.name }}</p>
              <p class="text-sm font-medium text-highlighted">{{ item.stack.name }}</p>
              <p
                v-for="message in item.messages"
                :key="message"
                class="whitespace-pre-line break-words text-xs text-muted"
              >
                {{ message }}
              </p>
            </div>
            <UBadge
              :color="0 === item.priority ? 'error' : 'warning'"
              :icon="0 === item.priority ? 'i-lucide-circle-alert' : 'i-lucide-triangle-alert'"
              variant="subtle"
              size="sm"
              >{{
                0 === item.priority
                  ? 'Unavailable'
                  : 1 === item.priority
                    ? 'Container'
                    : 'Check failed'
              }}</UBadge
            >
          </NuxtLink>
        </li>
      </ul>
    </section>

    <section v-if="busyStacks.length" aria-labelledby="busy-heading" class="space-y-3">
      <h2 id="busy-heading" class="text-sm font-semibold text-highlighted">In progress</h2>
      <div class="flex flex-wrap gap-2">
        <NuxtLink
          v-for="item in busyStacks"
          :key="`${item.node.id}/${item.stack.name}`"
          :to="`/stacks/${encodeURIComponent(item.node.id)}/${encodeURIComponent(item.stack.name)}`"
          class="rounded-md focus-visible:outline-2 focus-visible:outline-primary"
        >
          <UBadge
            color="info"
            variant="subtle"
            icon="i-lucide-loader-circle"
            :ui="{ leadingIcon: 'animate-spin' }"
            >{{ item.node.name }} / {{ item.stack.name }}</UBadge
          >
        </NuxtLink>
      </div>
    </section>

    <div class="min-w-0 space-y-6">
      <section class="min-w-0 space-y-3" aria-labelledby="resources-heading">
        <div class="flex items-center justify-between gap-2">
          <h2
            id="resources-heading"
            class="inline-flex items-center gap-2 text-sm font-semibold text-highlighted"
          >
            <UIcon name="i-lucide-chart-no-axes-combined" class="size-4 text-primary" />
            Node resources
          </h2>
        </div>
        <div class="grid items-start gap-4 md:grid-cols-2">
          <NodeResources
            v-for="node in nodes"
            :key="node.id"
            :node="node"
            :data-node-id="node.id"
            :class="{
              'opacity-50': node.id === draggedNode,
              'ring-2 ring-primary': node.id === dropTarget && node.id !== draggedNode,
            }"
          >
            <template #leading>
              <UTooltip
                v-if="1 < nodes.length"
                text="Drag to reorder, or use the up and down arrow keys."
              >
                <button
                  type="button"
                  class="flex size-8 shrink-0 cursor-grab touch-none items-center justify-center rounded-md bg-elevated text-toned hover:text-primary focus-visible:outline-2 focus-visible:outline-primary active:cursor-grabbing disabled:cursor-wait"
                  :aria-label="`Reorder ${node.name}`"
                  :disabled="workspace.savingNodeOrder"
                  @pointerdown="startDrag($event, node.id)"
                  @pointermove="dragNode"
                  @pointerup="dropNode"
                  @pointercancel="endDrag"
                  @keydown.up.prevent="moveByKeyboard($event, node.id, -1)"
                  @keydown.down.prevent="moveByKeyboard($event, node.id, 1)"
                >
                  <UIcon name="i-lucide-grip-vertical" class="size-5" />
                </button>
              </UTooltip>
            </template>
          </NodeResources>
        </div>
      </section>

      <section
        v-if="updates.length"
        class="app-card min-w-0 overflow-hidden"
        aria-labelledby="updates-heading"
      >
        <div
          class="flex flex-wrap items-center justify-between gap-2 border-b border-default px-4 py-3"
        >
          <h2
            id="updates-heading"
            class="inline-flex items-center gap-2 text-sm font-semibold text-highlighted"
          >
            <UIcon name="i-lucide-package" class="size-4 text-primary" />Updates
          </h2>
        </div>
        <ul class="divide-y divide-default">
          <li
            v-for="item in updates"
            :key="`${item.node.id}/${item.stack.name}`"
            class="space-y-3 px-4 py-3"
          >
            <div class="flex flex-wrap items-center justify-between gap-2">
              <NuxtLink
                :to="`/stacks/${encodeURIComponent(item.node.id)}/${encodeURIComponent(item.stack.name)}`"
                class="min-w-0 text-sm font-medium text-highlighted hover:text-primary"
                >{{ item.node.name }} / {{ item.stack.name }}</NuxtLink
              >
              <UButton
                size="xs"
                color="neutral"
                variant="outline"
                icon="i-lucide-refresh-cw"
                :loading="workspace.isCheckingImages(item.node.id, item.stack.name)"
                :disabled="
                  item.stack.busy || workspace.isCheckingImages(item.node.id, item.stack.name)
                "
                :aria-label="`Check updates for ${item.stack.name} on ${item.node.name}`"
                @click="workspace.checkImages(item.node.id, item.stack.name)"
                >Check</UButton
              >
            </div>
            <div v-for="service in item.services" :key="service.name" class="space-y-1">
              <div class="flex flex-wrap items-center gap-2">
                <span class="break-all text-xs font-medium">{{ service.service }}</span>
                <UTooltip
                  v-if="service.image_update"
                  :text="imageUpdateTooltip(service.image_update)"
                  :ui="{ text: 'whitespace-pre-line' }"
                >
                  <UBadge
                    :icon="
                      'available' === service.image_update.status
                        ? 'i-lucide-package'
                        : 'error' === service.image_update.status
                          ? 'i-lucide-circle-alert'
                          : 'i-lucide-loader-circle'
                    "
                    size="sm"
                    variant="subtle"
                    :color="
                      'available' === service.image_update.status
                        ? 'warning'
                        : 'error' === service.image_update.status
                          ? 'error'
                          : 'info'
                    "
                    tabindex="0"
                    >{{
                      'available' === service.image_update.status
                        ? 'Update'
                        : 'error' === service.image_update.status
                          ? 'Failed'
                          : 'Checking'
                    }}</UBadge
                  >
                </UTooltip>
              </div>
              <p class="break-all font-mono text-xs text-muted">{{ service.image }}</p>
              <UTooltip
                v-if="service.image_update?.checked_at"
                :text="formatLogDate(service.image_update.checked_at, 'timestamp')"
              >
                <span class="inline-flex items-center gap-1 text-xs text-muted" tabindex="0"
                  ><UIcon name="i-lucide-clock" class="size-3" /><time
                    :datetime="service.image_update.checked_at"
                    >{{ formatLogDate(service.image_update.checked_at, 'clock') }}</time
                  ></span
                >
              </UTooltip>
            </div>
          </li>
        </ul>
      </section>
    </div>

    <section
      v-if="recentErrors.length || logsError"
      class="app-card overflow-hidden"
      aria-labelledby="recent-errors-heading"
    >
      <div
        class="flex flex-wrap items-center justify-between gap-2 border-b border-default px-4 py-3"
      >
        <h2
          id="recent-errors-heading"
          class="inline-flex items-center gap-2 text-sm font-semibold text-highlighted"
        >
          <UIcon name="i-lucide-scroll-text" class="size-4 text-warning" />
          <NuxtLink :to="logsLink()" class="hover:text-primary">Recent logs</NuxtLink>
        </h2>
      </div>
      <UAlert v-if="logsError" color="warning" :title="logsError" class="m-3">
        <template #actions
          ><UButton size="xs" color="neutral" variant="outline" @click="refreshLogs"
            >Retry</UButton
          ></template
        >
      </UAlert>
      <ul v-if="recentErrors.length" class="divide-y divide-default">
        <li v-for="entry in recentErrors" :key="entry.id" :data-log-id="entry.id">
          <NuxtLink :to="logsLink(entry)" class="block space-y-2 px-4 py-3 hover:bg-elevated">
            <div class="flex flex-wrap items-center gap-2 text-xs text-muted">
              <UBadge
                :icon="
                  'error' === getLogLevel(entry.level)
                    ? 'i-lucide-circle-alert'
                    : 'i-lucide-triangle-alert'
                "
                size="sm"
                variant="subtle"
                :color="'error' === getLogLevel(entry.level) ? 'error' : 'warning'"
                >{{ entry.level }}</UBadge
              >
              <UBadge icon="i-lucide-server" color="neutral" variant="subtle" size="sm">{{
                logNodeLabel(entry)
              }}</UBadge>
              <UTooltip :text="formatLogDate(entry.timestamp, 'timestamp')"
                ><time :datetime="entry.timestamp" tabindex="0">{{
                  formatLogDate(entry.timestamp, 'clock')
                }}</time></UTooltip
              >
            </div>
            <p class="whitespace-pre-wrap break-words text-sm">{{ entry.message }}</p>
          </NuxtLink>
        </li>
      </ul>
      <div v-if="logsTruncated" class="border-t border-default px-4 py-2">
        <UTooltip text="Older application logs have expired. Stack command output is live-only."
          ><UBadge icon="i-lucide-history" color="neutral" variant="subtle" size="sm" tabindex="0"
            >Limited history</UBadge
          ></UTooltip
        >
      </div>
    </section>
  </div>
</template>
