import { afterEach, beforeEach, describe, expect, test } from 'bun:test'
import { compileScript, parse, registerTS } from '@vue/compiler-sfc'
import { createPinia, disposePinia, setActivePinia } from 'pinia'
import * as ts from 'typescript'
import * as vue from 'vue'
import type { Component } from 'vue'
import type { Pinia } from 'pinia'
import type { ContainerStats, NodeInfo, WorkspaceNode } from '../app/types'
import { addToast } from './mocks/nuxt'

registerTS(() => ts)

const { useAuthStore } = await import('../app/store/auth')
const { useWorkspaceStore } = await import('../app/store/workspace')

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
    if (element.parent) {
      const previous = element.parent.children
      previous.splice(previous.indexOf(element), 1)
    }
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
const text = (root: TestNode) =>
  flatten(root)
    .map((element) => element.text)
    .join(' ')

const stub = (tag: string) =>
  vue.defineComponent({
    setup:
      (_, { slots }) =>
      () =>
        vue.h(tag, slots.default?.()),
  })

const loadComponent = async (file: string, dependencies: Record<string, unknown>) => {
  const filename = `${import.meta.dir}/../app/components/${file}.vue`
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
const originalDocument = Object.getOwnPropertyDescriptor(globalThis, 'document')
const originalFetch = globalThis.fetch
let pinia: Pinia

beforeEach(() => {
  pinia = createPinia()
  setActivePinia(pinia)
  useAuthStore().authenticated = true
  addToast.mockClear()
  Object.defineProperty(globalThis, 'document', {
    value: Object.assign(new EventTarget(), { hidden: false }),
    configurable: true,
  })
})

afterEach(() => {
  for (const app of apps.splice(0)) {
    app.unmount()
  }
  disposePinia(pinia)
  setActivePinia(undefined)
  globalThis.fetch = originalFetch
  if (originalDocument) {
    Object.defineProperty(globalThis, 'document', originalDocument)
  } else {
    Reflect.deleteProperty(globalThis, 'document')
  }
})

const resourceContainer = (name: string, cpu: string, memory: string): ContainerStats => ({
  id: `private-${name}`,
  name,
  cpu,
  memory: `${memory} / 8GiB`,
  memory_percent: '0%',
  network_io: '0B / 0B',
  block_io: '0B / 0B',
  pids: '1',
})

const resourceInfo = (containers: Array<ContainerStats> = []): NodeInfo => ({
  runtime: {
    name: 'host',
    version: '1',
    operating_system: 'Linux',
    architecture: 'amd64',
    kernel_version: '1',
    cpus: 4,
    memory_bytes: 8 * 1024 ** 3,
    containers: containers.length,
    containers_running: containers.length,
    containers_stopped: 0,
    containers_paused: 0,
    images: 0,
    storage_driver: 'overlay',
  },
  containers,
  checked_at: '2026-11-22T12:00:00Z',
})

const workspaceNode = (id: string, overrides: Partial<WorkspaceNode> = {}): WorkspaceNode => ({
  id,
  name: id,
  public_host: '',
  stacks: [],
  health: null,
  connection: 'connected',
  error: '',
  loading: false,
  ...overrides,
})

interface FetchCall {
  url: string
  signal: AbortSignal | null | undefined
  reply: ReturnType<typeof Promise.withResolvers<Response>>
}

const deferredFetch = () => {
  const calls: Array<FetchCall> = []
  globalThis.fetch = Object.assign(
    async (input: string | URL | Request, options?: RequestInit): Promise<Response> => {
      const reply = Promise.withResolvers<Response>()
      calls.push({ url: String(input), signal: options?.signal, reply })
      return reply.promise
    },
    { preconnect: originalFetch.preconnect },
  )
  return calls
}

const flush = async () => {
  await new Promise<void>((resolve) => setImmediate(resolve))
  await vue.nextTick()
}

const mountResources = async () => {
  const component = await loadComponent('NodeResources', {
    '#components': { NuxtLink: stub('a') },
    '@nuxt/ui/components/Badge.vue': { default: stub('badge') },
    '@nuxt/ui/components/Button.vue': { default: stub('button') },
    '@nuxt/ui/components/Icon.vue': { default: stub('icon') },
    '@nuxt/ui/components/Tooltip.vue': { default: stub('tooltip') },
    '~/store/workspace': await import('../app/store/workspace'),
    '~/utils/node': await import('../app/utils/node'),
    '~/utils/stacks': await import('../app/utils/stacks'),
  })
  const root = node('root')
  const app = renderer.createApp({
    setup: () => () => vue.h(component, { node: useWorkspaceStore().nodes[0]! }),
  })
  app.mount(root)
  apps.push(app)
  return root
}

const mountNodePage = async (nodeID: string) => {
  const route = vue.reactive({ params: { node: nodeID } })
  const component = await loadComponent('../pages/nodes/[node]', {
    pinia: { storeToRefs: vue.toRefs },
    '#app': { useRoute: () => route, useHead: () => {} },
    '#imports': { definePageMeta: () => {} },
    'vue-router': { onBeforeRouteLeave: () => {} },
    '@nuxt/ui/components/Alert.vue': { default: stub('alert') },
    '@nuxt/ui/components/Button.vue': { default: stub('button') },
    '@nuxt/ui/components/Icon.vue': { default: stub('icon') },
    '@nuxt/ui/components/Tooltip.vue': { default: stub('tooltip') },
    '~/components/StatCard.vue': { default: stub('stat') },
    '~/store/workspace': await import('../app/store/workspace'),
    '~/utils/node': await import('../app/utils/node'),
    '~/utils/stacks': await import('../app/utils/stacks'),
  })
  const root = node('root')
  const app = renderer.createApp(component)
  app.mount(root)
  apps.push(app)
  return root
}

describe('Node resource cache', () => {
  test('cache dedup', async () => {
    const calls = deferredFetch()
    const workspace = useWorkspaceStore()
    const first = workspace.loadNodeInfo('node')
    const second = workspace.loadNodeInfo('node')
    expect(calls).toHaveLength(1)
    const response = resourceInfo([resourceContainer('web', '1%', '1GiB')])
    calls[0]!.reply.resolve(Response.json(response))
    const results = await Promise.all([first, second])
    expect(results[0]?.info).toEqual(response)
    expect(results[1]?.info).toEqual(response)
    expect(workspace.nodeInfoCache.node?.info).toEqual(response)
    const cached = await workspace.loadNodeInfo('node')
    expect(cached.info).toEqual(response)
    expect(calls).toHaveLength(1)
  })

  test('cache cleanup', async () => {
    const calls = deferredFetch()
    const workspace = useWorkspaceStore()
    workspace.nodes = [workspaceNode('node')]
    const pending = workspace.loadNodeInfo('node')
    calls[0]!.reply.resolve(Response.json(resourceInfo()))
    await pending
    expect(workspace.nodeInfoCache.node).toBeDefined()

    workspace.nodes = [workspaceNode('node', { paused: true, connection: 'paused' })]
    await vue.nextTick()
    expect(workspace.nodeInfoCache.node).toBeUndefined()

    workspace.nodes = [workspaceNode('node')]
    await vue.nextTick()
    const loading = workspace.loadNodeInfo('node')
    calls[1]!.reply.resolve(Response.json(resourceInfo()))
    await loading
    expect(workspace.nodeInfoCache.node).toBeDefined()
    workspace.nodes = []
    await vue.nextTick()
    expect(workspace.nodeInfoCache.node).toBeUndefined()
  })

  test('cache unsafe id', async () => {
    const calls = deferredFetch()
    const workspace = useWorkspaceStore()
    const pending = workspace.loadNodeInfo('__proto__')
    calls[0]!.reply.resolve(Response.json(resourceInfo()))
    const result = await pending
    expect(result.info?.runtime.name).toBe('host')
    expect(workspace.nodeInfoCache['__proto__']?.info).toEqual(result.info)
    expect(Object.prototype.hasOwnProperty.call(workspace.nodeInfoCache, '__proto__')).toBe(true)
  })

  test('cache sign out', async () => {
    const calls = deferredFetch()
    const workspace = useWorkspaceStore()
    const pending = workspace.loadNodeInfo('node')
    useAuthStore().authenticated = false
    await vue.nextTick()
    calls[0]!.reply.resolve(Response.json(resourceInfo()))
    const result = await pending
    expect(result.aborted).toBe(true)
    expect(workspace.nodeInfoCache.node).toBeUndefined()
  })

  test('cache remount', async () => {
    const calls = deferredFetch()
    const workspace = useWorkspaceStore()
    const info = resourceInfo([resourceContainer('shared', '1%', '1GiB')])
    workspace.nodes = [
      workspaceNode('node', {
        stacks: [
          {
            name: 'app',
            status: 'running',
            loading: false,
            services: [
              {
                name: 'shared',
                service: 'web',
                image: 'nginx',
                state: 'running',
                health: '',
                ports: '',
              },
            ],
          },
        ],
      }),
    ]
    const first = await mountResources()
    expect(calls).toHaveLength(1)
    calls[0]!.reply.resolve(Response.json(info))
    await flush()
    expect(text(first)).toContain('app')
    expect(flatten(first).some((element) => 'time' === element.type)).toBe(true)
    apps.pop()?.unmount()

    const detail = await mountNodePage('node')
    await flush()
    expect(calls).toHaveLength(1)
    expect(text(detail)).toContain('shared')
    apps.pop()?.unmount()

    const second = await mountResources()
    await flush()
    expect(calls).toHaveLength(1)
    expect(text(second)).toContain('app')
  })

  test('loading stack sample', async () => {
    const calls = deferredFetch()
    const workspace = useWorkspaceStore()
    workspace.nodes = [
      workspaceNode('node', {
        stacks: [
          {
            name: 'app',
            status: 'running',
            loading: true,
            services: [
              {
                name: 'web',
                service: 'web',
                image: 'nginx',
                state: 'running',
                health: '',
                ports: '',
              },
            ],
          },
        ],
      }),
    ]
    const root = await mountResources()
    calls[0]!.reply.resolve(Response.json(resourceInfo([resourceContainer('web', '50%', '1GiB')])))
    await flush()
    expect(
      flatten(root)
        .filter((element) => 'a' === element.type)
        .map((element) => element.props.to),
    ).toContain('/stacks/node/app')
    expect(
      flatten(root).some(
        (element) => 'icon' === element.type && 'i-lucide-history' === element.props.name,
      ),
    ).toBe(true)
  })

  test('resource refresh', async () => {
    const requests: Array<string> = []
    globalThis.fetch = Object.assign(
      async (input: string | URL | Request): Promise<Response> => {
        const url = String(input)
        requests.push(url)
        return url.includes('/fail/')
          ? Response.json({ error: { code: 503, message: 'failure-sentinel' } }, { status: 503 })
          : Response.json(resourceInfo())
      },
      { preconnect: originalFetch.preconnect },
    )
    const workspace = useWorkspaceStore()
    workspace.nodes = [
      workspaceNode('ok', { name: 'Office' }),
      workspaceNode('fail', { name: 'Garage' }),
      workspaceNode('offline', { name: 'Offline', connection: 'reconnecting' }),
      workspaceNode('paused', { name: 'Paused', paused: true, connection: 'paused' }),
    ]
    const failures = await workspace.refreshNodeInfo()
    expect(requests).toHaveLength(2)
    expect(requests).toContain('/api/nodes/ok/info')
    expect(requests).toContain('/api/nodes/fail/info')
    expect(failures).toHaveLength(1)
    expect(failures[0]).toContain('Garage')
    expect(failures[0]).toContain('failure-sentinel')
  })
})
