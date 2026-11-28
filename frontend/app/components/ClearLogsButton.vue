<template>
  <UButton
    color="error"
    variant="outline"
    size="sm"
    icon="i-lucide-rotate-cw"
    :loading="clearing"
    @click="clearLogs"
  >
    Rotate logs
  </UButton>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import UButton from '@nuxt/ui/components/Button.vue'
import { useDialog } from '~/composables/useDialog'
import { useLogSummaryStore } from '~/store/logSummary'
import { notification, parse_api_response, request } from '~/utils'

const emit = defineEmits<{ cleared: [] }>()
const { confirmDialog } = useDialog()
const logSummary = useLogSummaryStore()
const clearing = ref(false)

const clearLogs = async (): Promise<void> => {
  const { status } = await confirmDialog({
    message: 'Rotate dcompose logs?',
    confirmText: 'Yes',
    confirmColor: 'primary',
  })
  if (!status) {
    return
  }

  clearing.value = true
  try {
    const response = await request('/api/logs/active', { method: 'DELETE' })
    const body = await parse_api_response<{ status: 'ok' }>(response)
    if ('error' in body || !response.ok || 'ok' !== body.status) {
      throw new Error('error' in body ? body.error.message : 'Could not clear the active log file.')
    }
    logSummary.reset()
    emit('cleared')
  } catch (cause) {
    const message = cause instanceof Error ? cause.message : 'Could not clear the active log file.'
    notification('error', 'Log rotation failed', message)
  } finally {
    clearing.value = false
  }
}
</script>
