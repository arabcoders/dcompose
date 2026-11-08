<script setup lang="ts">
import { storeToRefs } from 'pinia'
import { NuxtLayout } from '#components'
import UAlert from '@nuxt/ui/components/Alert.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import { useWorkspaceStore } from '~/store/workspace'

const workspace = useWorkspaceStore()
const { nodes, ready, error, streamError } = storeToRefs(workspace)
</script>

<template>
  <NuxtLayout name="default">
    <div class="min-w-0 space-y-5">
      <UAlert v-if="error" color="error" variant="soft" :title="error" />
      <UAlert v-else-if="streamError" color="warning" variant="soft" :title="streamError" />
      <UAlert
        v-else-if="ready && 0 === nodes.length"
        color="neutral"
        variant="soft"
        title="No nodes added"
      >
        <template #actions
          ><UButton size="sm" icon="i-lucide-plus" @click="workspace.openSettings('nodes')"
            >Add node</UButton
          ></template
        >
      </UAlert>

      <div class="min-w-0">
        <div v-if="!ready" class="app-frame space-y-4 p-5" aria-label="Loading stacks">
          <div class="h-6 w-48 animate-pulse rounded bg-elevated" />
          <div class="h-48 animate-pulse rounded bg-elevated/50" />
        </div>
        <slot v-else-if="0 < nodes.length" />
      </div>
    </div>
  </NuxtLayout>
</template>
