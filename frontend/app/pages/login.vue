<template>
  <main class="app-shell-surface flex min-h-screen items-center justify-center px-4 py-8 sm:px-6">
    <div class="w-full max-w-md space-y-6">
      <header class="space-y-3 text-center">
        <div
          class="mx-auto inline-flex size-14 items-center justify-center rounded-md border border-default bg-elevated/80 shadow-sm"
        >
          <img src="/favicon.png" alt="DCompose" class="size-10 object-contain" />
        </div>
        <h1 class="text-2xl font-semibold text-highlighted">DCompose</h1>
        <p class="text-sm text-muted">Manage your Compose stacks</p>
      </header>

      <UAlert
        v-if="error || statusError"
        color="error"
        variant="soft"
        icon="i-lucide-circle-alert"
        :title="error || statusError"
      />

      <UCard :ui="cardUi">
        <template #header>
          <div class="flex items-center justify-between gap-3">
            <h2 class="text-base font-semibold text-highlighted">
              {{ setupRequired ? 'Create an account' : 'Sign in' }}
            </h2>
            <UTooltip :text="`Theme: ${colorMode.preference}`">
              <UButton
                type="button"
                color="neutral"
                variant="ghost"
                size="sm"
                square
                :icon="
                  'light' === colorMode.preference
                    ? 'i-lucide-sun'
                    : 'dark' === colorMode.preference
                      ? 'i-lucide-moon'
                      : 'i-lucide-monitor'
                "
                :aria-label="`Theme: ${colorMode.preference}`"
                @click="cycleTheme"
              />
            </UTooltip>
          </div>
        </template>

        <div class="space-y-5">
          <form method="post" class="space-y-5" @submit.prevent="login">
            <UFormField label="Username" name="username" required>
              <UInput
                id="user-id"
                v-model="user.username"
                type="text"
                required
                autocomplete="username"
                name="username"
                icon="i-lucide-user-round"
                placeholder="Username"
                autofocus
                class="w-full"
              />
            </UFormField>

            <UFormField label="Password" name="password" required>
              <UInput
                id="user-password"
                v-model="user.password"
                :type="showPassword ? 'text' : 'password'"
                required
                :autocomplete="setupRequired ? 'new-password' : 'current-password'"
                name="password"
                icon="i-lucide-lock-keyhole"
                placeholder="Password"
                class="w-full"
              >
                <template #trailing>
                  <UButton
                    type="button"
                    color="neutral"
                    variant="ghost"
                    square
                    size="xs"
                    :icon="showPassword ? 'i-lucide-eye-off' : 'i-lucide-eye'"
                    :aria-label="showPassword ? 'Hide password' : 'Show password'"
                    :aria-pressed="showPassword"
                    @click="showPassword = !showPassword"
                  />
                </template>
              </UInput>
            </UFormField>

            <UFormField v-if="setupRequired" label="Confirm password" name="confirmation" required>
              <UInput
                v-model="confirmation"
                :type="showPassword ? 'text' : 'password'"
                autocomplete="new-password"
                name="confirmation"
                icon="i-lucide-lock-keyhole"
                placeholder="Confirm password"
                required
                class="w-full"
              />
            </UFormField>

            <div
              class="grid gap-3"
              :class="!setupRequired && oidcAvailable ? 'grid-cols-2' : 'grid-cols-1'"
            >
              <UButton
                type="submit"
                color="primary"
                block
                :icon="setupRequired ? 'i-lucide-user-plus' : 'i-lucide-log-in'"
                :loading="loading"
                :disabled="loading || !!statusError"
                size="md"
              >
                {{ setupRequired ? 'Create account' : 'Sign in' }}
              </UButton>
              <UButton
                v-if="!setupRequired && oidcAvailable"
                :to="resolveApiUrl('/api/auth/oidc/login')"
                external
                color="neutral"
                variant="outline"
                block
                size="md"
                icon="i-lucide-key-round"
                aria-label="Sign in with SSO"
              >
                SSO
              </UButton>
            </div>
          </form>

          <UButton
            v-if="!setupRequired"
            type="button"
            variant="link"
            color="neutral"
            class="w-full"
            @click="recoveryOpen = true"
          >
            Forgot password?
          </UButton>
        </div>
      </UCard>

      <UModal
        v-model:open="recoveryOpen"
        title="Forgot password?"
        description="Resetting your password requires access to the server running DCompose."
      >
        <template #body>
          <UButton
            to="https://github.com/arabcoders/dcompose/blob/master/FAQ.md#how-do-i-reset-my-password"
            target="_blank"
            rel="noopener noreferrer"
            external
            variant="link"
            icon="i-lucide-external-link"
          >
            Password reset instructions
          </UButton>
        </template>
      </UModal>
    </div>
  </main>
</template>

<script setup lang="ts">
import { storeToRefs } from 'pinia'
import { computed, ref } from 'vue'
import type { Ref } from 'vue'
import { useHead, useRouter } from '#app'
import { definePageMeta, useColorMode } from '#imports'
import UAlert from '@nuxt/ui/components/Alert.vue'
import UFormField from '@nuxt/ui/components/FormField.vue'
import UInput from '@nuxt/ui/components/Input.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UCard from '@nuxt/ui/components/Card.vue'
import UModal from '@nuxt/ui/components/Modal.vue'
import UTooltip from '@nuxt/ui/components/Tooltip.vue'
import { useAuthStore } from '~/store/auth'
import { resolveApiUrl } from '~/utils/index'

definePageMeta({ name: 'login', layout: 'empty' })

type LoginCredentials = {
  username: string
  password: string
}

const authStore = useAuthStore()
const { authenticateUser } = authStore
const { authenticated, loading, setupRequired, oidcAvailable, statusError } = storeToRefs(authStore)
const router = useRouter()
const colorMode = useColorMode()

const cycleTheme = () => {
  const preferences = ['system', 'light', 'dark']
  const current = preferences.indexOf(colorMode.preference)
  colorMode.preference = preferences[(current + 1) % preferences.length] ?? 'system'
}

useHead({ title: computed(() => (setupRequired.value ? 'Create account' : 'Sign in')) })

const error: Ref<string> = ref('')
const user = ref<LoginCredentials>({ username: '', password: '' })
const confirmation = ref('')
const showPassword = ref(false)
const recoveryOpen = ref(false)

const cardUi = {
  root: 'bg-elevated/40 shadow-xl backdrop-blur-sm',
  header: 'px-5 py-4 sm:px-6',
  body: 'p-5 sm:p-6',
}

const login = async () => {
  try {
    error.value = ''
    if (setupRequired.value && user.value.password !== confirmation.value) {
      error.value = "Passwords don't match."
      return
    }
    await authenticateUser(user.value)

    if (authenticated.value) {
      await router.push('/')
    }
  } catch (err: unknown) {
    const message = err instanceof Error ? err.message : String(err)
    error.value = message
    user.value = { username: '', password: '' }
    confirmation.value = ''

    const element = document.querySelector('#user-id')
    if (element instanceof HTMLInputElement) {
      element.focus()
    }
  }
}
</script>
