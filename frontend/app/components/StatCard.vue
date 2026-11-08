<script setup lang="ts">
import { computed } from 'vue'
import UIcon from '@nuxt/ui/components/Icon.vue'
import UTooltip from '@nuxt/ui/components/Tooltip.vue'
import type { StatCardProps } from '~/types'

const props = withDefaults(defineProps<StatCardProps>(), {
  hint: '',
  tooltip: '',
  color: 'primary',
})
const tileClass = computed(() => {
  const colors = {
    primary: 'bg-elevated text-primary ring-default',
    success: 'bg-success/10 text-success ring-success/30',
    error: 'bg-error/10 text-error ring-error/30',
    warning: 'bg-warning/10 text-warning ring-warning/30',
    info: 'bg-info/10 text-info ring-info/30',
    neutral: 'bg-elevated text-toned ring-default',
  }
  return colors[props.color]
})
</script>

<template>
  <div class="app-card p-3">
    <div class="flex items-start justify-between gap-3">
      <div class="min-w-0 flex-1 space-y-1">
        <div class="text-[11px] font-semibold uppercase tracking-[0.18em] text-toned">
          <slot name="label" :label="label">{{ label }}</slot>
        </div>
        <div class="flex min-w-0 items-baseline gap-1.5">
          <slot name="value" :value="value">
            <UTooltip :text="tooltip || String(value)" class="min-w-0 flex-1">
              <span class="block truncate text-sm font-semibold text-highlighted">
                {{ value }}
              </span>
            </UTooltip>
          </slot>
          <span v-if="hint" class="truncate text-xs text-toned">{{ hint }}</span>
        </div>
      </div>
      <div v-if="$slots.status" class="flex shrink-0 flex-wrap items-center justify-end gap-1.5">
        <slot name="status" />
      </div>
      <span
        v-else
        class="flex size-8 shrink-0 items-center justify-center rounded-md ring-1 ring-inset"
        :class="tileClass"
      >
        <template v-if="tooltip">
          <UTooltip :text="tooltip">
            <UIcon :name="icon" class="size-4" />
          </UTooltip>
        </template>
        <template v-else>
          <UIcon :name="icon" class="size-4" />
        </template>
      </span>
    </div>
    <div v-if="$slots.default" class="mt-3">
      <slot />
    </div>
    <div v-if="$slots.actions" class="mt-3 flex flex-wrap items-center justify-end gap-2">
      <slot name="actions" />
    </div>
  </div>
</template>
