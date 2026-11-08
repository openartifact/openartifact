<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <SectionActions>
        <AppButton size="sm" @click="openAddDrawer">
          <PlusIcon class="w-4 h-4" aria-hidden="true" />
          {{ t('admin.sso.add') }}
        </AppButton>
      </SectionActions>

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
      <SSOProviderCard
        v-for="provider in providers"
        :key="provider.slug"
        :provider="provider"
        :test-result="testResults[provider.slug]"
        :testing="testing === provider.slug"
        @test="testProvider(provider.slug)"
        @edit="openEditDrawer(provider)"
        @toggle="toggleEnabled(provider)"
        @delete="deleteTarget = provider"
      />
    </div>

    <!-- Add/Edit Drawer -->
    <AdminDialog
      :open="drawerOpen"
      drawer
      :title="editingProvider ? `${t('common.edit')} ${editingProvider.display_name}` : t('admin.sso.add')"
      @close="closeDrawer"
    >
      <SSOProviderForm v-model="form" :editing="editingProvider" @submit="saveProvider" />
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
import SectionActions from '../../../components/SectionActions.vue'
import { ref, onMounted } from 'vue'
import { PlusIcon, ShieldCheckIcon } from '@heroicons/vue/24/outline'
import { ApiError } from '../../../api/client'
import {
  deleteSSOProvider,
  listAdminSSOProviders,
  saveSSOProvider,
  testSSOProvider,
  updateSSOProvider,
  type SSOProvider,
} from '../../../api/sso'
import AppButton from '../../../components/AppButton.vue'
import SSOProviderCard from './SSOProviderCard.vue'
import SSOProviderForm from './SSOProviderForm.vue'
import DocsLink from '../../../components/DocsLink.vue'
import ConfirmDialog from '../../../components/ConfirmDialog.vue'
import AdminDialog from '../../../components/AdminDialog.vue'
import { useDocsLink } from '../../../composables/useDocsLink'
import { useToastStore } from '../../../stores/toast'
import { t } from '../../../i18n'
import { formatTime } from '../../../lib/datetime'
import { buildSSOConfig, emptyForm, formFromProvider, type SSOTestOutcome } from '../../../lib/ssoProviderForm'

const toasts = useToastStore()
const docsUrl = useDocsLink('settings-sso')

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
const testResults = ref<Record<string, SSOTestOutcome>>({})

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
  form.value = formFromProvider(provider)
  drawerOpen.value = true
}

function closeDrawer(): void {
  drawerOpen.value = false
}

const CONFIG_ERRORS = {
  dexConnectorInvalid: 'adm.sso.error.dexConnectorInvalid',
  dexScopesFederated: 'adm.sso.error.dexScopesFederated',
  secretRequired: 'adm.sso.secretRequired',
} as const

async function saveProvider(): Promise<void> {
  if (saving.value) return
  const built = buildSSOConfig(form.value, editingProvider.value)
  if ('error' in built) {
    toasts.error(t(CONFIG_ERRORS[built.error]))
    return
  }
  const { config } = built
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
