<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <PageHeader :title="t('admin.sso.title')" :description="t('admin.sso.description')">
      <template #actions>
        <AppButton size="sm" @click="openAddDrawer">
          <PlusIcon class="w-4 h-4" aria-hidden="true" />
          {{ t('admin.sso.add') }}
        </AppButton>
      </template>
    </PageHeader>

    <div v-if="loading" class="text-muted">{{ t('common.loading') }}</div>

    <div v-else-if="loadError" class="text-danger" role="alert">{{ loadError }}</div>

    <div v-else-if="providers.length === 0" class="flex flex-col items-center justify-center py-12">
      <ShieldCheckIcon class="w-12 h-12 text-muted mb-4" aria-hidden="true" />
      <h3 class="text-lg font-semibold text-foreground mb-2">{{ t('admin.sso.empty') }}</h3>
      <p class="text-sm text-muted mb-6">{{ t('adm.sso.emptyHint') }}</p>
      <AppButton size="sm" @click="openAddDrawer">
        <PlusIcon class="w-4 h-4" aria-hidden="true" />
        {{ t('admin.sso.add') }}
      </AppButton>
      <DocsLink :url="docsUrl" class="mt-3">{{ t('docs.sso') }}</DocsLink>
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
            <span class="font-mono text-micro px-1.5 py-0.5 rounded bg-surface-hover text-muted">
              {{ provider.kind.toUpperCase() }}
            </span>
          </div>

          <p class="text-caption font-mono text-muted mb-3">{{ provider.slug }}</p>

          <div class="flex items-center justify-between mb-4">
            <span
              class="inline-flex items-center px-2 py-1 rounded-full text-xs font-medium"
              :class="provider.enabled ? 'bg-success/20 text-success' : 'bg-muted/20 text-muted'"
            >
              {{ provider.enabled ? t('adm.sso.enabled') : t('adm.sso.disabled') }}
            </span>
            <p class="text-caption text-muted" :class="testResults[provider.slug]?.ok === false ? 'text-danger' : ''">
              <template v-if="testResults[provider.slug]">
                {{ testResults[provider.slug].ok ? t('adm.sso.testOkAt') : t('adm.sso.testFailedAt') }}
                {{ testResults[provider.slug].at }}
              </template>
              <template v-else>{{ t('adm.sso.updated') }} {{ timeAgo(provider.updated_at, { maxDays: 7 }) }}</template>
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
            <AppButton variant="secondary" size="xs" :loading="testing === provider.slug" @click="testProvider(provider.slug)">
              <BoltIcon class="w-3.5 h-3.5" aria-hidden="true" />
              {{ t('adm.sso.test') }}
            </AppButton>
            <AppButton variant="secondary" size="xs" @click="openEditDrawer(provider)">
              <PencilIcon class="w-3.5 h-3.5" aria-hidden="true" />
              {{ t('common.edit') }}
            </AppButton>
            <AppToggle
              class="ml-auto"
              size="sm"
              :model-value="provider.enabled"
              :label="`${provider.enabled ? t('adm.sso.disable') : t('adm.sso.enable')}: ${provider.display_name}`"
              @update:model-value="toggleEnabled(provider)"
            />
            <AppButton
              variant="danger-outline"
              size="xs"
              :title="t('common.delete')"
              :aria-label="`${t('common.delete')}: ${provider.display_name}`"
              @click="deleteTarget = provider"
            >
              <TrashIcon class="w-3.5 h-3.5" aria-hidden="true" />
            </AppButton>
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
            class="w-full px-3 py-2 bg-bg border border-border-strong rounded-lg text-sm text-foreground placeholder-muted focus:ring-2 focus:ring-primary focus:border-primary"
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
            class="w-full px-3 py-2 bg-bg border border-border-strong rounded-lg text-sm font-mono text-foreground placeholder-muted focus:ring-2 focus:ring-primary focus:border-primary disabled:opacity-50 disabled:cursor-not-allowed"
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
              class="flex-1 px-3 py-2 rounded-lg text-sm font-medium transition-colors disabled:cursor-not-allowed border"
              :class="form.kind === kind
                ? 'bg-primary text-primary-contrast border-primary'
                : 'bg-surface-hover text-foreground border-border-strong hover:bg-border disabled:opacity-50'"
            >
              {{ kind.toUpperCase() }}
            </button>
          </div>
        </fieldset>

        <template v-if="form.kind === 'oidc'">
          <div>
            <label for="oidc-type" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.type') }}</label>
            <select id="oidc-type" v-model="form.oidc_config.type" :class="inputClass">
              <option value="">{{ t('adm.sso.type.oidc') }}</option>
              <option value="github">{{ t('adm.sso.type.github') }}</option>
              <option value="oauth2">{{ t('adm.sso.type.oauth2') }}</option>
            </select>
          </div>
          <div v-if="form.oidc_config.type === ''">
            <label for="issuer" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.issuer') }}</label>
            <input
              id="issuer"
              v-model="form.oidc_config.issuer"
              type="url"
              required
              :class="inputClass"
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
              :class="inputClass"
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
              :class="inputClass"
              placeholder="••••••••••••••••"
            />
            <p v-if="editingProvider" class="text-xs text-muted mt-1">{{ t('adm.sso.field.secretEditHint') }}</p>
          </div>
          <div v-for="field in oidcExtraFields" :key="field.key">
            <label :for="`oidc-${field.key}`" class="block text-sm font-medium text-foreground mb-2">{{ field.label }}</label>
            <input
              :id="`oidc-${field.key}`"
              v-model="form.oidc_config[field.key]"
              :type="field.url ? 'url' : 'text'"
              :required="field.required"
              :class="inputClass"
            />
            <p v-if="field.hint" class="text-xs text-muted mt-1">{{ field.hint }}</p>
          </div>
          <div class="flex items-center gap-3">
            <AppToggle v-model="form.oidc_config.require_email_verified" labelledby="sso-emailverified-label" />
            <span id="sso-emailverified-label" class="text-sm font-medium text-foreground">{{ t('adm.sso.field.requireEmailVerified') }}</span>
          </div>
          <div v-if="form.oidc_config.type === ''" class="flex items-center gap-3">
            <AppToggle v-model="form.oidc_config.graph_groups" labelledby="sso-graphgroups-label" />
            <span id="sso-graphgroups-label" class="text-sm font-medium text-foreground">{{ t('adm.sso.field.graphGroups') }}</span>
          </div>
        </template>

        <template v-else>
          <div>
            <label for="idp-metadata-url" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.metadataUrl') }}</label>
            <input
              id="idp-metadata-url"
              v-model="form.saml_config.idp_metadata_url"
              type="url"
              class="w-full px-3 py-2 bg-bg border border-border-strong rounded-lg text-sm font-mono text-foreground placeholder-muted focus:ring-2 focus:ring-primary focus:border-primary"
              placeholder="https://idp.example.com/metadata.xml"
            />
          </div>
          <div>
            <label for="idp-metadata-xml" class="block text-sm font-medium text-foreground mb-2">{{ t('adm.sso.field.metadataXml') }}</label>
            <textarea
              id="idp-metadata-xml"
              v-model="form.saml_config.idp_metadata_xml"
              rows="5"
              class="w-full px-3 py-2 bg-bg border border-border-strong rounded-lg text-sm font-mono text-foreground placeholder-muted focus:ring-2 focus:ring-primary focus:border-primary resize-none"
            />
          </div>
        </template>

        <div class="flex items-center gap-3">
          <AppToggle v-model="form.auto_provision_users" labelledby="sso-autoprovision-label" />
          <span id="sso-autoprovision-label" class="text-sm font-medium text-foreground">{{ t('adm.sso.field.autoProvision') }}</span>
        </div>

        <div class="flex items-center gap-3">
          <AppToggle v-model="form.link_by_email" labelledby="sso-linkemail-label" />
          <span id="sso-linkemail-label" class="text-sm font-medium text-foreground">{{ t('adm.sso.field.linkByEmail') }}</span>
        </div>
      </form>
      <template #footer>
        <AppButton variant="secondary" @click="closeDrawer">{{ t('common.cancel') }}</AppButton>
        <AppButton type="submit" form="sso-form" :loading="saving">
          {{ saving ? t('common.saving') : t('common.save') }}
        </AppButton>
      </template>
    </AdminDialog>

    <ConfirmDialog
      :open="!!deleteTarget"
      :title="t('adm.sso.confirmDelete')"
      :description="deleteTarget?.display_name"
      :confirm-label="t('common.delete')"
      :cancel-label="t('common.cancel')"
      :busy="deleting"
      @confirm="deleteProvider"
      @close="deleteTarget = null"
    />
  </div>
</template>

<script setup lang="ts">
import PageHeader from '../../components/PageHeader.vue'
import { ref, computed, onMounted } from 'vue'
import {
  BoltIcon,
  PencilIcon,
  TrashIcon,
  PlusIcon,
  ShieldCheckIcon,
} from '@heroicons/vue/24/outline'
import { ApiError } from '../../api/client'
import {
  deleteSSOProvider,
  listAdminSSOProviders,
  saveSSOProvider,
  testSSOProvider,
  updateSSOProvider,
  type SSOConfigInput,
  type SSOProvider,
} from '../../api/sso'
import {
  buildOIDCPayload, oidcFormFrom, emptyOIDCForm, type OIDCTextKey,
} from '../../lib/ssoOidcForm'
import AppButton from '../../components/AppButton.vue'
import AppToggle from '../../components/AppToggle.vue'
import DocsLink from '../../components/DocsLink.vue'
import ConfirmDialog from '../../components/ConfirmDialog.vue'
import AdminDialog from '../../components/AdminDialog.vue'
import { useDocsLink } from '../../composables/useDocsLink'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'
import { formatTime, timeAgo } from '../../lib/datetime'

interface TestOutcome {
  ok: boolean
  message?: string
  at: string
}

const toasts = useToastStore()
const docsUrl = useDocsLink('admin-sso')

const providers = ref<SSOProvider[]>([])
const loading = ref(true)
const loadError = ref('')
const drawerOpen = ref(false)
const editingProvider = ref<SSOProvider | null>(null)
const saving = ref(false)
const deleteTarget = ref<SSOProvider | null>(null)
const deleting = ref(false)
const testing = ref<string | null>(null)
// Results of tests run in this session (the API does not persist them).
const testResults = ref<Record<string, TestOutcome>>({})

interface ExtraField {
  key: OIDCTextKey
  label: string
  hint?: string
  url?: boolean
  required?: boolean
}

const inputClass =
  'w-full px-3 py-2 bg-bg border border-border-strong rounded-lg text-sm font-mono text-foreground placeholder-muted focus:ring-2 focus:ring-primary focus:border-primary'

// The settings that apply to the selected provider type, after the common ones.
const oidcExtraFields = computed<ExtraField[]>(() => {
  const scopes: ExtraField = { key: 'scopes', label: t('adm.sso.field.scopes'), hint: t('adm.sso.field.scopesHint') }
  switch (form.value.oidc_config.type) {
    case 'github':
      return [
        scopes,
        { key: 'allowed_orgs', label: t('adm.sso.field.allowedOrgs'), hint: t('adm.sso.field.allowedOrgsHint'), required: true },
        { key: 'auth_base_url', label: t('adm.sso.field.authBaseUrl'), hint: t('adm.sso.field.authBaseUrlHint'), url: true },
        { key: 'api_base_url', label: t('adm.sso.field.apiBaseUrl'), hint: t('adm.sso.field.apiBaseUrlHint'), url: true },
      ]
    case 'oauth2':
      return [
        scopes,
        { key: 'authorization_url', label: t('adm.sso.field.authorizationUrl'), url: true, required: true },
        { key: 'token_url', label: t('adm.sso.field.tokenUrl'), url: true, required: true },
        { key: 'userinfo_url', label: t('adm.sso.field.userinfoUrl'), url: true, required: true },
      ]
    default:
      return [
        scopes,
        { key: 'allowed_tenants', label: t('adm.sso.field.allowedTenants'), hint: t('adm.sso.field.allowedTenantsHint') },
        { key: 'hosted_domain', label: t('adm.sso.field.hostedDomain'), hint: t('adm.sso.field.hostedDomainHint') },
        { key: 'graph_endpoint', label: t('adm.sso.field.graphEndpoint'), hint: t('adm.sso.field.graphEndpointHint'), url: true },
      ]
  }
})

function emptyForm() {
  return {
    display_name: '',
    slug: '',
    kind: 'oidc' as 'oidc' | 'saml',
    oidc_config: emptyOIDCForm(),
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
    providers.value = await listAdminSSOProviders()
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
    oidc_config: oidcFormFrom(provider.oidc_config),
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
// stored client secret cannot be read back: leaving it blank keeps it (the
// server merges the patch) unless the type, issuer or client ID changed.
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
  if (f.oidc_config.client_secret || !editingProvider.value) {
    config.oidc_config = buildOIDCPayload(f.oidc_config, true)
    return config
  }
  // The stored secret cannot be read back and the server keeps it when it is
  // omitted, but pointing the stored secret at another issuer/client is refused.
  const changed =
    f.oidc_config.type !== (existing?.type ?? '') ||
    f.oidc_config.issuer !== (existing?.issuer ?? '') ||
    f.oidc_config.client_id !== (existing?.client_id ?? '')
  if (changed) {
    toasts.error(t('adm.sso.secretRequired'))
    return null
  }
  config.oidc_config = buildOIDCPayload(f.oidc_config, false)
  return config
}

async function saveProvider(): Promise<void> {
  if (saving.value) return
  const config = buildConfig()
  if (!config) return
  saving.value = true
  try {
    if (editingProvider.value) {
      await updateSSOProvider(editingProvider.value.slug, { display_name: form.value.display_name, ...config })
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
    await updateSSOProvider(provider.slug, { enabled: next })
    provider.enabled = next
    toasts.success(next ? t('adm.sso.providerEnabled') : t('adm.sso.providerDisabled'))
  } catch (err) {
    toasts.error(errMessage(err, t('adm.sso.updateFailed')))
  }
}

async function deleteProvider(): Promise<void> {
  const provider = deleteTarget.value
  if (!provider) return
  deleting.value = true
  try {
    await deleteSSOProvider(provider.slug)
    toasts.success(t('adm.sso.deleted'))
    await loadProviders()
  } catch (err) {
    toasts.error(errMessage(err, t('adm.sso.deleteFailed')))
  } finally {
    deleting.value = false
    deleteTarget.value = null
  }
}


onMounted(loadProviders)
</script>
