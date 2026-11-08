<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import UAlert from '@nuxt/ui/components/Alert.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UFormField from '@nuxt/ui/components/FormField.vue'
import UIcon from '@nuxt/ui/components/Icon.vue'
import UInput from '@nuxt/ui/components/Input.vue'
import UModal from '@nuxt/ui/components/Modal.vue'
import RegistrySettings from '~/components/RegistrySettings.vue'
import { useAuthStore } from '~/store/auth'
import { useRealtimeStore } from '~/store/realtime'
import { useWorkspaceStore } from '~/store/workspace'
import type {
  DisconnectNodeResponse,
  NodeConnectionRequest,
  NodeConnectionTest,
  PairNodeRequest,
  SettingsNode,
  SettingsResponse,
  SettingsSection,
  UpdateAccountRequest,
} from '~/types'
import { notification, parse_api_response, request } from '~/utils'
import { nodeConnectionRequest } from '~/utils/settings'

const props = defineProps<{ open: boolean; section: SettingsSection }>()
const emit = defineEmits<{
  'update:open': [value: boolean]
}>()

const auth = useAuthStore()
const realtime = useRealtimeStore()
const workspace = useWorkspaceStore()
const settings = ref<SettingsResponse | null>(null)
const loading = ref(false)
const error = ref('')
const saving = ref(false)
const registryBusy = ref(false)
const username = ref('')
const password = ref('')
const passwordConfirmation = ref('')
const currentPassword = ref('')
const showPassword = ref(false)
const showCurrentPassword = ref(false)
const showNode = ref(false)
const removingNode = ref<SettingsNode | null>(null)
const nodeForm = ref<HTMLFormElement | null>(null)
const editingId = ref<string | null>(null)
const pairingToken = ref('')
const showNodeToken = ref(false)
const testing = ref(false)
const testResult = ref<NodeConnectionTest | null>(null)
const testError = ref('')
const emptyNode = (): NodeConnectionRequest => ({
  name: '',
  url: '',
  token: '',
  fingerprint: '',
  public_host: '',
})
const node = ref<NodeConnectionRequest>(emptyNode())
const nodePaused = computed(() =>
  settings.value?.nodes.some((existing) => editingId.value === existing.id && existing.paused),
)
const controller = new AbortController()
const busy = computed(() => loading.value || saving.value || testing.value || registryBusy.value)
const modalTitle = computed(() => {
  if (removingNode.value) {
    return 'Disconnect node?'
  }
  if (showNode.value) {
    return null === editingId.value ? 'Add node' : 'Edit node'
  }
  return 'nodes' === props.section ? 'Nodes' : 'Account'
})

const api = async <T extends object>(
  url: string,
  options: RequestInit = {},
  failureMessage = "Couldn't save settings.",
): Promise<T> => {
  const response = await request(url, { ...options, signal: controller.signal })
  const json = await parse_api_response<T>(response)
  if ('error' in json) {
    throw new Error(json.error.message || failureMessage)
  }
  if (!response.ok) {
    throw new Error(failureMessage)
  }
  return json
}

const report = (cause: unknown): string => {
  const message =
    cause instanceof Error && !(cause instanceof TypeError)
      ? cause.message
      : "Couldn't reach the server."
  if (!controller.signal.aborted) {
    notification('error', 'Settings', message)
  }
  return message
}

const load = async () => {
  loading.value = true
  error.value = ''
  try {
    const json = await api<SettingsResponse>('/api/settings', {}, "Couldn't load settings.")
    settings.value = json
    username.value = json.username
  } catch (cause) {
    error.value = report(cause)
  } finally {
    loading.value = false
  }
}

const openNode = (existing?: SettingsNode) => {
  editingId.value = existing?.id ?? null
  pairingToken.value = ''
  showNodeToken.value = false
  node.value = existing
    ? {
        name: existing.name,
        url: existing.url,
        token: existing.token,
        fingerprint: existing.fingerprint,
        public_host: existing.public_host,
      }
    : emptyNode()
  testResult.value = null
  testError.value = ''
  showNode.value = true
}

const saveNode = async () => {
  if (busy.value) {
    return
  }
  saving.value = true
  try {
    const input: PairNodeRequest | NodeConnectionRequest =
      null === editingId.value
        ? {
            name: node.value.name.trim(),
            url: node.value.url.trim(),
            pairing_token: pairingToken.value,
            fingerprint: node.value.fingerprint.trim() || undefined,
          }
        : nodeConnectionRequest(node.value)
    await api<SettingsNode>(
      null === editingId.value
        ? '/api/settings/nodes/pair'
        : `/api/settings/nodes/${encodeURIComponent(editingId.value)}`,
      {
        method: null === editingId.value ? 'POST' : 'PUT',
        body: JSON.stringify(input),
      },
      "Couldn't save this node.",
    )
    showNode.value = false
    pairingToken.value = ''
    node.value.token = ''
    notification('success', 'Node saved', '')
  } catch (cause) {
    report(cause)
  } finally {
    saving.value = false
  }
}

const testNode = async () => {
  if (
    null === editingId.value ||
    nodePaused.value ||
    busy.value ||
    !nodeForm.value?.reportValidity()
  ) {
    return
  }

  testing.value = true
  testError.value = ''
  testResult.value = null
  try {
    testResult.value = await api<NodeConnectionTest>(
      '/api/settings/nodes/test',
      {
        method: 'POST',
        body: JSON.stringify({
          ...nodeConnectionRequest(node.value),
          id: editingId.value ?? undefined,
        }),
      },
      "Couldn't connect to this node.",
    )
  } catch (cause) {
    testError.value = report(cause)
  } finally {
    testing.value = false
  }
}

const resetNode = () => {
  openNode(settings.value?.nodes.find((existing) => editingId.value === existing.id))
}

const togglePaused = async (existing: SettingsNode) => {
  if (busy.value) {
    return
  }
  saving.value = true
  try {
    const result = await api<SettingsNode>(
      `/api/settings/nodes/${encodeURIComponent(existing.id)}`,
      {
        method: 'PUT',
        body: JSON.stringify({ ...nodeConnectionRequest(existing), paused: !existing.paused }),
      },
      "Couldn't change the node's paused state.",
    )
    const index = settings.value?.nodes.findIndex((entry) => result.id === entry.id) ?? -1
    if (settings.value && -1 !== index) {
      settings.value.nodes[index] = result
    }
    notification('success', result.paused ? 'Node paused' : 'Node resumed', result.name)
  } catch (cause) {
    report(cause)
  } finally {
    saving.value = false
  }
}

const removeNode = async (existing: SettingsNode) => {
  if (existing.paused || busy.value) {
    return
  }
  saving.value = true
  try {
    const result = await api<DisconnectNodeResponse>(
      `/api/settings/nodes/${encodeURIComponent(existing.id)}`,
      { method: 'DELETE' },
      "Couldn't disconnect this node.",
    )
    removingNode.value = null
    notification(result.warning ? 'warning' : 'success', 'Node disconnected', result.warning ?? '')
  } catch (cause) {
    report(cause)
  } finally {
    saving.value = false
  }
}

const saveAccount = async () => {
  if (busy.value) {
    return
  }
  if (password.value !== passwordConfirmation.value) {
    notification('error', 'Account', "New passwords don't match.")
    return
  }
  saving.value = true
  realtime.pause()
  try {
    const input: UpdateAccountRequest = {
      username: username.value.trim(),
      password: password.value,
      current_password: currentPassword.value,
    }
    await api<{ status: 'ok' }>(
      '/api/settings/account',
      {
        method: 'POST',
        body: JSON.stringify(input),
      },
      "Couldn't update your account.",
    )
    password.value = ''
    passwordConfirmation.value = ''
    currentPassword.value = ''
    await auth.refreshStatus()
    emit('update:open', false)
    notification('success', 'Account updated', '')
  } catch (cause) {
    report(cause)
  } finally {
    realtime.resume()
    saving.value = false
  }
}

const resetAccount = () => {
  username.value = settings.value?.username ?? ''
  password.value = ''
  passwordConfirmation.value = ''
  currentPassword.value = ''
  showPassword.value = false
  showCurrentPassword.value = false
}

watch(
  node,
  () => {
    testResult.value = null
    testError.value = ''
  },
  { deep: true },
)
watch(showNode, (open) => {
  if (!open) {
    pairingToken.value = ''
    node.value.token = ''
    showNodeToken.value = false
  }
})
watch(
  () => workspace.nodes,
  () => {
    if (props.open && 'nodes' === props.section) {
      void load()
    }
  },
)
watch(
  () => [props.open, props.section] as const,
  ([open, section]) => {
    if (open) {
      if ('registries' !== section) {
        void load()
      }
      return
    }
    resetAccount()
    showNode.value = false
    removingNode.value = null
    pairingToken.value = ''
    node.value.token = ''
    settings.value = null
  },
  { immediate: true },
)
onBeforeUnmount(() => {
  pairingToken.value = ''
  controller.abort()
})
</script>

<template>
  <RegistrySettings
    v-if="open && 'registries' === section"
    @busy="registryBusy = $event"
    @close="emit('update:open', false)"
  />
  <UModal
    v-if="'registries' !== section"
    :open="open"
    :title="modalTitle"
    :dismissible="!busy"
    :close="!busy"
    :ui="{ content: 'sm:max-w-2xl' }"
    @update:open="(value: boolean) => emit('update:open', value)"
  >
    <template v-if="!showNode && !removingNode" #body>
      <UAlert v-if="error" color="error" :title="error" />
      <p v-if="loading" class="text-sm text-muted">Loading settings...</p>
      <template v-if="settings && !loading">
        <section v-if="'nodes' === section" class="space-y-4">
          <p v-if="0 === settings.nodes.length" class="text-sm text-muted">No nodes added.</p>
          <div v-else class="space-y-3">
            <article
              v-for="existing in settings.nodes"
              :key="existing.id"
              class="app-card flex flex-col gap-3 p-4 sm:flex-row sm:items-center"
            >
              <div class="flex min-w-0 flex-1 items-center gap-3">
                <UIcon name="i-lucide-server" class="size-5 shrink-0 text-muted" />
                <div class="min-w-0">
                  <p class="wrap-break-word text-sm font-semibold text-highlighted">
                    {{ existing.name }}
                  </p>
                  <p class="mt-1 break-all text-xs text-muted">{{ existing.url }}</p>
                  <p v-if="existing.paused" class="mt-1 text-xs text-muted">Paused</p>
                </div>
              </div>
              <div class="flex shrink-0 flex-wrap items-center justify-end gap-2">
                <UButton
                  size="sm"
                  color="neutral"
                  variant="outline"
                  :icon="existing.paused ? 'i-lucide-play' : 'i-lucide-pause'"
                  :aria-pressed="!!existing.paused"
                  :disabled="busy"
                  @click="togglePaused(existing)"
                  >{{ existing.paused ? 'Resume' : 'Pause' }}</UButton
                >
                <UButton
                  size="sm"
                  color="neutral"
                  variant="outline"
                  icon="i-lucide-pencil"
                  :disabled="busy"
                  @click="openNode(existing)"
                  >Edit</UButton
                >
                <UButton
                  size="sm"
                  color="error"
                  variant="outline"
                  icon="i-lucide-unplug"
                  :disabled="busy || existing.paused"
                  @click="removingNode = existing"
                  >Disconnect</UButton
                >
              </div>
            </article>
          </div>
        </section>

        <form v-else id="settings-account" class="space-y-4" @submit.prevent="saveAccount">
          <UFormField label="Username" name="username" required>
            <UInput
              v-model="username"
              type="text"
              autocomplete="username"
              name="username"
              icon="i-lucide-user-round"
              placeholder="Username"
              required
              class="w-full"
              :disabled="busy"
            />
          </UFormField>
          <UFormField
            label="New password"
            name="password"
            help="Leave blank to keep the current password."
          >
            <UInput
              v-model="password"
              :type="showPassword ? 'text' : 'password'"
              autocomplete="new-password"
              name="password"
              icon="i-lucide-lock-keyhole"
              placeholder="New password"
              class="w-full"
              :disabled="busy"
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
                  :disabled="busy"
                  @click="showPassword = !showPassword"
                />
              </template>
            </UInput>
          </UFormField>
          <UFormField label="Confirm new password" name="confirmation" :required="!!password">
            <UInput
              v-model="passwordConfirmation"
              :type="showPassword ? 'text' : 'password'"
              autocomplete="new-password"
              name="confirmation"
              icon="i-lucide-lock-keyhole"
              placeholder="Confirm new password"
              :required="!!password"
              class="w-full"
              :disabled="busy"
            />
          </UFormField>
          <UFormField label="Current password" name="current_password" required>
            <UInput
              v-model="currentPassword"
              :type="showCurrentPassword ? 'text' : 'password'"
              autocomplete="current-password"
              name="current_password"
              icon="i-lucide-lock-keyhole"
              placeholder="Current password"
              required
              class="w-full"
              :disabled="busy"
            >
              <template #trailing>
                <UButton
                  type="button"
                  color="neutral"
                  variant="ghost"
                  square
                  size="xs"
                  :icon="showCurrentPassword ? 'i-lucide-eye-off' : 'i-lucide-eye'"
                  :aria-label="showCurrentPassword ? 'Hide password' : 'Show password'"
                  :aria-pressed="showCurrentPassword"
                  :disabled="busy"
                  @click="showCurrentPassword = !showCurrentPassword"
                />
              </template>
            </UInput>
          </UFormField>
        </form>
      </template>
    </template>
    <template v-else-if="removingNode" #body>
      <p class="text-sm text-default">Disconnect '{{ removingNode.name }}' from DCompose?</p>
    </template>
    <template v-else #body>
      <form id="settings-node" ref="nodeForm" class="space-y-4" @submit.prevent="saveNode">
        <UFormField
          label="Name"
          name="name"
          :required="null !== editingId"
          :help="
            null === editingId
              ? 'Display name in DCompose. Leave blank to use the node hostname.'
              : 'Display name in DCompose.'
          "
        >
          <UInput
            v-model="node.name"
            icon="i-lucide-server"
            :required="null !== editingId"
            :placeholder="null === editingId ? 'Node hostname' : undefined"
            :disabled="busy"
            class="w-full"
          />
        </UFormField>
        <UFormField label="Node address" name="url" help="Address to reach the node." required>
          <UInput
            v-model="node.url"
            :type="null === editingId ? 'text' : 'url'"
            icon="i-lucide-link"
            :pattern="null === editingId ? undefined : 'https://.*'"
            required
            :placeholder="
              null === editingId ? 'node.example.com:9077' : 'https://node.example.com:9077'
            "
            :disabled="busy"
            class="w-full"
          />
        </UFormField>
        <UFormField
          v-if="null === editingId"
          label="Pairing token"
          name="pairing_token"
          help="Enter the pairing token generated on the node."
          required
        >
          <UInput
            v-model="pairingToken"
            type="password"
            icon="i-lucide-key-round"
            autocomplete="new-password"
            required
            :disabled="busy"
            class="w-full"
          />
        </UFormField>
        <template v-if="null !== editingId">
          <UFormField label="API key" name="token" help="Leave blank to keep the saved API key.">
            <UInput
              v-model="node.token"
              :type="showNodeToken ? 'text' : 'password'"
              icon="i-lucide-key-round"
              autocomplete="new-password"
              :placeholder="
                settings?.nodes.find((existing) => editingId === existing.id)?.has_token
                  ? '•••••••• (saved)'
                  : undefined
              "
              :disabled="busy"
              class="w-full"
            >
              <template #trailing>
                <UButton
                  type="button"
                  color="neutral"
                  variant="ghost"
                  square
                  size="xs"
                  :icon="showNodeToken ? 'i-lucide-eye-off' : 'i-lucide-eye'"
                  :aria-label="showNodeToken ? 'Hide API key' : 'Show API key'"
                  :aria-pressed="showNodeToken"
                  :disabled="busy"
                  @click="showNodeToken = !showNodeToken"
                />
              </template>
            </UInput>
          </UFormField>
        </template>
        <UFormField
          label="TLS fingerprint"
          name="fingerprint"
          :help="
            null === editingId
              ? 'Optional. Copy from the agent console to verify its identity before pairing.'
              : 'SHA-256 fingerprint of the node TLS public key.'
          "
          :required="null !== editingId"
        >
          <UInput
            v-model="node.fingerprint"
            icon="i-lucide-fingerprint"
            :required="null !== editingId"
            :disabled="busy"
            class="w-full"
          />
        </UFormField>
        <UFormField
          v-if="null !== editingId"
          label="Public host"
          name="public_host"
          help="Host used for container port links."
        >
          <UInput
            v-model="node.public_host"
            icon="i-lucide-globe"
            :disabled="busy"
            class="w-full"
          />
        </UFormField>
        <UAlert v-if="testError" color="error" :title="testError" />
        <UAlert
          v-if="testResult"
          color="success"
          title="Connection successful"
          :description="`Node version: ${testResult.version}`"
        />
      </form>
    </template>
    <template v-if="!showNode && !removingNode" #footer>
      <div class="flex w-full flex-wrap items-center justify-between gap-2">
        <UButton
          v-if="'account' === section"
          type="button"
          color="neutral"
          variant="outline"
          size="sm"
          icon="i-lucide-rotate-ccw"
          :disabled="busy || !settings"
          @click="resetAccount"
          >Reset</UButton
        >
        <UButton
          v-else-if="'nodes' === section"
          type="button"
          color="neutral"
          variant="outline"
          size="sm"
          icon="i-lucide-refresh-cw"
          :loading="loading"
          :disabled="busy"
          @click="load"
          >Refresh</UButton
        >
        <div class="flex items-center justify-end gap-2">
          <UButton
            v-if="'account' === section"
            type="submit"
            form="settings-account"
            size="sm"
            icon="i-lucide-save"
            :loading="saving"
            :disabled="busy || !settings"
            >Save</UButton
          >
          <UButton
            v-else-if="'nodes' === section"
            type="button"
            size="sm"
            icon="i-lucide-plus"
            :disabled="busy || !settings"
            @click="openNode()"
            >Add node</UButton
          >
        </div>
      </div>
    </template>
    <template v-else-if="removingNode" #footer>
      <div class="flex w-full items-center justify-between gap-2">
        <UButton
          color="neutral"
          variant="outline"
          size="sm"
          icon="i-lucide-arrow-left"
          :disabled="busy"
          @click="removingNode = null"
          >Back</UButton
        >
        <UButton
          color="error"
          size="sm"
          icon="i-lucide-unplug"
          :loading="saving"
          :disabled="busy"
          @click="removeNode(removingNode)"
          >Disconnect</UButton
        >
      </div>
    </template>
    <template v-else #footer>
      <div class="flex w-full flex-wrap items-center justify-between gap-2">
        <div class="flex flex-wrap items-center gap-2">
          <UButton
            type="button"
            color="neutral"
            variant="outline"
            size="sm"
            icon="i-lucide-arrow-left"
            :disabled="busy"
            @click="showNode = false"
            >Back</UButton
          >
          <UButton
            type="button"
            color="neutral"
            variant="outline"
            size="sm"
            icon="i-lucide-rotate-ccw"
            :disabled="busy"
            @click="resetNode"
            >{{ null === editingId ? 'Clear' : 'Reset' }}</UButton
          >
        </div>
        <div class="flex flex-wrap items-center justify-end gap-2">
          <UButton
            v-if="null !== editingId"
            type="button"
            color="neutral"
            variant="outline"
            size="sm"
            icon="i-lucide-plug-zap"
            :loading="testing"
            :disabled="busy || nodePaused"
            @click="testNode"
            >Test connection</UButton
          >
          <UButton
            type="submit"
            form="settings-node"
            size="sm"
            icon="i-lucide-save"
            :loading="saving"
            :disabled="busy"
            >Save node</UButton
          >
        </div>
      </div>
    </template>
  </UModal>
</template>
