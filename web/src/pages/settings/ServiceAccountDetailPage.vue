<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- `/settings/service-accounts/:id`: one service account with its description, tokens, roles and trust policies. -->
<template>
  <div>
    <PageHeader :title="account?.name ?? t('sa.detail.loading')" :crumbs="crumbs">
      <template v-if="account" #meta>
        <StatusBadge :label="account.disabled ? t('sa.statusDisabled') : t('sa.statusActive')" :variant="account.disabled ? 'danger' : 'success'" />
      </template>
      <template v-if="account" #actions>
        <AppButton variant="secondary" size="sm" class="max-md:h-11" :loading="toggling" data-testid="account-toggle" @click="toggle">
          <component :is="account.disabled ? CheckIcon : NoSymbolIcon" class="h-4 w-4" aria-hidden="true" />
          {{ account.disabled ? t('sa.enable') : t('sa.disable') }}
        </AppButton>
        <AppButton variant="danger-outline" size="sm" class="max-md:h-11" data-testid="account-delete" @click="confirmingDelete = true">
          <TrashIcon class="h-4 w-4" aria-hidden="true" />
          {{ t('common.delete') }}
        </AppButton>
      </template>
    </PageHeader>

    <StateBlock v-if="error" :variant="notFound ? 'empty' : 'error'" :title="notFound ? t('sa.detail.notFound') : undefined" :hint="error">
      <template #action>
        <AppButton variant="secondary" :to="LIST_PATH">{{ t('sa.detail.back') }}</AppButton>
      </template>
    </StateBlock>

    <div v-else-if="account" :key="account.id" class="space-y-6">
      <p v-if="account.disabled" role="status" class="rounded-lg border border-warning bg-warning-soft px-4 py-2 text-sm text-foreground" data-testid="account-disabled-note">
        {{ t('sa.detail.disabledNote') }}
      </p>

      <CardSection :title="t('sa.detail.details')">
        <form class="flex flex-col gap-4 p-5" novalidate @submit.prevent="saveDescription">
          <FormField :label="t('sa.field.description')" id="sa-detail-description" :hint="t('sa.field.descriptionHint')">
            <input id="sa-detail-description" v-model="description" type="text" maxlength="255" autocomplete="off" :class="INPUT_CLASS" />
          </FormField>
          <dl class="flex flex-wrap gap-x-8 gap-y-1 text-xs text-muted">
            <div class="flex gap-1.5"><dt>{{ t('sa.col.created') }}</dt><dd class="text-foreground">{{ formatDateTime(account.created_at) }}</dd></div>
            <div class="flex gap-1.5"><dt>{{ t('sa.detail.updated') }}</dt><dd class="text-foreground">{{ formatDateTime(account.updated_at) }}</dd></div>
          </dl>
          <div>
            <AppButton type="submit" size="sm" class="max-md:h-11" :loading="saving" :disabled="description.trim() === (account.description ?? '')">{{ t('common.save') }}</AppButton>
          </div>
        </form>
      </CardSection>

      <AccountTokensPanel :account-id="account.id" />

      <div class="grid items-start gap-6 lg:grid-cols-2">
        <AccountRolesPanel :account-id="account.id" />
        <AccountGroupsPanel :account-id="account.id" />
      </div>
      <AccountProjectsPanel :account-id="account.id" />

      <section class="space-y-3" :aria-label="t('sa.tab.policies')">
        <div>
          <h2 class="text-sm font-semibold text-foreground">{{ t('sa.tab.policies') }}</h2>
          <p class="text-caption text-muted">{{ t('sa.detail.policiesDesc') }}</p>
        </div>
        <OidcPoliciesPanel :accounts="[account]" :account-id="account.id" />
      </section>
    </div>

    <ConfirmDialog
      :open="confirmingDelete"
      :title="t('sa.confirmDelete')"
      :description="account ? tp('sa.confirmDeleteBody', { name: account.name }) : ''"
      :confirm-label="t('common.delete')"
      :busy="deleting"
      @confirm="confirmDelete"
      @close="confirmingDelete = false"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { CheckIcon, NoSymbolIcon, TrashIcon } from '@heroicons/vue/20/solid'
import { ApiError } from '../../api/client'
import { deleteServiceAccount, getServiceAccount, updateServiceAccount, type ServiceAccountItem } from '../../api/serviceAccounts'
import AppButton from '../../components/AppButton.vue'
import ConfirmDialog from '../../components/ConfirmDialog.vue'
import FormField from '../../components/FormField.vue'
import PageHeader from '../../components/PageHeader.vue'
import StatusBadge from '../../components/StatusBadge.vue'
import CardSection from '../../components/ui/CardSection.vue'
import StateBlock from '../../components/ui/StateBlock.vue'
import AccountGroupsPanel from '../../components/serviceAccounts/AccountGroupsPanel.vue'
import AccountProjectsPanel from '../../components/serviceAccounts/AccountProjectsPanel.vue'
import AccountRolesPanel from '../../components/serviceAccounts/AccountRolesPanel.vue'
import AccountTokensPanel from '../../components/serviceAccounts/AccountTokensPanel.vue'
import OidcPoliciesPanel from '../../components/serviceAccounts/OidcPoliciesPanel.vue'
import type { Crumb } from '../../lib/crumbs'
import { formatDateTime } from '../../lib/datetime'
import { INPUT_CLASS } from '../../lib/formClasses'
import { settingsSectionTitle } from '../../lib/settingsSections'
import { useToastStore } from '../../stores/toast'
import { t, tp } from '../../i18n'

const LIST_PATH = '/settings/service-accounts'

const route = useRoute()
const router = useRouter()
const toasts = useToastStore()

const account = ref<ServiceAccountItem | null>(null)
const description = ref('')
const error = ref('')
const notFound = ref(false)

const crumbs = computed<Crumb[]>(() => [
  { label: t('ui.settings.hub'), to: '/settings' },
  { label: settingsSectionTitle('service-accounts'), to: LIST_PATH },
  { label: account.value?.name ?? t('sa.detail.loading') },
])

async function load(id: string | undefined): Promise<void> {
  // Leaving the page (to the list) clears the param before the page is unmounted: nothing to load then.
  if (!id) return
  error.value = ''
  notFound.value = false
  account.value = null
  try {
    account.value = await getServiceAccount(id)
    description.value = account.value.description ?? ''
  } catch (err) {
    notFound.value = err instanceof ApiError && err.status === 404
    error.value = err instanceof Error ? err.message : t('sa.error.failed')
  }
}
watch(() => route.params.id as string | undefined, load, { immediate: true })

function adopt(updated: ServiceAccountItem): void {
  account.value = updated
  description.value = updated.description ?? ''
}

const saving = ref(false)
async function saveDescription(): Promise<void> {
  if (!account.value || saving.value) return
  saving.value = true
  try {
    adopt(await updateServiceAccount(account.value.id, { description: description.value.trim() }))
    toasts.success(t('sa.detail.saved'))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sa.error.failed'))
  } finally {
    saving.value = false
  }
}

// Reversible: a toast, no confirmation.
const toggling = ref(false)
async function toggle(): Promise<void> {
  const current = account.value
  if (!current || toggling.value) return
  toggling.value = true
  try {
    adopt(await updateServiceAccount(current.id, { disabled: !current.disabled }))
    toasts.success(tp(current.disabled ? 'sa.enabledToast' : 'sa.disabledToast', { name: current.name }))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sa.error.failed'))
  } finally {
    toggling.value = false
  }
}

const confirmingDelete = ref(false)
const deleting = ref(false)
async function confirmDelete(): Promise<void> {
  const current = account.value
  if (!current) return
  deleting.value = true
  try {
    await deleteServiceAccount(current.id)
    toasts.success(tp('sa.deleted', { name: current.name }))
    confirmingDelete.value = false
    await router.push(LIST_PATH)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sa.error.failed'))
  } finally {
    deleting.value = false
  }
}
</script>
