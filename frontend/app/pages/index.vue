<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
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
import { useDialog } from '~/composables/useDialog'
import { useWorkspaceStore } from '~/store/workspace'
import type { StackFilter, StackRuntime } from '~/types'
import { containerProblem } from '~/utils/stacks'

definePageMeta({ layout: 'workspace' })

const workspace = useWorkspaceStore()
const { nodes, loading: workspaceLoading } = storeToRefs(workspace)
const { confirmDialog } = useDialog()
const draggedNode = ref('')
const dropTarget = ref('')

const filterStacks = (status: StackFilter) => {
  workspace.stackFilter = status
  workspace.stackSearch = ''
  workspace.sidebarOpen = window.matchMedia('(max-width: 1023px)').matches
}

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
const stacks = computed(() => nodes.value.flatMap((node) => node.stacks))
const problems = computed(() =>
  nodes.value.flatMap((node) =>
    'connected' === node.connection
      ? node.stacks.flatMap((stack) =>
          stack.loading || stack.error
            ? []
            : stack.services.flatMap((service) => {
                const problem = containerProblem(service)
                return problem ? [{ node, stack: stack.name, service, problem }] : []
              }),
        )
      : [],
  ),
)
const unavailableNodes = computed(() =>
  nodes.value.filter(
    (node) => 'unavailable' === node.connection || 'reconnecting' === node.connection,
  ),
)
const updatesCount = (items: Array<StackRuntime>) =>
  items.filter((stack) =>
    stack.services.some((service) => 'available' === service.image_update?.status),
  ).length
const counts = computed(() => [
  {
    label: 'Running',
    filter: 'running' as const,
    count: stacks.value.filter((stack) => 'running' === stack.status).length,
    icon: 'i-lucide-circle-play',
  },
  {
    label: 'Stopped',
    filter: 'stopped' as const,
    count: stacks.value.filter((stack) => 'stopped' === stack.status).length,
    icon: 'i-lucide-circle-stop',
  },
  {
    label: 'Drafts',
    filter: 'draft' as const,
    count: stacks.value.filter((stack) => 'draft' === stack.status).length,
    icon: 'i-lucide-file-pen',
  },
  {
    label: 'Updates',
    filter: 'updates' as const,
    count: updatesCount(stacks.value),
    icon: 'i-lucide-circle-arrow-up',
  },
])
</script>

<template>
  <div class="min-w-0 space-y-6">
    <div class="flex items-center justify-end gap-2">
      <UButton
        color="neutral"
        variant="outline"
        icon="i-lucide-refresh-cw"
        aria-label="Check for updates on all nodes"
        :loading="workspace.isCheckingImages()"
        :disabled="workspace.isCheckingImages() || nodes.every((node) => node.paused)"
        @click="checkUpdates"
        >Check for updates</UButton
      >
      <UButton
        color="neutral"
        variant="outline"
        icon="i-lucide-refresh-cw"
        aria-label="Refresh nodes"
        :loading="workspaceLoading"
        @click="workspace.load"
        >Refresh</UButton
      >
    </div>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <button
        v-for="count in counts"
        :key="count.filter"
        type="button"
        :aria-pressed="workspace.stackFilter === count.filter"
        class="min-w-0 cursor-pointer rounded-lg text-left focus-visible:outline-2 focus-visible:outline-primary"
        @click="filterStacks(count.filter)"
      >
        <StatCard :label="count.label" :value="count.count" :icon="count.icon" />
      </button>
    </div>

    <UAlert
      v-if="workspaceLoading"
      color="info"
      variant="soft"
      icon="i-lucide-loader-circle"
      title="Loading stacks..."
      :ui="{ icon: 'animate-spin' }"
    />

    <section
      v-if="problems.length"
      class="app-card overflow-hidden"
      aria-labelledby="attention-heading"
    >
      <div class="flex items-center gap-2 px-4 py-3">
        <UIcon name="i-lucide-triangle-alert" class="size-4 text-warning" />
        <h2 id="attention-heading" class="text-sm font-semibold text-highlighted">
          Needs attention
        </h2>
        <UBadge color="error" variant="soft" size="sm">
          {{ problems.length }}
        </UBadge>
      </div>
      <ul class="divide-y divide-default border-t border-default">
        <li
          v-for="item in problems"
          :key="JSON.stringify([item.node.id, item.stack, item.service.name])"
        >
          <NuxtLink
            :to="`/stacks/${encodeURIComponent(item.node.id)}/${encodeURIComponent(item.stack)}`"
            class="flex min-w-0 items-center gap-3 px-4 py-3 transition hover:bg-elevated focus-visible:outline-2 focus-visible:outline-primary"
          >
            <div class="min-w-0 flex-1">
              <p class="truncate text-xs text-muted">{{ item.node.name }} / {{ item.stack }}</p>
              <p class="truncate text-sm font-medium text-highlighted">{{ item.service.name }}</p>
            </div>
            <UBadge
              :color="'restarting' === item.service.state ? 'warning' : 'error'"
              variant="soft"
              size="sm"
              class="shrink-0"
            >
              {{ item.problem }}
            </UBadge>
            <UIcon name="i-lucide-chevron-right" class="size-4 shrink-0 text-muted" />
          </NuxtLink>
        </li>
      </ul>
    </section>

    <section v-if="unavailableNodes.length" aria-labelledby="unavailable-heading" class="space-y-3">
      <h2 id="unavailable-heading" class="text-sm font-semibold text-highlighted">
        Unavailable nodes
      </h2>
      <NuxtLink
        v-for="node in unavailableNodes"
        :key="node.id"
        :to="`/nodes/${encodeURIComponent(node.id)}`"
        class="app-card flex min-w-0 items-center gap-3 p-4 transition hover:bg-elevated focus-visible:outline-2 focus-visible:outline-primary"
      >
        <UIcon name="i-lucide-server-off" class="size-4 shrink-0 text-warning" />
        <div class="min-w-0 flex-1">
          <p class="truncate text-sm font-medium text-highlighted">{{ node.name }}</p>
          <p class="text-xs text-muted">{{ node.error || 'Container status is unavailable.' }}</p>
        </div>
        <UBadge color="warning" variant="soft" size="sm" class="shrink-0">
          {{ 'reconnecting' === node.connection ? 'Reconnecting' : 'Unavailable' }}
        </UBadge>
      </NuxtLink>
    </section>

    <div class="space-y-3">
      <article
        v-for="node in nodes"
        :key="node.id"
        :data-node-id="node.id"
        class="app-card flex items-center gap-3 p-4"
        :class="{
          'opacity-50': node.id === draggedNode,
          'ring-2 ring-primary': node.id === dropTarget && node.id !== draggedNode,
        }"
      >
        <UTooltip
          v-if="1 < nodes.length"
          text="Drag to reorder, or use the up and down arrow keys."
        >
          <button
            type="button"
            class="shrink-0 cursor-grab touch-none rounded text-muted hover:text-highlighted focus-visible:outline-2 focus-visible:outline-primary active:cursor-grabbing disabled:cursor-wait"
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
        <UTooltip :text="node.name">
          <NuxtLink
            :to="`/nodes/${encodeURIComponent(node.id)}`"
            class="min-w-0 flex-1 truncate text-sm font-semibold text-highlighted hover:text-primary hover:underline focus-visible:outline-2 focus-visible:outline-primary"
            >{{ node.name }}</NuxtLink
          >
        </UTooltip>
        <UTooltip
          v-if="node.health?.version"
          :text="
            node.health?.os && node.health.arch
              ? `${node.health.os}/${node.health.arch}`
              : undefined
          "
        >
          <UBadge
            color="neutral"
            variant="soft"
            size="sm"
            icon="i-lucide-tag"
            class="hidden shrink-0 md:inline-flex"
            tabindex="0"
            >v{{ node.health.version }}</UBadge
          >
        </UTooltip>
        <UTooltip v-if="node.error" :text="node.error">
          <p class="min-w-0 max-w-64 truncate text-xs text-error" tabindex="0">
            {{ node.error }}
          </p>
        </UTooltip>
        <template v-else>
          <UTooltip
            :text="`${node.stacks.length} ${1 === node.stacks.length ? 'stack' : 'stacks'}`"
          >
            <UBadge
              color="neutral"
              variant="soft"
              size="sm"
              icon="i-lucide-layers"
              class="shrink-0 tabular-nums"
              tabindex="0"
            >
              {{ node.stacks.length }}
              <span class="hidden sm:inline">{{
                1 === node.stacks.length ? 'stack' : 'stacks'
              }}</span>
            </UBadge>
          </UTooltip>
          <UTooltip
            v-if="updatesCount(node.stacks)"
            :text="`${updatesCount(node.stacks)} ${1 === updatesCount(node.stacks) ? 'stack' : 'stacks'} with updates`"
          >
            <button
              type="button"
              class="shrink-0 rounded-md focus-visible:outline-2 focus-visible:outline-primary"
              aria-label="View stacks with updates"
              @click="filterStacks('updates')"
            >
              <UBadge
                color="warning"
                variant="soft"
                size="sm"
                icon="i-lucide-circle-arrow-up"
                class="tabular-nums"
                >{{ updatesCount(node.stacks) }}</UBadge
              >
            </button>
          </UTooltip>
        </template>
        <UBadge
          :color="
            'connected' === node.connection
              ? 'success'
              : 'paused' === node.connection
                ? 'neutral'
                : 'unavailable' === node.connection
                  ? 'error'
                  : 'warning'
          "
          variant="soft"
          size="sm"
          :icon="'paused' === node.connection ? 'i-lucide-pause' : 'i-lucide-network'"
          class="shrink-0"
        >
          {{
            'connected' === node.connection
              ? 'Connected'
              : 'paused' === node.connection
                ? 'Paused'
                : 'unavailable' === node.connection
                  ? 'Unavailable'
                  : 'loading' === node.connection
                    ? 'Connecting'
                    : 'Reconnecting'
          }}
        </UBadge>
        <UTooltip :text="`Check for updates on ${node.name}`">
          <UButton
            size="xs"
            color="neutral"
            variant="outline"
            icon="i-lucide-refresh-cw"
            class="shrink-0 gap-1 rounded-sm px-1.5 text-[10px]/3"
            :ui="{ leadingIcon: 'size-3' }"
            :aria-label="`Check for updates on ${node.name}`"
            :loading="workspace.isCheckingImages(node.id)"
            :disabled="
              workspace.isCheckingImages(node.id) || node.loading || 'connected' !== node.connection
            "
            @click="workspace.checkImages(node.id)"
          >
            <span class="hidden sm:inline">Check for updates</span>
          </UButton>
        </UTooltip>
      </article>
    </div>
  </div>
</template>
