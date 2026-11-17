import { mock } from 'bun:test'
import type { RouteMiddleware } from '#app'

export const navigate = mock((path: string, _options?: { replace?: boolean }) => path)
export const addToast = mock((_message: { title: string; description: string }) => {})

// Bun shares module mocks between files, so register the Nuxt stubs only once.
mock.module('#app', () => ({
  useRuntimeConfig: () => ({ public: { domain: '' } }),
  defineNuxtRouteMiddleware: (middleware: RouteMiddleware) => middleware,
  navigateTo: navigate,
}))
mock.module('#imports', () => ({ useToast: () => ({ add: addToast }) }))
