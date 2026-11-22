import { afterEach, beforeEach, describe, expect, jest, mock, test } from 'bun:test'
import { createPinia, disposePinia, setActivePinia } from 'pinia'
import { computed, effectScope, nextTick } from 'vue'
import type { Pinia } from 'pinia'
import type { EffectScope } from 'vue'
import type { StackEvent } from '../app/types'
import { addToast, navigate } from './mocks/nuxt'

const { useWorkspaceStream } = await import('../app/composables/useWorkspaceStream')
const { useRealtimeStore } = await import('../app/store/realtime')
const { useWorkspaceStore } = await import('../app/store/workspace')
const { useAuthStore } = await import('../app/store/auth')
const { request } = await import('../app/utils')

class TestSocket {
  static OPEN = 1
  static instances: Array<TestSocket> = []
  onopen: (() => void) | null = null
  onmessage: ((message: MessageEvent<unknown>) => void) | null = null
  onerror: (() => void) | null = null
  onclose: (() => void) | null = null
  readyState = 0
  closed = false
  sent: Array<Record<string, unknown>> = []

  constructor(readonly url: string) {
    TestSocket.instances.push(this)
  }

  open() {
    this.readyState = TestSocket.OPEN
    this.onopen?.()
  }

  close() {
    this.closed = true
    this.readyState = 3
  }

  send(data: string) {
    this.sent.push(JSON.parse(data) as Record<string, unknown>)
  }

  message(value: unknown) {
    this.onmessage?.(new MessageEvent('message', { data: JSON.stringify(value) }))
  }
}

const originalSocket = Object.getOwnPropertyDescriptor(globalThis, 'WebSocket')
const originalFetch = globalThis.fetch
const originalLocation = Object.getOwnPropertyDescriptor(globalThis, 'location')
const originalStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
const readStorage = mock((_key: string): string | null => null)
const writeStorage = mock((_key: string, _value: string) => {})
let scope: EffectScope
let pinia: Pinia
let requests: Array<string>
const callbacks = () => ({
  onMessage: mock(() => {}),
  onDisconnect: mock(() => {}),
  onRejected: mock(() => {}),
})
const flush = async () => {
  for (let index = 0; index < 10; index++) {
    await Promise.resolve()
  }
}

beforeEach(() => {
  jest.useFakeTimers()
  pinia = createPinia()
  setActivePinia(pinia)
  useAuthStore().authenticated = true
  scope = effectScope()
  TestSocket.instances = []
  requests = []
  addToast.mockClear()
  navigate.mockClear()
  readStorage.mockClear()
  writeStorage.mockClear()
  Object.defineProperty(globalThis, 'localStorage', {
    value: { getItem: readStorage, setItem: writeStorage, removeItem: mock(() => {}) },
    configurable: true,
  })
  Object.defineProperty(globalThis, 'WebSocket', { value: TestSocket, configurable: true })
  Object.defineProperty(globalThis, 'location', {
    value: { origin: 'https://dash.example' },
    configurable: true,
  })
  globalThis.fetch = Object.assign(
    async (input: string | URL | Request): Promise<Response> => {
      const path = String(input)
      requests.push(path)
      if ('/api/nodes' === path) {
        return Response.json({ nodes: [{ id: 'n', name: 'Office', public_host: '' }] })
      }
      throw new Error(`Unexpected request: ${path}`)
    },
    { preconnect: originalFetch.preconnect },
  )
})

afterEach(() => {
  scope.stop()
  disposePinia(pinia)
  setActivePinia(undefined)
  globalThis.fetch = originalFetch
  for (const [key, descriptor] of [
    ['WebSocket', originalSocket],
    ['location', originalLocation],
    ['localStorage', originalStorage],
  ] as const) {
    if (descriptor) {
      Object.defineProperty(globalThis, key, descriptor)
    } else {
      Reflect.deleteProperty(globalThis, key)
    }
  }
  jest.useRealTimers()
})

test('update checks', async () => {
  const workspace = useWorkspaceStore()
  workspace.nodes = ['Kitchen', 'Garage', 'Maintenance'].map((name, index) => ({
    id: `private-node-${index}`,
    name,
    public_host: '',
    stacks: [],
    health: null,
    connection: 2 === index ? 'paused' : 'connected',
    paused: 2 === index,
    error: '',
    loading: false,
  }))
  let fail = false
  const checked: Array<string> = []
  const message = 'node-unavailable-sentinel'
  globalThis.fetch = Object.assign(
    async (input: string | URL | Request): Promise<Response> => {
      checked.push(String(input))
      return fail && String(input).includes('private-node-1')
        ? Response.json({ error: { code: 503, message } }, { status: 503 })
        : Response.json({ status: 'checking' }, { status: 202 })
    },
    { preconnect: originalFetch.preconnect },
  )
  await workspace.checkImages()
  expect(checked).toEqual(
    workspace.nodes
      .filter((node) => !node.paused)
      .map((node) => `/api/nodes/${node.id}/images/check`),
  )
  expect(addToast).toHaveBeenLastCalledWith(expect.objectContaining({ color: 'info' }))
  fail = true
  await workspace.checkImages()
  expect(addToast).toHaveBeenLastCalledWith(expect.objectContaining({ color: 'warning' }))
  const notification = addToast.mock.calls.at(-1)![0]
  expect(notification.description).toContain(workspace.nodes[1]!.name)
  expect(notification.description).toContain(message)
  for (const node of workspace.nodes) {
    expect(notification.description).not.toContain(node.id)
  }
  const previous = [...checked]
  await workspace.checkImages('private-node-2')
  await expect(workspace.runAction('private-node-2', 'app', 'start')).rejects.toThrow()
  expect(checked).toEqual(previous)
})

describe('Shared connection', () => {
  test('paused state', async () => {
    let paused = true
    globalThis.fetch = Object.assign(
      async (): Promise<Response> =>
        Response.json({
          nodes: [{ id: 'n', name: 'Node', public_host: '', paused }],
        }),
      { preconnect: originalFetch.preconnect },
    )
    const workspace = useWorkspaceStore()
    await workspace.load()
    expect(workspace.nodes[0]?.connection).toBe('paused')
    expect(workspace.nodes[0]?.loading).toBe(false)
    paused = false
    await workspace.load()
    expect(workspace.nodes[0]?.connection).toBe('loading')
    const socket = TestSocket.instances[0]!
    socket.open()
    socket.message({
      type: 'event',
      event: 'node.snapshot',
      payload: {
        node: 'n',
        health: { status: 'ok', version: '1', api_version: 1 },
        stacks: [{ name: 'app', status: 'draft', services: [], loading: false }],
      },
    })
    expect(workspace.nodes[0]?.stacks).toHaveLength(1)
    socket.message({ type: 'event', event: 'node.state', payload: { node: 'n', state: 'paused' } })
    expect(workspace.nodes[0]?.connection).toBe('paused')
    expect(workspace.nodes[0]?.stacks).toEqual([])
    expect(workspace.nodes[0]?.health).toBeNull()
    expect(workspace.nodes[0]?.error).toBe('')
    socket.onerror?.()
    await nextTick()
    expect(workspace.nodes[0]?.connection).toBe('paused')
  })

  test('node order', async () => {
    let fail = true
    globalThis.fetch = Object.assign(
      async (input: string | URL | Request, options?: RequestInit): Promise<Response> => {
        const path = String(input)
        if ('/api/nodes' === path) {
          return Response.json({
            nodes: [
              { id: 'z', name: 'Zulu', public_host: '' },
              { id: 'a', name: 'Alpha', public_host: '' },
            ],
          })
        }
        if ('/api/settings/node-order' === path) {
          expect(options?.method).toBe('PUT')
          expect(options?.body).toBe(JSON.stringify({ nodes: ['a', 'z'] }))
          return fail
            ? Response.json({ error: { code: 500, message: 'save-failure' } }, { status: 500 })
            : Response.json({ status: 'ok' })
        }
        throw new Error(`Unexpected request: ${path}`)
      },
      { preconnect: originalFetch.preconnect },
    )
    const workspace = useWorkspaceStore()
    await workspace.load()
    TestSocket.instances[0]!.open()
    await workspace.moveNode('a', 'z')
    expect(workspace.nodes.map((node) => node.id)).toEqual(['z', 'a'])
    expect(workspace.savingNodeOrder).toBe(false)
    fail = false
    await workspace.moveNode('a', 'z')
    expect(workspace.nodes.map((node) => node.id)).toEqual(['a', 'z'])
    expect(workspace.savingNodeOrder).toBe(false)
    expect(TestSocket.instances).toHaveLength(1)
    expect(readStorage).not.toHaveBeenCalled()
    expect(writeStorage).not.toHaveBeenCalled()
  })

  test('all readers', () => {
    const realtime = useRealtimeStore()
    const first = callbacks()
    const second = callbacks()
    const logs = callbacks()
    const shell = callbacks()
    realtime.subscribe({ type: 'runtime' }, first)
    realtime.subscribe({ type: 'runtime' }, second)
    const logReader = realtime.subscribe({ type: 'logs', node: 'n', resource: 'app' }, logs)
    const terminal = realtime.subscribe({ type: 'shell', node: 'n', shell: 'sh' }, shell)
    expect(TestSocket.instances).toHaveLength(1)
    const socket = TestSocket.instances[0]!
    expect(socket.url).toBe('wss://dash.example/api/events')
    socket.open()
    expect(socket.sent).toEqual([
      { type: 'logs.attach', stream: 'reader-3', node: 'n', resource: 'app' },
      { type: 'shell.attach', stream: 'reader-4', node: 'n', shell: 'sh' },
    ])
    socket.message({
      type: 'event',
      event: 'stack.output',
      payload: { node: 'n', name: 'app', output: '\x1b[31mhéllo 🐚' },
    })
    expect(first.onMessage).toHaveBeenCalledTimes(1)
    expect(second.onMessage).toHaveBeenCalledTimes(1)
    expect(logs.onMessage).not.toHaveBeenCalled()
    socket.message({ type: 'event', stream: 'reader-3', event: 'log', payload: { message: 'log' } })
    socket.message({
      type: 'shell.event',
      stream: 'reader-4',
      shell: 'sh',
      event: 'operation.output',
      payload: { id: 'sh', output: '$ ' },
    })
    expect(logs.onMessage).toHaveBeenCalledTimes(1)
    expect(shell.onMessage).toHaveBeenCalledTimes(1)
    expect(terminal.send({ type: 'shell.input', data: 'ls\r' })).toBe(true)
    expect(socket.sent.at(-1)).toEqual({ type: 'shell.input', stream: 'reader-4', data: 'ls\r' })
    logReader.stop()
    expect(socket.sent.at(-1)).toEqual({ type: 'stream.detach', stream: 'reader-3' })
    expect(requests).toEqual([])
  })

  test('stats reader', () => {
    const realtime = useRealtimeStore()
    const runtime = callbacks()
    const stats = callbacks()
    const logs = callbacks()
    realtime.subscribe({ type: 'runtime' }, runtime)
    const stream = scope.run(() => useWorkspaceStream(stats.onMessage, stats.onDisconnect))!
    stream.start({ type: 'stats', node: 'n', resource: 'app' })
    realtime.subscribe({ type: 'logs', node: 'n', resource: 'app' }, logs)
    const socket = TestSocket.instances[0]!
    socket.open()
    const attach = { type: 'stats.attach', stream: 'reader-2', node: 'n', resource: 'app' }
    expect(socket.sent).toContainEqual(attach)
    socket.message({
      type: 'event',
      stream: 'reader-2',
      event: 'stack.stats',
      payload: { containers: [] },
    })
    expect(stats.onMessage).toHaveBeenCalledTimes(1)
    expect(runtime.onMessage).not.toHaveBeenCalled()
    expect(logs.onMessage).not.toHaveBeenCalled()
    socket.onerror?.()
    expect(stats.onDisconnect).toHaveBeenCalledTimes(1)
    jest.advanceTimersByTime(1000)
    const replacement = TestSocket.instances[1]!
    replacement.open()
    expect(replacement.sent).toEqual([attach])
    scope.stop()
    expect(replacement.sent.at(-1)).toEqual({ type: 'stream.detach', stream: 'reader-2' })
    replacement.message({
      type: 'event',
      stream: 'reader-2',
      event: 'stack.stats',
      payload: { containers: [] },
    })
    expect(stats.onMessage).toHaveBeenCalledTimes(1)
  })

  test('stats retry', () => {
    const realtime = useRealtimeStore()
    const stats = callbacks()
    const logs = callbacks()
    realtime.subscribe({ type: 'stats', node: 'n', resource: 'app' }, stats)
    realtime.subscribe({ type: 'logs', node: 'n', resource: 'app' }, logs)
    const socket = TestSocket.instances[0]!
    socket.open()
    const attach = { type: 'stats.attach', stream: 'reader-1', node: 'n', resource: 'app' }
    const statsAttachments = () =>
      socket.sent.filter((message) => 'stats.attach' === message.type).length
    expect(socket.sent).toContainEqual(attach)

    socket.message({ type: 'error', stream: 'reader-1', status: 503, error: 'unavailable' })
    expect(stats.onDisconnect).toHaveBeenCalledTimes(1)
    expect(stats.onRejected).not.toHaveBeenCalled()
    expect(socket.closed).toBe(false)
    jest.advanceTimersByTime(1000)
    expect(statsAttachments()).toBe(2)

    socket.message({ type: 'error', stream: 'reader-1', status: 429, error: 'busy' })
    expect(stats.onDisconnect).toHaveBeenCalledTimes(2)
    jest.advanceTimersByTime(1999)
    expect(statsAttachments()).toBe(2)
    jest.advanceTimersByTime(1)
    expect(statsAttachments()).toBe(3)

    socket.message({
      type: 'event',
      stream: 'reader-1',
      event: 'stack.stats',
      payload: { containers: [] },
    })
    expect(stats.onMessage).toHaveBeenCalledTimes(1)
    expect(logs.onMessage).not.toHaveBeenCalled()
    expect(statsAttachments()).toBe(3)
  })

  test('stats cache methods', () => {
    const realtime = useRealtimeStore()
    const current = computed(() => realtime.stackStatsSample('n', 'app')?.receivedAt ?? null)
    expect(current.value).toBeNull()
    const sample = realtime.rememberStackStats('n', 'app', [], 1000)
    expect(current.value).toBe(1000)
    realtime.rememberStackStats('n', 'worker', [], 2000)
    realtime.rememberStackStats('m', 'app', [], 3000)

    expect(realtime.stackStatsSample('n', 'app')).toEqual(sample)
    expect(realtime.stackStatsSample('n', 'missing')).toBeNull()
    realtime.forgetStackStats('n', 'app')
    expect(current.value).toBeNull()
    expect(realtime.stackStatsSample('n', 'app')).toBeNull()
    expect(realtime.stackStatsSample('n', 'worker')?.receivedAt).toBe(2000)
    realtime.forgetNodeStackStats('n')
    expect(realtime.stackStatsSample('n', 'worker')).toBeNull()
    expect(realtime.stackStatsSample('m', 'app')?.receivedAt).toBe(3000)
    realtime.retainStackStats(['m'])
    expect(realtime.stackStatsSample('m', 'app')).not.toBeNull()
    realtime.retainStackStats([])
    expect(realtime.stackStatsSample('m', 'app')).toBeNull()
    realtime.rememberStackStats('m', 'app', [], 1000)
    realtime.rememberStackStats('m', 'worker', [], 1000)
    realtime.retainNodeStackStats('m', ['app'])
    expect(realtime.stackStatsSample('m', 'app')).not.toBeNull()
    expect(realtime.stackStatsSample('m', 'worker')).toBeNull()
  })

  test('stats cache workspace events', async () => {
    let nodes = [
      { id: 'n', name: 'Node', public_host: '' },
      { id: 'm', name: 'Other', public_host: '' },
    ]
    globalThis.fetch = Object.assign(
      async (input: string | URL | Request): Promise<Response> => {
        const path = String(input)
        if ('/api/nodes' === path) {
          return Response.json({ nodes })
        }
        throw new Error(`Unexpected request: ${path}`)
      },
      { preconnect: originalFetch.preconnect },
    )
    const workspace = useWorkspaceStore()
    await workspace.load()
    const realtime = useRealtimeStore()
    const socket = TestSocket.instances[0]!
    socket.open()
    realtime.rememberStackStats('n', 'app', [], 1000)
    realtime.rememberStackStats('n', 'worker', [], 1000)
    realtime.rememberStackStats('m', 'app', [], 1000)

    socket.message({
      type: 'event',
      event: 'stack.runtime',
      payload: { node: 'n', name: 'app', status: 'running', services: [] },
    })
    expect(realtime.stackStatsSample('n', 'app')).not.toBeNull()
    socket.message({
      type: 'event',
      event: 'stack.runtime',
      payload: { node: 'n', name: 'app', status: 'stopped', services: [] },
    })
    expect(realtime.stackStatsSample('n', 'app')).toBeNull()
    expect(realtime.stackStatsSample('n', 'worker')).not.toBeNull()

    realtime.rememberStackStats('n', 'app', [], 1000)
    realtime.rememberStackStats('n', 'old', [], 1000)
    socket.message({
      type: 'event',
      event: 'node.snapshot',
      payload: {
        node: 'n',
        health: { status: 'ok', version: '1', api_version: 1 },
        stacks: [
          { name: 'app', status: 'running', services: [], loading: true },
          { name: 'old', status: 'stopped', services: [], loading: false },
        ],
      },
    })
    expect(realtime.stackStatsSample('n', 'app')).not.toBeNull()
    expect(realtime.stackStatsSample('n', 'worker')).toBeNull()
    expect(realtime.stackStatsSample('n', 'old')).toBeNull()

    socket.message({
      type: 'event',
      event: 'stack.removed',
      payload: { node: 'n', name: 'app' },
    })
    expect(realtime.stackStatsSample('n', 'app')).toBeNull()

    realtime.rememberStackStats('n', 'worker', [], 1000)
    socket.message({ type: 'event', event: 'node.state', payload: { node: 'n', state: 'paused' } })
    expect(realtime.stackStatsSample('n', 'worker')).toBeNull()
    expect(realtime.stackStatsSample('m', 'app')).not.toBeNull()

    realtime.rememberStackStats('n', 'app', [], 1000)
    nodes = [{ id: 'm', name: 'Other', public_host: '' }]
    await workspace.load()
    expect(realtime.stackStatsSample('n', 'app')).toBeNull()
    expect(realtime.stackStatsSample('m', 'app')).not.toBeNull()

    nodes = [
      { id: 'm', name: 'Other', public_host: 'renamed.example' },
      { id: 'n', name: 'Node', public_host: '' },
    ]
    await workspace.load()
    expect(realtime.stackStatsSample('m', 'app')).not.toBeNull()
  })

  test('stats cache sign out', async () => {
    const realtime = useRealtimeStore()
    realtime.subscribe({ type: 'runtime' }, callbacks())
    realtime.rememberStackStats('n', 'app', [], 1000)
    useAuthStore().authenticated = false
    await nextTick()
    expect(realtime.stackStatsSample('n', 'app')).toBeNull()
  })

  test('logs transient error', () => {
    const realtime = useRealtimeStore()
    const logs = callbacks()
    realtime.subscribe({ type: 'logs', node: 'n', resource: 'app' }, logs)
    const socket = TestSocket.instances[0]!
    socket.open()
    socket.message({ type: 'error', stream: 'reader-1', status: 503, error: 'unavailable' })
    expect(logs.onDisconnect).toHaveBeenCalledTimes(1)
    expect(logs.onRejected).not.toHaveBeenCalled()
    expect(socket.closed).toBe(false)
    jest.advanceTimersByTime(60000)
    expect(socket.sent.filter((message) => 'logs.attach' === message.type)).toHaveLength(1)
  })

  test('global transient error', () => {
    const realtime = useRealtimeStore()
    const runtime = callbacks()
    realtime.subscribe({ type: 'runtime' }, runtime)
    const socket = TestSocket.instances[0]!
    socket.open()
    socket.message({ type: 'error', status: 503, error: 'unavailable' })
    expect(runtime.onDisconnect).toHaveBeenCalledTimes(1)
    expect(runtime.onRejected).not.toHaveBeenCalled()
    expect(socket.closed).toBe(true)
    expect(realtime.error).not.toBe('')
    jest.advanceTimersByTime(1000)
    const replacement = TestSocket.instances[1]!
    replacement.open()
    expect(realtime.error).toBe('')
    replacement.message({ type: 'event', event: 'nodes.changed', payload: {} })
    expect(runtime.onMessage).toHaveBeenCalledTimes(1)
  })

  test('healthy snapshot', async () => {
    let healthy = true
    globalThis.fetch = Object.assign(
      async (input: string | URL | Request): Promise<Response> => {
        const path = String(input)
        requests.push(path)
        if ('/api/nodes' !== path) {
          throw new Error(`Unexpected request: ${path}`)
        }
        return healthy
          ? Response.json({ nodes: [{ id: 'n', name: 'Office', public_host: '' }] })
          : Response.json({ error: { code: 503, message: 'unavailable' } }, { status: 503 })
      },
      { preconnect: originalFetch.preconnect },
    )
    const workspace = useWorkspaceStore()
    await workspace.load()
    const socket = TestSocket.instances[0]!
    socket.open()
    const snapshot = {
      type: 'event',
      event: 'node.snapshot',
      payload: {
        node: 'n',
        health: { status: 'ok', version: '1', api_version: 1 },
        stacks: [],
      },
    }

    healthy = false
    await workspace.load()
    expect(workspace.error).not.toBe('')
    socket.message(snapshot)
    expect(workspace.error).toBe('')
    expect(workspace.nodes[0]?.connection).toBe('loading')
    socket.message({
      type: 'event',
      event: 'node.state',
      payload: { node: 'n', state: 'connected' },
    })
    expect(workspace.nodes[0]?.connection).toBe('connected')

    socket.onerror?.()
    expect(workspace.streamError).not.toBe('')
    jest.advanceTimersByTime(1000)
    const replacement = TestSocket.instances[1]!
    replacement.open()
    replacement.message(snapshot)
    expect(workspace.streamError).toBe('')
    expect(workspace.nodes[0]?.connection).toBe('reconnecting')
    replacement.message({
      type: 'event',
      event: 'node.state',
      payload: { node: 'n', state: 'unavailable' },
    })
    expect(workspace.nodes[0]?.connection).toBe('unavailable')
  })

  test('navigation', async () => {
    const previousMessage = mock(() => {})
    const nextMessage = mock(() => {})
    const first = scope.run(() => useWorkspaceStream(previousMessage))!
    first.start({ type: 'runtime' })
    TestSocket.instances[0]!.open()
    scope.stop()
    scope = effectScope()
    const next = scope.run(() => useWorkspaceStream(nextMessage))!
    next.start({ type: 'runtime' })
    next.start({ type: 'runtime' })
    await useWorkspaceStore().load()
    expect(TestSocket.instances).toHaveLength(1)
    expect(requests).toEqual(['/api/nodes'])
    expect(TestSocket.instances[0]!.sent).toEqual([{ type: 'workspace.sync' }])
    TestSocket.instances[0]!.message({
      type: 'event',
      event: 'stack.output',
      payload: { node: 'n', name: 'app', output: 'future' },
    })
    expect(previousMessage).not.toHaveBeenCalled()
    expect(nextMessage).toHaveBeenCalledTimes(1)
  })

  test('reconnect', () => {
    const realtime = useRealtimeStore()
    const runtime = callbacks()
    const logs = callbacks()
    const shell = callbacks()
    realtime.subscribe({ type: 'runtime' }, runtime)
    realtime.subscribe({ type: 'logs', node: 'n', resource: 'app' }, logs)
    const terminal = realtime.subscribe({ type: 'shell', node: 'n', shell: 'sh' }, shell)
    const socket = TestSocket.instances[0]!
    socket.open()
    terminal.send({ type: 'shell.input', data: 'not replayed' })
    socket.onerror?.()
    expect(terminal.send({ type: 'shell.input', data: 'not queued' })).toBe(false)
    expect(logs.onDisconnect).toHaveBeenCalledTimes(1)
    expect(shell.onDisconnect).toHaveBeenCalledTimes(1)
    jest.advanceTimersByTime(1000)
    const replacement = TestSocket.instances[1]!
    replacement.open()
    expect(replacement.sent).toEqual([])
    replacement.message({
      type: 'event',
      event: 'stack.output',
      payload: { node: 'n', name: 'app', output: 'future only' },
    })
    expect(runtime.onMessage).toHaveBeenCalledTimes(1)
    expect(logs.onMessage).not.toHaveBeenCalled()
    expect(shell.onMessage).not.toHaveBeenCalled()
    expect(requests).toEqual([])
  })

  test('failed handshake', () => {
    const realtime = useRealtimeStore()
    realtime.subscribe({ type: 'runtime' }, callbacks())
    TestSocket.instances[0]!.onerror?.()
    expect(realtime.error).not.toBe('')
    expect(useAuthStore().authenticated).toBe(true)
    jest.advanceTimersByTime(1000)
    expect(TestSocket.instances).toHaveLength(2)
    TestSocket.instances[1]!.onerror?.()
    jest.advanceTimersByTime(1999)
    expect(TestSocket.instances).toHaveLength(2)
    jest.advanceTimersByTime(1)
    expect(TestSocket.instances).toHaveLength(3)
    expect(requests).toEqual([])
  })

  test('resource rejection', () => {
    const realtime = useRealtimeStore()
    const runtime = callbacks()
    const logs = callbacks()
    realtime.subscribe({ type: 'runtime' }, runtime)
    realtime.subscribe({ type: 'logs', node: 'n', resource: 'app' }, logs)
    const socket = TestSocket.instances[0]!
    socket.open()
    socket.message({ type: 'error', stream: 'reader-2', status: 404, error: 'unavailable' })
    expect(logs.onRejected).toHaveBeenCalledWith(404)
    socket.message({ type: 'event', event: 'nodes.changed', payload: {} })
    expect(runtime.onMessage).toHaveBeenCalledTimes(1)
    expect(socket.closed).toBe(false)
    expect(useAuthStore().authenticated).toBe(true)
  })

  test('session rejection', async () => {
    const realtime = useRealtimeStore()
    const runtime = callbacks()
    realtime.subscribe({ type: 'runtime' }, runtime)
    const socket = TestSocket.instances[0]!
    socket.open()
    socket.message({ type: 'error', status: 401, error: 'unauthorized' })
    await nextTick()
    expect(runtime.onRejected).toHaveBeenCalledWith(401)
    expect(useAuthStore().authenticated).toBe(false)
    expect(navigate).toHaveBeenCalledWith('/login', { replace: true })
    expect(socket.closed).toBe(true)
    jest.advanceTimersByTime(60000)
    expect(TestSocket.instances).toHaveLength(1)
  })

  test('paused request', async () => {
    const realtime = useRealtimeStore()
    const runtime = callbacks()
    const logs = callbacks()
    realtime.subscribe({ type: 'runtime' }, runtime)
    realtime.subscribe({ type: 'logs', node: 'n', resource: 'app' }, logs)
    const socket = TestSocket.instances[0]!
    socket.open()
    const staleMessage = socket.onmessage
    const staleOpen = socket.onopen
    const staleError = socket.onerror
    const response = Promise.withResolvers<Response>()
    globalThis.fetch = Object.assign(async (): Promise<Response> => response.promise, {
      preconnect: originalFetch.preconnect,
    })
    realtime.pause()
    const saving = request('/api/settings/account', { method: 'POST' }).finally(realtime.resume)
    expect(socket.closed).toBe(true)
    expect(logs.onDisconnect).toHaveBeenCalledTimes(1)
    staleMessage?.(
      new MessageEvent('message', {
        data: JSON.stringify({ type: 'error', status: 401, error: 'old' }),
      }),
    )
    staleOpen?.()
    staleError?.()
    const shell = callbacks()
    const terminal = realtime.subscribe({ type: 'shell', node: 'n', shell: 'new' }, shell)
    expect(terminal.send({ type: 'shell.input', data: 'not queued' })).toBe(false)
    expect(shell.onDisconnect).toHaveBeenCalledTimes(1)
    jest.advanceTimersByTime(60000)
    expect(TestSocket.instances).toHaveLength(1)
    expect(useAuthStore().authenticated).toBe(true)
    response.resolve(Response.json({ status: 'ok' }))
    await saving
    const replacement = TestSocket.instances[1]!
    replacement.open()
    expect(replacement.sent).toEqual([])
    replacement.message({ type: 'event', event: 'nodes.changed', payload: {} })
    expect(runtime.onMessage).toHaveBeenCalledTimes(1)
    expect(logs.onMessage).not.toHaveBeenCalled()
  })

  test('disposal', () => {
    const realtime = useRealtimeStore()
    realtime.subscribe({ type: 'runtime' }, callbacks())
    const socket = TestSocket.instances[0]!
    socket.open()
    realtime.pause()
    realtime.stop()
    realtime.resume()
    realtime.$dispose()
    jest.advanceTimersByTime(60000)
    expect(socket.closed).toBe(true)
    expect(TestSocket.instances).toHaveLength(1)
  })

  test('reset and disposal', () => {
    const realtime = useRealtimeStore()
    realtime.subscribe({ type: 'runtime' }, callbacks())
    const socket = TestSocket.instances[0]!
    socket.open()
    realtime.stop()
    expect(socket.closed).toBe(true)
    realtime.subscribe({ type: 'runtime' }, callbacks())
    const next = TestSocket.instances[1]!
    next.open()
    realtime.$dispose()
    expect(next.closed).toBe(true)
    jest.advanceTimersByTime(60000)
    expect(TestSocket.instances).toHaveLength(2)
  })
})

describe('Workspace bootstrap', () => {
  test('transient retry', async () => {
    let attempts = 0
    globalThis.fetch = Object.assign(
      async (input: string | URL | Request): Promise<Response> => {
        const path = String(input)
        requests.push(path)
        if ('/api/nodes' !== path) {
          throw new Error(`Unexpected request: ${path}`)
        }
        attempts++
        if (1 === attempts) {
          throw new TypeError('fetch failed')
        }
        if (2 === attempts) {
          return Response.json({ error: { code: 503, message: 'unavailable' } }, { status: 503 })
        }
        return Response.json({ nodes: [{ id: 'n', name: 'Office', public_host: '' }] })
      },
      { preconnect: originalFetch.preconnect },
    )
    const workspace = useWorkspaceStore()
    await workspace.load()
    expect(workspace.error).not.toBe('')
    expect(workspace.ready).toBe(true)
    expect(workspace.nodes).toEqual([])
    expect(addToast).not.toHaveBeenCalled()

    jest.advanceTimersByTime(1000)
    await flush()
    expect(requests).toHaveLength(2)
    expect(addToast).not.toHaveBeenCalled()
    expect(workspace.error).not.toBe('')

    jest.advanceTimersByTime(2000)
    await flush()
    expect(requests).toHaveLength(3)
    expect(workspace.nodes.map((node) => node.id)).toEqual(['n'])
    expect(workspace.error).toBe('')
    expect(addToast).not.toHaveBeenCalled()
    expect(TestSocket.instances).toHaveLength(1)

    jest.advanceTimersByTime(60000)
    await flush()
    expect(requests).toHaveLength(3)
  })

  test('stale data', async () => {
    let healthy = true
    globalThis.fetch = Object.assign(
      async (input: string | URL | Request): Promise<Response> => {
        const path = String(input)
        requests.push(path)
        if ('/api/nodes' !== path) {
          throw new Error(`Unexpected request: ${path}`)
        }
        return healthy
          ? Response.json({ nodes: [{ id: 'n', name: 'Office', public_host: '' }] })
          : Response.json({ error: { code: 502, message: 'unavailable' } }, { status: 502 })
      },
      { preconnect: originalFetch.preconnect },
    )
    const workspace = useWorkspaceStore()
    await workspace.load()
    workspace.nodes[0]!.stacks = [{ name: 'app', status: 'running', services: [], loading: false }]
    workspace.nodes[0]!.connection = 'connected'
    healthy = false
    await workspace.load()
    expect(workspace.nodes[0]!.stacks).toHaveLength(1)
    expect(workspace.nodes[0]!.connection).toBe('connected')
    expect(workspace.error).not.toBe('')

    healthy = true
    jest.advanceTimersByTime(1000)
    await flush()
    expect(workspace.nodes[0]!.stacks).toHaveLength(1)
    expect(workspace.error).toBe('')
  })

  test('permanent failure', async () => {
    globalThis.fetch = Object.assign(
      async (input: string | URL | Request): Promise<Response> => {
        const path = String(input)
        requests.push(path)
        return '/api/nodes' === path
          ? Response.json({ error: { code: 403, message: 'forbidden' } }, { status: 403 })
          : Response.json({ nodes: [] })
      },
      { preconnect: originalFetch.preconnect },
    )
    const workspace = useWorkspaceStore()
    await workspace.load()
    expect(workspace.error).not.toBe('')
    expect(useAuthStore().authenticated).toBe(true)
    jest.advanceTimersByTime(60000)
    await flush()
    expect(requests).toHaveLength(1)
    expect(addToast).not.toHaveBeenCalled()
  })

  test('unauthorized failure', async () => {
    globalThis.fetch = Object.assign(
      async (input: string | URL | Request): Promise<Response> => {
        const path = String(input)
        requests.push(path)
        return '/api/nodes' === path
          ? Response.json({ error: { code: 401, message: 'unauthorized' } }, { status: 401 })
          : Response.json({ nodes: [] })
      },
      { preconnect: originalFetch.preconnect },
    )
    const workspace = useWorkspaceStore()
    await workspace.load()
    await nextTick()
    expect(useAuthStore().authenticated).toBe(false)
    jest.advanceTimersByTime(60000)
    await flush()
    expect(requests).toHaveLength(1)
  })

  test('logout cleanup', async () => {
    globalThis.fetch = Object.assign(
      async (input: string | URL | Request): Promise<Response> => {
        const path = String(input)
        requests.push(path)
        return '/api/nodes' === path
          ? Response.json({ error: { code: 503, message: 'unavailable' } }, { status: 503 })
          : Response.json({ nodes: [] })
      },
      { preconnect: originalFetch.preconnect },
    )
    const workspace = useWorkspaceStore()
    await workspace.load()
    expect(requests).toHaveLength(1)
    expect(addToast).not.toHaveBeenCalled()

    useAuthStore().invalidate()
    await nextTick()
    jest.advanceTimersByTime(60000)
    await flush()
    expect(requests).toHaveLength(1)
    expect(workspace.nodes).toEqual([])
  })

  test('dispose cleanup', async () => {
    globalThis.fetch = Object.assign(
      async (input: string | URL | Request): Promise<Response> => {
        const path = String(input)
        requests.push(path)
        return '/api/nodes' === path
          ? Response.json({ error: { code: 503, message: 'unavailable' } }, { status: 503 })
          : Response.json({ nodes: [] })
      },
      { preconnect: originalFetch.preconnect },
    )
    const workspace = useWorkspaceStore()
    await workspace.load()
    expect(requests).toHaveLength(1)

    workspace.$dispose()
    jest.advanceTimersByTime(60000)
    await flush()
    expect(requests).toHaveLength(1)
  })
})

describe('Live stack actions', () => {
  test('forbidden workspace', async () => {
    const workspace = useWorkspaceStore()
    await workspace.load()
    globalThis.fetch = Object.assign(
      async (): Promise<Response> =>
        Response.json({ error: { code: 403, message: 'forbidden' } }, { status: 403 }),
      { preconnect: originalFetch.preconnect },
    )
    await workspace.load()
    expect(workspace.error).not.toBe('')
    expect(workspace.nodes[0]?.connection).toBe('unavailable')
    expect(useAuthStore().authenticated).toBe(true)
  })

  test('broadcast without retention', async () => {
    const workspace = useWorkspaceStore()
    await workspace.load()
    workspace.nodes[0]!.stacks = [{ name: 'app', status: 'running', services: [], loading: false }]
    const first = mock((_event: StackEvent) => {})
    const second = mock((_event: StackEvent) => {})
    const release = workspace.onStackEvent(first)
    workspace.onStackEvent(second)
    const socket = TestSocket.instances[0]!
    socket.open()
    const emit = (event: StackEvent) => {
      const { event: name, ...payload } = event
      socket.message({ type: 'event', event: name, payload })
    }
    emit({ event: 'stack.action', node: 'n', name: 'app', action: 'update', phase: 'start' })
    expect(workspace.nodes[0]!.stacks[0]!.busy).toBe(true)
    const output: StackEvent = {
      event: 'stack.output',
      node: 'n',
      name: 'app',
      output: '\x1b[31mnative failure\x1b[0m\r\n',
    }
    emit(output)
    expect(first).toHaveBeenLastCalledWith(output)
    expect(second).toHaveBeenLastCalledWith(output)
    const cause = 'native-compose-failure-sentinel'
    emit({
      event: 'stack.action',
      node: 'n',
      name: 'app',
      action: 'update',
      phase: 'exit',
      exit_code: 1,
      message: cause,
    })
    expect(workspace.nodes[0]!.stacks[0]!.busy).toBe(false)
    expect(addToast).toHaveBeenCalledTimes(1)
    const toast = addToast.mock.calls[0]![0]
    for (const detail of ['Office', 'app', 'update', cause]) {
      expect(toast.description).toContain(detail)
    }
    const future = mock((_event: StackEvent) => {})
    release()
    workspace.onStackEvent(future)
    expect(future).not.toHaveBeenCalled()
    emit(output)
    expect(first).toHaveBeenCalledTimes(3)
    expect(second).toHaveBeenCalledTimes(4)
    expect(future).toHaveBeenCalledTimes(1)
    expect(JSON.stringify(workspace.$state)).not.toContain(output.output)
    await workspace.load()
    expect(addToast).toHaveBeenCalledTimes(1)
    expect(requests).toEqual(['/api/nodes', '/api/nodes'])
    expect(socket.sent.every((message) => 'workspace.sync' === message.type)).toBe(true)
    expect(TestSocket.instances).toHaveLength(1)
    expect(readStorage).not.toHaveBeenCalled()
    expect(writeStorage).not.toHaveBeenCalled()
  })

  test('live exit notification', async () => {
    const workspace = useWorkspaceStore()
    await workspace.load()
    const socket = TestSocket.instances[0]!
    socket.open()
    for (const payload of [
      { phase: 'start' },
      { phase: 'exit' },
      { phase: 'exit', exit_code: 0 },
    ]) {
      socket.message({
        type: 'event',
        event: 'stack.action',
        payload: {
          node: 'missing-node',
          name: 'app',
          action: 'restart',
          ...payload,
        },
      })
    }
    expect(addToast).not.toHaveBeenCalled()
    socket.message({
      type: 'event',
      event: 'stack.action',
      payload: {
        node: 'missing-node',
        name: 'app',
        action: 'restart',
        phase: 'exit',
        exit_code: 1,
        message: 'captured-execution-cause',
      },
    })
    expect(addToast).toHaveBeenCalledTimes(1)
    const toast = addToast.mock.calls[0]![0]
    for (const detail of ['missing-node', 'app', 'restart', 'captured-execution-cause']) {
      expect(toast.description).toContain(detail)
    }
    socket.message({
      type: 'event',
      event: 'node.snapshot',
      payload: {
        node: 'n',
        health: { status: 'ok', version: '1', api_version: 1 },
        stacks: [{ name: 'app', status: 'running', services: [], loading: false, busy: false }],
      },
    })
    await workspace.load()
    expect(addToast).toHaveBeenCalledTimes(1)
    expect(requests).toEqual(['/api/nodes', '/api/nodes'])
    expect(readStorage).not.toHaveBeenCalled()
    expect(writeStorage).not.toHaveBeenCalled()
  })

  test.each(['start', 'delete'] as const)('accepted %s', async (action) => {
    const workspace = useWorkspaceStore()
    globalThis.fetch = Object.assign(
      async (input: string | URL | Request, options?: RequestInit): Promise<Response> => {
        expect(String(input)).toBe(
          `/api/nodes/n/stacks/app${'delete' === action ? '' : '/actions'}`,
        )
        expect(options?.method).toBe('delete' === action ? 'DELETE' : 'POST')
        if ('delete' !== action) {
          expect(options?.body).toBe(JSON.stringify({ action, service: 'web' }))
        }
        return Response.json({ status: 'accepted' }, { status: 202 })
      },
      { preconnect: originalFetch.preconnect },
    )
    expect(await workspace.runAction('n', 'app', action, 'web')).toEqual({ status: 'accepted' })
    expect(workspace.isSubmitting('n', 'app')).toBe(false)
    expect(TestSocket.instances).toHaveLength(0)
    expect(addToast).not.toHaveBeenCalled()
  })

  test.each([400, 403, 409, 503])('preflight %s', async (status) => {
    const workspace = useWorkspaceStore()
    globalThis.fetch = Object.assign(
      async (): Promise<Response> =>
        Response.json({ error: { code: status, message: 'actual-safe-api-reason' } }, { status }),
      { preconnect: originalFetch.preconnect },
    )
    await expect(workspace.runAction('n', 'app', 'restart')).rejects.toThrow(
      'actual-safe-api-reason',
    )
    expect(workspace.isSubmitting('n', 'app')).toBe(false)
    expect(addToast).not.toHaveBeenCalled()
    expect(useAuthStore().authenticated).toBe(true)
  })

  test('logout abort', async () => {
    const workspace = useWorkspaceStore()
    const response = Promise.withResolvers<Response>()
    let signal: AbortSignal | null | undefined
    globalThis.fetch = Object.assign(
      async (_input: string | URL | Request, options?: RequestInit): Promise<Response> => {
        signal = options?.signal
        return response.promise
      },
      { preconnect: originalFetch.preconnect },
    )
    const pending = workspace.runAction('n', 'app', 'start')
    useAuthStore().invalidate()
    await nextTick()
    expect(signal?.aborted).toBe(true)
    response.resolve(Response.json({ status: 'accepted' }, { status: 202 }))
    await pending
    expect(workspace.isSubmitting('n', 'app')).toBe(false)
    expect(TestSocket.instances).toHaveLength(0)
  })
})
