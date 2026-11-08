<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import UAlert from '@nuxt/ui/components/Alert.vue'
import UButton from '@nuxt/ui/components/Button.vue'
import UCheckbox from '@nuxt/ui/components/Checkbox.vue'
import UFormField from '@nuxt/ui/components/FormField.vue'
import UInput from '@nuxt/ui/components/Input.vue'
import UModal from '@nuxt/ui/components/Modal.vue'
import USwitch from '@nuxt/ui/components/Switch.vue'
import type { RegistriesResponse, RegistryCredentialInput, RegistryImportResult } from '~/types'
import { notification, parse_api_response, request } from '~/utils'
import { decodeAuth, encodeAuth, normalizeRegistry, parseDockerConfig } from '~/utils/registries'

const emit = defineEmits<{ busy: [value: boolean]; close: [] }>()
const rows = ref<
  Array<
    RegistryCredentialInput & {
      key: number
      username: string
      password: string
      originalUsername: string
      originalPassword: string
      has_auth: boolean
      showSecret: boolean
      imported: boolean
      selected: boolean
      message: string
    }
  >
>([])
const editing = ref<(typeof rows.value)[number] | null>(null)
const removing = ref<(typeof rows.value)[number] | null>(null)
const loading = ref(false)
const loaded = ref(false)
const saving = ref(false)
const importing = ref(false)
const error = ref('')
const importError = ref('')
const importNotice = ref('')
const fileInput = ref<HTMLInputElement | null>(null)
const controller = new AbortController()
const busy = computed(() => loading.value || saving.value || importing.value)
const importedRows = computed(() => rows.value.filter((row) => row.imported))
const modalTitle = computed(() => {
  if (removing.value) {
    return 'Remove registry login?'
  }
  if (!editing.value) {
    return 'Registries'
  }
  return rows.value.some((row) => row.key === editing.value?.key)
    ? 'Edit registry login'
    : 'Add registry login'
})
let nextKey = 0

const loginFields = (auth: Record<string, string>, savedUsername = '') => {
  let username = auth.username ?? savedUsername
  let password = auth.password ?? ''
  if (auth.auth) {
    try {
      const login = decodeAuth(auth.auth)
      username = auth.username ?? login.username
      password = auth.password ?? login.password
    } catch {
      // Keep unreadable native auth intact until a replacement login is supplied.
    }
  }
  return { username, password, originalUsername: username, originalPassword: password }
}

const extraFields = (auth: Record<string, string>): Array<string> =>
  Object.keys(auth).filter((field) => !['auth', 'username', 'password'].includes(field))

const secretField = (field: string): boolean => !['email', 'serveraddress'].includes(field)

const showLogin = (auth: Record<string, string>): boolean =>
  ['auth', 'username', 'password'].some((field) => field in auth) ||
  !['identitytoken', 'registrytoken'].some((field) => field in auth)

const setRegistries = (response: RegistriesResponse) => {
  rows.value = response.registries.map((entry) => ({
    ...entry,
    ...loginFields(entry.auth, entry.username),
    key: nextKey++,
    showSecret: false,
    imported: false,
    selected: true,
    message: '',
  }))
}

const report = (cause: unknown): string => {
  const message =
    cause instanceof Error && !(cause instanceof TypeError)
      ? cause.message
      : "Couldn't reach the server."
  if (!controller.signal.aborted) {
    notification('error', 'Registries', message)
  }
  return message
}

const api = async (options: RequestInit = {}): Promise<RegistriesResponse> => {
  const response = await request('/api/settings/registries', {
    ...options,
    signal: controller.signal,
  })
  const json = await parse_api_response<RegistriesResponse>(response)
  if ('error' in json) {
    throw new Error(json.error.message || "Couldn't save registry settings.")
  }
  if (!response.ok) {
    throw new Error("Couldn't save registry settings.")
  }
  return json
}

const load = async () => {
  loading.value = true
  error.value = ''
  try {
    setRegistries(await api())
    loaded.value = true
  } catch (cause) {
    error.value = report(cause)
  } finally {
    loading.value = false
  }
}

const add = () => {
  editing.value = {
    key: nextKey++,
    registry: '',
    label: '',
    auth: {},
    ...loginFields({}),
    background_checks: false,
    has_auth: false,
    showSecret: false,
    imported: false,
    selected: true,
    message: '',
  }
}

const edit = (row: (typeof rows.value)[number]) => {
  editing.value = { ...row, auth: { ...row.auth }, showSecret: false }
}

const apply = async () => {
  if (!editing.value) {
    return
  }
  const index = rows.value.findIndex((row) => editing.value?.key === row.key)
  if (-1 === index) {
    rows.value.push(editing.value)
  } else {
    rows.value[index] = editing.value
  }
  editing.value = null
  await save()
}

const remove = (key: number) => {
  const row = rows.value.find((entry) => key === entry.key)
  if (!row || busy.value) {
    return
  }
  if (row.id) {
    removing.value = row
    return
  }
  rows.value = rows.value.filter((entry) => key !== entry.key)
}

const confirmRemove = (key: number) => {
  rows.value = rows.value.filter((row) => key !== row.key)
  removing.value = null
}

const save = async () => {
  if (busy.value || !loaded.value) {
    return
  }
  error.value = ''
  try {
    const registries: Array<RegistryCredentialInput> = rows.value
      .filter((row) => !row.imported || row.selected)
      .map((row) => {
        const auth = { ...row.auth }
        const edited =
          row.username !== row.originalUsername || row.password !== row.originalPassword
        if ('auth' in auth) {
          if (edited) {
            if (auth.auth) {
              auth.auth = encodeAuth(row.username, row.password)
              if ('username' in auth) {
                auth.username = row.username
              }
              if ('password' in auth) {
                auth.password = row.password
              }
            } else {
              // The server re-encodes a masked saved auth using its saved password.
              auth.username = row.username
              auth.password = row.password
            }
          }
        } else if (showLogin(auth)) {
          if (0 === Object.keys(auth).length && row.password) {
            auth.auth = encodeAuth(row.username, row.password)
          } else {
            if (edited || 'username' in auth) {
              auth.username = row.username
            }
            if (edited || 'password' in auth) {
              auth.password = row.password
            }
          }
        }
        if (
          !row.has_auth &&
          !['auth', 'password', 'identitytoken', 'registrytoken'].some((field) => auth[field])
        ) {
          throw new Error('Enter a password, access token or native token for each new login.')
        }
        return {
          id: row.id,
          registry: normalizeRegistry(row.registry),
          label: row.label,
          auth,
          background_checks: row.background_checks,
        }
      })

    saving.value = true
    const response = await api({ method: 'PUT', body: JSON.stringify({ registries }) })
    setRegistries(response)
    importNotice.value = ''
    notification('success', 'Registries saved', '')
  } catch (cause) {
    error.value = report(cause)
  } finally {
    saving.value = false
  }
}

const appendImport = (result: RegistryImportResult) => {
  rows.value.push(
    ...result.entries.map((entry) => ({
      ...entry,
      ...loginFields(entry.auth),
      key: nextKey++,
      has_auth: false,
      showSecret: false,
      imported: true,
    })),
  )
  const notices: Array<string> = []
  if (result.credentialStore) {
    notices.push('Passwords stored in a credential helper are not included in config.json.')
  }
  if (0 === result.entries.length) {
    notices.push('No registry logins were found in this file.')
  }
  importNotice.value = notices.join(' ')
}

const importFile = async (event: Event) => {
  const input = event.target
  if (!(input instanceof HTMLInputElement) || busy.value) {
    return
  }
  const file = input.files?.[0]
  if (!file) {
    return
  }

  importing.value = true
  importError.value = ''
  try {
    appendImport(parseDockerConfig(await file.text()))
  } catch (cause) {
    importError.value = cause instanceof Error ? cause.message : "Couldn't read this file."
    notification('error', 'Import config.json', importError.value)
  } finally {
    input.value = ''
    importing.value = false
  }
}

const selectImported = (selected: boolean) => {
  for (const row of importedRows.value) {
    row.selected = selected
  }
}

watch(busy, (value) => emit('busy', value), { immediate: true })
onMounted(load)
onBeforeUnmount(() => {
  controller.abort()
  emit('busy', false)
})
</script>

<template>
  <UModal
    :open="true"
    :title="modalTitle"
    description="logins are shared to all nodes."
    :dismissible="!busy"
    :close="!busy"
    :ui="{ content: 'sm:max-w-2xl' }"
    @update:open="
      (value) => {
        if (!value) {
          emit('close')
        }
      }
    "
  >
    <template v-if="!editing && !removing" #body>
      <div class="space-y-4">
        <UAlert v-if="error" color="error" :title="error" />
        <p v-if="loading" class="text-sm text-muted">Loading registry logins...</p>
        <input
          ref="fileInput"
          type="file"
          accept=".json,application/json"
          class="hidden"
          aria-label="Import Docker config.json"
          :disabled="busy"
          @change="importFile"
        />
        <UAlert v-if="importError" color="error" :title="importError" />
        <UAlert v-if="importNotice" color="info" :title="importNotice" />
        <div v-if="0 < importedRows.length" class="flex flex-wrap items-center gap-2">
          <span class="text-sm text-muted">Imported logins:</span>
          <UButton
            type="button"
            size="xs"
            color="neutral"
            variant="outline"
            :disabled="busy"
            @click="selectImported(true)"
            >Select all</UButton
          >
          <UButton
            type="button"
            size="xs"
            color="neutral"
            variant="outline"
            :disabled="busy"
            @click="selectImported(false)"
            >Select none</UButton
          >
        </div>
        <form v-if="loaded" id="registry-settings" class="space-y-3" @submit.prevent="save">
          <p v-if="0 === rows.length" class="text-sm text-muted">No registry logins saved.</p>
          <article
            v-for="row in rows"
            :key="row.key"
            class="app-card flex flex-col gap-3 p-4 sm:flex-row sm:items-center"
          >
            <UCheckbox
              v-if="row.imported"
              v-model="row.selected"
              :aria-label="`Include imported login for ${row.registry}`"
              :disabled="busy"
            />
            <div class="min-w-0 flex-1">
              <p class="wrap-break-word text-sm font-semibold text-highlighted">
                {{ row.label || row.registry }}
              </p>
              <p v-if="row.label" class="mt-1 break-all text-xs text-muted">{{ row.registry }}</p>
              <p v-if="row.username" class="mt-1 break-all text-xs text-muted">
                {{ row.username }}
              </p>
              <p v-if="row.message" class="mt-1 text-xs text-muted">{{ row.message }}</p>
            </div>
            <div class="flex shrink-0 flex-wrap items-center justify-end gap-2">
              <UButton
                type="button"
                size="sm"
                color="neutral"
                variant="outline"
                icon="i-lucide-pencil"
                :disabled="busy || (row.imported && !row.selected)"
                @click="edit(row)"
                >Edit</UButton
              >
              <UButton
                type="button"
                size="sm"
                color="error"
                variant="outline"
                icon="i-lucide-trash-2"
                :disabled="busy"
                @click="remove(row.key)"
                >Remove</UButton
              >
            </div>
          </article>
        </form>
      </div>
    </template>
    <template v-else-if="removing" #body>
      <p class="text-sm text-default">
        Remove the login for {{ removing.registry }}? Save to apply this change.
      </p>
    </template>
    <template v-else #body>
      <UAlert v-if="error" color="error" :title="error" />
      <form v-if="editing" id="registry-editor" class="space-y-4" @submit.prevent="apply">
        <UFormField
          label="Registry address"
          :name="`registry-${editing.key}`"
          help="Use a hostname such as ghcr.io or registry.example.com:5000."
          required
        >
          <UInput
            v-model="editing.registry"
            icon="i-lucide-link"
            placeholder="docker.io or registry.example.com:5000"
            required
            class="w-full"
            :disabled="busy"
          />
        </UFormField>
        <UFormField
          label="Label"
          :name="`label-${editing.key}`"
          help="Optional display name for this login."
        >
          <UInput v-model="editing.label" icon="i-lucide-tag" class="w-full" :disabled="busy" />
        </UFormField>
        <UFormField
          v-if="showLogin(editing.auth)"
          label="Username"
          :name="`username-${editing.key}`"
        >
          <UInput
            v-model="editing.username"
            icon="i-lucide-user-round"
            autocomplete="off"
            class="w-full"
            :disabled="busy"
          />
        </UFormField>
        <UFormField
          v-if="showLogin(editing.auth)"
          label="Secret"
          :name="`secret-${editing.key}`"
          :help="
            editing.has_auth
              ? 'Leave blank to keep the saved secret.'
              : 'Use your registry password or an access token with permission to pull images.'
          "
        >
          <UInput
            v-model="editing.password"
            icon="i-lucide-key-round"
            :type="editing.showSecret ? 'text' : 'password'"
            autocomplete="new-password"
            :placeholder="editing.has_auth ? '•••••••• (saved)' : undefined"
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
                :icon="editing.showSecret ? 'i-lucide-eye-off' : 'i-lucide-eye'"
                :aria-label="editing.showSecret ? 'Hide secret' : 'Show secret'"
                :aria-pressed="editing.showSecret"
                :disabled="busy"
                @click="editing.showSecret = !editing.showSecret"
              />
            </template>
          </UInput>
        </UFormField>
        <UFormField
          v-for="field in extraFields(editing.auth)"
          :key="field"
          :label="field"
          :name="`${field}-${editing.key}`"
          :help="
            editing.has_auth && secretField(field)
              ? 'Leave blank to keep the saved value.'
              : undefined
          "
        >
          <UInput
            v-model="editing.auth[field]"
            :icon="
              'email' === field
                ? 'i-lucide-mail'
                : 'serveraddress' === field
                  ? 'i-lucide-link'
                  : 'i-lucide-key-round'
            "
            :type="secretField(field) && !editing.showSecret ? 'password' : 'text'"
            :placeholder="editing.has_auth && secretField(field) ? '•••••••• (saved)' : undefined"
            autocomplete="off"
            class="w-full"
            :disabled="busy"
          />
        </UFormField>
        <UFormField
          label="Background update checks"
          :name="`background-checks-${editing.key}`"
          help="Allow scheduled update checks to use this login."
        >
          <USwitch v-model="editing.background_checks" :disabled="busy" />
        </UFormField>
      </form>
    </template>
    <template v-if="!editing && !removing" #footer>
      <div class="flex w-full flex-wrap items-center justify-between gap-2">
        <div class="flex flex-wrap items-center gap-2">
          <UButton
            type="button"
            size="sm"
            color="neutral"
            variant="outline"
            icon="i-lucide-refresh-cw"
            :disabled="busy"
            :loading="loading"
            @click="load"
            >Refresh</UButton
          >
          <UButton
            type="button"
            size="sm"
            color="neutral"
            variant="outline"
            icon="i-lucide-file-input"
            :disabled="busy || !loaded"
            :loading="importing"
            @click="fileInput?.click()"
            >Import config.json</UButton
          >
        </div>
        <div class="flex flex-wrap items-center justify-end gap-2">
          <UButton
            type="button"
            size="sm"
            color="neutral"
            variant="outline"
            icon="i-lucide-plus"
            :disabled="busy || !loaded"
            @click="add"
            >Add login</UButton
          >
          <UButton
            type="submit"
            form="registry-settings"
            size="sm"
            icon="i-lucide-save"
            :loading="saving"
            :disabled="busy || !loaded"
            >Save</UButton
          >
        </div>
      </div>
    </template>
    <template v-else-if="removing" #footer>
      <div class="flex w-full items-center justify-between gap-2">
        <UButton
          color="neutral"
          variant="outline"
          size="sm"
          icon="i-lucide-arrow-left"
          :disabled="busy"
          @click="removing = null"
          >Back</UButton
        >
        <UButton
          color="error"
          size="sm"
          icon="i-lucide-trash-2"
          :disabled="busy"
          @click="confirmRemove(removing.key)"
          >Remove</UButton
        >
      </div>
    </template>
    <template v-else #footer>
      <div class="flex w-full items-center justify-between gap-2">
        <UButton
          type="button"
          color="neutral"
          variant="outline"
          size="sm"
          icon="i-lucide-arrow-left"
          :disabled="busy"
          @click="editing = null"
          >Back</UButton
        >
        <UButton
          type="submit"
          form="registry-editor"
          size="sm"
          icon="i-lucide-save"
          :disabled="busy"
        >
          Save
        </UButton>
      </div>
    </template>
  </UModal>
</template>
