<script setup lang="ts">
import { ref } from 'vue'
import { useRuntimeHook } from '#app'
import UButton from '@nuxt/ui/components/Button.vue'
import UIcon from '@nuxt/ui/components/Icon.vue'

const frontendUpdated = ref(false)
useRuntimeHook('app:manifest:update', () => {
  frontendUpdated.value = true
})

const reloadPage = () => window.location.reload()
</script>

<template>
  <div
    v-if="frontendUpdated"
    class="app-card flex flex-wrap items-center gap-x-4 gap-y-3 border-success/35 bg-success/10 px-3 py-2.5 sm:px-4"
  >
    <div class="flex min-w-0 items-center gap-3" role="status">
      <span class="app-detail-icon border-success/30 bg-success/15 text-success" aria-hidden="true">
        <UIcon name="i-lucide-circle-check" class="size-4" />
      </span>
      <p class="text-sm font-medium text-highlighted">DCompose has been updated</p>
    </div>

    <div class="ms-auto flex max-w-full flex-wrap justify-end gap-2">
      <UButton icon="i-lucide-refresh-cw" size="sm" label="Reload app" @click="reloadPage" />
    </div>
  </div>
</template>
