<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useStorage } from '@vueuse/core'
import { useRoute, useRuntimeConfig, useRuntimeHook } from '#app'
import { useColorMode } from '#imports'
import { NuxtLink, NuxtLoadingIndicator } from '#components'
import UAlert from '@nuxt/ui/components/Alert.vue'
import UBreadcrumb from '@nuxt/ui/components/Breadcrumb.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UDashboardGroup from '@nuxt/ui/components/DashboardGroup.vue'
import UDashboardSidebar from '@nuxt/ui/components/DashboardSidebar.vue'
import UDashboardPanel from '@nuxt/ui/components/DashboardPanel.vue'
import UDashboardNavbar from '@nuxt/ui/components/DashboardNavbar.vue'
import UDashboardSidebarToggle from '@nuxt/ui/components/DashboardSidebarToggle.vue'
import UDropdownMenu from '@nuxt/ui/components/DropdownMenu.vue'
import UFieldGroup from '@nuxt/ui/components/FieldGroup.vue'
import UInput from '@nuxt/ui/components/Input.vue'
import USelect from '@nuxt/ui/components/Select.vue'
import UTooltip from '@nuxt/ui/components/Tooltip.vue'
import BackupModal from '~/components/BackupModal.vue'
import CreateStackModal from '~/components/CreateStackModal.vue'
import Dialog from '~/components/Dialog.vue'
import SettingsModal from '~/components/SettingsModal.vue'
import StackList from '~/components/stacks/StackList.vue'
import { useDialog } from '~/composables/useDialog'
import { useWorkspace } from '~/composables/useWorkspace'
import { useAuthStore } from '~/store/auth'
import { useWorkspaceStore } from '~/store/workspace'
import { notification } from '~/utils'

const route = useRoute()
const runtimeConfig = useRuntimeConfig()
const colorMode = useColorMode()
const auth = useAuthStore()
const workspace = useWorkspaceStore()
const { nodes, loading, load } = useWorkspace()
const { stackSearch, stackFilter, sidebarOpen, settingsOpen, settingsSection } =
  storeToRefs(workspace)
const { confirmDialog } = useDialog()
const frontendUpdated = ref(false)

useRuntimeHook('app:manifest:update', () => {
  frontendUpdated.value = true
})

const reloadPage = () => window.location.reload()

const showCreate = ref(false)
const showBackup = ref(false)
const collapsed = useStorage<Record<string, boolean>>('stacks_collapsed_nodes', {})
const allCollapsed = computed(
  () => 0 < nodes.value.length && nodes.value.every((node) => collapsed.value[node.id]),
)
const toggleAll = () => {
  const collapse = !allCollapsed.value
  for (const node of nodes.value) {
    collapsed.value[node.id] = collapse
  }
}

const statusOptions = [
  { label: 'All stacks', value: 'all' },
  { label: 'Updates available', value: 'updates' },
  { label: 'Running', value: 'running' },
  { label: 'Partly running', value: 'partial' },
  { label: 'Stopped', value: 'stopped' },
  { label: 'Draft', value: 'draft' },
  { label: 'Status unknown', value: 'unknown' },
]

const selectedNode = computed(() => nodes.value.find((node) => route.params.node === node.id))
const breadcrumbs = computed(() => [
  { label: 'Overview', to: '/' },
  ...('/logs' === route.path ? [{ label: 'Logs' }] : []),
  ...(route.params.node
    ? [
        {
          label: selectedNode.value?.name ?? String(route.params.node),
          to: `/nodes/${encodeURIComponent(String(route.params.node))}`,
        },
      ]
    : []),
  ...(route.params.stack ? [{ label: String(route.params.stack) }] : []),
])
const openCreate = () => {
  sidebarOpen.value = false
  showCreate.value = true
}

const cycleTheme = () => {
  const preferences = ['system', 'light', 'dark']
  const current = preferences.indexOf(colorMode.preference)
  colorMode.preference = preferences[(current + 1) % preferences.length] ?? 'system'
}

const signOut = async () => {
  const { status } = await confirmDialog({ message: 'Sign out?', confirmText: 'Sign out' })
  if (!status) {
    return
  }
  try {
    await auth.logUserOut()
  } catch (error) {
    notification('error', 'Account', error instanceof Error ? error.message : "Couldn't sign out.")
  }
}

const accountMenuItems = computed(() => [
  [
    {
      label: 'Account',
      icon: 'i-lucide-circle-user',
      onSelect: () => workspace.openSettings('account'),
    },
    {
      label: 'Registries',
      icon: 'i-lucide-container',
      onSelect: () => workspace.openSettings('registries'),
    },
    {
      label: 'Backup',
      icon: 'i-lucide-archive',
      onSelect: () => {
        sidebarOpen.value = false
        showBackup.value = true
      },
    },
    {
      label: `Theme: ${colorMode.preference}`,
      icon:
        'light' === colorMode.preference
          ? 'i-lucide-sun'
          : 'dark' === colorMode.preference
            ? 'i-lucide-moon'
            : 'i-lucide-monitor',
      onSelect: cycleTheme,
    },
  ],
  [{ label: 'Sign out', icon: 'i-lucide-log-out', color: 'error' as const, onSelect: signOut }],
])

watch(
  () => route.path,
  () => {
    sidebarOpen.value = false
  },
)
onMounted(load)
</script>

<template>
  <div class="app-shell-surface min-h-screen">
    <UDashboardGroup storage="local" storage-key="stack-workspace" class="min-h-screen">
      <UDashboardSidebar
        v-model:open="sidebarOpen"
        resizable
        :default-size="18"
        :min-size="13"
        :max-size="26"
        :ui="{
          root: 'app-shell-surface border-r border-default',
          header: 'border-b border-default p-3',
          body: 'min-h-0 gap-0 overflow-hidden p-0',
        }"
      >
        <template #header>
          <NuxtLink to="/" class="flex items-center gap-3" @click="sidebarOpen = false">
            <img src="/favicon.png" alt="DCompose" class="size-8 rounded-md" />
            <div class="min-w-0">
              <p class="text-sm font-semibold text-highlighted">DCompose</p>
              <p class="text-xs text-toned">Stacks Management</p>
            </div>
          </NuxtLink>
        </template>
        <template #default>
          <div v-if="0 < nodes.length" class="space-y-2 border-b border-default p-3">
            <UFieldGroup class="w-full">
              <UInput
                v-model="stackSearch"
                type="search"
                icon="i-lucide-search"
                placeholder="Search stacks"
                aria-label="Search stacks"
                class="min-w-0 flex-1"
              />
              <UTooltip :text="allCollapsed ? 'Expand all' : 'Collapse all'">
                <UButton
                  color="neutral"
                  variant="outline"
                  :icon="allCollapsed ? 'i-lucide-chevrons-down' : 'i-lucide-chevrons-up'"
                  :aria-label="allCollapsed ? 'Expand all' : 'Collapse all'"
                  :aria-expanded="!allCollapsed"
                  @click="toggleAll"
                />
              </UTooltip>
            </UFieldGroup>
            <USelect
              v-model="stackFilter"
              :items="statusOptions"
              icon="i-lucide-list-filter"
              aria-label="Filter stacks"
              class="w-full"
            />
          </div>
          <StackList
            v-model:collapsed="collapsed"
            :nodes="nodes"
            :workspace-loading="loading"
            :search="stackSearch"
            :status="stackFilter"
            class="min-h-0 flex-1"
            @selected="sidebarOpen = false"
          />
        </template>
      </UDashboardSidebar>

      <UDashboardPanel
        class="min-w-0 bg-transparent"
        :ui="{
          root: 'min-w-0 min-h-screen flex flex-1 flex-col bg-transparent',
          body: 'min-h-0 min-w-0 flex-1 overflow-y-auto p-4 sm:p-5 lg:p-6',
        }"
      >
        <template #header>
          <UDashboardNavbar
            :toggle="false"
            :ui="{ left: 'min-w-0 flex-1', right: 'shrink-0 gap-2' }"
          >
            <template #left>
              <UDashboardSidebarToggle class="lg:hidden" />
              <UBreadcrumb :items="breadcrumbs" />
            </template>
            <template #right>
              <UTooltip text="Create stack">
                <UButton
                  icon="i-lucide-plus"
                  color="neutral"
                  variant="ghost"
                  aria-label="Create stack"
                  :disabled="!nodes.some((node) => 'connected' === node.connection)"
                  @click="openCreate"
                >
                  <span class="hidden xl:inline">Create stack</span>
                </UButton>
              </UTooltip>
              <UTooltip text="Nodes">
                <UButton
                  icon="i-lucide-server"
                  color="neutral"
                  variant="ghost"
                  aria-label="Nodes"
                  @click="workspace.openSettings('nodes')"
                >
                  <span class="hidden xl:inline">Nodes</span>
                </UButton>
              </UTooltip>
              <UTooltip text="Logs">
                <UButton
                  to="/logs"
                  icon="i-lucide-scroll-text"
                  color="neutral"
                  variant="ghost"
                  aria-label="Logs"
                  @click="sidebarOpen = false"
                >
                  <span class="hidden xl:inline">Logs</span>
                </UButton>
              </UTooltip>
              <UDropdownMenu :items="accountMenuItems" :content="{ align: 'end' }">
                <UButton
                  color="neutral"
                  variant="ghost"
                  icon="i-lucide-circle-user"
                  trailing-icon="i-lucide-chevron-down"
                  aria-label="Account menu"
                >
                  <span class="hidden max-w-32 truncate sm:inline">{{
                    auth.account?.username ?? 'Account'
                  }}</span>
                </UButton>
              </UDropdownMenu>
            </template>
          </UDashboardNavbar>
          <UAlert
            v-if="frontendUpdated"
            color="success"
            variant="soft"
            icon="i-lucide-info"
            title="DCompose has been updated. Click reload apply changes."
            orientation="horizontal"
            class="rounded-none"
          >
            <template #actions>
              <UButton
                color="neutral"
                variant="outline"
                size="sm"
                @click="reloadPage"
                icon="i-lucide-refresh-cw"
              >
                Reload
              </UButton>
            </template>
          </UAlert>
        </template>
        <template #body>
          <NuxtLoadingIndicator />
          <div class="mx-auto flex w-full max-w-400 min-w-0 flex-1 flex-col gap-6">
            <div class="min-w-0 flex-1"><slot /></div>
            <footer class="mt-auto border-t border-default pt-6 text-xs text-muted">
              v{{ runtimeConfig.public.version }}
            </footer>
          </div>
        </template>
      </UDashboardPanel>
    </UDashboardGroup>
    <CreateStackModal v-model:open="showCreate" />
    <BackupModal v-if="showBackup" v-model:open="showBackup" :nodes="nodes" />
    <SettingsModal v-model:open="settingsOpen" :section="settingsSection" />
    <Dialog />
  </div>
</template>
