<template>
  <div class="app-page-header">
    <div class="app-page-heading">
      <span class="app-page-icon">
        <UIcon :name="icon" class="size-5" />
      </span>

      <div class="min-w-0 space-y-2">
        <div class="app-page-kicker">
          <span>{{ sectionLabel }}</span>
          <span>/</span>
          <span>{{ pageLabel }}</span>
        </div>

        <p class="max-w-3xl text-sm text-toned">{{ description }}</p>
      </div>
    </div>

    <div class="flex w-full flex-wrap items-center gap-2 xl:w-auto xl:justify-end">
      <slot name="actions" />
    </div>
  </div>
</template>

<script setup lang="ts">
import UIcon from '@nuxt/ui/components/Icon.vue'
import type { PageHeaderProps } from '~/types'

defineProps<PageHeaderProps>()
</script>
