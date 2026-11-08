<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import type { CodeJar } from 'codejar'
import type { EditorProps } from '~/types'
import { loadHighlighter } from '~/utils/highlight'

const emit = defineEmits<{ updated: [content: string] }>()
const props = withDefaults(defineProps<EditorProps>(), {
  mode: 'yaml',
  height: '70vh',
  readOnly: false,
})

const editorElement = ref<HTMLElement | null>(null)
const editor = shallowRef<CodeJar | null>(null)
let editableMode = 'plaintext-only'

onMounted(async () => {
  const [{ CodeJar }, { withLineNumbers }, highlighter] = await Promise.all([
    import('codejar'),
    import('codejar-linenumbers'),
    loadHighlighter(),
    import('codejar-linenumbers/es/codejar-linenumbers.css'),
  ])
  const element = editorElement.value
  if (!element) {
    return
  }

  const instance = CodeJar(
    element,
    withLineNumbers(
      (element) => {
        element.innerHTML = highlighter.highlight(element.textContent ?? '', {
          language: props.mode,
        }).value
      },
      {
        class: 'codejar-linenumbers select-none',
        wrapClass: 'codejar-wrap h-full',
        backgroundColor: 'transparent',
        color: 'var(--ui-text-muted)',
      },
    ),
    { tab: '  ', addClosing: false },
  )
  editor.value = instance
  editableMode = element.contentEditable
  element.contentEditable = props.readOnly ? 'false' : editableMode
  instance.updateCode(props.content, false)
  instance.onUpdate((content) => {
    if (!props.readOnly && content !== props.content) {
      emit('updated', content)
    }
  })
  if (!props.readOnly) {
    element.focus()
  }
})

watch(
  () => props.content,
  (content) => {
    if (editor.value && content !== editor.value.toString()) {
      editor.value.updateCode(content, false)
    }
  },
)
watch(
  () => props.readOnly,
  (readOnly) => {
    if (editorElement.value && editor.value) {
      editorElement.value.contentEditable = readOnly ? 'false' : editableMode
    }
  },
)
watch(
  () => props.mode,
  () => {
    if (editor.value) {
      editor.value.updateCode(editor.value.toString(), false)
    }
  },
)
onBeforeUnmount(() => {
  editor.value?.destroy()
  editor.value = null
})
</script>

<template>
  <div :style="{ height }">
    <div
      ref="editorElement"
      role="textbox"
      aria-label="Code editor"
      aria-multiline="true"
      :aria-readonly="readOnly"
      contenteditable="false"
      class="editor-host h-full w-full overflow-auto p-3 font-mono text-sm leading-relaxed text-default outline-none"
    />
  </div>
</template>
