<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import type { OutputTerminalProps } from '~/types'

const props = withDefaults(defineProps<OutputTerminalProps>(), {
  placeholder: '',
  interactive: false,
})
const emit = defineEmits<{ input: [data: string]; resize: [cols: number, rows: number] }>()
const surface = ref<HTMLElement | null>(null)
const host = ref<HTMLElement | null>(null)
const empty = ref(true)
let terminal: Terminal | null = null
let observer: ResizeObserver | null = null

const write = (chunk: string) => {
  if (!terminal || '' === chunk) {
    return
  }
  empty.value = false
  terminal.write(chunk)
}

const reset = () => {
  terminal?.reset()
  empty.value = true
}

onMounted(() => {
  if (!host.value || !surface.value) {
    return
  }
  const style = getComputedStyle(surface.value)
  const output = new Terminal({
    allowTransparency: true,
    disableStdin: !props.interactive,
    convertEol: !props.interactive,
    cursorBlink: props.interactive,
    cursorInactiveStyle: props.interactive ? 'outline' : 'none',
    screenReaderMode: true,
    scrollback: 2000,
    fontFamily: "'JetBrains Mono', monospace",
    fontSize: 12,
    theme: { background: '#00000000', foreground: style.color },
  })
  const fit = new FitAddon()
  terminal = output
  if (props.interactive) {
    output.onData((data) => emit('input', data))
    output.onResize(({ cols, rows }) => emit('resize', cols, rows))
  }
  output.loadAddon(fit)
  output.open(host.value)
  fit.fit()
  observer = new ResizeObserver(() => fit.fit())
  observer.observe(host.value)
  if (props.interactive) {
    output.focus()
  }
})

onBeforeUnmount(() => {
  observer?.disconnect()
  terminal?.dispose()
  terminal = null
})

const focus = () => terminal?.focus()

defineExpose({ write, reset, focus })
</script>

<template>
  <div ref="surface" class="relative min-w-0 overflow-hidden bg-transparent p-3 text-default">
    <div ref="host" class="h-full w-full" />
    <p
      v-if="empty && placeholder"
      class="pointer-events-none absolute inset-x-4 top-4 whitespace-pre-wrap wrap-break-word font-mono text-xs"
    >
      {{ placeholder }}
    </p>
  </div>
</template>

<style scoped>
:deep(.xterm .xterm-viewport) {
  background-color: transparent;
}
</style>
