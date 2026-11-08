import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { navigateTo } from '#app'
import { parse_api_response, request } from '~/utils'
import type { AuthAccount, AuthStatusResponse, AuthUserResponse } from '~/types'

export const useAuthStore = defineStore('auth', () => {
  const authenticated = ref(false)
  const initialized = ref(false)
  const loading = ref(false)
  const setupRequired = ref(false)
  const oidcAvailable = ref(false)
  const statusError = ref('')
  const account = ref<AuthAccount | null>(null)
  const username = computed(() => account.value?.username ?? null)
  let statusRequest: Promise<boolean> | null = null
  let statusGeneration = 0

  const invalidate = () => {
    const wasAuthenticated = authenticated.value
    statusGeneration++
    statusRequest = null
    authenticated.value = false
    account.value = null
    statusError.value = ''
    initialized.value = true
    if (wasAuthenticated) {
      void navigateTo('/login', { replace: true })
    }
  }

  const checkStatus = async (generation: number): Promise<boolean> => {
    try {
      statusError.value = ''
      const statusResponse = await request('/api/auth/status', { cache: 'no-store' })
      const status = await parse_api_response<AuthStatusResponse>(statusResponse)
      if (generation !== statusGeneration) {
        return authenticated.value
      }
      if (!statusResponse.ok || 'error' in status) {
        throw new Error("Couldn't check your session. Reload the page to try again.")
      }
      setupRequired.value = status.setup_required
      oidcAvailable.value = status.oidc_available
      if (!status.authenticated) {
        invalidate()
        return false
      }
      authenticated.value = true
      const response = await request('/api/auth/user', { cache: 'no-store' })
      const json = await parse_api_response<AuthUserResponse>(response)
      if (generation !== statusGeneration) {
        return authenticated.value
      }
      if (!response.ok || 'error' in json) {
        if (401 === response.status) {
          invalidate()
          return false
        }
        throw new Error("Couldn't check your session. Reload the page to try again.")
      }
      account.value = json.account
      authenticated.value = true
      return true
    } catch {
      if (generation !== statusGeneration) {
        return authenticated.value
      }
      authenticated.value = false
      account.value = null
      setupRequired.value = false
      oidcAvailable.value = false
      statusError.value = "Couldn't check your session. Reload the page to try again."
      return false
    } finally {
      if (generation === statusGeneration) {
        initialized.value = true
      }
    }
  }

  const refreshStatus = (): Promise<boolean> => {
    const pending = checkStatus(++statusGeneration).finally(() => {
      if (pending === statusRequest) {
        statusRequest = null
      }
    })
    statusRequest = pending
    return pending
  }

  const ensureStatus = (): Promise<boolean> => {
    if (statusRequest) {
      return statusRequest
    }
    if (initialized.value) {
      return Promise.resolve(authenticated.value)
    }
    return refreshStatus()
  }

  const authenticateUser = async (credentials: { username: string; password: string }) => {
    loading.value = true
    try {
      const response = await request(setupRequired.value ? '/api/auth/setup' : '/api/auth/login', {
        method: 'POST',
        body: JSON.stringify(credentials),
      })
      const json = await parse_api_response<{ authenticated: boolean }>(response)
      if ('error' in json) {
        if (409 === response.status) {
          await refreshStatus()
        }
        throw new Error(json.error.message)
      }
      if (!response.ok || !json.authenticated || !(await refreshStatus())) {
        throw new Error("Couldn't sign in.")
      }
    } catch (error) {
      if (error instanceof TypeError || !(error instanceof Error)) {
        throw new Error('Could not sign in. Try again.', { cause: error })
      }
      throw error
    } finally {
      loading.value = false
    }
  }

  const logUserOut = async () => {
    try {
      const response = await request('/api/auth/logout', { method: 'POST' })
      const json = await parse_api_response<{ authenticated: boolean }>(response)
      if ('error' in json) {
        throw new Error(json.error.message)
      }
      if (!response.ok) {
        throw new Error("Couldn't sign out.")
      }
      invalidate()
    } catch (error) {
      if (error instanceof TypeError || !(error instanceof Error)) {
        throw new Error('Could not sign out. Try again.', { cause: error })
      }
      throw error
    }
  }

  return {
    authenticated,
    initialized,
    loading,
    account,
    username,
    setupRequired,
    oidcAvailable,
    statusError,
    ensureStatus,
    refreshStatus,
    invalidate,
    authenticateUser,
    logUserOut,
  }
})
