<script setup lang="ts">
import { computed } from 'vue'
import { createReusableTemplate } from '@vueuse/core'
import { useRoute } from '#app'
import { NuxtLink } from '#components'
import UAlert from '@nuxt/ui/components/Alert.vue'
import UBadge from '@nuxt/ui/components/Badge.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UIcon from '@nuxt/ui/components/Icon.vue'
import UPopover from '@nuxt/ui/components/Popover.vue'
import UTooltip from '@nuxt/ui/components/Tooltip.vue'
import type { StackListProps, StackRuntime, StackStatus, WorkspaceNode } from '~/types'
import { compareStacks, stackStatusLabel } from '~/utils/stacks'

const props = withDefaults(defineProps<StackListProps>(), { compact: false })
const emit = defineEmits<{ selected: [] }>()
const route = useRoute()
const collapsed = defineModel<Record<string, boolean>>('collapsed', { required: true })
const [DefineStacks, ReuseStacks] = createReusableTemplate<{
  node: WorkspaceNode
  select: () => void
}>()
const hasImageUpdates = (stack: StackRuntime) =>
  stack.services.some((service) => 'available' === service.image_update?.status)
const autoUpdateCount = (stack: StackRuntime) =>
  stack.services.filter((service) => service.auto_update).length
const filteredNodes = computed(() =>
  props.nodes.map((node) => {
    const stacks = node.stacks
      .filter(
        (stack) =>
          stack.name.includes(props.search.trim().toLowerCase()) &&
          ('all' === props.status ||
            ('updates' === props.status ? hasImageUpdates(stack) : stack.status === props.status)),
      )
      .sort(compareStacks)

    return { ...node, stacks, updateCount: stacks.filter(hasImageUpdates).length }
  }),
)
const statusClass = (status: StackStatus) => {
  if ('running' === status) {
    return 'bg-success'
  }
  if ('partial' === status) {
    return 'bg-warning'
  }
  if ('stopped' === status) {
    return 'bg-error'
  }
  return 'bg-muted'
}
const selected = (node: string, stack: string) =>
  route.params.node === node && route.params.stack === stack
</script>

<template>
  <nav aria-label="Compose stacks" class="flex min-h-0 flex-col">
    <DefineStacks v-slot="{ node, select }">
      <div class="space-y-1">
        <UAlert
          v-if="'connected' === node.connection && 0 === node.stacks.length && node.loading"
          color="info"
          variant="soft"
          icon="i-lucide-loader-circle"
          title="Loading stacks..."
          class="p-2"
          :ui="{ title: 'text-xs', icon: 'size-4 animate-spin' }"
        />
        <UAlert
          v-else-if="'connected' === node.connection && 0 === node.stacks.length && !node.loading"
          color="neutral"
          variant="soft"
          :icon="search || 'all' !== status ? 'i-lucide-search-x' : 'i-lucide-layers'"
          :title="search || 'all' !== status ? 'No matching stacks' : 'No stacks'"
          class="mt-2 p-2"
          :ui="{ title: 'text-xs', icon: 'size-4' }"
        />
        <NuxtLink
          v-for="stack in node.stacks"
          :key="stack.name"
          :to="`/stacks/${encodeURIComponent(node.id)}/${encodeURIComponent(stack.name)}`"
          :aria-current="selected(node.id, stack.name) ? 'page' : undefined"
          class="group flex min-w-0 items-center gap-2.5 rounded-md px-2 py-2 text-sm transition focus-visible:outline-2 focus-visible:outline-primary"
          :class="
            selected(node.id, stack.name)
              ? 'bg-primary/10 font-medium text-primary'
              : 'text-default hover:bg-elevated/70'
          "
          @click="select"
        >
          <span
            class="size-2 shrink-0 rounded-full"
            :class="stack.loading ? 'animate-pulse bg-muted' : statusClass(stack.status)"
          />
          <span class="min-w-0 flex-1 truncate">{{ stack.name }}</span>
          <UTooltip
            v-if="autoUpdateCount(stack)"
            :text="`${autoUpdateCount(stack)} of ${stack.services.length} containers auto-update`"
          >
            <span class="inline-flex shrink-0 text-primary" tabindex="0">
              <UIcon name="i-lucide-bot" class="size-3.5" />
              <span class="sr-only">Auto-updates</span>
            </span>
          </UTooltip>
          <UTooltip v-if="hasImageUpdates(stack)" text="Updates available">
            <span class="inline-flex shrink-0 text-warning">
              <UIcon name="i-lucide-circle-arrow-up" class="size-3.5" />
              <span class="sr-only">Updates available</span>
            </span>
          </UTooltip>
          <span class="text-[10px] text-muted">{{
            stack.loading ? 'Loading' : stackStatusLabel(stack.status)
          }}</span>
        </NuxtLink>
      </div>
    </DefineStacks>
    <div class="min-h-0 flex-1 space-y-1.5 overflow-y-auto p-2">
      <UAlert
        v-if="workspaceLoading && 0 === nodes.length"
        color="info"
        variant="soft"
        icon="i-lucide-loader-circle"
        title="Loading stacks..."
        class="p-2"
        :ui="{ title: compact ? 'sr-only' : 'text-xs', icon: 'size-4 animate-spin' }"
      />
      <UAlert
        v-else-if="0 === nodes.length"
        color="neutral"
        variant="soft"
        icon="i-lucide-server-off"
        title="No nodes added."
        class="p-2"
        :ui="{ title: compact ? 'sr-only' : 'text-xs', icon: 'size-4' }"
      />
      <section v-for="node in filteredNodes" :key="node.id" class="space-y-1">
        <UPopover
          v-if="compact"
          :content="{ side: 'right', align: 'start' }"
          :ui="{ content: 'w-72 max-w-[calc(100vw-2rem)] p-2' }"
        >
          <UTooltip :text="node.name">
            <UButton
              :aria-label="node.name"
              :icon="'paused' === node.connection ? 'i-lucide-pause' : 'i-lucide-server'"
              color="neutral"
              :variant="route.params.node === node.id ? 'soft' : 'ghost'"
              class="relative w-full justify-center"
              :ui="{
                leadingIcon:
                  'connected' === node.connection
                    ? 'text-success'
                    : 'paused' === node.connection
                      ? 'text-muted'
                      : 'unavailable' === node.connection
                        ? 'text-error'
                        : 'text-warning',
              }"
            >
              <span
                v-if="0 < node.updateCount"
                class="absolute top-1 right-1 size-2 rounded-full bg-warning"
              >
                <span class="sr-only">{{ node.updateCount }} stacks with updates</span>
              </span>
            </UButton>
          </UTooltip>
          <template #content="{ close }">
            <p class="truncate px-2 py-2 text-xs font-semibold text-highlighted">{{ node.name }}</p>
            <div class="max-h-80 overflow-y-auto">
              <ReuseStacks
                :node="node"
                :select="
                  () => {
                    close()
                    emit('selected')
                  }
                "
              />
            </div>
          </template>
        </UPopover>
        <template v-else>
          <button
            type="button"
            class="flex w-full min-w-0 items-center gap-2 overflow-hidden rounded-md px-2.5 py-2 text-start text-sm font-medium transition-colors hover:bg-elevated/60 focus-visible:outline-2 focus-visible:outline-primary"
            :class="route.params.node === node.id ? 'text-primary' : 'text-default'"
            :aria-label="`${collapsed[node.id] ? 'Expand' : 'Collapse'} stacks on ${node.name}${0 < node.updateCount ? `, ${node.updateCount} with updates` : ''}`"
            :aria-expanded="!collapsed[node.id]"
            @click="collapsed[node.id] = !collapsed[node.id]"
          >
            <UTooltip
              :text="
                'connected' === node.connection
                  ? 'Connected'
                  : 'paused' === node.connection
                    ? 'Paused'
                    : 'loading' === node.connection
                      ? 'Connecting'
                      : 'reconnecting' === node.connection
                        ? 'Reconnecting'
                        : 'Unavailable'
              "
            >
              <UIcon
                :name="'paused' === node.connection ? 'i-lucide-pause' : 'i-lucide-server'"
                class="size-3.5 shrink-0"
                :class="
                  'connected' === node.connection
                    ? 'text-success'
                    : 'paused' === node.connection
                      ? 'text-muted'
                      : 'unavailable' === node.connection
                        ? 'text-error'
                        : 'text-warning'
                "
              />
            </UTooltip>
            <span class="min-w-0 flex-1 truncate">
              {{ node.name }}
            </span>
            <UTooltip
              v-if="0 < node.updateCount"
              :text="`${node.updateCount} ${1 === node.updateCount ? 'stack' : 'stacks'} with updates`"
            >
              <UBadge
                color="warning"
                variant="soft"
                size="sm"
                icon="i-lucide-circle-arrow-up"
                class="shrink-0 tabular-nums"
                >{{ node.updateCount }}</UBadge
              >
            </UTooltip>
            <UBadge
              color="neutral"
              variant="soft"
              size="sm"
              icon="i-lucide-layers"
              class="shrink-0 tabular-nums"
            >
              {{ node.stacks.length }}
            </UBadge>
            <UIcon
              v-if="node.loading"
              name="i-lucide-loader-circle"
              class="size-3 shrink-0 animate-spin text-muted"
            />
            <UIcon
              :name="collapsed[node.id] ? 'i-lucide-chevron-right' : 'i-lucide-chevron-down'"
              class="size-4 shrink-0 text-muted"
            />
          </button>
          <div v-show="!collapsed[node.id]" class="ms-4 border-s border-default ps-2">
            <ReuseStacks :node="node" :select="() => emit('selected')" />
          </div>
        </template>
      </section>
    </div>
  </nav>
</template>
