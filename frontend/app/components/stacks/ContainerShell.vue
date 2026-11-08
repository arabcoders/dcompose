<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useFullscreen, useStorage } from '@vueuse/core'
import UAlert from '@nuxt/ui/components/Alert.vue'
import UBadge from '@nuxt/ui/components/Badge.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UModal from '@nuxt/ui/components/Modal.vue'
import USelect from '@nuxt/ui/components/Select.vue'
import UTooltip from '@nuxt/ui/components/Tooltip.vue'
import OutputTerminal from '~/components/stacks/OutputTerminal.vue'
import { useRealtimeStore } from '~/store/realtime'
import type { TerminalSnapshot, RealtimeReader, ShellClientMessage, ShellSession } from '~/types'
import { parse_api_response, request } from '~/utils'
import { workspaceErrorMessage } from '~/utils/stacks'

const props = defineProps<{ node: string; stack: string; container: string }>()
const realtime = useRealtimeStore()
const emit = defineEmits<{ close: [] }>()
const open = ref(true)
const controls = ref<HTMLElement | null>(null)
const fullscreenTarget = computed(() => controls.value?.closest<HTMLElement>('[role="dialog"]'))
const { isFullscreen: fullscreen, toggle } = useFullscreen(fullscreenTarget, { autoExit: true })
const shell = useStorage<ShellSession['shell']>('shell_command', 'sh')
const shellOptions = [
  { label: 'sh', value: 'sh' },
  { label: 'bash', value: 'bash' },
  { label: 'ash', value: 'ash' },
  { label: 'dash', value: 'dash' },
]
const session = ref<ShellSession | null>(null)
const operation = ref<TerminalSnapshot | null>(null)
const creating = ref(false)
const closing = ref(false)
const error = ref('')
const output = ref<InstanceType<typeof OutputTerminal> | null>(null)
const size = ref({ cols: 120, rows: 30 })
const base = `/api/nodes/${encodeURIComponent(props.node)}`
const connected = ref(false)
const running = computed(
  () => connected.value && (!operation.value || 'running' === operation.value.status),
)
let reader: RealtimeReader | null = null
let disposed = false
let connectTimer: ReturnType<typeof setTimeout> | undefined
let closeTimer: ReturnType<typeof setTimeout> | undefined
let resizeTimer: ReturnType<typeof setTimeout> | undefined

const toggleFullscreen = async () => {
  try {
    await toggle()
  } catch {
    error.value = "Couldn't change fullscreen mode."
  }
}

const deleteShell = async (id: string) => {
  const response = await request(`${base}/shells/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    keepalive: true,
  })
  if (404 === response.status) {
    return
  }
  const json = await parse_api_response<{ status: 'ok' }>(response)
  if ('error' in json || !response.ok || 'ok' !== json.status) {
    throw new Error(workspaceErrorMessage(response.status, "Couldn't close the shell."))
  }
}

const stopSocket = () => {
  clearTimeout(connectTimer)
  clearTimeout(closeTimer)
  clearTimeout(resizeTimer)
  reader?.stop()
  reader = null
  connected.value = false
  creating.value = false
}

const fallbackClose = async (id: string) => {
  stopSocket()
  try {
    await deleteShell(id)
    if (session.value?.id === id) {
      session.value = null
    }
  } catch (cause) {
    if (!disposed) {
      error.value = cause instanceof Error ? cause.message : "Couldn't close the shell."
    }
  } finally {
    closing.value = false
  }
}

const failSocket = (message: string) => {
  error.value = message
  const id = session.value?.id
  closing.value = true
  stopSocket()
  if (id) {
    void fallbackClose(id)
  } else {
    closing.value = false
  }
}

const sendMessage = (message: ShellClientMessage): boolean => {
  if (!reader?.send(message)) {
    failSocket('The shell connection is unavailable. Connect again to open a new shell.')
    return false
  }
  return true
}

const queueInput = (data: string) => {
  if (!running.value || closing.value) {
    return
  }
  // At most 16 Ki UTF-16 code units keeps each chunk below 64 KiB of UTF-8.
  while (data) {
    let length = Math.min(data.length, 16384)
    const last = data.charCodeAt(length - 1)
    if (length < data.length && 0xd800 <= last && 0xdbff >= last) {
      length--
    }
    if (!sendMessage({ type: 'shell.input', data: data.slice(0, length) })) {
      return
    }
    data = data.slice(length)
  }
}

const resize = (cols: number, rows: number) => {
  size.value = { cols: Math.max(1, Math.min(cols, 500)), rows: Math.max(1, Math.min(rows, 500)) }
  clearTimeout(resizeTimer)
  if (!running.value || !session.value || closing.value) {
    return
  }
  resizeTimer = setTimeout(() => {
    sendMessage({ type: 'shell.resize', ...size.value })
  }, 100)
}

const attach = (id: string) => {
  const unavailable = "Couldn't connect to the shell. Try again."
  connectTimer = setTimeout(() => failSocket(unavailable), 10000)
  reader = realtime.subscribe(
    { type: 'shell', node: props.node, shell: id },
    {
      onMessage: (parsed) => {
        try {
          if (!('type' in parsed) || id !== parsed.shell) {
            throw new Error("Couldn't read shell output.")
          }
          if ('shell.error' === parsed.type) {
            throw new Error(parsed.error)
          }
          const event = parsed.event
          if ('operation.output' === event.event) {
            if (id !== event.id || !operation.value) {
              throw new Error("Couldn't read shell output.")
            }
            const text = operation.value.output + event.output
            let start = Math.max(0, text.length - (1 << 20))
            const first = text.charCodeAt(start)
            if (0xdc00 <= first && 0xdfff >= first) {
              start++
            }
            operation.value = { ...operation.value, output: text.slice(start) }
            output.value?.write(event.output)
            return
          }
          if (
            id !== event.operation.id ||
            props.stack !== event.operation.stack ||
            'shell' !== event.operation.action
          ) {
            throw new Error("Couldn't read shell output.")
          }
          operation.value = event.operation
          clearTimeout(connectTimer)
          if (!connected.value) {
            connected.value = true
            creating.value = false
            resize(size.value.cols, size.value.rows)
            output.value?.focus()
          }
          output.value?.reset()
          output.value?.write(event.operation.output)
          if ('operation.done' === event.event || 'running' !== event.operation.status) {
            if (closing.value && 'failed' === event.operation.status) {
              error.value = "The shell didn't close cleanly. See the output for details."
            }
            stopSocket()
            closing.value = false
          }
        } catch (cause) {
          failSocket(cause instanceof Error ? cause.message : "Couldn't read shell output.")
        }
      },
      onRejected: () => {},
      onDisconnect: () => {
        if (closing.value) {
          void fallbackClose(id)
        } else {
          failSocket('The shell connection was lost. Connect again to open a new shell.')
        }
      },
    },
  )
}

const connect = async () => {
  if (disposed || creating.value || closing.value || running.value) {
    return
  }
  error.value = ''
  stopSocket()
  creating.value = true
  const failure = `Couldn't open ${shell.value}. Make sure it's installed in the container.`
  try {
    if (session.value) {
      await deleteShell(session.value.id)
    }
    if (disposed) {
      return
    }
    operation.value = null
    session.value = null
    output.value?.reset()
    const response = await request(`${base}/stacks/${encodeURIComponent(props.stack)}/shells`, {
      method: 'POST',
      body: JSON.stringify({ container: props.container, shell: shell.value, ...size.value }),
    })
    const json = await parse_api_response<ShellSession>(response)
    if ('error' in json || !response.ok) {
      throw new Error(
        workspaceErrorMessage(
          response.status,
          502 === response.status || 503 === response.status || 504 === response.status
            ? "Couldn't open the shell because the node is unavailable."
            : failure,
        ),
      )
    }
    if (disposed) {
      await deleteShell(json.id)
      return
    }
    session.value = json
    attach(json.id)
  } catch (cause) {
    if (!disposed) {
      failSocket(cause instanceof Error && !(cause instanceof TypeError) ? cause.message : failure)
    }
  }
}

const disconnect = () => {
  if (!session.value || closing.value) {
    return
  }
  closing.value = true
  error.value = ''
  clearTimeout(resizeTimer)
  const id = session.value.id
  if (sendMessage({ type: 'shell.close' })) {
    closeTimer = setTimeout(() => void fallbackClose(id), 10000)
  }
}

const dispose = () => {
  if (disposed) {
    return
  }
  disposed = true
  let closeSent = false
  closeSent = reader?.send({ type: 'shell.close' }) ?? false
  stopSocket()
  if (session.value && !closeSent) {
    void deleteShell(session.value.id).catch(() => {})
  }
}

watch(output, (terminal) => {
  if (terminal) {
    terminal.reset()
    terminal.write(operation.value?.output ?? '')
  }
})
watch(open, (value) => {
  if (!value) {
    dispose()
    emit('close')
  }
})
onMounted(connect)
onBeforeUnmount(dispose)
</script>

<template>
  <UModal
    v-model:open="open"
    :fullscreen="fullscreen"
    :title="`Shell - ${container}`"
    :ui="{
      content: fullscreen ? '' : 'sm:max-w-5xl',
      header: 'shrink-0',
      wrapper: 'min-w-0 flex-1',
      title: 'truncate',
      close: 'static',
      body: 'flex min-h-0 flex-1 flex-col overflow-hidden p-0 sm:p-0',
    }"
  >
    <template #actions>
      <UTooltip :text="fullscreen ? 'Exit fullscreen' : 'Fullscreen'">
        <UButton
          color="neutral"
          variant="ghost"
          :icon="fullscreen ? 'i-lucide-minimize' : 'i-lucide-maximize'"
          :aria-label="fullscreen ? 'Exit fullscreen' : 'Fullscreen'"
          :aria-pressed="fullscreen"
          @click="toggleFullscreen"
        />
      </UTooltip>
    </template>
    <template #body>
      <div
        ref="controls"
        class="flex shrink-0 flex-wrap items-center justify-between gap-2 border-b border-default px-4 py-3"
      >
        <UBadge color="neutral" variant="subtle" icon="i-lucide-terminal">{{
          creating ? 'Connecting' : running ? 'Running' : 'Disconnected'
        }}</UBadge>
        <div class="flex items-center gap-2">
          <USelect
            v-model="shell"
            :items="shellOptions"
            icon="i-lucide-terminal"
            aria-label="Shell"
            size="sm"
            :disabled="creating || running || closing"
          />
          <UButton
            v-if="running"
            color="neutral"
            variant="outline"
            size="sm"
            icon="i-lucide-unplug"
            :loading="closing"
            @click="disconnect"
            >Disconnect</UButton
          >
          <UButton
            v-else
            color="neutral"
            variant="outline"
            size="sm"
            icon="i-lucide-plug-zap"
            :loading="creating"
            :disabled="closing"
            @click="connect"
            >Connect</UButton
          >
        </div>
      </div>
      <UAlert v-if="error" color="error" variant="soft" :title="error" class="mx-3 mt-3 shrink-0" />
      <div
        class="m-3 min-h-0 overflow-hidden rounded-md border-2 border-accented"
        :class="fullscreen ? 'flex-1' : 'h-[min(60dvh,36rem)]'"
      >
        <OutputTerminal
          ref="output"
          interactive
          class="h-full"
          @input="queueInput"
          @resize="resize"
        />
      </div>
    </template>
  </UModal>
</template>
