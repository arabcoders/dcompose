<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { navigateTo, useRoute } from '#app'
import { isNavigationFailure } from 'vue-router'
import UButton from '@nuxt/ui/components/Button.vue'
import UFormField from '@nuxt/ui/components/FormField.vue'
import UInput from '@nuxt/ui/components/Input.vue'
import UModal from '@nuxt/ui/components/Modal.vue'
import USelect from '@nuxt/ui/components/Select.vue'
import { useWorkspace } from '~/composables/useWorkspace'
import { notification } from '~/utils'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()
const route = useRoute()
const { nodes } = useWorkspace()
const creating = ref(false)
const newNode = ref('')
const newName = ref('')
const nodeOptions = computed(() =>
  nodes.value.map((node) => ({
    label: node.name,
    value: node.id,
    disabled: 'connected' !== node.connection,
  })),
)

watch(
  nodeOptions,
  (options) => {
    if (!options.some((node) => newNode.value === node.value && !node.disabled)) {
      newNode.value = options.find((node) => !node.disabled)?.value ?? ''
    }
  },
  { immediate: true },
)

watch(
  () => props.open,
  (open) => {
    if (open && !creating.value) {
      const selectedNode = nodes.value.find((node) => route.params.node === node.id)
      if ('connected' === selectedNode?.connection) {
        newNode.value = selectedNode.id
      }
      newName.value = ''
    }
  },
)

const create = async () => {
  if (creating.value) {
    return
  }

  const name = newName.value.trim()
  if (!/^[a-z0-9][a-z0-9_-]{0,127}$/.test(name) || '' === newNode.value) {
    notification(
      'error',
      'Invalid stack',
      'Choose a node. Use a stack name of 1-128 lowercase letters, numbers, hyphens or underscores, starting with a letter or number.',
    )
    return
  }
  if (
    nodes.value
      .find((node) => node.id === newNode.value)
      ?.stacks.some((stack) => name === stack.name)
  ) {
    notification('error', 'Stack already exists', 'Choose another name.')
    return
  }
  creating.value = true
  try {
    emit('update:open', false)
    await nextTick()
    const result = await navigateTo({
      path: `/stacks/${encodeURIComponent(newNode.value)}/${encodeURIComponent(name)}`,
      query: { new: '1' },
    })
    if (false === result || isNavigationFailure(result)) {
      emit('update:open', true)
      await nextTick()
    }
  } finally {
    creating.value = false
  }
}
</script>

<template>
  <UModal
    :open="open"
    title="Create compose stack"
    :dismissible="!creating"
    :close="!creating"
    :ui="{ content: 'sm:max-w-xl' }"
    @update:open="(value) => emit('update:open', value)"
  >
    <template #body>
      <form id="stack-create" class="space-y-4" @submit.prevent="create">
        <UFormField
          label="Node"
          name="node"
          help="Select the node where the stack will be created."
          required
        >
          <USelect
            v-model="newNode"
            :items="nodeOptions"
            icon="i-lucide-server"
            :disabled="creating"
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
            v-model="newName"
            icon="i-lucide-layers"
            required
            maxlength="128"
            pattern="[a-z0-9][a-z0-9_-]*"
            placeholder="my-stack"
            :disabled="creating"
            class="w-full"
          />
        </UFormField>
      </form>
    </template>
    <template #footer>
      <div class="flex w-full flex-wrap items-center justify-end gap-2">
        <UButton
          type="submit"
          form="stack-create"
          size="sm"
          icon="i-lucide-arrow-right"
          :disabled="creating || !newNode || !newName.trim()"
          :loading="creating"
          >Continue</UButton
        >
      </div>
    </template>
  </UModal>
</template>
