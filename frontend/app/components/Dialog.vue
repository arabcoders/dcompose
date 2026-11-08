<template>
  <UModal
    v-model:open="isOpen"
    :title="options?.title ?? 'Confirm'"
    :ui="modalUi"
    @after:enter="focusTarget"
  >
    <template #body>
      <p v-if="options?.message" class="text-sm text-default">
        {{ options.message }}
      </p>
    </template>

    <template #footer>
      <div class="flex w-full flex-wrap items-center justify-end gap-2">
        <UButton
          type="button"
          icon="i-lucide-check"
          data-dialog-primary="true"
          :color="confirmColor"
          :variant="'neutral' === confirmColor ? 'outline' : 'solid'"
          @click="confirm"
          size="sm"
        >
          {{ options?.confirmText ?? 'OK' }}
        </UButton>
      </div>
    </template>
  </UModal>
</template>

<script setup lang="ts">
import { computed, nextTick, watch } from 'vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UModal from '@nuxt/ui/components/Modal.vue'
import { useDialog } from '~/composables/useDialog'

const { state, confirm, cancel } = useDialog()

const modalUi = {
  overlay: 'z-120',
  content: 'z-121 max-w-lg',
  footer: 'flex items-center justify-end gap-2 p-4 sm:px-6',
}

const options = computed(() => state.current?.opts)
const confirmColor = computed(() => options.value?.confirmColor ?? 'primary')

const isOpen = computed({
  get: () => null !== state.current,
  set: (value: boolean) => {
    if (!value) {
      cancel()
    }
  },
})

watch(
  () => state.current,
  (dialog) => {
    if (dialog) {
      void focusTarget()
    }
  },
  { immediate: true },
)

async function focusTarget(): Promise<void> {
  await nextTick()

  requestAnimationFrame(() => {
    document.querySelector<HTMLButtonElement>('[data-dialog-primary="true"]')?.focus()
  })
}
</script>
