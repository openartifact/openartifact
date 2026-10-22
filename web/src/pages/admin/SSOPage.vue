<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <PageHeader :title="t('admin.sso.title')" :description="t('admin.sso.description')">
      <template #actions>
        <button
          type="button"
          @click="openAddDrawer"
          class="inline-flex items-center gap-2 px-3 py-1.5 bg-primary text-primary-contrast rounded-lg hover:opacity-90 transition-opacity font-medium text-sm"
        >
          <PlusIcon class="w-4 h-4" aria-hidden="true" />
          {{ t('admin.sso.add') }}
        </button>
      </template>
    </PageHeader>

    <div v-if="loading" class="text-muted">{{ t('common.loading') }}</div>

    <div v-else-if="loadError" class="text-danger" role="alert">{{ loadError }}</div>

    <div v-else-if="providers.length === 0" class="flex flex-col items-center justify-center py-12">
      <ShieldCheckIcon class="w-12 h-12 text-muted mb-4" aria-hidden="true" />
      <h3 class="text-lg font-semibold text-foreground mb-2">{{ t('admin.sso.empty') }}</h3>
      <p class="text-sm text-muted mb-6">{{ t('adm.sso.emptyHint') }}</p>
      <button
        type="button"
        @click="openAddDrawer"
        class="inline-flex items-center gap-2 px-3 py-1.5 bg-primary text-primary-contrast rounded-lg hover:opacity-90 transition-opacity font-medium text-sm"
      >
        <PlusIcon class="w-4 h-4" aria-hidden="true" />
        {{ t('admin.sso.add') }}
      </button>
    </div>

    <!-- Provider Cards Grid -->
    <div v-else class="grid gap-3 md:grid-cols-2">
      <div
        v-for="provider in providers"
        :key="provider.slug"
        class="bg-surface rounded-xl border border-border p-5 relative overflow-hidden group"
      >
        <div
          class="absolute left-0 top-0 bottom-0 w-[3px]"
          :class="provider.enabled ? 'bg-success' : 'bg-muted'"
        />
        <div class="pl-2">
          <div class="flex items-start justify-between mb-3">
            <h3 class="font-semibold text-foreground">{{ provider.display_name }}</h3>
            <span class="font-mono text-[10px] px-1.5 py-0.5 rounded bg-surface-hover text-muted">
              {{ provider.kind.toUpperCase() }}
            </span>
          </div>

          <p class="text-[11px] font-mono text-muted mb-3">{{ provider.slug }}</p>

          <div class="flex items-center justify-between mb-4">
            <span
              class="inline-flex items-center px-2 py-1 rounded-full text-xs font-medium"
              :class="provider.enabled ? 'bg-success/20 text-success' : 'bg-muted/20 text-muted'"
            >
              {{ provider.enabled ? t('adm.sso.enabled') : t('adm.sso.disabled') }}
            </span>
            <p class="text-[11px] text-muted" :class="testResults[provider.slug]?.ok === false ? 'text-danger' : ''">
              <template v-if="testResults[provider.slug]">
                {{ testResults[provider.slug].ok ? t('adm.sso.testOkAt') : t('adm.sso.testFailedAt') }}
                {{ testResults[provider.slug].at }}
              </template>
              <template v-else>{{ t('adm.sso.updated') }} {{ formatRelativeDate(provider.updated_at) }}</template>
            </p>
          </div>

          <p
            v-if="testResults[provider.slug]?.ok === false && testResults[provider.slug].message"
            class="mb-3 text-xs text-danger break-words"
            role="alert"
          >
            {{ testResults[provider.slug].message }}
          </p>

          <div class="flex items-center gap-2">
            <button
              type="button"
              @click="testProvider(provider.slug)"
              :disabled="testing === provider.slug"
              class="inline-flex items-center gap-1 px-2 py-1.5 text-xs rounded bg-surface-hover text-foreground hover:bg-border transition-colors disabled:opacity-50"
            >
              <BoltIcon class="w-3.5 h-3.5" aria-hidden="true" />
              {{ t('adm.sso.test') }}
            </button>
            <button
              type="button"
              @click="openEditDrawer(provider)"
              class="inline-flex items-center gap-1 px-2 py-1.5 text-xs rounded bg-surface-hover text-foreground hover:bg-border transition-colors"
            >
              <PencilIcon class="w-3.5 h-3.5" aria-hidden="true" />
              {{ t('common.edit') }}
            </button>
            <button
              type="button"
              role="switch"
              :aria-checked="provider.enabled"
              :aria-label="`${provider.enabled ? t('adm.sso.disable') : t('adm.sso.enable')}: ${provider.display_name}`"
              @click="toggleEnabled(provider)"
              class="ml-auto relative inline-flex h-5 w-9 shrink-0 rounded-full transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary"
              :class="provider.enabled ? 'bg-success' : 'bg-border'"
            >
              <span
                class="inline-block h-4 w-4 rounded-full bg-white shadow transition-transform mt-0.5"
                :class="provider.enabled ? 'translate-x-4' : 'translate-x-0.5'"
              />
            </button>
            <button
              type="button"
              @click="deleteProvider(provider)"
              :title="t('common.delete')"
              :aria-label="`${t('common.delete')}: ${provider.display_name}`"
              class="inline-flex items-center gap-1 px-2 py-1.5 text-xs rounded bg-surface-hover text-danger hover:bg-danger/10 transition-colors"
            >
              <TrashIcon class="w-3.5 h-3.5" aria-hidden="true" />
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- Add/Edit Drawer -->
    <AdminDialog
      :open="drawerOpen"
      drawer
      :title="editingProvider ? `${t('common.edit')} ${editingProvider.display_name}` : t('admin.sso.add')"
      @close="closeDrawer"
    >
      <form id="sso-form" @submit.prevent="saveProvider" class="space-y-5">
        <div>
          <label for="display-name" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.displayName') }}</label>
          <input
            id="display-name"
            v-model="form.display_name"
            type="text"
            required
            class="w-full px-3 py-2 bg-bg border border-border rounded-lg text-sm text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent"
            placeholder="Google Workspace"
          />
        </div>

        <div>
          <label for="slug" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.slug') }}</label>
          <input
            id="slug"
            v-model="form.slug"
            type="text"
            required
            pattern="[a-z0-9_-]{1,64}"
            :disabled="!!editingProvider"
            class="w-full px-3 py-2 bg-bg border border-border rounded-lg text-sm font-mono text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent disabled:opacity-50 disabled:cursor-not-allowed"
            placeholder="google-workspace"
          />
          <p class="text-xs text-muted mt-1">{{ t('adm.sso.field.slugHint') }}</p>
        </div>

        <fieldset>
          <legend class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.kind') }}</legend>
          <div class="flex gap-2" role="radiogroup">
            <button
              v-for="kind in (['oidc', 'saml'] as const)"
              :key="kind"
              type="button"
              role="radio"
              :aria-checked="form.kind === kind"
              @click="form.kind = kind"
              :disabled="!!editingProvider"
              class="flex-1 px-3 py-2 rounded-lg text-sm font-medium transition-colors disabled:cursor-not-allowed"
              :class="form.kind === kind
                ? 'bg-primary text-primary-contrast'
                : 'bg-surface-hover text-foreground hover:bg-border disabled:opacity-50'"
            >
              {{ kind.toUpperCase() }}
            </button>
          </div>
        </fieldset>

        <template v-if="form.kind === 'oidc'">
          <div>
            <label for="issuer" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.issuer') }}</label>
            <input
              id="issuer"
              v-model="form.oidc_config.issuer"
              type="url"
              required
              class="w-full px-3 py-2 bg-bg border border-border rounded-lg text-sm font-mono text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent"
              placeholder="https://accounts.google.com"
            />
          </div>
          <div>
            <label for="client-id" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.clientId') }}</label>
            <input
              id="client-id"
              v-model="form.oidc_config.client_id"
              type="text"
              required
              class="w-full px-3 py-2 bg-bg border border-border rounded-lg text-sm font-mono text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent"
            />
          </div>
          <div>
            <label for="client-secret" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.clientSecret') }}</label>
            <input
              id="client-secret"
              v-model="form.oidc_config.client_secret"
              type="password"
              autocomplete="new-password"
              :required="!editingProvider"
              class="w-full px-3 py-2 bg-bg border border-border rounded-lg text-sm font-mono text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent"
              placeholder="••••••••••••••••"
            />
            <p v-if="editingProvider" class="text-xs text-muted mt-1">{{ t('adm.sso.field.secretEditHint') }}</p>
          </div>
        </template>

        <template v-else>
          <div>
            <label for="idp-metadata-url" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.metadataUrl') }}</label>
            <input
              id="idp-metadata-url"
              v-model="form.saml_config.idp_metadata_url"
              type="url"
              class="w-full px-3 py-2 bg-bg border border-border rounded-lg text-sm font-mono text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent"
              placeholder="https://idp.example.com/metadata.xml"
            />
          </div>
          <div>
            <label for="idp-metadata-xml" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.metadataXml') }}</label>
            <textarea
              id="idp-metadata-xml"
              v-model="form.saml_config.idp_metadata_xml"
              rows="5"
              class="w-full px-3 py-2 bg-bg border border-border rounded-lg text-sm font-mono text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent resize-none"
            />
          </div>
        </template>

        <div class="flex items-center gap-3">
          <button
            type="button"
            role="switch"
            aria-labelledby="sso-autoprovision-label"
            :aria-checked="form.auto_provision_users"
            @click="form.auto_provision_users = !form.auto_provision_users"
            class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary"
            :class="form.auto_provision_users ? 'bg-success' : 'bg-border'"
          >
            <span
              class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform"
              :class="form.auto_provision_users ? 'translate-x-6' : 'translate-x-1'"
            />
          </button>
          <span id="sso-autoprovision-label" class="text-sm font-medium text-foreground">{{ t('adm.sso.field.autoProvision') }}</span>
        </div>

        <div class="flex items-center gap-3">
          <button
            type="button"
            role="switch"
            aria-labelledby="sso-linkemail-label"
            :aria-checked="form.link_by_email"
            @click="form.link_by_email = !form.link_by_email"
            class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary"
            :class="form.link_by_email ? 'bg-success' : 'bg-border'"
          >
            <span
              class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform"
              :class="form.link_by_email ? 'translate-x-6' : 'translate-x-1'"
            />
          </button>
          <span id="sso-linkemail-label" class="text-sm font-medium text-foreground">{{ t('adm.sso.field.linkByEmail') }}</span>
        </div>
      </form>
      <template #footer>
        <button
          type="button"
          @click="closeDrawer"
          class="px-4 py-2 rounded-lg border border-border text-foreground hover:bg-surface-hover transition-colors font-medium text-sm"
        >
          {{ t('common.cancel') }}
        </button>
        <button
          type="submit"
          form="sso-form"
          :disabled="saving"
          class="px-4 py-2 rounded-lg bg-primary text-primary-contrast hover:opacity-90 transition-opacity font-medium text-sm disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {{ saving ? t('common.saving') : t('common.save') }}
        </button>
      </template>
    </AdminDialog>
  </div>
</template>

<script setup lang="ts">
import PageHeader from '../../components/PageHeader.vue'
import { ref, onMounted } from 'vue'
import {
  BoltIcon,
  PencilIcon,
  TrashIcon,
  PlusIcon,
  ShieldCheckIcon,
} from '@heroicons/vue/24/outline'
import { request, ApiError } from '../../api/client'
import { saveSSOProvider, testSSOProvider, type SSOConfigInput } from '../../api/admin'
import AdminDialog from '../../components/AdminDialog.vue'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'
import { formatDate, formatTime } from '../../lib/datetime'

// Response shape of the admin SSO API: the client secret is never returned,
// so `oidc_config.client_secret` is always absent.
interface SSOProvider {
  id: string
  slug: string
  kind: 'oidc' | 'saml'
  display_name: string
  enabled: boolean
  oidc_config?: {
    issuer?: string
    client_id?: string
  }
  saml_config?: {
    idp_metadata_url?: string
    idp_metadata_xml?: string
  }
  auto_provision_users: boolean
  link_by_email: boolean
  created_at: string
  updated_at: string
}

interface TestOutcome {
  ok: boolean
  message?: string
  at: string
}

const toasts = useToastStore()

const providers = ref<SSOProvider[]>([])
const loading = ref(true)
const loadError = ref('')
const drawerOpen = ref(false)
const editingProvider = ref<SSOProvider | null>(null)
const saving = ref(false)
const testing = ref<string | null>(null)
// Results of tests run in this session (the API does not persist them).
const testResults = ref<Record<string, TestOutcome>>({})

function emptyForm() {
  return {
    display_name: '',
    slug: '',
    kind: 'oidc' as 'oidc' | 'saml',
    oidc_config: { issuer: '', client_id: '', client_secret: '' },
    saml_config: { idp_metadata_url: '', idp_metadata_xml: '' },
    auto_provision_users: false,
    link_by_email: false,
  }
}

const form = ref(emptyForm())

function errMessage(err: unknown, fallback: string): string {
  return err instanceof ApiError || err instanceof Error ? err.message : fallback
}

async function loadProviders(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const response = await request<{ providers?: SSOProvider[] }>('/api/v1/admin/sso-providers')
    providers.value = response?.providers ?? []
  } catch (err) {
    loadError.value = errMessage(err, t('adm.sso.loadFailed'))
    toasts.error(loadError.value)
  } finally {
    loading.value = false
  }
}

function openAddDrawer(): void {
  editingProvider.value = null
  form.value = emptyForm()
  drawerOpen.value = true
}

function openEditDrawer(provider: SSOProvider): void {
  editingProvider.value = provider
  form.value = {
    display_name: provider.display_name,
    slug: provider.slug,
    kind: provider.kind,
    oidc_config: {
      issuer: provider.oidc_config?.issuer ?? '',
      client_id: provider.oidc_config?.client_id ?? '',
      client_secret: '',
    },
    saml_config: {
      idp_metadata_url: provider.saml_config?.idp_metadata_url ?? '',
      idp_metadata_xml: provider.saml_config?.idp_metadata_xml ?? '',
    },
    auto_provision_users: provider.auto_provision_users,
    link_by_email: provider.link_by_email,
  }
  drawerOpen.value = true
}

function closeDrawer(): void {
  drawerOpen.value = false
}

// Builds the top-level config fields shared by POST and PATCH. On edit the
// stored client secret cannot be read back, so oidc_config is only sent when
// the secret is re-entered (a PATCH replaces the whole object).
function buildConfig(): Partial<SSOConfigInput> | null {
  const f = form.value
  const config: Partial<SSOConfigInput> = {
    auto_provision_users: f.auto_provision_users,
    link_by_email: f.link_by_email,
  }
  if (f.kind === 'saml') {
    config.saml_config = { ...f.saml_config }
    return config
  }
  const existing = editingProvider.value?.oidc_config
  if (f.oidc_config.client_secret) {
    config.oidc_config = { ...f.oidc_config }
  } else if (editingProvider.value) {
    const changed = f.oidc_config.issuer !== (existing?.issuer ?? '') || f.oidc_config.client_id !== (existing?.client_id ?? '')
    if (changed) {
      toasts.error(t('adm.sso.secretRequired'))
      return null
    }
  } else {
    config.oidc_config = { ...f.oidc_config }
  }
  return config
}

async function saveProvider(): Promise<void> {
  if (saving.value) return
  const config = buildConfig()
  if (!config) return
  saving.value = true
  try {
    if (editingProvider.value) {
      await request<SSOProvider>(
        `/api/v1/admin/sso-providers/${encodeURIComponent(editingProvider.value.slug)}`,
        {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ display_name: form.value.display_name, ...config }),
        },
      )
      toasts.success(t('adm.sso.updatedToast'))
    } else {
      await saveSSOProvider({
        slug: form.value.slug,
        kind: form.value.kind,
        display_name: form.value.display_name,
        ...config,
      })
      toasts.success(t('adm.sso.createdToast'))
    }
    closeDrawer()
    await loadProviders()
  } catch (err) {
    toasts.error(errMessage(err, t('adm.sso.saveFailed')))
  } finally {
    saving.value = false
  }
}

async function testProvider(slug: string): Promise<void> {
  testing.value = slug
  try {
    const result = await testSSOProvider(slug)
    testResults.value = {
      ...testResults.value,
      [slug]: { ok: result.ok, message: result.message ?? result.error, at: formatTime(new Date()) },
    }
    if (result.ok) toasts.success(t('adm.sso.testSucceeded'))
    else toasts.error(result.message || result.error || t('adm.sso.testFailed'))
  } catch (err) {
    toasts.error(errMessage(err, t('adm.sso.testFailed')))
  } finally {
    testing.value = null
  }
}

async function toggleEnabled(provider: SSOProvider): Promise<void> {
  const next = !provider.enabled
  try {
    await request<SSOProvider>(`/api/v1/admin/sso-providers/${encodeURIComponent(provider.slug)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled: next }),
    })
    provider.enabled = next
    toasts.success(next ? t('adm.sso.providerEnabled') : t('adm.sso.providerDisabled'))
  } catch (err) {
    toasts.error(errMessage(err, t('adm.sso.updateFailed')))
  }
}

async function deleteProvider(provider: SSOProvider): Promise<void> {
  if (!window.confirm(`${t('adm.sso.confirmDelete')} "${provider.display_name}"?`)) return
  try {
    await request<void>(`/api/v1/admin/sso-providers/${encodeURIComponent(provider.slug)}`, { method: 'DELETE' })
    toasts.success(t('adm.sso.deleted'))
    await loadProviders()
  } catch (err) {
    toasts.error(errMessage(err, t('adm.sso.deleteFailed')))
  }
}

function formatRelativeDate(dateString: string): string {
  if (!dateString) return '—'
  const date = new Date(dateString)
  const diffMs = Date.now() - date.getTime()
  const diffMins = Math.floor(diffMs / 60000)
  const diffHours = Math.floor(diffMs / 3600000)
  const diffDays = Math.floor(diffMs / 86400000)

  if (diffMins < 1) return t('adm.sso.justNow')
  if (diffMins < 60) return `${diffMins}m`
  if (diffHours < 24) return `${diffHours}h`
  if (diffDays < 7) return `${diffDays}d`

  return formatDate(date)
}

onMounted(loadProviders)
</script>
