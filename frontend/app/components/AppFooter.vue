<script setup lang="ts">
import { computed } from 'vue'
import { useRuntimeConfig } from '#app'
import { NuxtLink } from '#components'
import UIcon from '@nuxt/ui/components/Icon.vue'
import UTooltip from '@nuxt/ui/components/Tooltip.vue'

const projectUrl = 'https://github.com/arabcoders/dcompose'
const runtimeConfig = useRuntimeConfig()
const version = computed(() => runtimeConfig.public.version)
const buildDate = computed(() => runtimeConfig.public.buildDate)
const branch = computed(() => runtimeConfig.public.branch)
const commitSha = computed(() => runtimeConfig.public.commitSha)
const hasCommit = computed(() => '' !== commitSha.value && 'unknown' !== commitSha.value)
const shortCommit = computed(() => commitSha.value.slice(0, 7))
const commitUrl = computed(() => `${projectUrl}/commit/${commitSha.value}`)
</script>

<template>
  <footer class="mt-auto border-t border-default pt-6 text-xs text-muted">
    <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
      <NuxtLink
        :to="projectUrl"
        target="_blank"
        rel="noopener noreferrer"
        class="inline-flex items-center gap-2 font-semibold text-highlighted"
      >
        <UIcon name="i-lucide-github" class="size-4" />
        <span>DCompose</span>
      </NuxtLink>
      <span class="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span>{{ version }}</span>
        <span aria-hidden="true">·</span>
        <span>{{ buildDate }}</span>
        <span aria-hidden="true">·</span>
        <UTooltip v-if="hasCommit" :text="commitSha">
          <NuxtLink
            :to="commitUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="hover:text-primary hover:underline"
          >
            {{ shortCommit }}
          </NuxtLink>
        </UTooltip>
        <span v-else>unknown</span>
        <span aria-hidden="true">·</span>
        <span>{{ branch }}</span>
      </span>
      <NuxtLink
        to="/changelog"
        class="inline-flex items-center gap-1.5 hover:text-primary hover:underline"
      >
        <UIcon name="i-lucide-history" class="size-3.5" />
        Changelog
      </NuxtLink>
    </div>
  </footer>
</template>
