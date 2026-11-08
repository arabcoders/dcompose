import { afterEach, describe, expect, mock, test } from 'bun:test'
import { compileScript, parse, registerTS } from '@vue/compiler-sfc'
import * as ts from 'typescript'
import * as vue from 'vue'
import type { Component } from 'vue'
import type { DropdownMenuItem } from '@nuxt/ui'
import type {
  EditorProps,
  GenericError,
  StackEvent,
  RegistriesResponse,
  RegistryCredential,
  SettingsNode,
  StackLogsProps,
  WorkspaceNode,
  WorkspaceStreamMessage,
} from '../app/types'

registerTS(() => ts)

interface TestNode {
  type: string
  text: string
  children: Array<TestNode>
  parent: TestNode | null
  props: Record<string, unknown>
  style: { display: string }
}

const node = (type: string): TestNode => ({
  type,
  text: '',
  children: [],
  parent: null,
  props: {},
  style: { display: '' },
})
const renderer = vue.createRenderer<TestNode, TestNode>({
  createElement: node,
  createText: (text) => ({ ...node('text'), text }),
  createComment: () => node('comment'),
  setText: (element, text) => {
    element.text = text
  },
  setElementText: (element, text) => {
    element.text = text
  },
  patchProp: (element, key, _previous, value) => {
    element.props[key] = value
  },
  parentNode: (element) => element.parent,
  nextSibling: (element) => {
    const siblings = element.parent?.children ?? []
    return siblings[siblings.indexOf(element) + 1] ?? null
  },
  insert: (element, parent, anchor) => {
    element.parent = parent
    const index = anchor ? parent.children.indexOf(anchor) : -1
    if (-1 === index) {
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

const flatten = (element: TestNode): Array<TestNode> => [
  element,
  ...element.children.flatMap(flatten),
]
const alerts = (root: TestNode) => flatten(root).filter((element) => 'alert' === element.type)

test('node header', async () => {
  const item = vue.reactive<WorkspaceNode>({
    id: 'node',
    name: 'Node',
    public_host: '',
    stacks: [],
    health: null,
    connection: 'connected',
    error: '',
    loading: false,
  })
  const component = await loadComponent('StackList', {
    '#app': { useRoute: () => ({ params: {} }) },
    '#components': { NuxtLink: stub('a') },
    '@nuxt/ui/components/Alert.vue': { default: stub('alert') },
    '@nuxt/ui/components/Badge.vue': { default: stub('badge') },
    '@nuxt/ui/components/Icon.vue': { default: stub('icon') },
    '~/utils/stacks': await import('../app/utils/stacks'),
  })
  const collapsed = vue.reactive<Record<string, boolean>>({ node: false })
  const selected = mock(() => {})
  const root = node('root')
  const app = renderer.createApp({
    setup: () => () =>
      vue.h(component, {
        nodes: [item],
        workspaceLoading: false,
        search: '',
        status: 'all',
        collapsed,
        onSelected: selected,
      }),
  })
  app.mount(root)
  apps.push(app)
  const states: Array<WorkspaceNode['connection']> = [
    'connected',
    'loading',
    'reconnecting',
    'unavailable',
    'paused',
    'connected',
  ]
  for (const connection of states) {
    item.connection = connection
    await vue.nextTick()
    expect(alerts(root)).toHaveLength('connected' === connection ? 1 : 0)
  }
  const header = flatten(root).find((element) => 'button' === element.type)
  expect(header?.props['aria-expanded']).toBe(true)
  for (const value of [true, false]) {
    if ('function' !== typeof header?.props.onClick) {
      throw new Error('Missing node toggle')
    }
    header.props.onClick()
    await vue.nextTick()
    expect(collapsed.node).toBe(value)
    expect(header.props['aria-expanded']).toBe(!value)
    expect(selected).not.toHaveBeenCalled()
  }
})

const stub = (tag: string) =>
  vue.defineComponent({
    setup:
      (_, { slots }) =>
      () =>
        vue.h(tag, slots.default?.()),
  })

const loadComponent = async (file: string, dependencies: Record<string, unknown>) => {
  const filename = `${import.meta.dir}/../app/components/stacks/${file}.vue`
  const { descriptor } = parse(await Bun.file(filename).text(), { filename })
  const script = compileScript(descriptor, { id: file, inlineTemplate: true })
  const { outputText } = ts.transpileModule(script.content, {
    compilerOptions: {
      target: ts.ScriptTarget.ESNext,
      module: ts.ModuleKind.CommonJS,
      esModuleInterop: false,
    },
  })
  const imports: Record<string, unknown> = {
    vue,
    '@nuxt/ui/components/Tooltip.vue': { default: stub('tooltip') },
    ...dependencies,
  }
  const exports = {} as { default: Component }
  const run = new Function('require', 'exports', outputText) as (
    require: (name: string) => unknown,
    exports: { default: Component },
  ) => void
  run((name) => imports[name], exports)
  return exports.default
}

const apps: Array<ReturnType<typeof renderer.createApp>> = []
const originalWindow = globalThis.window
const originalInput = globalThis.HTMLInputElement
afterEach(() => {
  for (const app of apps.splice(0)) {
    app.unmount()
  }
  Object.defineProperty(globalThis, 'window', { value: originalWindow, configurable: true })
  Object.defineProperty(globalThis, 'HTMLInputElement', {
    value: originalInput,
    configurable: true,
  })
})

test('overview attention', async () => {
  let mobile = false
  let confirmation = false
  const confirm = mock(async () => ({ status: confirmation }))
  Object.defineProperty(globalThis, 'window', {
    value: { matchMedia: () => ({ matches: mobile }) },
    configurable: true,
  })
  const item = vue.reactive<WorkspaceNode>({
    id: 'office',
    name: 'Office',
    public_host: '',
    health: null,
    connection: 'connected',
    error: '',
    loading: false,
    stacks: [
      {
        name: 'app',
        status: 'running',
        loading: false,
        services: [
          {
            name: 'app-web-1',
            service: 'web',
            image: 'nginx:alpine',
            state: 'running',
            health: 'unhealthy',
            ports: '',
          },
        ],
      },
    ],
  })
  const workspace = vue.reactive({
    nodes: [item],
    ready: true,
    loading: false,
    error: '',
    streamError: '',
    stackFilter: 'all',
    stackSearch: '',
    sidebarOpen: false,
    isCheckingImages: () => false,
    savingNodeOrder: false,
    load: () => {},
    checkImages: mock(async () => {}),
  })
  const store = { useWorkspaceStore: () => workspace }
  const component = await loadComponent('../../pages/index', {
    pinia: { storeToRefs: vue.toRefs },
    '#app': { useHead: () => {} },
    '#imports': { definePageMeta: () => {} },
    '#components': { NuxtLink: stub('a') },
    '@nuxt/ui/components/Alert.vue': { default: stub('alert') },
    '@nuxt/ui/components/Badge.vue': { default: stub('badge') },
    '@nuxt/ui/components/Button.vue': { default: stub('button') },
    '@nuxt/ui/components/Icon.vue': { default: stub('icon') },
    '~/components/StatCard.vue': { default: stub('stat') },
    '~/utils/stacks': await import('../app/utils/stacks'),
    '~/store/workspace': store,
    '~/composables/useDialog': { useDialog: () => ({ confirmDialog: confirm }) },
  })
  const wrapper = await loadComponent('../../layouts/workspace', {
    pinia: { storeToRefs: vue.toRefs },
    '#components': { NuxtLayout: stub('layout') },
    '@nuxt/ui/components/Alert.vue': { default: stub('alert') },
    '@nuxt/ui/components/Button.vue': { default: stub('button') },
    '~/store/workspace': store,
  })
  const root = node('root')
  const app = renderer.createApp({
    setup: () => () => vue.h(wrapper, null, { default: () => vue.h(component) }),
  })
  app.mount(root)
  apps.push(app)
  const attentionRows = () => {
    const section = flatten(root).find(
      (element) => 'attention-heading' === element.props['aria-labelledby'],
    )
    return section ? flatten(section).filter((element) => 'li' === element.type) : []
  }
  const attention = () =>
    flatten(root).find((element) => 'attention-heading' === element.props['aria-labelledby'])
  const unavailable = () =>
    flatten(root).find((element) => 'unavailable-heading' === element.props['aria-labelledby'])
  const stack = item.stacks[0]!
  const service = stack.services[0]!
  expect(attentionRows()).toHaveLength(1)
  expect(flatten(attentionRows()[0]!).find((element) => 'a' === element.type)?.props.to).toBe(
    '/stacks/office/app',
  )
  expect(unavailable()).toBeUndefined()

  service.health = 'healthy'
  await vue.nextTick()
  expect(attentionRows()).toHaveLength(0)
  expect(attention()).toBeUndefined()
  service.state = 'restarting'
  await vue.nextTick()
  expect(attentionRows()).toHaveLength(1)

  stack.loading = true
  await vue.nextTick()
  expect(attentionRows()).toHaveLength(0)
  stack.loading = false
  await vue.nextTick()
  expect(attentionRows()).toHaveLength(1)

  for (const connection of ['unavailable', 'reconnecting'] as const) {
    item.connection = connection
    await vue.nextTick()
    expect(attentionRows()).toHaveLength(0)
    expect(unavailable()).toBeDefined()
    expect(flatten(unavailable()!).find((element) => 'a' === element.type)?.props.to).toBe(
      '/nodes/office',
    )
  }
  item.connection = 'paused'
  item.paused = true
  await vue.nextTick()
  expect(attentionRows()).toHaveLength(0)
  expect(unavailable()).toBeUndefined()
  item.paused = false
  item.connection = 'connected'
  await vue.nextTick()
  expect(attentionRows()).toHaveLength(1)
  expect(unavailable()).toBeUndefined()

  service.state = 'exited'
  service.exit_code = 23
  await vue.nextTick()
  expect(attentionRows()).toHaveLength(1)
  service.exit_code = 0
  await vue.nextTick()
  expect(attentionRows()).toHaveLength(0)

  const filters = flatten(root).filter(
    (element) => 'button' === element.type && undefined !== element.props['aria-pressed'],
  )
  expect(filters).toHaveLength(4)
  for (const [index, status] of ['running', 'stopped', 'draft', 'updates'].entries()) {
    workspace.stackSearch = 'previous-search'
    mobile = 0 === index % 2
    const button = filters[index]!
    await trigger(button, 'onClick')
    await vue.nextTick()
    expect(workspace.stackFilter).toBe(status)
    expect(workspace.stackSearch).toBe('')
    expect(workspace.sidebarOpen).toBe(mobile)
    expect(button.props['aria-pressed']).toBe(true)
  }

  const check = flatten(root).find(
    (element) =>
      'button' === element.type &&
      'i-lucide-refresh-cw' === element.props.icon &&
      undefined !== element.props.disabled &&
      !element.props.size,
  )
  await trigger(check!, 'onClick')
  expect(workspace.checkImages).not.toHaveBeenCalled()
  confirmation = true
  await trigger(check!, 'onClick')
  expect(workspace.checkImages).toHaveBeenCalledTimes(1)
})

const mountEditor = async () => {
  const props = vue.reactive<EditorProps>({ content: 'services:\n', mode: 'yaml', readOnly: false })
  const state: { element: HTMLElement | null } = { element: null }
  const ready = Promise.withResolvers<undefined>()
  const updated = mock(() => {})
  const focus = mock(() => {})
  const highlight = mock((content: string, _options: { language: string }) => ({ value: content }))
  let input: (content: string) => void = () => {}
  let code = ''
  let paint: () => void = () => {}
  const jar = {
    updateCode: mock((content: string, notify = true) => {
      code = content
      if (state.element) {
        state.element.textContent = content
      }
      paint()
      if (notify) {
        input(content)
      }
    }),
    onUpdate: (callback: typeof input) => {
      input = callback
    },
    toString: () => code,
    destroy: mock(() => {}),
  }
  const component = await loadComponent('../Editor', {
    codejar: {
      CodeJar: (element: HTMLElement, highlight: (element: HTMLElement) => void) => {
        state.element = element
        element.contentEditable = 'plaintext-only'
        element.focus = focus
        paint = () => highlight(element)
        ready.resolve(undefined)
        return jar
      },
    },
    'codejar-linenumbers': {
      withLineNumbers: (highlight: (element: HTMLElement) => void) => highlight,
    },
    'codejar-linenumbers/es/codejar-linenumbers.css': {},
    '~/utils/highlight': { loadHighlighter: async () => ({ highlight }) },
  })
  const app = renderer.createApp({
    setup: () => () => vue.h(component, { ...props, onUpdated: updated }),
  })
  app.mount(node('root'))
  apps.push(app)
  await ready.promise
  return {
    props,
    state,
    jar,
    updated,
    focus,
    highlight,
    input: (content: string) => {
      code = content
      input(content)
    },
    unmount: () => {
      app.unmount()
      apps.splice(apps.indexOf(app), 1)
    },
  }
}

describe('Editor', () => {
  test('initial content', async () => {
    const view = await mountEditor()
    expect(view.jar.updateCode).toHaveBeenCalledWith('services:\n', false)
    expect(view.updated).not.toHaveBeenCalled()
    expect(view.highlight).toHaveBeenCalledWith('services:\n', { language: 'yaml' })
    expect(view.focus).toHaveBeenCalledTimes(1)
  })

  test('edits and props', async () => {
    const view = await mountEditor()
    view.input('services:\n  web:\n')
    expect(view.updated).toHaveBeenCalledWith('services:\n  web:\n')
    view.props.content = 'services:\n  web:\n'
    await vue.nextTick()
    expect(view.jar.updateCode).toHaveBeenCalledTimes(1)
    view.props.content = 'services: {}\n'
    await vue.nextTick()
    expect(view.jar.updateCode).toHaveBeenLastCalledWith('services: {}\n', false)
    expect(view.updated).toHaveBeenCalledTimes(1)
  })

  test('mode and readonly', async () => {
    const view = await mountEditor()
    view.props.mode = 'ini'
    await vue.nextTick()
    expect(view.highlight).toHaveBeenLastCalledWith('services:\n', { language: 'ini' })
    view.props.readOnly = true
    await vue.nextTick()
    expect(view.state.element?.contentEditable).toBe('false')
    view.input('ignored')
    expect(view.updated).not.toHaveBeenCalled()
    view.props.readOnly = false
    await vue.nextTick()
    expect(view.state.element?.contentEditable).toBe('plaintext-only')
    view.unmount()
    expect(view.jar.destroy).toHaveBeenCalledTimes(1)
  })
})

const mountLogs = async (busy = false, draft = false) => {
  const chunks: Array<string> = []
  const buffers: Array<Array<string>> = []
  const startedOutput: Array<string> = []
  const reset = mock(() => {})
  const start = mock(() => {
    startedOutput.push(chunks.join(''))
  })
  const stop = mock(() => {})
  const completed = mock((_action: string, _exitCode?: number) => {})
  const listeners = new Set<(event: StackEvent) => void>()
  const workspace = vue.reactive({
    error: '',
    streamError: '',
    onStackEvent: (listener: (event: StackEvent) => void) => {
      listeners.add(listener)
      return () => {
        listeners.delete(listener)
      }
    },
  })
  let message: (event: WorkspaceStreamMessage) => void = () => {}
  const props = vue.reactive<StackLogsProps>({
    node: 'node',
    stack: 'app',
    connected: true,
    busy,
    draft,
  })
  const component = await loadComponent('StackLogs', {
    '@nuxt/ui/components/Alert.vue': {
      default: vue.defineComponent({
        setup:
          (_, { slots }) =>
          () =>
            vue.h('alert', [slots.default?.(), slots.actions?.()]),
      }),
    },
    '@nuxt/ui/components/Button.vue': { default: stub('button') },
    '@nuxt/ui/components/Card.vue': { default: stub('section') },
    '~/components/stacks/OutputTerminal.vue': {
      default: vue.defineComponent({
        setup: (_, { expose }) => {
          const buffer: Array<string> = []
          buffers.push(buffer)
          expose({
            write: (chunk: string) => {
              chunks.push(chunk)
              buffer.push(chunk)
            },
            reset,
          })
          return () => vue.h('pre')
        },
      }),
    },
    '~/store/workspace': { useWorkspaceStore: () => workspace },
    '~/composables/useWorkspaceStream': {
      useWorkspaceStream: (onMessage: typeof message) => {
        message = onMessage
        return { start, stop, error: vue.ref('') }
      },
    },
  })
  const root = node('root')
  const app = renderer.createApp({
    setup: () => () => vue.h(component, { ...props, onCompleted: completed }),
  })
  app.mount(root)
  apps.push(app)
  return {
    props,
    chunks,
    buffers,
    startedOutput,
    start,
    stop,
    reset,
    root,
    workspace,
    listeners,
    completed,
    event: (event: StackEvent) => {
      for (const listener of listeners) {
        listener(event)
      }
    },
    unmount: () => {
      app.unmount()
      apps.splice(apps.indexOf(app), 1)
    },
    reconnect: () => {
      const handler = flatten(root).find((element) => 'button' === element.type)?.props.onClick
      if ('function' !== typeof handler) {
        throw new Error('missing retry handler')
      }
      handler()
    },
    fail: () => message({ event: 'message', data: JSON.stringify({ event: 'error' }) }),
    log: (text: string) => message({ event: 'message', data: JSON.stringify({ message: text }) }),
    done: () => message({ event: 'done', data: JSON.stringify({ exit_code: 0 }) }),
  }
}

describe('Stack logs', () => {
  test('pause and resume', async () => {
    const view = await mountLogs()
    view.log('before maintenance')
    view.fail()
    view.props.connected = false
    view.props.paused = true
    await vue.nextTick()
    expect(view.start).toHaveBeenCalledTimes(1)
    expect(flatten(view.root).some((element) => 'alert' === element.type)).toBe(false)
    view.props.paused = false
    view.props.connected = true
    await vue.nextTick()
    expect(view.start).toHaveBeenCalledTimes(2)
    expect(view.buffers).toHaveLength(1)
    expect(view.buffers[0]).toEqual(['before maintenance\r\n'])
    expect(view.reset).not.toHaveBeenCalled()
  })

  test('draft', async () => {
    const view = await mountLogs(false, true)
    expect(view.start).not.toHaveBeenCalled()
    view.props.draft = false
    await vue.nextTick()
    expect(view.start).toHaveBeenCalledTimes(1)
    view.props.draft = true
    await vue.nextTick()
    expect(view.start).toHaveBeenCalledTimes(1)
    expect(view.stop).toHaveBeenCalledTimes(1)
  })

  test('single terminal', async () => {
    const view = await mountLogs()
    expect(view.buffers).toHaveLength(1)
    const buffer = view.buffers[0]!
    view.log('before')
    const frame = '\x1b[31mpulling\x1b[0m\r\n\x1b[1A\x1b[2K\r\x1b[32mpulled\x1b[0m\r\n'
    view.event({
      event: 'stack.action',
      node: 'node',
      name: 'app',
      action: 'update',
      phase: 'start',
    })
    view.event({ event: 'stack.output', node: 'node', name: 'app', output: frame })
    view.event({
      event: 'stack.action',
      node: 'node',
      name: 'app',
      action: 'update',
      phase: 'exit',
      exit_code: 0,
    })
    view.props.connected = false
    await vue.nextTick()
    view.props.connected = true
    await vue.nextTick()
    view.reconnect()
    view.event({
      event: 'stack.action',
      node: 'node',
      name: 'app',
      action: 'restart',
      phase: 'start',
    })
    view.event({
      event: 'stack.output',
      node: 'node',
      name: 'app',
      output: '\x1b[33mrestarting\x1b[0m\r\n',
    })
    view.props.actionError = 'rejection-sentinel'
    await vue.nextTick()
    view.event({
      event: 'stack.action',
      node: 'node',
      name: 'app',
      action: 'restart',
      phase: 'exit',
      exit_code: 0,
    })
    view.log('after')

    expect(view.buffers).toHaveLength(1)
    expect(view.buffers[0]).toBe(buffer)
    expect(buffer.join('')).toStartWith('before\r\n')
    expect(buffer.join('')).toContain(frame)
    expect(buffer.join('')).toContain('\x1b[33mrestarting\x1b[0m\r\n')
    expect(buffer.join('')).toContain('rejection-sentinel')
    expect(buffer.join('')).toEndWith('after\r\n')
    expect(view.reset).not.toHaveBeenCalled()
  })

  test('split ANSI', async () => {
    const view = await mountLogs()
    view.log('before')
    view.event({
      event: 'stack.action',
      node: 'node',
      name: 'app',
      action: 'update',
      phase: 'start',
    })
    view.event({ event: 'stack.output', node: 'node', name: 'app', output: '\x1b[' })
    expect(view.stop).toHaveBeenCalledTimes(1)
    view.log('paused')
    view.fail()
    view.done()
    const frame = '\x1b[31mpulling\x1b[0m\r\n\x1b[1A\x1b[2K\r\x1b[32mpulled\x1b[0m\r\n'
    view.event({ event: 'stack.output', node: 'node', name: 'app', output: frame.slice(2) })

    expect(view.start).toHaveBeenCalledTimes(1)
    expect(view.stop).toHaveBeenCalledTimes(1)
    expect(view.chunks.slice(-2)).toEqual(['\x1b[', frame.slice(2)])
    expect(view.chunks.join('')).toContain(frame)
    expect(view.chunks.join('')).not.toContain('paused')
    expect(alerts(view.root)).toHaveLength(0)

    view.event({
      event: 'stack.action',
      node: 'node',
      name: 'app',
      action: 'update',
      phase: 'exit',
      exit_code: 0,
    })
    view.log('after')
    expect(view.start).toHaveBeenCalledTimes(2)
    expect(view.chunks.join('')).toEndWith('after\r\n')
    expect(view.reset).not.toHaveBeenCalled()
  })

  test('running reconnect', async () => {
    const view = await mountLogs(true)
    expect(view.start).not.toHaveBeenCalled()
    view.log('paused')
    view.props.connected = false
    await vue.nextTick()
    view.props.connected = true
    await vue.nextTick()
    expect(view.start).not.toHaveBeenCalled()
    expect(view.chunks.join('')).not.toContain('paused')
    expect(view.reset).not.toHaveBeenCalled()
  })

  test('paused retry', async () => {
    const view = await mountLogs()
    view.fail()
    view.props.busy = true
    await vue.nextTick()
    expect(alerts(view.root)).toHaveLength(1)
    view.reconnect()
    expect(view.start).toHaveBeenCalledTimes(1)
    view.log('paused')
    expect(view.chunks.join('')).not.toContain('paused')
  })

  test('parent alert', async () => {
    const view = await mountLogs()
    view.props.showErrors = false
    await vue.nextTick()
    expect(view.start).toHaveBeenCalledTimes(1)
    expect(view.stop).not.toHaveBeenCalled()

    view.fail()
    await vue.nextTick()
    expect(alerts(view.root)).toHaveLength(0)

    view.props.showErrors = true
    await vue.nextTick()
    expect(alerts(view.root)).toHaveLength(1)
    view.reconnect()
    expect(view.start).toHaveBeenCalledTimes(2)
  })

  test('connection alerts', async () => {
    const view = await mountLogs()
    view.fail()
    await vue.nextTick()
    expect(alerts(view.root)).toHaveLength(1)

    view.props.connected = false
    await vue.nextTick()
    expect(alerts(view.root)).toHaveLength(0)

    view.workspace.streamError = 'server-error-sentinel'
    view.props.connected = true
    await vue.nextTick()
    expect(alerts(view.root)).toHaveLength(0)

    view.workspace.streamError = ''
    view.workspace.error = 'workspace-error-sentinel'
    await vue.nextTick()
    expect(alerts(view.root)).toHaveLength(0)

    view.workspace.error = ''
    await vue.nextTick()
    expect(alerts(view.root)).toHaveLength(1)

    view.reconnect()
    await vue.nextTick()
    expect(alerts(view.root)).toHaveLength(0)
  })

  test('output retries', async () => {
    const view = await mountLogs()
    view.fail()
    await vue.nextTick()
    expect(alerts(view.root)).toHaveLength(1)
    view.reconnect()
    await vue.nextTick()
    expect(view.start).toHaveBeenCalledTimes(2)
    expect(alerts(view.root)).toHaveLength(0)

    view.fail()
    await vue.nextTick()
    expect(alerts(view.root)).toHaveLength(1)
    view.reconnect()
    await vue.nextTick()
    expect(view.start).toHaveBeenCalledTimes(3)
    expect(alerts(view.root)).toHaveLength(0)
  })

  test('offline failure output', async () => {
    const view = await mountLogs(true)
    const output = 'failure-output-sentinel'
    view.workspace.streamError = 'server-error-sentinel'
    view.props.connected = false
    view.event({ event: 'stack.output', node: 'node', name: 'app', output })
    view.event({
      event: 'stack.action',
      node: 'node',
      name: 'app',
      action: 'update',
      phase: 'exit',
      exit_code: 1,
    })
    await vue.nextTick()
    expect(alerts(view.root)).toHaveLength(0)
    expect(view.chunks.join('')).toContain(output)
    expect(view.buffers).toHaveLength(1)
    expect(view.reset).not.toHaveBeenCalled()
  })

  test('mixed output', async () => {
    const view = await mountLogs()
    view.log('before')
    view.event({
      event: 'stack.action',
      node: 'node',
      name: 'app',
      action: 'update',
      phase: 'start',
    })
    view.event({ event: 'stack.output', node: 'node', name: 'app', output: 'pulling\r\n' })
    view.log('during')
    view.event({ event: 'stack.output', node: 'node', name: 'app', output: 'updated\r\n' })
    view.event({
      event: 'stack.action',
      node: 'node',
      name: 'app',
      action: 'update',
      phase: 'exit',
      exit_code: 0,
    })
    view.log('after')

    const payload = ['before', 'pulling', 'updated', 'after']
    expect(
      view.chunks
        .join('')
        .split('\r\n')
        .filter((line) => payload.includes(line)),
    ).toEqual(payload)
    expect(view.chunks.join('')).not.toContain('during')
    expect(view.start).toHaveBeenCalledTimes(2)
    expect(view.stop).toHaveBeenCalledTimes(1)
    expect(view.reset).not.toHaveBeenCalled()
  })

  test('live busy and removal', async () => {
    const view = await mountLogs()
    view.props.busy = true
    view.event({
      event: 'stack.action',
      node: 'node',
      name: 'app',
      action: 'delete',
      phase: 'start',
    })
    await vue.nextTick()
    expect(view.stop).toHaveBeenCalledTimes(1)
    view.props.busy = undefined
    await vue.nextTick()
    expect(view.start).toHaveBeenCalledTimes(1)
    const output = '\x1b[32mremoved\x1b[0m\r\n'
    view.event({ event: 'stack.output', node: 'node', name: 'app', output })
    view.event({
      event: 'stack.action',
      node: 'node',
      name: 'app',
      action: 'delete',
      phase: 'exit',
      exit_code: 0,
    })
    expect(view.start).toHaveBeenCalledTimes(2)
    expect(view.startedOutput[1]).toContain(output)
    expect(view.completed).toHaveBeenCalledWith('delete', 0)
    expect(view.buffers).toHaveLength(1)
  })

  test('exit before follower', async () => {
    const view = await mountLogs()
    view.props.busy = true
    view.event({
      event: 'stack.action',
      node: 'node',
      name: 'app',
      action: 'update',
      phase: 'start',
    })
    await vue.nextTick()
    view.props.busy = false
    await vue.nextTick()
    expect(view.start).toHaveBeenCalledTimes(1)
    view.log('premature-follower-output')
    const finalOutput = '\x1b[32mfinal-command-output\x1b[0m\r\n'
    view.event({ event: 'stack.output', node: 'node', name: 'app', output: finalOutput })
    expect(view.start).toHaveBeenCalledTimes(1)
    view.event({
      event: 'stack.action',
      node: 'node',
      name: 'app',
      action: 'update',
      phase: 'exit',
      exit_code: 0,
    })
    expect(view.start).toHaveBeenCalledTimes(2)
    expect(view.chunks.join('')).not.toContain('premature-follower-output')
    const resumedOutput = view.startedOutput[1]!
    expect(resumedOutput).toBe(view.chunks.join(''))
    expect(resumedOutput).toContain(finalOutput)
    expect(resumedOutput.length).toBeGreaterThan(
      resumedOutput.indexOf(finalOutput) + finalOutput.length,
    )
    expect(view.completed).toHaveBeenCalledWith('update', 0)
    expect(view.buffers).toHaveLength(1)
    expect(view.reset).not.toHaveBeenCalled()
  })

  test('snapshot busy clears', async () => {
    const view = await mountLogs(true)
    expect(view.start).not.toHaveBeenCalled()
    expect(view.chunks).toEqual([])
    view.props.busy = false
    await vue.nextTick()
    expect(view.start).toHaveBeenCalledTimes(1)
    expect(view.completed).not.toHaveBeenCalled()
    expect(view.buffers).toHaveLength(1)
    expect(view.reset).not.toHaveBeenCalled()
  })

  test('lost action exit', async () => {
    const view = await mountLogs()
    view.props.busy = true
    view.event({
      event: 'stack.action',
      node: 'node',
      name: 'app',
      action: 'update',
      phase: 'start',
    })
    await vue.nextTick()
    view.props.connected = false
    await vue.nextTick()
    view.props.connected = true
    await vue.nextTick()
    view.props.busy = false
    await vue.nextTick()
    expect(view.start).toHaveBeenCalledTimes(1)
    expect(alerts(view.root)).toHaveLength(1)
    view.reconnect()
    view.log('future-container-output')
    expect(view.start).toHaveBeenCalledTimes(2)
    expect(view.chunks.join('')).toEndWith('future-container-output\r\n')
    expect(view.completed).not.toHaveBeenCalled()
    expect(view.buffers).toHaveLength(1)
    expect(view.reset).not.toHaveBeenCalled()
  })

  test('future output only', async () => {
    const view = await mountLogs()
    expect(view.listeners.size).toBe(1)
    view.log('container')
    view.event({ event: 'stack.output', node: 'other', name: 'app', output: 'wrong-node' })
    view.event({ event: 'stack.output', node: 'node', name: 'other', output: 'wrong-stack' })
    expect(view.chunks.join('')).toBe('container\r\n')
    view.unmount()
    expect(view.listeners.size).toBe(0)
    view.event({ event: 'stack.output', node: 'node', name: 'app', output: 'dropped' })
    expect(view.chunks.join('')).toBe('container\r\n')
    const next = await mountLogs()
    expect(next.chunks).toEqual([])
    expect(view.reset).not.toHaveBeenCalled()
  })

  test('rejected action', async () => {
    const view = await mountLogs()
    view.log('before')
    view.props.actionError = 'rejection-sentinel'
    await vue.nextTick()
    view.props.actionError = ''
    await vue.nextTick()
    view.props.actionError = 'rejection-sentinel'
    await vue.nextTick()
    view.log('after')

    const output = view.chunks.join('')
    expect(output.indexOf('before')).toBeLessThan(output.indexOf('rejection-sentinel'))
    expect(output.split('rejection-sentinel')).toHaveLength(3)
    expect(output).toEndWith('after\r\n')
    expect(view.reset).not.toHaveBeenCalled()
  })

  test('reconnect', async () => {
    const view = await mountLogs()
    view.log('before')
    view.props.connected = false
    await vue.nextTick()
    view.props.connected = true
    await vue.nextTick()
    expect(view.start).toHaveBeenCalledTimes(1)
    view.reconnect()
    view.log('after')

    expect(view.start).toHaveBeenCalledTimes(2)
    expect(view.stop).toHaveBeenCalledTimes(1)
    expect(view.chunks.join('')).toBe('before\r\nafter\r\n')
    expect(view.reset).not.toHaveBeenCalled()
  })

  test.each([0, 1])('update %s', async (exitCode) => {
    const view = await mountLogs(true)
    expect(view.start).not.toHaveBeenCalled()
    const finalOutput = 'final-output-sentinel\r\n'
    view.event({ event: 'stack.output', node: 'node', name: 'app', output: finalOutput })
    view.event({
      event: 'stack.action',
      node: 'node',
      name: 'app',
      action: 'update',
      phase: 'exit',
      exit_code: exitCode,
    })
    view.props.busy = false
    await vue.nextTick()
    const completedOutput = view.chunks.join('')
    expect(completedOutput).toContain(finalOutput)
    expect(completedOutput.length).toBeGreaterThan(
      completedOutput.indexOf(finalOutput) + finalOutput.length,
    )
    expect(view.startedOutput).toEqual([completedOutput])
    expect(view.completed).toHaveBeenCalledWith('update', exitCode)
    expect(view.chunks.join('')).toBe(completedOutput)
    expect(view.buffers).toHaveLength(1)
    view.log('after')

    expect(view.start).toHaveBeenCalledTimes(1)
    expect(view.stop).not.toHaveBeenCalled()
    expect(view.chunks.join('')).toEndWith('after\r\n')
    expect(view.reset).not.toHaveBeenCalled()
  })
})

class ImportInput {
  files: Array<{ text: () => Promise<string> }>
  value = 'config.json'

  constructor(text: string) {
    this.files = [{ text: async () => text }]
  }
}

const trigger = (element: TestNode | undefined, event: string, value?: unknown) => {
  const handler = element?.props[event]
  if ('function' !== typeof handler) {
    throw new Error(`Missing ${event} handler`)
  }
  return handler(value)
}

const registryField = (element: TestNode, name: string): TestNode | undefined => {
  const field = flatten(element).find((child) => child.props.name === name)
  return field?.children.find((child) => ['input', 'select'].includes(child.type))
}

const mountCopy = async (conflict = false) => {
  const props = vue.reactive({ open: true, node: 'source', stack: 'demo' })
  const nodes = vue.ref<Array<WorkspaceNode>>(
    ['source', 'target', 'offline'].map((id) => ({
      id,
      name: id,
      public_host: '',
      stacks: [],
      health: null,
      connection: 'offline' === id ? 'unavailable' : 'connected',
      error: '',
      loading: false,
    })),
  )
  const calls: Array<{ url: string; options: RequestInit }> = []
  const files = {
    compose: '# saved compose\nservices:\n  app:\n    env_file: ./custom.env\n',
    env: '# saved env\nSECRET=private:value\r\n',
  }
  const navigate = mock(async (_path: string) => {})
  const notify = mock((_type: string, _title: string, _description: string) => {})
  const component = await loadComponent('../CreateCopyModal', {
    '#app': { navigateTo: navigate },
    '@nuxt/ui/components/Alert.vue': { default: stub('alert') },
    '@nuxt/ui/components/Button.vue': { default: stub('button') },
    '@nuxt/ui/components/FormField.vue': { default: stub('field') },
    '@nuxt/ui/components/Input.vue': { default: stub('input') },
    '@nuxt/ui/components/Select.vue': { default: stub('select') },
    '@nuxt/ui/components/Switch.vue': { default: stub('switch') },
    '@nuxt/ui/components/Modal.vue': {
      default: vue.defineComponent({
        setup:
          (_, { slots }) =>
          () =>
            vue.h('modal', [slots.body?.(), slots.footer?.()]),
      }),
    },
    '~/composables/useWorkspace': { useWorkspace: () => ({ nodes }) },
    '~/utils': {
      request: async (url: string, options: RequestInit = {}) => {
        calls.push({ url, options })
        if ('POST' !== options.method) {
          return Response.json({ name: 'demo', status: 'draft', services: [], ...files })
        }
        return conflict
          ? Response.json({ error: { code: 409, message: 'conflict-sentinel' } }, { status: 409 })
          : Response.json({ name: 'demo-copy' }, { status: 201 })
      },
      parse_api_response: async (response: Response) => response.json(),
      notification: notify,
    },
  })
  const root = node('root')
  const app = renderer.createApp({
    setup: () => () =>
      vue.h(component, {
        ...props,
        'onUpdate:open': (open: boolean) => {
          props.open = open
        },
      }),
  })
  app.mount(root)
  apps.push(app)
  await vue.nextTick()
  return {
    root,
    props,
    calls,
    files,
    navigate,
    notify,
    submit: () =>
      trigger(
        flatten(root).find((element) => 'stack-copy' === element.props.id),
        'onSubmit',
        { preventDefault: () => {} },
      ),
  }
}

describe('Create copy', () => {
  test.each([false, true])('files %s', async (overwrite) => {
    const view = await mountCopy()
    const destination = registryField(view.root, 'node')
    expect(destination?.props.items).toEqual([
      { label: 'target', value: 'target', disabled: false },
      { label: 'offline', value: 'offline', disabled: true },
    ])
    expect(registryField(view.root, 'name')?.props.modelValue).toBe('demo')
    const toggle = flatten(view.root).find((element) => 'switch' === element.type)
    expect(toggle?.props.modelValue).toBe(false)
    await trigger(toggle, 'onUpdate:modelValue', overwrite)
    await trigger(registryField(view.root, 'name'), 'onUpdate:modelValue', ' demo-copy ')
    await view.submit()

    expect(view.calls.map(({ url, options }) => [url, options.method ?? 'GET'])).toEqual([
      ['/api/nodes/source/stacks/demo', 'GET'],
      ['/api/nodes/target/stacks/demo-copy', 'POST'],
    ])
    expect(JSON.parse(String(view.calls[1]?.options.body))).toEqual({ ...view.files, overwrite })
    expect(view.props.open).toBe(false)
    expect(view.navigate).toHaveBeenCalledWith('/stacks/target/demo-copy')
  })

  test('conflict keeps form', async () => {
    const view = await mountCopy(true)
    await view.submit()
    expect(view.props.open).toBe(true)
    expect(registryField(view.root, 'name')?.props.modelValue).toBe('demo')
    expect(view.navigate).not.toHaveBeenCalled()
    expect(view.notify.mock.calls[0]?.[0]).toBe('error')
  })
})

const savedRegistry: RegistryCredential = {
  id: 'saved',
  registry: 'docker.io',
  label: '',
  auth: { auth: '' },
  username: 'account',
  has_auth: true,
  background_checks: false,
}

const mountRegistries = async (saved = savedRegistry) => {
  Object.defineProperty(globalThis, 'HTMLInputElement', { value: ImportInput, configurable: true })
  const ready = Promise.withResolvers<undefined>()
  const reply = Promise.withResolvers<Response>()
  let started = false
  const calls: Array<{ url: string; options: RequestInit }> = []
  const notify = mock(() => {})
  const component = await loadComponent('../RegistrySettings', {
    '@nuxt/ui/components/Alert.vue': { default: stub('alert') },
    '@nuxt/ui/components/Button.vue': { default: stub('button') },
    '@nuxt/ui/components/Checkbox.vue': { default: stub('checkbox') },
    '@nuxt/ui/components/Switch.vue': { default: stub('switch') },
    '@nuxt/ui/components/FormField.vue': { default: stub('field') },
    '@nuxt/ui/components/Input.vue': { default: stub('input') },
    '@nuxt/ui/components/Modal.vue': {
      default: vue.defineComponent({
        props: { open: Boolean },
        setup:
          (props, { slots }) =>
          () =>
            props.open ? vue.h('modal', [slots.body?.(), slots.footer?.()]) : null,
      }),
    },
    '~/utils/registries': await import('../app/utils/registries'),
    '~/utils': {
      request: async (url: string, options: RequestInit = {}) => {
        calls.push({ url, options })
        started = true
        return 'PUT' === options.method ? reply.promise : Response.json({ registries: [saved] })
      },
      parse_api_response: async (response: Response) => response.json(),
      notification: notify,
    },
  })
  const root = node('root')
  const app = renderer.createApp({
    setup: () => () =>
      vue.h(component, {
        onBusy: (busy: boolean) => {
          if (started && !busy) {
            ready.resolve(undefined)
          }
        },
      }),
  })
  app.mount(root)
  apps.push(app)
  await ready.promise
  await vue.nextTick()
  return {
    root,
    calls,
    notify,
    edit: async (index: number) => {
      const article = flatten(root).filter((element) => 'article' === element.type)[index]
      await trigger(
        article && flatten(article).find((element) => 'i-lucide-pencil' === element.props.icon),
        'onClick',
      )
      await vue.nextTick()
    },
    submit: () => {
      const editor = flatten(root).find((element) => 'registry-editor' === element.props.id)
      if (editor) {
        return trigger(editor, 'onSubmit', { preventDefault: () => {} })
      }
      return trigger(
        flatten(root).find((element) => 'registry-settings' === element.props.id),
        'onSubmit',
        {
          preventDefault: () => {},
        },
      )
    },
    respond: (response: RegistriesResponse | GenericError, status = 200) =>
      reply.resolve(Response.json(response, { status })),
    import: async (text: string) => {
      const input = new ImportInput(text)
      await trigger(
        flatten(root).find((element) => 'file' === element.props.type),
        'onChange',
        { target: input },
      )
      await vue.nextTick()
      expect(input.value).toBe('')
    },
  }
}

describe('Registry settings', () => {
  test('saved secrets and busy state', async () => {
    const view = await mountRegistries()
    await view.edit(0)
    expect(registryField(view.root, 'secret-0')?.props.modelValue).toBe('')
    expect(registryField(view.root, 'secret-0')?.props.type).toBe('password')
    expect(registryField(view.root, 'secret-0')?.props.placeholder).toBeTruthy()
    await trigger(registryField(view.root, 'username-0'), 'onUpdate:modelValue', 'edited')
    await trigger(registryField(view.root, 'secret-0'), 'onUpdate:modelValue', 'replacement')
    const pending = view.submit()
    await vue.nextTick()
    expect(view.calls).toHaveLength(2)
    expect(view.calls[1]?.url).toBe('/api/settings/registries')
    expect(JSON.parse(String(view.calls[1]?.options.body))).toEqual({
      registries: [
        {
          id: 'saved',
          registry: 'docker.io',
          label: '',
          auth: { auth: '', username: 'edited', password: 'replacement' },
          background_checks: false,
        },
      ],
    })
    expect(
      flatten(view.root)
        .filter((element) => 'button' === element.type)
        .every((element) => element.props.disabled),
    ).toBe(true)
    view.respond({ registries: [{ ...savedRegistry, username: 'edited' }] })
    await pending
    await vue.nextTick()
    await view.edit(0)
    const secret = flatten(view.root).find(
      (element) => 'input' === element.type && 'password' === element.props.type,
    )
    expect(secret).toBeDefined()
    expect(secret?.props.modelValue).toBe('')
    expect(view.notify).toHaveBeenCalledTimes(1)
  })

  test('delete confirmation', async () => {
    const view = await mountRegistries()
    const remove = () =>
      trigger(
        flatten(view.root).find((element) => 'i-lucide-trash-2' === element.props.icon),
        'onClick',
      )
    await remove()
    await vue.nextTick()
    await trigger(
      flatten(view.root).find((element) => 'i-lucide-arrow-left' === element.props.icon),
      'onClick',
    )
    await vue.nextTick()
    expect(flatten(view.root).filter((element) => 'article' === element.type)).toHaveLength(1)
    await remove()
    await vue.nextTick()
    await trigger(
      flatten(view.root).find((element) => 'i-lucide-trash-2' === element.props.icon),
      'onClick',
    )
    await vue.nextTick()
    expect(flatten(view.root).filter((element) => 'article' === element.type)).toHaveLength(0)
    expect(view.calls).toHaveLength(1)
    const pending = view.submit()
    expect(JSON.parse(String(view.calls[1]?.options.body))).toEqual({ registries: [] })
    view.respond({ registries: [] })
    await pending
  })

  test('manual Docker login', async () => {
    const view = await mountRegistries()
    await trigger(
      flatten(view.root).find((element) => 'i-lucide-plus' === element.props.icon),
      'onClick',
    )
    await vue.nextTick()
    await trigger(
      registryField(view.root, 'registry-1'),
      'onUpdate:modelValue',
      'registry.example:5000',
    )
    await trigger(registryField(view.root, 'username-1'), 'onUpdate:modelValue', 'manual')
    await trigger(registryField(view.root, 'secret-1'), 'onUpdate:modelValue', 'password:token')
    const pending = view.submit()
    expect(JSON.parse(String(view.calls[1]?.options.body))).toEqual({
      registries: [
        {
          id: 'saved',
          registry: 'docker.io',
          label: '',
          auth: { auth: '' },
          background_checks: false,
        },
        {
          registry: 'registry.example:5000',
          label: '',
          auth: { auth: btoa('manual:password:token') },
          background_checks: false,
        },
      ],
    })
    view.respond({ registries: [savedRegistry] })
    await pending
  })

  test('import preview and selection', async () => {
    const view = await mountRegistries()
    await view.import(
      JSON.stringify({
        auths: {
          'https://index.docker.io/v1/': { auth: btoa('account:imported:password') },
          'https://index.docker.io/v1/access-token': { auth: btoa('account:oauth-access') },
          'https://index.docker.io/v1/refresh-token': {
            auth: btoa('account:oauth-refresh-client'),
          },
          'ghcr.io': { identitytoken: 'native-identity' },
          'helper.example.com': {},
        },
        credsStore: 'desktop',
      }),
    )
    expect(view.calls).toHaveLength(1)
    const articles = flatten(view.root).filter((element) => 'article' === element.type)
    expect(articles).toHaveLength(6)
    const imported = articles
      .slice(1)
      .map((article) => flatten(article).find((element) => 'checkbox' === element.type))
    expect(imported.map((element) => element?.props.modelValue)).toEqual([
      true,
      true,
      true,
      true,
      false,
    ])
    await view.edit(1)
    await trigger(
      flatten(view.root).find((element) => 'switch' === element.type),
      'onUpdate:modelValue',
      true,
    )
    const pending = view.submit()
    const body: unknown = JSON.parse(String(view.calls[1]?.options.body))
    expect(body).toMatchObject({
      registries: [
        { id: 'saved', auth: { auth: '' } },
        {
          registry: 'https://index.docker.io/v1/',
          auth: { auth: btoa('account:imported:password') },
          background_checks: true,
        },
        {
          registry: 'https://index.docker.io/v1/access-token',
          auth: { auth: btoa('account:oauth-access') },
          background_checks: false,
        },
        {
          registry: 'https://index.docker.io/v1/refresh-token',
          auth: { auth: btoa('account:oauth-refresh-client') },
          background_checks: false,
        },
        {
          registry: 'ghcr.io',
          auth: { identitytoken: 'native-identity' },
          background_checks: false,
        },
      ],
    })
    view.respond({ registries: [savedRegistry] })
    await pending
  })

  test('bad import stays local', async () => {
    const view = await mountRegistries()
    await view.import('{')
    expect(view.calls).toHaveLength(1)
    expect(flatten(view.root).filter((element) => 'article' === element.type)).toHaveLength(1)
    expect(alerts(view.root)).toHaveLength(1)
    expect(view.notify).toHaveBeenCalledTimes(1)
  })

  test('OAuth selection is independent', async () => {
    const view = await mountRegistries()
    await view.import(
      JSON.stringify({
        auths: {
          'https://index.docker.io/v1/': { auth: btoa('account:primary') },
          'https://index.docker.io/v1/access-token': { auth: btoa('account:oauth-access') },
          'https://index.docker.io/v1/refresh-token': { auth: btoa('account:oauth-refresh') },
        },
      }),
    )
    const articles = flatten(view.root).filter((element) => 'article' === element.type)
    const access = articles[2]
    if (!access) {
      throw new Error('Missing OAuth import row')
    }
    await trigger(
      flatten(access).find((element) => 'checkbox' === element.type),
      'onUpdate:modelValue',
      false,
    )
    const pending = view.submit()
    expect(JSON.parse(String(view.calls[1]?.options.body))).toMatchObject({
      registries: [
        { id: 'saved' },
        { registry: 'https://index.docker.io/v1/', auth: { auth: btoa('account:primary') } },
        {
          registry: 'https://index.docker.io/v1/refresh-token',
          auth: { auth: btoa('account:oauth-refresh') },
        },
      ],
    })
    view.respond({ registries: [savedRegistry] })
    await pending
  })

  test('helper credentials can be filled', async () => {
    const view = await mountRegistries()
    await view.import('{"auths":{"helper.example.com":{}},"credsStore":"desktop"}')
    const helper = flatten(view.root).filter((element) => 'article' === element.type)[1]
    if (!helper) {
      throw new Error('Missing helper import row')
    }
    await trigger(
      flatten(helper).find((element) => 'checkbox' === element.type),
      'onUpdate:modelValue',
      true,
    )
    await view.edit(1)
    await trigger(registryField(view.root, 'username-1'), 'onUpdate:modelValue', 'helper-user')
    await trigger(registryField(view.root, 'secret-1'), 'onUpdate:modelValue', 'helper-password')
    const pending = view.submit()
    expect(JSON.parse(String(view.calls[1]?.options.body))).toMatchObject({
      registries: [
        { id: 'saved' },
        {
          registry: 'helper.example.com',
          auth: { auth: btoa('helper-user:helper-password') },
        },
      ],
    })
    view.respond({ registries: [savedRegistry] })
    await pending
  })

  test('native fields and label', async () => {
    const auth = {
      auth: '',
      username: 'account',
      password: '',
      identitytoken: '',
      registrytoken: '',
      email: 'account@example.com',
      serveraddress: 'docker.io',
    }
    const view = await mountRegistries({ ...savedRegistry, auth })
    await view.edit(0)
    expect(registryField(view.root, 'identitytoken-0')?.props.modelValue).toBe('')
    expect(registryField(view.root, 'registrytoken-0')?.props.modelValue).toBe('')
    await trigger(registryField(view.root, 'identitytoken-0'), 'onUpdate:modelValue', 'replacement')
    await trigger(registryField(view.root, 'label-0'), 'onUpdate:modelValue', 'My login / checks')
    const pending = view.submit()
    expect(JSON.parse(String(view.calls[1]?.options.body))).toMatchObject({
      registries: [
        {
          id: 'saved',
          label: 'My login / checks',
          auth: { ...auth, identitytoken: 'replacement' },
        },
      ],
    })
    view.respond({ registries: [{ ...savedRegistry, auth, label: 'My login / checks' }] })
    await pending
  })

  test('import login edits', async () => {
    const view = await mountRegistries()
    const original = btoa('account:original:password')
    await view.import(
      JSON.stringify({
        auths: {
          'ghcr.io': { auth: original, identitytoken: 'native-token', email: 'mail@example.com' },
        },
      }),
    )
    await view.edit(1)
    expect(registryField(view.root, 'username-1')?.props.modelValue).toBe('account')
    expect(registryField(view.root, 'secret-1')?.props.modelValue).toBe('original:password')
    await trigger(registryField(view.root, 'username-1'), 'onUpdate:modelValue', 'edited')
    const pending = view.submit()
    expect(JSON.parse(String(view.calls[1]?.options.body))).toMatchObject({
      registries: [
        { id: 'saved' },
        {
          registry: 'ghcr.io',
          auth: {
            auth: btoa('edited:original:password'),
            identitytoken: 'native-token',
            email: 'mail@example.com',
          },
        },
      ],
    })
    const body = JSON.parse(String(view.calls[1]?.options.body)) as {
      registries: Array<{ auth: Record<string, string> }>
    }
    expect(Object.keys(body.registries[1]?.auth ?? {}).sort()).toEqual([
      'auth',
      'email',
      'identitytoken',
    ])
    view.respond({ registries: [savedRegistry] })
    await pending
  })

  test('metadata keeps native auth', async () => {
    const view = await mountRegistries()
    const auth = {
      auth: btoa('encoded:encoded-password'),
      username: 'native-user',
      password: 'native-password',
      email: 'native@example.com',
      serveraddress: 'HTTPS://GHCR.IO/',
      identitytoken: 'identity',
      registrytoken: 'bearer',
    }
    const tokenAuth = { username: 'token-user', identitytoken: 'identity-only' }
    await view.import(
      JSON.stringify({ auths: { 'HTTPS://GHCR.IO/': auth, 'token.example': tokenAuth } }),
    )
    await view.edit(1)
    await trigger(registryField(view.root, 'label-1'), 'onUpdate:modelValue', 'Optional label')
    const pending = view.submit()
    expect(JSON.parse(String(view.calls[1]?.options.body))).toEqual({
      registries: [
        {
          id: 'saved',
          registry: 'docker.io',
          label: '',
          auth: { auth: '' },
          background_checks: false,
        },
        { registry: 'HTTPS://GHCR.IO/', label: 'Optional label', auth, background_checks: false },
        { registry: 'token.example', label: '', auth: tokenAuth, background_checks: false },
      ],
    })
    view.respond({ registries: [savedRegistry] })
    await pending
  })

  test('failed save keeps edits', async () => {
    const view = await mountRegistries()
    await view.edit(0)
    await trigger(registryField(view.root, 'secret-0'), 'onUpdate:modelValue', 'replacement')
    const pending = view.submit()
    view.respond({ error: { code: 400, message: 'error-sentinel' } }, 400)
    await pending
    await vue.nextTick()
    await view.edit(0)
    expect(registryField(view.root, 'secret-0')?.props.modelValue).toBe('replacement')
    expect(registryField(view.root, 'secret-0')?.props.disabled).toBe(false)
    expect(alerts(view.root)).toHaveLength(1)
  })
})

const savedNode: SettingsNode = {
  id: 'saved',
  name: 'Node',
  url: 'https://node.example.com:9077',
  fingerprint: 'a'.repeat(64),
  public_host: 'node.example.com',
  has_token: true,
  token: 'saved-key',
}

const mountSettings = async (saved = savedNode, section: 'nodes' | 'account' = 'nodes') => {
  let reply = Promise.withResolvers<Response>()
  const calls: Array<{ url: string; options: RequestInit }> = []
  const notification = mock((_type: string, _title: string, _description?: string) => {})
  const realtime = { pause: mock(() => {}), resume: mock(() => {}) }
  const auth = { refreshStatus: mock(async () => {}) }
  const updateOpen = mock((_open: boolean) => {})
  const component = await loadComponent('../SettingsModal', {
    '@nuxt/ui/components/Alert.vue': { default: stub('alert') },
    '@nuxt/ui/components/Button.vue': { default: stub('button') },
    '@nuxt/ui/components/FormField.vue': { default: stub('field') },
    '@nuxt/ui/components/Icon.vue': { default: stub('icon') },
    '@nuxt/ui/components/Input.vue': { default: stub('input') },
    '@nuxt/ui/components/Modal.vue': {
      default: vue.defineComponent({
        props: { open: Boolean },
        setup:
          (props, { slots }) =>
          () =>
            props.open ? vue.h('modal', [slots.body?.(), slots.footer?.()]) : null,
      }),
    },
    '~/components/RegistrySettings.vue': { default: stub('registries') },
    '~/composables/useDialog': {
      useDialog: () => ({ confirmDialog: async () => ({ status: true }) }),
    },
    '~/store/auth': { useAuthStore: () => auth },
    '~/store/realtime': { useRealtimeStore: () => realtime },
    '~/store/workspace': { useWorkspaceStore: () => ({ nodes: [] }) },
    '~/utils/settings': await import('../app/utils/settings'),
    '~/utils': {
      request: async (url: string, options: RequestInit = {}) => {
        calls.push({ url, options })
        if (options.method) {
          reply = Promise.withResolvers<Response>()
          return reply.promise
        }
        return Response.json({ username: 'account', nodes: [saved] })
      },
      parse_api_response: async (response: Response) => response.json(),
      notification,
    },
  })
  const root = node('root')
  const app = renderer.createApp(component, { open: true, section, 'onUpdate:open': updateOpen })
  app.mount(root)
  apps.push(app)
  await new Promise<void>((resolve) => setImmediate(resolve))
  await vue.nextTick()
  if ('nodes' === section) {
    await trigger(
      flatten(root).find((element) => 'i-lucide-pencil' === element.props.icon),
      'onClick',
    )
    await vue.nextTick()
    const form = flatten(root).find((element) => 'settings-node' === element.props.id)
    if (!form) {
      throw new Error('Missing node form')
    }
    Object.assign(form, { reportValidity: () => true })
  }
  return {
    root,
    calls,
    notification,
    realtime,
    auth,
    updateOpen,
    back: async () => {
      await trigger(
        flatten(root).find((element) => 'i-lucide-arrow-left' === element.props.icon),
        'onClick',
      )
      await vue.nextTick()
    },
    field: (name: string) => registryField(root, name),
    submit: () =>
      trigger(
        flatten(root).find(
          (element) =>
            `settings-${'account' === section ? 'account' : 'node'}` === element.props.id,
        ),
        'onSubmit',
        { preventDefault: () => {} },
      ),
    test: () =>
      trigger(
        flatten(root).find((element) => 'i-lucide-plug-zap' === element.props.icon),
        'onClick',
      ),
    respond: (response: object = saved, status = 200) =>
      reply.resolve(Response.json(response, { status })),
  }
}

describe('Account settings', () => {
  test.each([200, 400])('save %s', async (status) => {
    const view = await mountSettings(savedNode, 'account')
    await trigger(view.field('current_password'), 'onUpdate:modelValue', 'current-secret')
    const pending = view.submit()
    expect(view.realtime.pause).toHaveBeenCalledTimes(1)
    expect(view.realtime.resume).not.toHaveBeenCalled()
    expect(view.calls[1]?.url).toBe('/api/settings/account')
    expect(view.calls[1]?.options.method).toBe('POST')
    expect(JSON.parse(String(view.calls[1]?.options.body))).toEqual({
      username: 'account',
      password: '',
      current_password: 'current-secret',
    })
    view.respond(
      200 === status ? { status: 'ok' } : { error: { code: 400, message: 'failure-sentinel' } },
      status,
    )
    await pending
    expect(view.realtime.resume).toHaveBeenCalledTimes(1)
    expect(view.auth.refreshStatus).toHaveBeenCalledTimes(200 === status ? 1 : 0)
    if (200 === status) {
      expect(view.updateOpen).toHaveBeenCalledWith(false)
    } else {
      expect(view.updateOpen).not.toHaveBeenCalled()
    }
  })
})

describe('Node settings', () => {
  test.each([false, true])('pause %s', async (paused) => {
    const existing = { ...savedNode, paused }
    const view = await mountSettings(existing)
    if (paused) {
      await view.test()
      expect(view.calls).toHaveLength(1)
    }
    await view.back()
    const pending = trigger(
      flatten(view.root).find(
        (element) => (paused ? 'i-lucide-play' : 'i-lucide-pause') === element.props.icon,
      ),
      'onClick',
    )
    expect(view.calls[1]?.url).toBe(`/api/settings/nodes/${existing.id}`)
    expect(view.calls[1]?.options.method).toBe('PUT')
    expect(JSON.parse(String(view.calls[1]?.options.body))).toMatchObject({
      name: existing.name,
      url: existing.url,
      token: existing.token,
      fingerprint: existing.fingerprint,
      public_host: existing.public_host,
      paused: !paused,
    })
    view.respond({ ...existing, paused: !paused })
    await pending
    await vue.nextTick()
    const toggle = flatten(view.root).find(
      (element) => (paused ? 'i-lucide-pause' : 'i-lucide-play') === element.props.icon,
    )
    expect(toggle?.props['aria-pressed']).toBe(!paused)
    expect(
      flatten(view.root).find((element) => 'i-lucide-unplug' === element.props.icon)?.props
        .disabled,
    ).toBe(!paused)
  })

  test.each([undefined, 'pairing-warning-sentinel'])('disconnect %s', async (warning) => {
    const view = await mountSettings()
    await view.back()
    await trigger(
      flatten(view.root).find((element) => 'i-lucide-unplug' === element.props.icon),
      'onClick',
    )
    await vue.nextTick()
    const pending = trigger(
      flatten(view.root).find((element) => 'i-lucide-unplug' === element.props.icon),
      'onClick',
    )
    await vue.nextTick()
    expect(view.calls[1]?.url).toBe(`/api/settings/nodes/${savedNode.id}`)
    expect(view.calls[1]?.options.method).toBe('DELETE')
    view.respond({ status: 'ok', warning })
    await pending
    expect(view.notification.mock.calls.at(-1)?.[0]).toBe(warning ? 'warning' : 'success')
    expect(view.notification.mock.calls.at(-1)?.[2]).toBe(warning ?? '')
  })

  test.each([true, false])('edit prefill %s', async (has_token) => {
    const token = has_token ? savedNode.token : ''
    const view = await mountSettings({ ...savedNode, has_token, token })
    expect(view.field('token')?.props.modelValue).toBe(token)
    expect(view.field('token')?.props.type).toBe('password')
    expect(Boolean(view.field('token')?.props.placeholder)).toBe(has_token)
    expect(view.field('token')?.props.required).toBeUndefined()
    expect(view.field('fingerprint')?.props.modelValue).toBe(savedNode.fingerprint)
    for (const name of ['token', 'fingerprint']) {
      expect(view.field(name)?.props.disabled).toBe(false)
      expect(view.field(name)?.props.readonly).toBeUndefined()
    }
    expect(view.field('pairing_token')).toBeUndefined()
  })

  test('edit credentials', async () => {
    const view = await mountSettings()
    const fingerprint = 'b'.repeat(64)
    await trigger(view.field('token'), 'onUpdate:modelValue', 'replacement')
    await trigger(view.field('fingerprint'), 'onUpdate:modelValue', ` ${fingerprint} `)
    const testing = view.test()
    await vue.nextTick()
    expect(view.calls[1]?.url).toBe('/api/settings/nodes/test')
    expect(view.calls[1]?.options.method).toBe('POST')
    const input = {
      name: savedNode.name,
      url: savedNode.url,
      token: 'replacement',
      fingerprint,
      public_host: savedNode.public_host,
    }
    expect(JSON.parse(String(view.calls[1]?.options.body))).toEqual({ ...input, id: savedNode.id })
    expect(view.field('token')?.props.disabled).toBe(true)
    expect(view.field('fingerprint')?.props.disabled).toBe(true)
    view.respond({ version: '1' })
    await testing
    const saving = view.submit()
    await vue.nextTick()
    expect(view.calls[2]?.url).toBe(`/api/settings/nodes/${savedNode.id}`)
    expect(view.calls[2]?.options.method).toBe('PUT')
    expect(JSON.parse(String(view.calls[2]?.options.body))).toEqual(input)
    expect(view.field('token')?.props.disabled).toBe(true)
    expect(view.field('fingerprint')?.props.disabled).toBe(true)
    view.respond()
    await saving
  })

  test('blank key keeps saved', async () => {
    const view = await mountSettings()
    await trigger(view.field('token'), 'onUpdate:modelValue', '')
    const testing = view.test()
    expect(JSON.parse(String(view.calls[1]?.options.body))).toMatchObject({
      id: savedNode.id,
      token: '',
      fingerprint: savedNode.fingerprint,
    })
    view.respond({ version: '1' })
    await testing
    const saving = view.submit()
    expect(view.calls[2]?.options.method).toBe('PUT')
    expect(JSON.parse(String(view.calls[2]?.options.body))).toMatchObject({
      token: '',
      fingerprint: savedNode.fingerprint,
    })
    view.respond()
    await saving
  })

  test.each(['', 'supplied-fingerprint'])('pair fingerprint %s', async (fingerprint) => {
    const view = await mountSettings()
    await view.back()
    await trigger(
      flatten(view.root).find((element) => 'i-lucide-plus' === element.props.icon),
      'onClick',
    )
    await vue.nextTick()
    expect(view.field('token')).toBeUndefined()
    expect(view.field('fingerprint')?.props.modelValue).toBe('')
    expect(view.field('fingerprint')?.props.required).toBe(false)
    expect([true, '']).toContain(view.field('pairing_token')?.props.required)
    await trigger(view.field('name'), 'onUpdate:modelValue', 'New node')
    await trigger(view.field('url'), 'onUpdate:modelValue', 'node.example.com:9077')
    await trigger(view.field('pairing_token'), 'onUpdate:modelValue', 'temporary-token')
    await trigger(view.field('fingerprint'), 'onUpdate:modelValue', ` ${fingerprint} `)
    const pending = view.submit()
    expect(view.calls[1]?.url).toBe('/api/settings/nodes/pair')
    expect(view.calls[1]?.options.method).toBe('POST')
    expect(JSON.parse(String(view.calls[1]?.options.body))).toEqual({
      name: 'New node',
      url: 'node.example.com:9077',
      pairing_token: 'temporary-token',
      ...(fingerprint ? { fingerprint } : {}),
    })
    view.respond()
    await pending
  })
})

test.each([404, 500])('error recovery %s', async (status) => {
  const clearError = mock(async (_options: { redirect: string }) => {})
  const path = '/stacks/node/app?view=compose'
  const component = await loadComponent('../../error', {
    '#app': { clearError, useRoute: () => ({ fullPath: path }), useHead: () => {} },
    '@nuxt/ui/components/Alert.vue': { default: stub('alert') },
    '@nuxt/ui/components/App.vue': { default: stub('app') },
    '@nuxt/ui/components/Button.vue': { default: stub('button') },
    '@nuxt/ui/components/Card.vue': { default: stub('section') },
    '@nuxt/ui/components/Icon.vue': { default: stub('icon') },
  })
  const root = node('root')
  const app = renderer.createApp(component, {
    error: {
      status,
      statusText: 404 === status ? 'Not found' : 'Application error',
      message: 'error-sentinel',
      ...(500 === status ? { stack: 'stack-sentinel' } : {}),
    },
  })
  app.mount(root)
  apps.push(app)
  expect(alerts(root)[0]?.props.description).toBe('error-sentinel')
  expect(flatten(root).find((element) => 'error-stack-trace' === element.props.id)).toBeUndefined()
  const toggle = flatten(root).find(
    (element) => 'error-stack-trace' === element.props['aria-controls'],
  )
  if (500 === status) {
    expect(toggle?.props['aria-expanded']).toBe(false)
    await trigger(toggle, 'onClick')
    await vue.nextTick()
    expect(toggle?.props['aria-expanded']).toBe(true)
    expect(flatten(root).find((element) => 'error-stack-trace' === element.props.id)).toBeDefined()
  } else {
    expect(toggle).toBeUndefined()
  }
  await trigger(
    flatten(root).find((element) => 'i-lucide-house' === element.props.icon),
    'onClick',
  )
  expect(clearError).toHaveBeenLastCalledWith({ redirect: '/' })
  await trigger(
    flatten(root).find((element) => 'i-lucide-rotate-cw' === element.props.icon),
    'onClick',
  )
  expect(clearError).toHaveBeenLastCalledWith({ redirect: path })
})

test.each(['stack', 'node'] as const)('%s page alerts', async (page) => {
  Object.defineProperty(globalThis, 'window', { value: new EventTarget(), configurable: true })
  const navigate = mock(async (_path: string) => {})
  const replace = mock(async (_route: { query: Record<string, unknown> }) => {})
  const notify = mock((_color: string, _title: string, _description: string) => {})
  const auth = vue.reactive({ authenticated: true })
  const confirm = mock(async () => ({ status: false }))
  let leave = async (): Promise<boolean> => true
  let failAction = true
  const now = vue.ref(new Date('2026-11-03T03:55:52Z'))
  const item = vue.reactive<WorkspaceNode>({
    id: 'node',
    name: 'Node',
    public_host: '',
    connection: 'unavailable',
    error: 'node-error-sentinel',
    loading: false,
    health: null,
    stacks: [
      {
        name: 'app',
        status: 'running',
        loading: false,
        services: [
          {
            name: 'app-1',
            service: 'app',
            image: 'example',
            state: 'running',
            health: '',
            created_at: '2026-11-02T00:39:58.237Z',
            started_at: '2026-11-03T03:55:14Z',
            status: 'stale-status-sentinel',
            running_for: 'stale-age-sentinel',
            ports: '',
          },
        ],
      },
    ],
  })
  const workspace = vue.reactive({
    nodes: [item],
    loading: false,
    ready: true,
    error: '',
    streamError: '',
    load: () => {},
    isCheckingImages: () => false,
    isSubmitting: () => false,
    runAction: mock(async () => {
      if (failAction) {
        throw new Error('action-error-sentinel')
      }
      return { status: 'accepted' as const }
    }),
  })
  const store = { useWorkspaceStore: () => workspace }
  const child = await loadComponent(
    `../../pages/${'stack' === page ? 'stacks/[node]/[stack]' : 'nodes/[node]'}`,
    {
      pinia: { storeToRefs: vue.toRefs },
      '#app': {
        useRoute: () => ({
          params: { node: item.id, stack: 'app' },
          query: { op: 'stale', tab: 'logs' },
        }),
        useRouter: () => ({ replace }),
        navigateTo: navigate,
        useHead: () => {},
      },
      '#imports': { definePageMeta: () => {} },
      'vue-router': {
        onBeforeRouteLeave: (guard: typeof leave) => {
          leave = guard
        },
        onBeforeRouteUpdate: () => {},
      },
      '@vueuse/core': {
        useNow: () => now,
        useStorage: (_key: string, value: boolean) => vue.ref(value),
        useClipboard: () => ({ copyPending: vue.ref(false) }),
      },
      '@nuxt/ui/components/Alert.vue': { default: stub('alert') },
      '@nuxt/ui/components/Badge.vue': { default: stub('badge') },
      '@nuxt/ui/components/Button.vue': { default: stub('button') },
      '@nuxt/ui/components/Card.vue': {
        default: vue.defineComponent({
          setup:
            (_, { slots }) =>
            () =>
              vue.h('section', [slots.header?.(), slots.default?.()]),
        }),
      },
      '@nuxt/ui/components/DropdownMenu.vue': { default: stub('menu') },
      '@nuxt/ui/components/Icon.vue': { default: stub('icon') },
      '~/components/Editor.vue': { default: stub('editor') },
      '~/components/CreateCopyModal.vue': { default: stub('copy-modal') },
      '~/components/StatCard.vue': {
        default: vue.defineComponent({
          setup:
            (_, { slots }) =>
            () =>
              vue.h('stat', [slots.default?.(), slots.status?.(), slots.actions?.()]),
        }),
      },
      '~/components/stacks/ContainerShell.vue': { default: stub('shell') },
      '~/components/stacks/StackLogs.vue': {
        default: vue.defineComponent({
          props: {
            showErrors: { type: Boolean, default: true },
            actionError: { type: String, default: '' },
          },
          setup: (props) => () =>
            vue.h('logs', { showErrors: props.showErrors, actionError: props.actionError }),
        }),
      },
      '~/composables/useDialog': {
        useDialog: () => ({ confirmDialog: confirm }),
      },
      '~/store/auth': { useAuthStore: () => auth },
      '~/store/workspace': store,
      '~/utils/stacks': await import('../app/utils/stacks'),
      '~/utils/node': await import('../app/utils/node'),
      '~/utils': {
        notification: notify,
        request: async (url: string) =>
          Response.json(
            url.endsWith('/stacks/app')
              ? {
                  name: 'app',
                  status: 'running',
                  services: [],
                  compose: 'services: {}',
                  env: '',
                }
              : {
                  runtime: {
                    name: 'host',
                    version: '1',
                    operating_system: 'Linux',
                    architecture: 'amd64',
                    kernel_version: '6',
                    cpus: 4,
                    memory_bytes: 1024,
                    containers: 1,
                    containers_running: 1,
                    containers_stopped: 0,
                    containers_paused: 0,
                    images: 1,
                    storage_driver: 'overlay',
                  },
                  containers: [],
                  checked_at: '2026-10-25T09:00:00Z',
                },
          ),
        parse_api_response: async (response: Response) => response.json(),
      },
    },
  )
  const wrapper = await loadComponent('../../layouts/workspace', {
    pinia: { storeToRefs: vue.toRefs },
    '#components': { NuxtLayout: stub('layout') },
    '@nuxt/ui/components/Alert.vue': { default: stub('alert') },
    '@nuxt/ui/components/Button.vue': { default: stub('button') },
    '~/store/workspace': store,
  })
  const root = node('root')
  const app = renderer.createApp({
    setup: () => () => vue.h(wrapper, null, { default: () => vue.h(child) }),
  })
  app.mount(root)
  apps.push(app)
  expect(alerts(root)).toHaveLength(1)

  workspace.streamError = 'server-error-sentinel'
  await vue.nextTick()
  expect(alerts(root)).toHaveLength(1)
  expect(alerts(root)[0]?.props.title).toBe(workspace.streamError)

  workspace.error = 'workspace-error-sentinel'
  await vue.nextTick()
  expect(alerts(root)).toHaveLength(1)
  expect(alerts(root)[0]?.props.title).toBe(workspace.error)

  workspace.error = ''
  workspace.streamError = ''
  await vue.nextTick()
  expect(alerts(root)).toHaveLength(1)
  expect(alerts(root)[0]?.props.title).toBe(item.error)

  item.connection = 'connected'
  item.error = ''
  await new Promise<void>((resolve) => setImmediate(resolve))
  await vue.nextTick()
  expect(alerts(root)).toHaveLength(0)

  item.connection = 'paused'
  item.paused = true
  await vue.nextTick()
  expect(alerts(root)).toHaveLength(1)
  expect(alerts(root)[0]?.props.color).toBe('neutral')
  item.connection = 'connected'
  item.paused = false
  await new Promise<void>((resolve) => setImmediate(resolve))
  await vue.nextTick()
  expect(alerts(root)).toHaveLength(0)

  if ('stack' === page) {
    const service = item.stacks[0]!.services[0]!
    const created = flatten(root).find((element) => 'time' === element.type)
    expect(created?.props.datetime).toBe(service.created_at)
    const createdText = created?.text
    const renderedText = () =>
      flatten(root)
        .map((element) => element.text)
        .join(' ')
    const before = renderedText()
    expect(before).not.toContain(service.status!)
    expect(before).not.toContain(service.running_for!)
    const snapshot = JSON.stringify(service)
    now.value = new Date(now.value.getTime() + 1000)
    await vue.nextTick()
    expect(renderedText()).not.toBe(before)
    expect(JSON.stringify(service)).toBe(snapshot)
    expect(flatten(root).find((element) => 'time' === element.type)?.text).toBe(createdText)
    expect(replace).toHaveBeenCalledTimes(1)
    expect(replace).toHaveBeenCalledWith({ query: { tab: 'logs' } })
    expect(navigate).not.toHaveBeenCalled()
    const runtime = item.stacks[0]!
    runtime.error = 'runtime-error-sentinel'
    await vue.nextTick()
    expect(alerts(root)).toHaveLength(1)
    expect(alerts(root)[0]?.props.title).toBe(runtime.error)
    expect(alerts(root)[0]?.props.description).toBeUndefined()
    expect(flatten(root).find((element) => 'logs' === element.type)?.props.showErrors).toBe(false)

    item.connection = 'unavailable'
    item.error = 'node-error-sentinel'
    await vue.nextTick()
    expect(alerts(root)).toHaveLength(1)
    expect(alerts(root)[0]?.props.title).toBe(item.error)

    item.connection = 'connected'
    item.error = ''
    runtime.error = ''
    await vue.nextTick()
    expect(alerts(root)).toHaveLength(0)
    expect(flatten(root).find((element) => 'logs' === element.type)?.props.showErrors).toBe(true)

    const logs = flatten(root).find((element) => 'logs' === element.type)
    const menu = flatten(root).find(
      (element) =>
        'menu' === element.type &&
        Array.isArray(element.props.items) &&
        !Array.isArray(element.props.items[0]),
    )
    const items = menu?.props.items as Array<DropdownMenuItem>
    const restart = items.find((item) => 'i-lucide-rotate-cw' === item.icon)
    expect(restart).toBeDefined()
    runtime.busy = true
    await vue.nextTick()
    await restart?.onSelect?.(new Event('select'))
    expect(workspace.runAction).not.toHaveBeenCalled()
    runtime.busy = false
    await vue.nextTick()
    await restart?.onSelect?.(new Event('select'))
    await vue.nextTick()

    expect(workspace.runAction).toHaveBeenCalledWith('node', 'app', 'restart', 'app')
    expect(flatten(root).find((element) => 'logs' === element.type)).toBe(logs)
    expect(logs?.props.actionError).toContain('action-error-sentinel')
    expect(logs?.props.actionError).toContain('restart')
    expect(logs?.props.actionError).toContain(item.name)
    expect(notify).toHaveBeenCalledTimes(1)
    expect(notify.mock.calls[0]![2]).toBe(logs?.props.actionError)
    failAction = false
    await restart?.onSelect?.(new Event('select'))
    await vue.nextTick()
    expect(workspace.runAction).toHaveBeenCalledTimes(2)
    expect(notify).toHaveBeenCalledTimes(1)
    expect(replace).toHaveBeenCalledTimes(1)
    expect(navigate).not.toHaveBeenCalled()

    await trigger(
      flatten(root).find(
        (element) => 'button' === element.type && 'i-lucide-pencil' === element.props.icon,
      ),
      'onClick',
    )
    await vue.nextTick()
    await trigger(
      flatten(root).find((element) => 'editor' === element.type),
      'onUpdated',
      'changed',
    )
    await vue.nextTick()
    expect(await leave()).toBe(false)
    expect(confirm).toHaveBeenCalledTimes(1)
    auth.authenticated = false
    expect(await leave()).toBe(true)
    expect(confirm).toHaveBeenCalledTimes(1)

    const complete = logs?.props.onCompleted
    if ('function' !== typeof complete) {
      throw new Error('Missing completion handler')
    }
    complete('delete', 1)
    complete('update', 0)
    expect(navigate).not.toHaveBeenCalled()
    item.stacks = []
    await vue.nextTick()
    expect(flatten(root).find((element) => 'logs' === element.type)).toBe(logs)
    complete('delete', 0)
    expect(navigate).toHaveBeenCalledTimes(1)
    expect(navigate).toHaveBeenCalledWith('/')
  }
})
