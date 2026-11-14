import { afterEach, expect, jest, mock, test } from 'bun:test'
import { compileScript, parse, registerTS } from '@vue/compiler-sfc'
import * as ts from 'typescript'
import * as vue from 'vue'
import type { Component } from 'vue'
import type { LocationQuery } from 'vue-router'
import type { LogEntry, LogFilters, LogLevel } from '../app/types'
import * as logs from '../app/utils/logs'

registerTS(() => ts)

interface TestNode {
  type: string
  text: string
  children: Array<TestNode>
  parent: TestNode | null
  props: Record<string, unknown>
  dataset: { logId?: string }
  scrollTop: number
  scrollHeight: number
  clientHeight: number
  getBoundingClientRect: () => { top: number; bottom: number }
  querySelectorAll: () => Array<TestNode>
  querySelector: (selector: string) => TestNode | null
  scrollTo: (options: { top: number }) => void
}
const flatten = (element: TestNode): Array<TestNode> => [
  element,
  ...element.children.flatMap(flatten),
]
const text = (element: TestNode): string => element.text + element.children.map(text).join('')
const scrolls: Array<number> = []
const node = (type: string): TestNode => {
  const element: TestNode = {
    type,
    text: '',
    children: [],
    parent: null,
    props: {},
    dataset: {},
    scrollTop: 0,
    scrollHeight: 1000,
    clientHeight: 600,
    getBoundingClientRect: () => ({ top: 0, bottom: 10 }),
    querySelectorAll: () => flatten(element).filter((child) => child.dataset.logId),
    querySelector: (selector) =>
      flatten(element).find((child) => `[data-log-id="${child.dataset.logId}"]` === selector) ??
      null,
    scrollTo: ({ top }) => {
      element.scrollTop = top
      scrolls.push(top)
    },
  }
  return element
}
const renderer = vue.createRenderer<TestNode, TestNode>({
  createElement: node,
  createText: (value) => ({ ...node('text'), text: value }),
  createComment: () => node('comment'),
  setText: (element, value) => {
    element.text = value
  },
  setElementText: (element, value) => {
    element.text = value
  },
  patchProp: (element, key, _previous, value) => {
    element.props[key] = value
    if ('data-log-id' === key) {
      element.dataset.logId = String(value)
    }
  },
  parentNode: (element) => element.parent,
  nextSibling: (element) => {
    const siblings = element.parent?.children ?? []
    return siblings[siblings.indexOf(element) + 1] ?? null
  },
  insert: (element, parent, anchor) => {
    if (element.parent) {
      const previous = element.parent.children
      previous.splice(previous.indexOf(element), 1)
    }
    element.parent = parent
    const index = anchor ? parent.children.indexOf(anchor) : -1
    if (0 > index) {
      parent.children.push(element)
    } else {
      parent.children.splice(index, 0, element)
    }
  },
  remove: (element) => {
    const siblings = element.parent?.children
    siblings?.splice(siblings.indexOf(element), 1)
    element.parent = null
  },
})
const stub = (tag: string) =>
  vue.defineComponent({
    inheritAttrs: false,
    setup:
      (_, { attrs, slots }) =>
      () =>
        'modal' === tag && !attrs.open
          ? null
          : vue.h(tag, attrs, [
              slots.default?.(),
              slots.body?.(),
              slots.actions?.(),
              slots.description?.(),
            ]),
  })
const loadComponent = async (path: string, dependencies: Record<string, unknown>) => {
  const filename = `${import.meta.dir}/../app/${path}.vue`
  const { descriptor } = parse(await Bun.file(filename).text(), { filename })
  const script = compileScript(descriptor, { id: path, inlineTemplate: true })
  const { outputText } = ts.transpileModule(script.content, {
    compilerOptions: {
      target: ts.ScriptTarget.ESNext,
      module: ts.ModuleKind.CommonJS,
      esModuleInterop: false,
    },
  })
  const imports: Record<string, unknown> = { vue, ...dependencies }
  const exports = {} as { default: Component }
  const run = new Function('require', 'exports', outputText) as (
    require: (name: string) => unknown,
    exports: { default: Component },
  ) => void
  run((name) => imports[name], exports)
  return exports.default
}
const apps: Array<ReturnType<typeof renderer.createApp>> = []
afterEach(() => {
  for (const app of apps.splice(0)) {
    app.unmount()
  }
  jest.useRealTimers()
  scrolls.length = 0
})

const entry: LogEntry = {
  id: 10,
  timestamp: '2026-10-30T12:00:00.123456789Z',
  level: 'error',
  logger: 'node.kitchen',
  message: 'message\n<script>not html</script>',
  rendered:
    '2026-10-30T12:00:00.123456789Z ERROR message="message\\n<script>not html</script>" error="native failure" stderr="full stderr\\nsecond line" node="Kitchen"\n',
  fields: {
    node_id: 'kitchen',
    node_name: 'Kitchen',
    stderr: 'full stderr\nsecond line',
    error: { cause: ['nested', { code: 23 }] },
    image: 'redis:7',
    flag: false,
    count: 0,
    nil: null,
  },
  source: { file: 'compose.go', function: 'Deploy', line: 17 },
}
const mountLogs = async (
  options: { query?: LocationQuery; storedLevels?: Array<LogLevel> } = {},
) => {
  const storage = new Map<string, vue.Ref<boolean | Array<LogLevel>>>()
  const route = vue.reactive({ query: options.query ?? {} })
  const state = {
    entries: vue.ref<Array<LogEntry>>([entry]),
    observedNodes: vue.ref(new Map([['kitchen', 'Kitchen']])),
    loading: vue.ref(false),
    loadingOlder: vue.ref(false),
    error: vue.ref(''),
    status: vue.ref('live'),
    truncated: vue.ref(false),
    hasOlder: vue.ref(true),
    refresh: mock(async () => {}),
    search: mock(async (_filters: LogFilters) => {}),
    loadOlder: mock(async () => {}),
  }
  const copied = mock(async (_text: string) => {})
  const dependencies: Record<string, unknown> = {
    '#app': { useHead: () => {}, useRoute: () => route },
    '@vueuse/core': {
      useStorage: (key: string, value: boolean | Array<LogLevel>) => {
        const stored = vue.ref('logs_levels' === key ? (options.storedLevels ?? value) : value)
        storage.set(key, stored)
        return stored
      },
    },
    '~/utils': { copyText: copied, notification: mock(() => {}) },
    '~/utils/logs': logs,
    '~/composables/useLogs': { useLogs: () => state },
    '~/composables/useWorkspace': {
      useWorkspace: () => ({ nodes: vue.ref([{ id: 'garage', name: 'Garage' }]) }),
    },
  }
  for (const [component, tag] of Object.entries({
    Alert: 'alert',
    Button: 'control',
    DropdownMenu: 'menu',
    Icon: 'icon',
    Input: 'input',
    Select: 'select',
    SelectMenu: 'select-menu',
    Modal: 'modal',
    Tooltip: 'tooltip',
  })) {
    dependencies[`@nuxt/ui/components/${component}.vue`] = { default: stub(tag) }
  }
  const statCard = await loadComponent('components/StatCard', dependencies)
  dependencies['~/components/StatCard.vue'] = { default: statCard }
  const details = await loadComponent('components/LogDetailModal', dependencies)
  const pageHeader = await loadComponent('components/PageHeader', dependencies)
  const page = await loadComponent('pages/logs', {
    ...dependencies,
    '~/components/LogDetailModal.vue': { default: details },
    '~/components/PageHeader.vue': { default: pageHeader },
  })
  const root = node('root')
  const app = renderer.createApp(page)
  app.mount(root)
  apps.push(app)
  await vue.nextTick()
  return { root, state, copied, storage, app, route }
}
const find = (root: TestNode, predicate: (element: TestNode) => boolean): TestNode => {
  const result = flatten(root).find(predicate)
  if (!result) {
    throw new Error('Missing UI element')
  }
  return result
}
const trigger = (element: TestNode, event: string, value?: unknown) => {
  const handler = element.props[event]
  if ('function' !== typeof handler) {
    throw new Error(`Missing ${event} handler`)
  }
  return handler(
    undefined === value ? { stopPropagation: () => {}, preventDefault: () => {} } : value,
  )
}
const control = (root: TestNode, icon: string) =>
  find(root, (element) => 'control' === element.type && icon === element.props.icon)
const menuItems = (menu: TestNode): Array<{ icon: string; onSelect: () => void; disabled?: boolean }> => {
  const items: unknown = menu.props.items
  if (!Array.isArray(items)) {
    throw new Error('Missing menu items')
  }
  return items
    .flat()
    .filter(
      (item: unknown): item is { icon: string; onSelect: () => void } =>
        null !== item &&
        'object' === typeof item &&
        'icon' in item &&
        'string' === typeof item.icon &&
        'onSelect' in item &&
        'function' === typeof item.onSelect,
    )
}

test('filters and controls', async () => {
  jest.useFakeTimers()
  const { root, state, storage } = await mountLogs()
  expect(state.search).toHaveBeenCalledTimes(1)
  expect(state.search).toHaveBeenLastCalledWith({
    levels: [...logs.logLevels],
    node: '',
    search: '',
  })
  const nodeFilter = find(root, (element) => 'select' === element.type)
  const options: unknown = nodeFilter.props.items
  expect(nodeFilter.props.modelValue).toBe('all')
  if (!Array.isArray(options)) {
    throw new Error('Missing node options')
  }
  expect(options.map((option: { value: string }) => option.value)).toEqual([
    'all',
    'server',
    'kitchen',
    'garage',
  ])
  trigger(
    find(root, (element) => 'input' === element.type),
    'onUpdate:modelValue',
    'nested',
  )
  trigger(nodeFilter, 'onUpdate:modelValue', 'kitchen')
  trigger(
    find(root, (element) => 'select-menu' === element.type),
    'onUpdate:modelValue',
    ['error'],
  )
  await vue.nextTick()
  jest.advanceTimersByTime(299)
  expect(state.search).toHaveBeenCalledTimes(1)
  jest.advanceTimersByTime(1)
  expect(state.search).toHaveBeenLastCalledWith({
    levels: ['error'],
    node: 'kitchen',
    search: 'nested',
  })
  trigger(nodeFilter, 'onUpdate:modelValue', 'server')
  await vue.nextTick()
  jest.advanceTimersByTime(300)
  expect(state.search).toHaveBeenLastCalledWith({
    levels: ['error'],
    node: 'server',
    search: 'nested',
  })
  trigger(nodeFilter, 'onUpdate:modelValue', 'all')
  await vue.nextTick()
  jest.advanceTimersByTime(300)
  expect(state.search).toHaveBeenLastCalledWith({
    levels: ['error'],
    node: '',
    search: 'nested',
  })
  expect(Object.fromEntries(logs.logQuery(state.search.mock.calls.at(-1)![0]))).toEqual({
    levels: 'error',
    search: 'nested',
  })
  trigger(control(root, 'i-lucide-align-justify'), 'onClick')
  expect(storage.get('logs_text_wrap')?.value).toBe(true)
  trigger(control(root, 'i-lucide-refresh-cw'), 'onClick')
  expect(state.refresh).toHaveBeenCalledTimes(1)
  state.error.value = 'failure-sentinel'
  await vue.nextTick()
  const alert = find(root, (element) => 'alert' === element.type)
  expect(alert.props.description).toBe(state.error.value)
  trigger(
    find(alert, (element) => 'control' === element.type),
    'onClick',
  )
  expect(state.refresh).toHaveBeenCalledTimes(2)
  const container = find(root, (element) => 'function' === typeof element.props.onScrollPassive)
  await trigger(
    find(container, (element) => 'control' === element.type),
    'onClick',
  )
  expect(state.loadOlder).toHaveBeenCalledTimes(1)
})

test('refresh pending filters', async () => {
  jest.useFakeTimers()
  const { root, state } = await mountLogs()
  trigger(
    find(root, (element) => 'select' === element.type),
    'onUpdate:modelValue',
    'garage',
  )
  trigger(
    find(root, (element) => 'input' === element.type),
    'onUpdate:modelValue',
    'retry',
  )
  trigger(
    find(root, (element) => 'select-menu' === element.type),
    'onUpdate:modelValue',
    ['warning'],
  )
  await vue.nextTick()
  trigger(control(root, 'i-lucide-refresh-cw'), 'onClick')
  expect(state.search).toHaveBeenLastCalledWith({
    levels: ['warning'],
    node: 'garage',
    search: 'retry',
  })
  jest.advanceTimersByTime(300)
  expect(state.search).toHaveBeenCalledTimes(2)
  expect(state.refresh).not.toHaveBeenCalled()
})

test('query filters', async () => {
  jest.useFakeTimers()
  const { root, state, storage } = await mountLogs({
    query: { levels: 'warning,unknown,error,error', node: 'kitchen', search: 'failure' },
    storedLevels: ['info'],
  })
  expect(state.search).toHaveBeenCalledTimes(1)
  expect(state.search).toHaveBeenLastCalledWith({
    levels: ['warning', 'error'],
    node: 'kitchen',
    search: 'failure',
  })
  expect(find(root, (element) => 'select-menu' === element.type).props.modelValue).toEqual([
    'warning',
    'error',
  ])
  expect(find(root, (element) => 'select' === element.type).props.modelValue).toBe('kitchen')
  expect(find(root, (element) => 'input' === element.type).props.modelValue).toBe('failure')
  expect(storage.get('logs_levels')?.value).toEqual(['info'])
  trigger(
    find(root, (element) => 'select-menu' === element.type),
    'onUpdate:modelValue',
    ['error'],
  )
  await vue.nextTick()
  jest.advanceTimersByTime(300)
  expect(state.search).toHaveBeenLastCalledWith({
    levels: ['error'],
    node: 'kitchen',
    search: 'failure',
  })
  expect(storage.get('logs_levels')?.value).toEqual(['info'])
})

test('query defaults', async () => {
  const { state } = await mountLogs({ query: { node: 'server' }, storedLevels: ['debug', 'info'] })
  expect(state.search).toHaveBeenLastCalledWith({
    levels: ['debug', 'info'],
    node: 'server',
    search: '',
  })
  const { state: unsupported } = await mountLogs({
    query: { levels: ['warning', 'error'], node: null, search: ['first', 'second'] },
    storedLevels: ['info'],
  })
  expect(unsupported.search).toHaveBeenLastCalledWith({ levels: ['info'], node: '', search: '' })
})

test('query changes', async () => {
  jest.useFakeTimers()
  const { route, state, storage } = await mountLogs({ storedLevels: ['info'] })
  route.query = { levels: 'warning,error', node: 'garage' }
  await vue.nextTick()
  jest.advanceTimersByTime(300)
  expect(state.search).toHaveBeenLastCalledWith({
    levels: ['warning', 'error'],
    node: 'garage',
    search: '',
  })
  route.query = { levels: 'error', search: 'retry' }
  await vue.nextTick()
  jest.advanceTimersByTime(300)
  expect(state.search).toHaveBeenLastCalledWith({ levels: ['error'], node: '', search: 'retry' })
  route.query = {}
  await vue.nextTick()
  jest.advanceTimersByTime(300)
  expect(state.search).toHaveBeenLastCalledWith({ levels: ['info'], node: '', search: '' })
  expect(storage.get('logs_levels')?.value).toEqual(['info'])
  expect(state.search).toHaveBeenCalledTimes(4)
})

test('detail data and copy', async () => {
  const { root, copied } = await mountLogs()
  trigger(
    find(root, (element) => 'button' === element.type && entry.level === text(element).trim()),
    'onClick',
  )
  await vue.nextTick()
  const modal = find(root, (element) => 'modal' === element.type)
  for (const field of ['stderr', 'error']) {
    trigger(
      find(
        modal,
        (element) =>
          'button' === element.type &&
          element.children.some((child) => 'span' === child.type && field === text(child).trim()),
      ),
      'onClick',
    )
  }
  await vue.nextTick()
  for (const value of [
    entry.timestamp,
    'compose.go',
    'Deploy',
    '17',
    'full stderr\nsecond line',
    'redis:7',
    'false',
    'null',
    'nested',
    '<script>not html</script>',
  ]) {
    expect(text(modal)).toContain(value)
  }
  expect(flatten(modal).filter((element) => 'script' === element.type)).toHaveLength(0)
  const items = menuItems(find(modal, (element) => 'menu' === element.type))
  items.find((item) => 'i-lucide-file-text' === item.icon)!.onSelect()
  expect(copied).toHaveBeenLastCalledWith(entry.rendered)
  items.find((item) => 'i-lucide-braces' === item.icon)!.onSelect()
  expect(copied).toHaveBeenLastCalledWith(JSON.stringify(entry, null, 2))
})

test('source message', async () => {
  const { root, state, copied } = await mountLogs()
  const value: LogEntry = {
    ...entry,
    logger: 'server',
    message: 'Could not connect to Gaia: rejected-sentinel.\nTrying again in 1s.',
    rendered:
      '2026-10-30T12:00:00Z ERROR Could not connect to Gaia: rejected-sentinel. error="field-only-error-sentinel" node_id="long-machine-id-sentinel"\n',
    fields: {
      node: 'node "Gaia" at https://gaia.test',
      node_id: 'long-machine-id-sentinel',
      error: 'field-only-error-sentinel',
      stderr: 'field-only-stderr-first\nfield-only-stderr-last',
      image: 'image-sentinel',
      stack: 'stack-sentinel',
      service: 'service-sentinel',
      retry_in: 1_000_000_000,
    },
  }
  state.entries.value = [value]
  await vue.nextTick()
  const row = find(root, (element) => 'article' === element.type)
  expect(text(row)).toContain(value.message)
  for (const field of Object.values(value.fields)) {
    expect(text(row)).not.toContain(String(field))
  }
  expect(flatten(root).filter((element) => 'modal' === element.type)).toHaveLength(0)
  const message = find(
    row,
    (element) => 'button' === element.type && 'aria-expanded' in element.props,
  )
  expect(message.props['aria-expanded']).toBe(false)
  trigger(message, 'onClick')
  await vue.nextTick()
  expect(message.props['aria-expanded']).toBe(true)
  trigger(message, 'onClick')
  await vue.nextTick()
  expect(message.props['aria-expanded']).toBe(false)
  const items = menuItems(find(row, (element) => 'menu' === element.type))
  items.find((item) => 'i-lucide-file-text' === item.icon)!.onSelect()
  expect(copied).toHaveBeenLastCalledWith(value.rendered)
  items.find((item) => 'i-lucide-braces' === item.icon)!.onSelect()
  expect(copied).toHaveBeenLastCalledWith(JSON.stringify(value, null, 2))
  items.find((item) => 'i-lucide-panel-right-open' === item.icon)!.onSelect()
  await vue.nextTick()
  expect(text(find(root, (element) => 'modal' === element.type))).toContain(value.message)
})

test('field and raw filters', async () => {
  const { root, copied } = await mountLogs()
  trigger(
    find(root, (element) => 'button' === element.type && entry.level === text(element).trim()),
    'onClick',
  )
  await vue.nextTick()
  const modal = find(root, (element) => 'modal' === element.type)
  trigger(
    find(modal, (element) => 'input' === element.type),
    'onUpdate:modelValue',
    'stderr',
  )
  await vue.nextTick()
  expect(
    flatten(modal).filter(
      (element) =>
        'button' === element.type &&
        element.children.some((child) => 'span' === child.type && 'image' === text(child).trim()),
    ),
  ).toHaveLength(0)
  const stderr = find(
    modal,
    (element) =>
      'button' === element.type &&
      element.children.some((child) => 'span' === child.type && 'stderr' === text(child).trim()),
  )
  trigger(stderr, 'onClick')
  await vue.nextTick()
  trigger(
    find(stderr.parent!, (element) => 'input' === element.type),
    'onUpdate:modelValue',
    'second',
  )
  await vue.nextTick()
  const field = find(modal, (element) => 'pre' === element.type)
  expect(text(field)).toBe('second line')
  const fieldContent = field.parent!
  trigger(
    find(fieldContent, (element) => 'input' === element.type),
    'onUpdate:modelValue',
    'no-match-sentinel',
  )
  await vue.nextTick()
  expect(text(fieldContent)).not.toContain('second line')
  const raw = find(
    modal,
    (element) =>
      'section' === element.type &&
      flatten(element).some((child) => 'i-lucide-braces' === child.props.name),
  )
  trigger(
    find(raw, (element) => 'button' === element.type),
    'onClick',
  )
  await vue.nextTick()
  const rawFilter = find(raw, (element) => 'input' === element.type)
  trigger(rawFilter, 'onUpdate:modelValue', '"image"')
  await vue.nextTick()
  const pre = find(raw, (element) => 'pre' === element.type)
  expect(text(pre)).toContain('redis:7')
  expect(text(pre)).not.toContain('node_name')
  const items = menuItems(find(modal, (element) => 'menu' === element.type))
  items.find((item) => 'i-lucide-braces' === item.icon)!.onSelect()
  expect(copied).toHaveBeenLastCalledWith(JSON.stringify(entry, null, 2))
})

test('visible copy contract', async () => {
  const { root, state, copied } = await mountLogs()
  state.entries.value = [
    { ...entry, rendered: 'first\n' },
    { ...entry, id: 11, rendered: 'second\n' },
  ]
  await vue.nextTick()
  const menu = find(root, (element) => 'menu' === element.type)
  const items = menuItems(menu)
  items.find((item) => 'i-lucide-file-text' === item.icon)!.onSelect()
  expect(copied).toHaveBeenLastCalledWith('first\nsecond\n')
  items.find((item) => 'i-lucide-braces' === item.icon)!.onSelect()
  expect(copied).toHaveBeenLastCalledWith(JSON.stringify(state.entries.value, null, 2))
})

test('follow only bottom', async () => {
  const { root, state } = await mountLogs()
  const container = find(root, (element) => 'function' === typeof element.props.onScrollPassive)
  container.scrollTop = 100
  trigger(container, 'onScrollPassive')
  state.entries.value = [...state.entries.value, { ...entry, id: 11 }]
  await vue.nextTick()
  await vue.nextTick()
  expect(scrolls).not.toContain(1000)
  trigger(control(root, 'i-lucide-arrow-down'), 'onClick')
  expect(scrolls.at(-1)).toBe(1000)
  container.scrollHeight = 1200
  state.entries.value = [...state.entries.value, { ...entry, id: 12 }]
  await vue.nextTick()
  await vue.nextTick()
  expect(scrolls.at(-1)).toBe(1200)
})

test('empty and loading', async () => {
  const { root, state } = await mountLogs()
  state.entries.value = []
  state.loading.value = true
  await vue.nextTick()
  expect(flatten(root).some((element) => 'status' === element.props.role)).toBe(true)
  state.loading.value = false
  await vue.nextTick()
  expect(flatten(root).filter((element) => 'article' === element.type)).toHaveLength(0)
  expect(menuItems(find(root, (element) => 'menu' === element.type))
    .find((item) => 'i-lucide-download' === item.icon)?.disabled).toBe(true)
})

test('download visible', async () => {
  const { root } = await mountLogs()
  const originalDocument = globalThis.document
  const createObjectURL = URL.createObjectURL
  const revokeObjectURL = URL.revokeObjectURL
  const blobs: Array<Blob> = []
  const link = { href: '', download: '', click: mock(() => {}) }
  const revoked = mock((_url: string) => {})
  Object.defineProperty(globalThis, 'document', {
    configurable: true,
    value: { createElement: () => link },
  })
  Object.defineProperty(URL, 'createObjectURL', {
    configurable: true,
    value: (blob: Blob) => {
      blobs.push(blob)
      return 'blob:logs'
    },
  })
  Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: revoked })
  try {
    const download = menuItems(find(root, (element) => 'menu' === element.type))
      .find((item) => 'i-lucide-download' === item.icon)
    expect(download?.disabled).toBe(false)
    download?.onSelect()
    expect(link.click).toHaveBeenCalledTimes(1)
    expect(link.download).toBe('dcompose-logs.jsonl')
    expect(link.href).toBe('blob:logs')
    expect(await blobs[0]?.text()).toBe(`${JSON.stringify(entry)}\n`)
    expect(revoked).toHaveBeenCalledWith('blob:logs')
  } finally {
    Object.defineProperty(globalThis, 'document', { configurable: true, value: originalDocument })
    Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: createObjectURL })
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: revokeObjectURL })
  }
})

test('debounce cleanup', async () => {
  jest.useFakeTimers()
  const { root, state } = await mountLogs()
  trigger(
    find(root, (element) => 'input' === element.type),
    'onUpdate:modelValue',
    'pending',
  )
  await vue.nextTick()
  apps.pop()!.unmount()
  jest.advanceTimersByTime(1000)
  expect(state.search).toHaveBeenCalledTimes(1)
})
