<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UCard from '@nuxt/ui/components/Card.vue'
import UIcon from '@nuxt/ui/components/Icon.vue'
import USelect from '@nuxt/ui/components/Select.vue'
import OutputTerminal from '~/components/stacks/OutputTerminal.vue'
import { useWorkspaceStream } from '~/composables/useWorkspaceStream'
import { useWorkspaceStore } from '~/store/workspace'
import type { StackAction, StackLogsProps } from '~/types'

const props = withDefaults(defineProps<StackLogsProps>(), {
  paused: false,
  draft: false,
  showErrors: true,
  actionError: '',
})
const workspace = useWorkspaceStore()
const emit = defineEmits<{ completed: [action: StackAction | 'delete', exitCode?: number] }>()
const output = ref<InstanceType<typeof OutputTerminal> | null>(null)
const error = ref('')
const ALL_CONTAINERS = '*'
const selectedContainer = ref(ALL_CONTAINERS)
const actionRunning = ref(props.busy ?? false)
let active = false
let awaitingExit = false
let releaseStackEvents = () => {}

const containerItems = computed(() => [
  { label: 'All containers', value: ALL_CONTAINERS },
  ...props.containers.map((name) => ({ label: name, value: name })),
])

const stream = useWorkspaceStream(
  ({ data }) => {
    if (!active) {
      return
    }
    const payload: unknown = JSON.parse(data)
    if ('object' !== typeof payload || null === payload || Array.isArray(payload)) {
      throw new Error("Couldn't read logs.")
    }
    if ('message' in payload && 'string' === typeof payload.message) {
      output.value?.write(payload.message + '\r\n')
    }
    if ('event' in payload && 'error' === payload.event) {
      fail('Connection lost.')
    } else if ('exit_code' in payload && 'number' === typeof payload.exit_code) {
      if (0 !== payload.exit_code) {
        fail(`Couldn't read logs (exit code ${payload.exit_code}).`)
      } else {
        stopLogs()
      }
    }
  },
  () => {
    if (active) {
      fail(stream.error.value || 'Connection lost.')
    }
  },
)

const placeholder = computed(() => {
  if (props.paused) {
    return 'Node is paused.'
  }
  if (props.draft) {
    return 'No containers yet. Start or deploy this stack to see logs.'
  }
  return props.connected ? 'Waiting for logs...' : 'Waiting for the node to connect.'
})
const outputError = computed(() =>
  props.showErrors && props.connected && !workspace.error && !workspace.streamError
    ? error.value
    : '',
)

const appendActionError = () => {
  if (props.actionError) {
    output.value?.write(`\r\n${props.actionError}\r\n`)
  }
}

const stopLogs = () => {
  active = false
  stream.stop()
}

const fail = (message: string) => {
  stopLogs()
  error.value = props.paused ? '' : message
}

const startLogs = () => {
  if (
    active ||
    !props.connected ||
    props.paused ||
    props.draft ||
    actionRunning.value ||
    props.busy
  ) {
    return
  }
  active = true
  error.value = ''
  const container = selectedContainer.value
  stream.start({
    type: 'logs',
    node: props.node,
    resource: props.stack,
    ...(ALL_CONTAINERS !== container ? { container } : {}),
  })
}

watch(
  () => props.busy,
  (busy) => {
    if (undefined === busy) {
      return
    }
    actionRunning.value = busy || awaitingExit
    if (actionRunning.value && active) {
      stopLogs()
    } else if (!error.value) {
      startLogs()
    }
  },
)
watch(selectedContainer, () => {
  stopLogs()
  output.value?.reset()
  error.value = ''
  startLogs()
})
watch(() => props.actionError, appendActionError)
watch(
  () => props.draft,
  (draft) => {
    if (draft) {
      stopLogs()
      error.value = ''
    } else if (!error.value) {
      startLogs()
    }
  },
)
watch([() => props.connected, () => props.paused], ([connected, paused]) => {
  if (paused) {
    awaitingExit = false
    actionRunning.value = false
    stopLogs()
    error.value = ''
    return
  }
  if (connected) {
    if (!error.value) {
      startLogs()
    }
  } else {
    const interrupted = active || awaitingExit
    awaitingExit = false
    actionRunning.value = props.busy ?? false
    if (interrupted) {
      fail('Connection lost. Reconnect to continue.')
    } else {
      stopLogs()
    }
  }
})
onMounted(() => {
  releaseStackEvents = workspace.onStackEvent((event) => {
    if (props.node !== event.node || props.stack !== event.name) {
      return
    }
    if ('stack.output' === event.event) {
      output.value?.write(event.output)
      return
    }
    awaitingExit = 'start' === event.phase
    actionRunning.value = awaitingExit
    if (actionRunning.value) {
      stopLogs()
      error.value = ''
      output.value?.write(`\r\n[${event.action}]\r\n`)
      return
    }
    const code = undefined === event.exit_code ? '' : `, exit code ${event.exit_code}`
    output.value?.write(`\r\n[${event.action}: ${event.message || 'exited'}${code}]\r\n`)
    emit('completed', event.action, event.exit_code)
    startLogs()
  })
  appendActionError()
  startLogs()
})
onBeforeUnmount(() => {
  releaseStackEvents()
  stopLogs()
})
</script>

<template>
  <UCard as="section" class="min-w-0" aria-label="Stack logs" :ui="{ body: 'p-0 sm:p-0' }">
    <template #header>
      <div class="flex flex-wrap items-center justify-between gap-2">
        <h2 class="text-sm font-semibold text-highlighted">Logs</h2>
        <USelect
          v-model="selectedContainer"
          :items="containerItems"
          icon="i-lucide-container"
          size="sm"
          class="w-full sm:w-64"
          aria-label="Filter logs by container"
          :placeholder="selectedContainer"
          :disabled="actionRunning"
        />
      </div>
    </template>
    <div
      v-if="outputError"
      role="status"
      class="flex flex-wrap items-center gap-2 border-b border-default px-3 py-2 text-sm text-muted"
    >
      <UIcon name="i-lucide-unplug" class="size-4 shrink-0" />
      <span class="flex-1">{{ outputError }}</span>
      <UButton size="xs" color="neutral" variant="ghost" @click="startLogs()">Reconnect</UButton>
    </div>
    <OutputTerminal ref="output" :placeholder="placeholder" class="h-96" />
  </UCard>
</template>
