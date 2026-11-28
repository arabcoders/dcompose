<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useStorage } from '@vueuse/core'
import { useHead, useRuntimeConfig } from '#app'
import UAlert from '@nuxt/ui/components/Alert.vue'
import UBadge from '@nuxt/ui/components/Badge.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UEmpty from '@nuxt/ui/components/Empty.vue'
import UIcon from '@nuxt/ui/components/Icon.vue'
import UInput from '@nuxt/ui/components/Input.vue'
import USwitch from '@nuxt/ui/components/Switch.vue'
import UTooltip from '@nuxt/ui/components/Tooltip.vue'
import PageHeader from '~/components/PageHeader.vue'
import type { Changelog } from '~/types'
import { parse_api_response } from '~/utils'
import { loadChangelog } from '~/utils/changelog'

useHead({ title: 'Changelog · DCompose' })

const projectUrl = 'https://github.com/arabcoders/dcompose'
const changelogUrl = 'https://arabcoders.github.io/dcompose/CHANGELOG.json'
const latestLimit = 10
const { version: installedVersion, commitSha: installedSha } = useRuntimeConfig().public
const lifecycle = new AbortController()
const changelog = ref<Changelog | null>(null)
const source = computed(() => changelog.value?.source ?? 'loading')
const history = computed(() => changelog.value?.history ?? [])
const search = ref('')
const query = computed(() => search.value.trim().toLowerCase())
const latestOnly = useStorage('changelog_latest_only', true)
const openReleases = ref(new Set<string>())
const dateFormatter = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeZone: 'UTC' })

const visible = computed(() => {
  const releases = latestOnly.value ? history.value.slice(0, latestLimit) : history.value
  return releases.map((release) => ({
    ...release,
    latestDate: release.commits.reduce(
      (date, commit) => (date < commit.date ? commit.date : date),
      '',
    ),
  }))
})

const filtered = computed(() => {
  const term = query.value
  if (!term) {
    return visible.value
  }
  return visible.value.flatMap((release) => {
    if (release.tag.toLowerCase().includes(term)) {
      return [release]
    }
    const commits = release.commits.filter(
      ({ sha, subject }) => sha.startsWith(term) || subject.toLowerCase().includes(term),
    )
    return 0 < commits.length ? [{ ...release, commits }] : []
  })
})

function isInstalledRelease(tag: string) {
  return 'Unreleased' !== tag && installedVersion === tag
}

watch(filtered, (releases) => {
  if (query.value) {
    openReleases.value = new Set(releases.map(({ tag }) => tag))
    return
  }
  const installed = releases.findIndex(
    ({ tag, commits }) =>
      isInstalledRelease(tag) || commits.some(({ sha }) => installedSha === sha),
  )
  openReleases.value = new Set(releases.slice(0, Math.max(0, installed) + 1).map(({ tag }) => tag))
})

function toggleRelease(tag: string) {
  if (openReleases.value.has(tag)) {
    openReleases.value.delete(tag)
  } else {
    openReleases.value.add(tag)
  }
}

function formatDate(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : dateFormatter.format(date)
}

async function readHistory(url: string, signal: AbortSignal): Promise<unknown> {
  const response = await fetch(url, {
    signal,
    credentials: 'omit',
    referrerPolicy: 'no-referrer',
    cache: 'no-cache',
  })
  if (!response.ok) {
    throw new Error('Changelog request failed')
  }
  return parse_api_response<unknown>(response)
}

async function load() {
  changelog.value = null
  const signal = lifecycle.signal
  try {
    changelog.value = await loadChangelog(
      signal,
      () => readHistory(changelogUrl, AbortSignal.any([signal, AbortSignal.timeout(5000)])),
      () => readHistory('/CHANGELOG.json', signal),
    )
  } catch {
    if (!signal.aborted) {
      changelog.value = { source: 'unavailable', history: [] }
    }
  }
}

onMounted(load)
onBeforeUnmount(() => lifecycle.abort())
</script>

<template>
  <main class="w-full min-w-0 space-y-6">
    <PageHeader
      icon="i-lucide-history"
      section-label="DCompose"
      page-label="Changelog"
      description="Browse changes by release or search for a commit."
    />
    <section aria-label="Release history" :aria-busy="'loading' === source" class="space-y-4">
      <div class="flex flex-wrap items-end justify-between gap-4">
        <div class="w-full space-y-1.5 sm:max-w-md">
          <label for="changelog-filter" class="block text-xs font-medium text-toned">
            Search changelog
          </label>
          <UInput
            id="changelog-filter"
            v-model="search"
            type="search"
            icon="i-lucide-search"
            placeholder="Release, change, or commit"
            size="sm"
            class="w-full"
          />
        </div>
        <USwitch
          v-model="latestOnly"
          size="sm"
          label="Latest only"
          :description="`${latestLimit} most recent releases`"
          :ui="{ root: 'items-center gap-2', wrapper: 'ms-0 text-xs text-toned' }"
        />
      </div>

      <UAlert
        v-if="'loading' === source"
        color="info"
        variant="soft"
        icon="i-lucide-loader-circle"
        title="Loading changelog"
        :ui="{ icon: 'motion-safe:animate-spin' }"
        role="status"
      />
      <div
        v-else-if="'bundled' === source"
        class="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 rounded-md bg-elevated/60 px-3 py-2"
      >
        <p class="inline-flex items-center gap-2 text-xs text-toned" role="status">
          <UIcon name="i-lucide-cloud-off" class="size-4 shrink-0" />
          Showing bundled history
        </p>
        <UButton color="neutral" variant="ghost" size="xs" icon="i-lucide-refresh-cw" @click="load">
          Retry live feed
        </UButton>
      </div>
      <UAlert
        v-else-if="'unavailable' === source"
        color="warning"
        variant="soft"
        icon="i-lucide-cloud-off"
        title="Changelog unavailable"
        description="Neither the live feed nor the bundled history could be loaded."
      >
        <template #actions>
          <UButton
            color="neutral"
            variant="outline"
            size="sm"
            icon="i-lucide-refresh-cw"
            @click="load"
          >
            Retry
          </UButton>
        </template>
      </UAlert>

      <div v-if="0 < filtered.length" class="divide-y divide-default border-y border-default">
        <section
          v-for="({ tag, latestDate, commits }, index) in filtered"
          :key="tag"
          :aria-labelledby="`changelog-release-${index}`"
          class="py-5"
        >
          <h2>
            <button
              :id="`changelog-release-${index}`"
              type="button"
              :aria-expanded="openReleases.has(tag)"
              :aria-controls="`changelog-commits-${index}`"
              class="group flex w-full items-start gap-3 rounded-sm text-start focus-visible:ring-2 focus-visible:ring-primary focus-visible:outline-none"
              @click="toggleRelease(tag)"
            >
              <UIcon
                name="i-lucide-chevron-right"
                class="mt-0.5 size-5 shrink-0 text-muted motion-safe:transition-transform"
                :class="{ 'rotate-90': openReleases.has(tag) }"
                aria-hidden="true"
              />
              <span
                class="flex min-w-0 flex-1 flex-wrap items-center justify-between gap-x-6 gap-y-2"
              >
                <span class="flex min-w-0 flex-wrap items-center gap-2">
                  <span
                    class="font-mono text-base font-semibold wrap-anywhere text-highlighted group-hover:text-primary"
                  >
                    {{ tag }}
                  </span>
                  <UBadge
                    v-if="isInstalledRelease(tag)"
                    color="success"
                    variant="soft"
                    size="sm"
                    icon="i-lucide-check"
                  >
                    Installed
                  </UBadge>
                </span>
                <span class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-toned">
                  <time v-if="latestDate" :datetime="latestDate">
                    <span class="sr-only">Last change: </span>{{ formatDate(latestDate) }}
                  </time>
                  <span class="tabular-nums">
                    {{ commits.length }} {{ 1 === commits.length ? 'commit' : 'commits' }}
                  </span>
                </span>
              </span>
            </button>
          </h2>

          <div
            :id="`changelog-commits-${index}`"
            :hidden="!openReleases.has(tag)"
            class="mt-4 sm:ms-8"
          >
            <ul v-if="openReleases.has(tag)" class="divide-y divide-default">
              <li v-for="{ sha, subject, date } in commits" :key="sha" class="space-y-2 py-3">
                <a
                  :href="`${projectUrl}/commit/${sha}`"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="block text-sm leading-6 font-medium wrap-anywhere text-highlighted transition-colors hover:text-primary focus-visible:underline"
                >
                  {{ subject }}
                </a>
                <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-toned">
                  <time :datetime="date">{{ formatDate(date) }}</time>
                  <UTooltip :text="sha">
                    <a
                      :href="`${projectUrl}/commit/${sha}`"
                      target="_blank"
                      rel="noopener noreferrer"
                      :aria-label="`Commit ${sha}`"
                      class="inline-flex items-center gap-1.5 font-mono transition-colors hover:text-primary focus-visible:underline"
                    >
                      <UIcon name="i-lucide-git-commit-horizontal" class="size-3.5 text-muted" />
                      {{ sha.slice(0, 7) }}
                    </a>
                  </UTooltip>
                  <span
                    v-if="installedSha === sha"
                    class="inline-flex items-center gap-1.5 font-medium text-success"
                  >
                    <UIcon name="i-lucide-check" class="size-3.5" />
                    Installed
                  </span>
                </div>
              </li>
            </ul>
          </div>
        </section>
      </div>

      <UEmpty
        v-else-if="0 < history.length"
        variant="naked"
        size="sm"
        icon="i-lucide-search"
        title="No matching changes"
        role="status"
      />
    </section>
  </main>
</template>
