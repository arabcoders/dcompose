import { useRuntimeConfig } from '#app'
import { useToast } from '#imports'
import { useAuthStore } from '~/store/auth'
import type { GenericError } from '~/types'

export const resolveApiUrl = (url: string): string => {
  const runtimeConfig = useRuntimeConfig()
  return url.startsWith('/') ? runtimeConfig.public.domain + url : url
}

export const resolveWebSocketUrl = (path: string): string => {
  const url = new URL(resolveApiUrl(path), location.origin)
  if (location.origin !== url.origin) {
    throw new Error('Could not connect. Check the server address.')
  }
  url.protocol = 'https:' === url.protocol ? 'wss:' : 'ws:'
  return url.href
}

export const request = async (url: string, options: RequestInit = {}): Promise<Response> => {
  const headers = new Headers(options.headers)
  if (!headers.has('Content-Type') && !(options.body instanceof FormData)) {
    headers.set('Content-Type', 'application/json')
  }
  if (!headers.has('Accept')) {
    headers.set('Accept', 'application/json')
  }
  const response = await fetch(resolveApiUrl(url), { ...options, headers, credentials: 'include' })
  if (401 === response.status && 'Session' === response.headers.get('WWW-Authenticate')) {
    useAuthStore().invalidate()
  }
  return response
}

export const parse_api_response = async <T>(response: Response): Promise<T | GenericError> => {
  try {
    return (await response.json()) as T
  } catch {
    return { error: { code: response.status, message: response.statusText } }
  }
}

export const notification = (type: string, title: string, text: string, duration = 5000) => {
  const color = 'crit' === type ? 'error' : type
  const config = {
    info: { color: 'info', icon: 'i-lucide-info' },
    success: { color: 'success', icon: 'i-lucide-circle-check' },
    warning: { color: 'warning', icon: 'i-lucide-triangle-alert' },
    error: { color: 'error', icon: 'i-lucide-circle-alert' },
  } as const
  const theme = color in config ? config[color as keyof typeof config] : config.info
  return useToast().add({ title, description: text, duration, ...theme })
}

export const copyText = async (text: string): Promise<void> => {
  try {
    await navigator.clipboard.writeText(text)
    notification('success', 'Copied', 'Copied to clipboard.')
  } catch {
    notification('error', 'Copy failed', "Couldn't copy to the clipboard.")
  }
}
