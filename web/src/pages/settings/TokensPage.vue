<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <!-- Page Head -->
    <div class="flex flex-wrap items-center justify-between gap-3">
      <p class="text-sm text-muted">{{ t('settings.tokens.subtitle') }}</p>
      <AppButton variant="primary" size="md" @click="createOpen = true">
        <PlusIcon class="w-4 h-4" aria-hidden="true" />
        {{ t('settings.tokens.create') }}
      </AppButton>
    </div>

    <!-- Token List -->
    <div v-if="!loading" class="mt-5">
      <EmptyState
        v-if="tokens.length === 0"
        :title="t('settings.tokens.empty')"
        :description="t('settings.tokens.emptyHint')"
        :docs-url="docsUrl"
        :docs-label="t('docs.tokens')"
      />
      <div v-else class="bg-surface rounded-xl border border-border overflow-hidden">
        <!-- List Header -->
        <div class="px-6 py-4 border-b border-border">
          <h2 class="text-sm font-semibold text-foreground">
            {{ t('settings.tokens.yourTokens') }}
            <span class="text-muted">({{ tokens.length }})</span>
          </h2>
        </div>

        <!-- Table -->
        <table class="w-full">
          <thead>
            <tr class="text-caption font-semibold text-muted bg-surface-hover/30 border-b border-border">
              <th class="px-6 py-3 text-left">{{ t('settings.tokens.column.label') }}</th>
              <th class="px-6 py-3 text-left">{{ t('settings.tokens.column.created') }}</th>
              <th class="px-6 py-3 text-left">{{ t('settings.tokens.column.lastUsed') }}</th>
              <th class="px-6 py-3 text-left">{{ t('settings.tokens.column.expires') }}</th>
              <th class="px-6 py-3 text-right">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="token in tokens"
              :key="token.id"
              class="border-b border-border hover:bg-surface-hover/40 transition-colors"
            >
              <td class="px-6 py-4">
                <span class="font-mono font-medium">{{ token.name }}</span>
              </td>
              <td class="px-6 py-4 text-sm text-muted">
                {{ token.created_at ? formatDate(token.created_at) : '-' }}
              </td>
              <td class="px-6 py-4 text-sm text-muted">
                {{ token.last_used_at ? formatDate(token.last_used_at) : t('settings.tokens.never') }}
              </td>
              <td class="px-6 py-4 text-sm" :class="isExpired(token) ? 'text-danger' : 'text-muted'">
                {{ token.expires_at ? formatDate(token.expires_at) : t('settings.tokens.never') }}
              </td>
              <td class="px-6 py-4 text-right">
                <button
                  type="button"
                  class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger-soft/40 transition-colors"
                  :aria-label="t('settings.tokens.revoke')"
                  :title="t('settings.tokens.revoke')"
                  @click="revoke(token.id)"
                >
                  <TrashIcon class="w-4 h-4" aria-hidden="true" />
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Loading State -->
    <div v-else class="text-muted">{{ t('common.loading') }}</div>

    <!-- Create Token Dialog -->
    <AdminDialog :open="createOpen" drawer :title="t('settings.tokens.create')" @close="closeCreate">
      <form id="create-token-form" class="space-y-6" @submit.prevent="submitCreate">
        <div>
          <label for="token-label" class="text-caption font-semibold text-muted block mb-2">
            {{ t('settings.tokens.label') }}
          </label>
          <input
            id="token-label"
            v-model="newToken.label"
            type="text"
            required
            :placeholder="t('settings.tokens.labelPlaceholder')"
            class="w-full h-10 px-3 rounded-lg bg-surface-hover/40 border border-border-strong text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent transition-colors"
          />
        </div>

        <div>
          <span class="text-caption font-semibold text-muted block mb-2">
            {{ t('settings.tokens.kind') }}
          </span>
          <div class="p-3 rounded-lg bg-surface-hover/40 border border-border text-sm text-foreground">
            {{ t('settings.tokens.accessToken') }}
          </div>
        </div>
      </form>
      <template #footer>
        <AppButton variant="secondary" @click="closeCreate">
          {{ t('common.cancel') }}
        </AppButton>
        <AppButton variant="primary" type="submit" form="create-token-form" :loading="creating">
          {{ t('common.create') }}
        </AppButton>
      </template>
    </AdminDialog>

    <TokenSecretDialog
      :title="t('settings.tokens.create')"
      :token="revealed?.token ?? null"
      @close="revealed = null"
    />
  </div>
</template>

<script setup lang="ts">
import AppButton from '../../components/AppButton.vue'
import { formatDate } from '../../lib/datetime'
import { ref, onMounted } from 'vue'
import AdminDialog from '../../components/AdminDialog.vue'
import { listMyTokens, createMyToken, revokeMyToken, type ApiToken, type CreatedToken } from '../../api/tokens'
import EmptyState from '../../components/EmptyState.vue'
import TokenSecretDialog from '../../components/TokenSecretDialog.vue'
import { useDocsLink } from '../../composables/useDocsLink'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'
import { PlusIcon, TrashIcon } from '@heroicons/vue/24/outline'

const tokens = ref<ApiToken[]>([])
const loading = ref(false)
const creating = ref(false)
const createOpen = ref(false)
const newToken = ref({ label: '' })
const revealed = ref<CreatedToken | null>(null)
const toasts = useToastStore()
const docsUrl = useDocsLink('settings-tokens')

function isExpired(token: ApiToken): boolean {
  return !!(token.expires_at && new Date(token.expires_at) < new Date())
}

async function load(): Promise<void> {
  loading.value = true
  try {
    const res = await listMyTokens()
    tokens.value = res.items
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('settings.tokens.loadFailed'))
  } finally {
    loading.value = false
  }
}

function closeCreate(): void {
  createOpen.value = false
}

async function submitCreate(): Promise<void> {
  if (creating.value) return
  creating.value = true
  try {
    revealed.value = await createMyToken({ name: newToken.value.label, kind: 'access' })
    createOpen.value = false
    newToken.value = { label: '' }
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('settings.tokens.createFailed'))
  } finally {
    creating.value = false
  }
}

async function revoke(id: string | undefined): Promise<void> {
  if (!id) return
  if (!window.confirm(t('settings.tokens.revokeConfirm'))) return
  try {
    await revokeMyToken(id)
    toasts.success(t('settings.tokens.revoked'))
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('settings.tokens.revokeFailed'))
  }
}

onMounted(load)
</script>
