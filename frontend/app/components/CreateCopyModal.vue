<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { navigateTo } from '#app'
import UAlert from '@nuxt/ui/components/Alert.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UFormField from '@nuxt/ui/components/FormField.vue'
import UInput from '@nuxt/ui/components/Input.vue'
import UModal from '@nuxt/ui/components/Modal.vue'
import USelect from '@nuxt/ui/components/Select.vue'
import USwitch from '@nuxt/ui/components/Switch.vue'
import { useWorkspace } from '~/composables/useWorkspace'
import type { CreateCopyModalProps, Stack } from '~/types'
import { notification, parse_api_response, request } from '~/utils'

const props = defineProps<CreateCopyModalProps>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()
const { nodes } = useWorkspace()
const destination = ref('')
const name = ref('')
const overwrite = ref(false)
const copying = ref(false)
const nodeOptions = computed(() =>
  nodes.value
    .filter((node) => props.node !== node.id)
    .map((node) => ({
      label: node.name,
      value: node.id,
      disabled: 'connected' !== node.connection,
    })),
)
const target = computed(() => nodes.value.find((node) => destination.value === node.id))
const connected = computed(
  () =>
    'connected' === target.value?.connection &&
    'connected' === nodes.value.find((node) => props.node === node.id)?.connection,
)

watch(
  () => props.open,
  (open) => {
    if (open) {
      destination.value = nodeOptions.value.find((node) => !node.disabled)?.value ?? ''
      name.value = props.stack
      overwrite.value = false
    }
  },
  { immediate: true },
)

const create = async () => {
  const stack = name.value.trim()
  if (copying.value || !connected.value || !/^[a-z0-9][a-z0-9_-]{0,127}$/.test(stack)) {
    return
  }

  const targetNode = destination.value
  const targetName = target.value?.name ?? targetNode
  copying.value = true
  try {
    const source = await request(
      `/api/nodes/${encodeURIComponent(props.node)}/stacks/${encodeURIComponent(props.stack)}`,
    )
    const files = await parse_api_response<Stack>(source)
    if ('error' in files) {
      throw new Error(files.error.message)
    }
    if (!source.ok) {
      throw new Error("Couldn't read the source stack files.")
    }

    const response = await request(
      `/api/nodes/${encodeURIComponent(targetNode)}/stacks/${encodeURIComponent(stack)}`,
      {
        method: 'POST',
        body: JSON.stringify({
          compose: files.compose,
          env: files.env,
          overwrite: overwrite.value,
        }),
      },
    )
    const result = await parse_api_response<{ name: string }>(response)
    if ('error' in result) {
      throw new Error(result.error.message)
    }
    if (!response.ok) {
      throw new Error("Couldn't write the destination stack files.")
    }

    notification(
      'success',
      'Copy created',
      `Stack "${stack}" on node "${targetName}". Containers were not changed.`,
    )
    emit('update:open', false)
    await nextTick()
    await navigateTo(`/stacks/${encodeURIComponent(targetNode)}/${encodeURIComponent(stack)}`)
  } catch (cause) {
    const message =
      cause instanceof Error && !(cause instanceof TypeError)
        ? cause.message
        : "Couldn't reach the server."
    notification(
      'error',
      'Create copy',
      `Could not copy stack "${props.stack}" to "${stack}" on node "${targetName}": ${message}`,
    )
  } finally {
    copying.value = false
  }
}
</script>

<template>
  <UModal
    :open="open"
    title="Create copy"
    :dismissible="!copying"
    :close="!copying"
    :ui="{ content: 'sm:max-w-xl' }"
    @update:open="(value) => emit('update:open', value)"
  >
    <template #body>
      <form id="stack-copy" class="space-y-4" @submit.prevent="create">
        <UAlert
          color="warning"
          icon="i-lucide-triangle-alert"
          title="Only Compose and .env are copied"
          description="Set up host paths, volumes, and networks on the destination before starting."
        />
        <UFormField label="Destination node" name="node" required>
          <USelect
            v-model="destination"
            :items="nodeOptions"
            icon="i-lucide-server"
            placeholder="Choose a node"
            :disabled="copying"
            required
            class="w-full"
          />
        </UFormField>
        <UFormField
          label="Stack name"
          name="name"
          help="Use 1-128 lowercase letters, numbers, hyphens or underscores. Start with a letter or number."
          required
        >
          <UInput
            v-model="name"
            icon="i-lucide-layers"
            maxlength="128"
            pattern="[a-z0-9][a-z0-9_-]*"
            :disabled="copying"
            required
            class="w-full"
          />
        </UFormField>
        <UFormField
          label="Overwrite"
          name="overwrite"
          help="Replace the destination Compose and .env files if the stack already exists."
        >
          <USwitch v-model="overwrite" :disabled="copying" />
        </UFormField>
      </form>
    </template>
    <template #footer>
      <div class="flex w-full justify-end">
        <UButton
          type="submit"
          form="stack-copy"
          size="sm"
          icon="i-lucide-copy"
          :loading="copying"
          :disabled="copying || !connected || !name.trim()"
          >Create copy</UButton
        >
      </div>
    </template>
  </UModal>
</template>
