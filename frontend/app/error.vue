<template>
  <UApp>
    <main class="app-shell-surface flex min-h-screen items-start justify-center px-4 py-8 sm:px-6">
      <UCard class="w-full max-w-4xl" :ui="{ body: 'p-0 sm:p-0' }">
        <div class="space-y-6 px-4 py-5 sm:px-6 sm:py-6 lg:px-7">
          <div class="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
            <div class="min-w-0 space-y-2">
              <div class="flex flex-wrap items-center gap-2 text-sm text-toned">
                <UIcon name="i-lucide-triangle-alert" class="size-4 text-warning" />
                <span>DCompose error</span>
              </div>

              <h1 class="text-2xl font-semibold text-highlighted sm:text-3xl">
                {{ error.status || 'Error' }}
                <span v-if="error.statusText"> - {{ error.statusText }}</span>
              </h1>

              <p class="max-w-3xl text-sm leading-6 text-toned">
                {{
                  404 === error.status
                    ? "This page doesn't exist."
                    : "DCompose couldn't load this page."
                }}
              </p>
            </div>

            <div class="flex shrink-0 items-center gap-2 lg:justify-end">
              <UButton
                color="neutral"
                variant="outline"
                size="sm"
                icon="i-lucide-house"
                class="text-sm whitespace-nowrap"
                @click="clearError({ redirect: '/' })"
              >
                Back to home
              </UButton>

              <UButton
                color="primary"
                size="sm"
                icon="i-lucide-rotate-cw"
                class="text-sm whitespace-nowrap"
                @click="handleRetry"
              >
                Retry
              </UButton>
            </div>
          </div>

          <UAlert
            v-if="error.message"
            color="warning"
            variant="soft"
            icon="i-lucide-circle-alert"
            title="Details"
            :description="error.message"
          />

          <section v-if="error.stack" class="space-y-3 border-t border-default pt-5">
            <button
              type="button"
              class="inline-flex items-center gap-2 text-sm font-semibold text-highlighted transition hover:text-primary"
              :aria-expanded="showStacks"
              aria-controls="error-stack-trace"
              @click="showStacks = !showStacks"
            >
              <UIcon
                :name="showStacks ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'"
                class="size-4"
              />
              <span>Stack trace</span>
            </button>

            <div
              v-if="showStacks"
              id="error-stack-trace"
              class="overflow-x-auto rounded-lg border border-default bg-elevated/60"
            >
              <pre
                class="min-w-0 p-4 text-xs leading-6 whitespace-pre-wrap text-default"
              ><code>{{ error.stack }}</code></pre>
            </div>
          </section>
        </div>
      </UCard>
    </main>
  </UApp>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { clearError, useHead, useRoute } from '#app'
import type { NuxtError } from '#app'
import UAlert from '@nuxt/ui/components/Alert.vue'
import UApp from '@nuxt/ui/components/App.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UCard from '@nuxt/ui/components/Card.vue'
import UIcon from '@nuxt/ui/components/Icon.vue'

defineProps<{ error: NuxtError }>()

const route = useRoute()
const showStacks = ref(false)
useHead({ title: 'Error', titleTemplate: '%s - DCompose' })
const handleRetry = async (): Promise<void> => await clearError({ redirect: route.fullPath })
</script>
