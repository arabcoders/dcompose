import { afterEach, beforeEach, describe, expect, mock, test } from 'bun:test'
import { createPinia, setActivePinia } from 'pinia'
import type { RouteLocationNormalized } from 'vue-router'

const responses: Array<Response | Error | Promise<Response>> = []
const calls: Array<{ path: string; options: RequestInit }> = []
const originalFetch = globalThis.fetch
const navigate = mock((path: string, _options?: { replace?: boolean }) => path)

mock.module('#app', () => ({
  useRuntimeConfig: () => ({ public: { domain: '' } }),
  defineNuxtRouteMiddleware: (middleware: (to: { name: string }) => Promise<unknown>) => middleware,
  navigateTo: navigate,
}))
mock.module('#imports', () => ({
  useToast: () => ({ add: () => {} }),
}))

const { useAuthStore } = await import('../app/store/auth')
const { default: authGuard } = await import('../app/middleware/auth.global')
const { request } = await import('../app/utils/index')
const status = (setup: boolean, authenticated = false, oidc = false) =>
  Response.json({ setup_required: setup, authenticated, oidc_available: oidc })
const user = (username = 'admin') =>
  Response.json({
    account: { username },
  })
const flushRequests = () => new Promise<void>((resolve) => setImmediate(resolve))
const route = (name: string): RouteLocationNormalized => ({
  name,
  path: `/${name}`,
  fullPath: `/${name}`,
  hash: '',
  query: {},
  params: {},
  meta: {},
  matched: [],
  redirectedFrom: undefined,
})

beforeEach(() => {
  setActivePinia(createPinia())
  responses.length = 0
  calls.length = 0
  navigate.mockClear()
  globalThis.fetch = mock(async (input: RequestInfo | URL, options: RequestInit = {}) => {
    const path = 'string' === typeof input ? input : input instanceof URL ? input.href : input.url
    calls.push({ path, options })
    const response = responses.shift()
    if (!response || response instanceof Error) {
      throw response ?? new Error('No response queued')
    }
    return response
  })
})

afterEach(() => {
  globalThis.fetch = originalFetch
})

describe('Authentication', () => {
  test('first run', async () => {
    responses.push(status(true))
    const auth = useAuthStore()
    expect(await auth.refreshStatus()).toBe(false)
    expect(auth.setupRequired).toBe(true)
    expect(auth.oidcAvailable).toBe(false)
    expect(calls.map((call) => call.path)).toEqual(['/api/auth/status'])
  })

  test('routes reuse status', async () => {
    responses.push(status(false, true), user())
    await authGuard(route('stacks'), route('login'))
    await authGuard(route('settings'), route('stacks'))
    expect(await authGuard(route('login'), route('settings'))).toBe('/')
    expect(calls.map((call) => call.path)).toEqual(['/api/auth/status', '/api/auth/user'])
    expect(useAuthStore().initialized).toBe(true)
    expect(useAuthStore().account).toEqual({ username: 'admin' })
  })

  test('signed out routes', async () => {
    responses.push(status(false))
    expect(await authGuard(route('stacks'), route('login'))).toBe('/login')
    await authGuard(route('login'), route('stacks'))
    expect(calls.map((call) => call.path)).toEqual(['/api/auth/status'])
  })

  test('concurrent initialization', async () => {
    const auth = useAuthStore()
    let resolveStatus: (response: Response) => void = () => {}
    responses.push(
      new Promise<Response>((resolve) => {
        resolveStatus = resolve
      }),
      user(),
    )
    const first = auth.ensureStatus()
    const second = auth.ensureStatus()
    expect(calls).toHaveLength(1)
    resolveStatus(status(false, true))
    expect(await Promise.all([first, second])).toEqual([true, true])
    expect(calls.map((call) => call.path)).toEqual(['/api/auth/status', '/api/auth/user'])
    expect(await auth.ensureStatus()).toBe(true)
    expect(calls).toHaveLength(2)
  })

  test.each(['invalidate', 'logout', 'session'])('pending user after %s', async (action) => {
    const pendingUser = Promise.withResolvers<Response>()
    responses.push(status(false, true), pendingUser.promise)
    const auth = useAuthStore()
    const pending = auth.ensureStatus()
    await flushRequests()
    expect(calls.map((call) => call.path)).toEqual(['/api/auth/status', '/api/auth/user'])

    if ('logout' === action) {
      responses.push(Response.json({ authenticated: false }))
      await auth.logUserOut()
    } else if ('session' === action) {
      responses.push(Response.json({}, { status: 401, headers: { 'WWW-Authenticate': 'Session' } }))
      await request('/api/stacks')
    } else {
      auth.invalidate()
    }

    expect(await auth.ensureStatus()).toBe(false)
    pendingUser.resolve(user())
    expect(await pending).toBe(false)
    expect(auth.authenticated).toBe(false)
    expect(auth.account).toBeNull()
    expect(auth.initialized).toBe(true)
    expect(auth.statusError).toBe('')
  })

  test('pending status after invalidate', async () => {
    const pendingStatus = Promise.withResolvers<Response>()
    responses.push(pendingStatus.promise)
    const auth = useAuthStore()
    const pending = auth.ensureStatus()
    auth.invalidate()
    pendingStatus.resolve(status(true, true, true))
    expect(await pending).toBe(false)
    expect(auth.setupRequired).toBe(false)
    expect(auth.oidcAvailable).toBe(false)
    expect(auth.account).toBeNull()
    expect(calls).toHaveLength(1)
  })

  test('newer refresh wins', async () => {
    const pendingUser = Promise.withResolvers<Response>()
    responses.push(status(false, true), pendingUser.promise)
    const auth = useAuthStore()
    const older = auth.ensureStatus()
    await flushRequests()

    responses.push(status(false, true), user('updated'))
    expect(await auth.refreshStatus()).toBe(true)
    pendingUser.resolve(user())
    expect(await older).toBe(true)
    expect(auth.username).toBe('updated')
    expect(auth.statusError).toBe('')
    expect(auth.initialized).toBe(true)
  })

  test('older error keeps newer pending', async () => {
    const pendingUser = Promise.withResolvers<Response>()
    const newerStatus = Promise.withResolvers<Response>()
    responses.push(status(false, true), pendingUser.promise)
    const auth = useAuthStore()
    const older = auth.ensureStatus()
    await flushRequests()

    responses.push(newerStatus.promise, user('updated'))
    const newer = auth.refreshStatus()
    pendingUser.reject(new TypeError('Failed to fetch'))
    await older
    expect(auth.initialized).toBe(false)
    expect(auth.statusError).toBe('')
    const ensured = auth.ensureStatus()
    expect(calls).toHaveLength(3)
    newerStatus.resolve(status(false, true))
    expect(await Promise.all([newer, ensured])).toEqual([true, true])
    expect(auth.username).toBe('updated')
    expect(calls).toHaveLength(4)
  })

  test('signup session', async () => {
    const auth = useAuthStore()
    responses.push(status(true))
    await auth.refreshStatus()
    responses.push(
      Response.json({ authenticated: true }, { status: 201 }),
      status(false, true),
      user(),
    )
    await auth.authenticateUser({ username: 'admin', password: 'password' })
    expect(calls[1]?.path).toBe('/api/auth/setup')
    expect(calls[1]?.options.method).toBe('POST')
    expect(JSON.parse(String(calls[1]?.options.body))).toEqual({
      username: 'admin',
      password: 'password',
    })
    expect(auth.authenticated).toBe(true)
    expect(auth.setupRequired).toBe(false)
    expect(auth.username).toBe('admin')
    expect(auth.loading).toBe(false)
  })

  test('existing login', async () => {
    const auth = useAuthStore()
    responses.push(status(false, false, true))
    await auth.ensureStatus()
    expect(auth.oidcAvailable).toBe(true)
    responses.push(Response.json({ authenticated: true }), status(false, true, true), user())
    await auth.authenticateUser({ username: 'admin', password: 'password' })
    expect(calls[1]?.path).toBe('/api/auth/login')
    expect(auth.authenticated).toBe(true)
    expect(auth.setupRequired).toBe(false)
    expect(await auth.ensureStatus()).toBe(true)
    expect(calls.map((call) => call.path)).toEqual([
      '/api/auth/status',
      '/api/auth/login',
      '/api/auth/status',
      '/api/auth/user',
    ])
  })

  test('signup closed', async () => {
    const auth = useAuthStore()
    const message = 'setup-conflict-sentinel'
    responses.push(status(true))
    await auth.refreshStatus()
    responses.push(Response.json({ error: { code: 409, message } }, { status: 409 }), status(false))
    await expect(
      auth.authenticateUser({ username: 'admin', password: 'password' }),
    ).rejects.toThrow(message)
    expect(auth.setupRequired).toBe(false)
    expect(auth.authenticated).toBe(false)
    expect(auth.loading).toBe(false)
  })

  test('failed status stays closed', async () => {
    const auth = useAuthStore()
    responses.push(Response.json({ error: { code: 500, message: 'Unavailable' } }, { status: 500 }))
    expect(await auth.ensureStatus()).toBe(false)
    expect(auth.setupRequired).toBe(false)
    expect(auth.oidcAvailable).toBe(false)
    expect(auth.statusError).not.toBe('')
    expect(await auth.ensureStatus()).toBe(false)
    await authGuard(route('login'), route('stacks'))
    expect(calls).toHaveLength(1)
    responses.push(status(false, true), user())
    expect(await auth.refreshStatus()).toBe(true)
    expect(auth.statusError).toBe('')
    expect(calls).toHaveLength(3)
  })

  test('network failure cached', async () => {
    responses.push(new Error('Failed to fetch'))
    const auth = useAuthStore()
    expect(await auth.ensureStatus()).toBe(false)
    expect(await auth.ensureStatus()).toBe(false)
    expect(auth.initialized).toBe(true)
    expect(auth.statusError).not.toBe('')
    expect(calls).toHaveLength(1)
  })

  test('user failure cached', async () => {
    responses.push(status(false, true), Response.json({}, { status: 503 }))
    const auth = useAuthStore()
    expect(await auth.ensureStatus()).toBe(false)
    expect(await auth.ensureStatus()).toBe(false)
    expect(auth.account).toBeNull()
    expect(auth.statusError).not.toBe('')
    expect(calls).toHaveLength(2)
  })

  test('logout clears account', async () => {
    responses.push(status(false, true), user(), Response.json({ authenticated: false }))
    const auth = useAuthStore()
    await auth.ensureStatus()
    await auth.logUserOut()
    expect(auth.authenticated).toBe(false)
    expect(auth.account).toBeNull()
    expect(auth.username).toBeNull()
    expect(auth.initialized).toBe(true)
    expect(await auth.ensureStatus()).toBe(false)
    expect(calls.map((call) => call.path)).toEqual([
      '/api/auth/status',
      '/api/auth/user',
      '/api/auth/logout',
    ])
  })

  test('invalidate clears account', async () => {
    responses.push(status(false, true), user())
    const auth = useAuthStore()
    await auth.ensureStatus()
    auth.invalidate()
    expect(auth.authenticated).toBe(false)
    expect(auth.account).toBeNull()
    expect(auth.username).toBeNull()
    expect(await auth.ensureStatus()).toBe(false)
    expect(calls).toHaveLength(2)
  })

  test('session rejection invalidates', async () => {
    responses.push(
      status(false, true),
      user(),
      Response.json({}, { status: 401, headers: { 'WWW-Authenticate': 'Session' } }),
    )
    const auth = useAuthStore()
    await auth.ensureStatus()
    const response = await request('/api/stacks')
    expect(response.status).toBe(401)
    expect(auth.authenticated).toBe(false)
    expect(auth.account).toBeNull()
    expect(await auth.ensureStatus()).toBe(false)
    expect(calls).toHaveLength(3)
    expect(navigate).toHaveBeenCalledWith('/login', { replace: true })
  })

  test('expired status redirects', async () => {
    responses.push(status(false, true), user())
    const auth = useAuthStore()
    await auth.ensureStatus()
    responses.push(status(false))
    expect(await auth.refreshStatus()).toBe(false)
    expect(auth.account).toBeNull()
    expect(navigate).toHaveBeenCalledWith('/login', { replace: true })
    auth.invalidate()
    expect(navigate).toHaveBeenCalledTimes(1)
  })

  test('password rejection stays signed in', async () => {
    responses.push(status(false, true), user(), Response.json({}, { status: 401 }))
    const auth = useAuthStore()
    await auth.ensureStatus()
    await request('/api/auth/account', { method: 'PATCH' })
    expect(auth.authenticated).toBe(true)
    expect(auth.username).toBe('admin')
    expect(calls).toHaveLength(3)
    expect(navigate).not.toHaveBeenCalled()
  })

  test('bad credentials', async () => {
    const auth = useAuthStore()
    const message = 'credentials-rejected-sentinel'
    responses.push(status(false))
    await auth.refreshStatus()
    responses.push(Response.json({ error: { code: 401, message } }, { status: 401 }))
    await expect(auth.authenticateUser({ username: 'admin', password: 'wrong' })).rejects.toThrow(
      message,
    )
    expect(auth.setupRequired).toBe(false)
    expect(auth.authenticated).toBe(false)
  })

  test('login network error', async () => {
    responses.push(new TypeError('Failed to fetch'))
    const auth = useAuthStore()
    await expect(
      auth.authenticateUser({ username: 'admin', password: 'password' }),
    ).rejects.toThrow()
    expect(auth.loading).toBe(false)
    expect(auth.authenticated).toBe(false)
  })

  test('logout network error', async () => {
    responses.push(status(false, true), user(), new TypeError('Failed to fetch'))
    const auth = useAuthStore()
    await auth.ensureStatus()
    await expect(auth.logUserOut()).rejects.toThrow()
    expect(auth.authenticated).toBe(true)
    expect(auth.username).toBe('admin')
  })

  test('non-error failures', async () => {
    const auth = useAuthStore()
    responses.push(Promise.reject('offline'))
    await expect(
      auth.authenticateUser({ username: 'admin', password: 'password' }),
    ).rejects.toThrow()
    responses.push(Promise.reject('offline'))
    await expect(auth.logUserOut()).rejects.toThrow()
  })

  test('logout server error', async () => {
    const message = 'logout-rejected-sentinel'
    responses.push(Response.json({ error: { code: 403, message } }, { status: 403 }))
    await expect(useAuthStore().logUserOut()).rejects.toThrow(message)
  })
})
