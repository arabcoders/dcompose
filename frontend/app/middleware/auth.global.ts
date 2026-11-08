import { defineNuxtRouteMiddleware, navigateTo } from '#app'
import { useAuthStore } from '~/store/auth'

export default defineNuxtRouteMiddleware(async (to) => {
  const auth = useAuthStore()
  await auth.ensureStatus()
  if ('login' !== to.name && !auth.authenticated) {
    return navigateTo('/login')
  }
  if ('login' === to.name && auth.authenticated) {
    return navigateTo('/')
  }
})
