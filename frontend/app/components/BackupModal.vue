<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UFormField from '@nuxt/ui/components/FormField.vue'
import UInput from '@nuxt/ui/components/Input.vue'
import UModal from '@nuxt/ui/components/Modal.vue'
import USelect from '@nuxt/ui/components/Select.vue'
import type { GenericError, Node } from '~/types'
import { notification, parse_api_response, request } from '~/utils'

const props = defineProps<{ open: boolean; nodes: Array<Node> }>()
const emit = defineEmits<{
  'update:open': [value: boolean]
}>()

const selectedNodes = ref(props.nodes.filter((node) => !node.paused).map((node) => node.id))
const password = ref('')
const confirmation = ref('')
const loading = ref(false)
const controller = new AbortController()
const nodeOptions = computed(() =>
  props.nodes.map((node) => ({ label: node.name, value: node.id, disabled: !!node.paused })),
)
const valid = computed(
  () =>
    0 < selectedNodes.value.length &&
    8 <= Array.from(password.value).length &&
    password.value === confirmation.value,
)

const clearPasswords = () => {
  password.value = ''
  confirmation.value = ''
}

const open = computed({
  get: () => props.open,
  set: (value: boolean) => {
    if (!value) {
      clearPasswords()
    }
    emit('update:open', value)
  },
})

const download = async () => {
  if (loading.value) {
    return
  }
  if (!valid.value) {
    notification(
      'error',
      'Invalid backup',
      'Choose at least one node. Enter a password of at least 8 characters and confirm it.',
    )
    return
  }

  loading.value = true
  try {
    const response = await request('/api/backup', {
      method: 'POST',
      headers: { Accept: 'application/zip' },
      body: JSON.stringify({ nodes: selectedNodes.value, password: password.value }),
      signal: controller.signal,
    })
    if (!response.ok) {
      const json = await parse_api_response<GenericError>(response)
      throw new Error(json.error.message || "Couldn't download the backup.")
    }

    const blob = await response.blob()
    if (controller.signal.aborted) {
      return
    }
    const url = URL.createObjectURL(blob)
    try {
      const link = document.createElement('a')
      link.href = url
      link.download = 'dcompose-backup.zip'
      document.body.append(link)
      link.click()
      link.remove()
    } finally {
      URL.revokeObjectURL(url)
    }
    open.value = false
  } catch (cause) {
    if (!controller.signal.aborted) {
      notification(
        'error',
        'Backup',
        cause instanceof Error ? cause.message : "Couldn't download the backup.",
      )
    }
  } finally {
    clearPasswords()
    loading.value = false
  }
}

onBeforeUnmount(() => {
  controller.abort()
  clearPasswords()
})
</script>

<template>
  <UModal
    v-model:open="open"
    title="Backup"
    description="DCompose configuration and selected nodes compose/.env files."
    :dismissible="!loading"
    :close="!loading"
    :ui="{ content: 'sm:max-w-xl' }"
  >
    <template #body>
      <form id="backup-download" class="space-y-4" @submit.prevent="download">
        <UFormField
          label="Nodes"
          name="nodes"
          help="Select the nodes to include in the backup."
          required
        >
          <USelect
            v-model="selectedNodes"
            :items="nodeOptions"
            multiple
            icon="i-lucide-server"
            placeholder="Choose nodes"
            :disabled="loading"
            class="w-full"
          />
        </UFormField>
        <UFormField
          label="Password"
          name="password"
          help="At least 8 characters. This password is used to encrypt the contents"
          required
        >
          <UInput
            v-model="password"
            type="password"
            autocomplete="new-password"
            icon="i-lucide-lock-keyhole"
            required
            :disabled="loading"
            class="w-full"
          />
        </UFormField>
        <UFormField label="Confirm password" name="confirmation" required>
          <UInput
            v-model="confirmation"
            type="password"
            autocomplete="new-password"
            icon="i-lucide-lock-keyhole"
            required
            :disabled="loading"
            class="w-full"
          />
        </UFormField>
      </form>
    </template>
    <template #footer>
      <div class="flex w-full flex-wrap items-center justify-end gap-2">
        <UButton
          type="submit"
          form="backup-download"
          size="sm"
          icon="i-lucide-download"
          :disabled="loading || !valid"
          :loading="loading"
          >Download</UButton
        >
      </div>
    </template>
  </UModal>
</template>
