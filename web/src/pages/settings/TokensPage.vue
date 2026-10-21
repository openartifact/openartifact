<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <!-- Page Head -->
    <div class="flex items-start justify-between">
      <div>
        <h1 class="text-2xl font-bold tracking-tight">{{ t('settings.tokens') }}</h1>
        <p class="text-sm text-muted mt-1">{{ t('settings.tokens.subtitle') }}</p>
      </div>
      <button
        type="button"
        class="px-4 py-2 text-sm font-medium rounded-lg bg-primary text-primary-contrast hover:opacity-90 transition-opacity flex items-center gap-2"
        @click="createOpen = true"
      >
        <PlusIcon class="w-4 h-4" aria-hidden="true" />
        {{ t('settings.tokens.create') }}
      </button>
    </div>

    <!-- Token List -->
    <div v-if="!loading" class="mt-5">
      <EmptyState v-if="tokens.length === 0" :title="t('settings.tokens.empty')" />
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
            <tr class="text-[11px] font-semibold text-muted bg-surface-hover/30 border-b border-border">
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
                {{ token.created_at ? new Date(token.created_at).toLocaleDateString() : '-' }}
              </td>
              <td class="px-6 py-4 text-sm text-muted">
                {{ token.last_used_at ? new Date(token.last_used_at).toLocaleDateString() : t('settings.tokens.never') }}
              </td>
              <td class="px-6 py-4 text-sm" :class="isExpired(token) ? 'text-danger' : 'text-muted'">
                {{ token.expires_at ? new Date(token.expires_at).toLocaleDateString() : t('settings.tokens.never') }}
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
    <Dialog :open="createOpen" class="relative z-50" @close="closeCreate">
      <div class="fixed inset-0 bg-foreground/30" aria-hidden="true" />
      <div class="fixed inset-y-0 right-0 flex">
        <DialogPanel class="w-screen max-w-md bg-surface border-l border-border shadow-2xl flex flex-col">
          <div class="flex items-center justify-between px-6 py-4 border-b border-border">
            <DialogTitle class="text-lg font-semibold">{{ t('settings.tokens.create') }}</DialogTitle>
            <button
              type="button"
              class="p-1.5 rounded-md text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
              :aria-label="t('common.close')"
              @click="closeCreate"
            >
              <XMarkIcon class="w-5 h-5" aria-hidden="true" />
            </button>
          </div>

          <form id="create-token-form" class="flex-1 overflow-y-auto px-6 py-6 space-y-6" @submit.prevent="submitCreate">
            <div>
              <label for="token-label" class="text-[11px] font-semibold text-muted block mb-2">
                {{ t('settings.tokens.label') }}
              </label>
              <input
                id="token-label"
                v-model="newToken.label"
                type="text"
                required
                :placeholder="t('settings.tokens.labelPlaceholder')"
                class="w-full h-10 px-3 rounded-lg bg-surface-hover/40 border border-border text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent transition-colors"
              />
            </div>

            <div>
              <span class="text-[11px] font-semibold text-muted block mb-2">
                {{ t('settings.tokens.kind') }}
              </span>
              <div class="p-3 rounded-lg bg-surface-hover/40 border border-border text-sm text-foreground">
                {{ t('settings.tokens.accessToken') }}
              </div>
            </div>
          </form>

          <div class="flex items-center gap-3 px-6 py-4 border-t border-border">
            <button
              type="button"
              class="flex-1 px-4 py-2 text-sm font-medium rounded-lg border border-border text-foreground hover:bg-surface-hover transition-colors"
              @click="closeCreate"
            >
              {{ t('common.cancel') }}
            </button>
            <button
              type="submit"
              form="create-token-form"
              :disabled="creating"
              class="flex-1 px-4 py-2 text-sm font-medium rounded-lg bg-primary text-primary-contrast hover:opacity-90 transition-opacity disabled:opacity-50"
            >
              {{ t('common.create') }}
            </button>
          </div>
        </DialogPanel>
      </div>
    </Dialog>

    <!-- Token Reveal Dialog -->
    <Dialog :open="revealed !== null" class="relative z-50" @close="revealed = null">
      <div class="fixed inset-0 bg-foreground/30" aria-hidden="true" />
      <div class="fixed inset-0 flex items-center justify-center p-4">
        <DialogPanel class="bg-surface border border-border rounded-lg p-6 w-full max-w-md space-y-4">
          <DialogTitle class="text-lg font-semibold">{{ t('settings.tokens.create') }}</DialogTitle>
          <p class="text-sm text-warning">{{ t('admin.tokens.reveal') }}</p>
          <div class="bg-surface-hover rounded-lg p-3 font-mono text-xs break-all text-foreground flex items-start gap-3">
            <span class="flex-1">{{ revealed?.token }}</span>
            <button
              type="button"
              class="flex-shrink-0 p-1.5 rounded-md text-muted hover:text-foreground hover:bg-surface-hover/60 transition-colors"
              :aria-label="t('common.copy')"
              :title="t('common.copy')"
              @click="copyToken"
            >
              <ClipboardDocumentIcon class="w-4 h-4" aria-hidden="true" />
            </button>
          </div>
          <div class="flex justify-end">
            <button
              type="button"
              class="px-4 py-2 text-sm font-medium rounded-lg bg-primary text-primary-contrast hover:opacity-90 transition-opacity"
              @click="revealed = null"
            >
              {{ t('common.close') }}
            </button>
          </div>
        </DialogPanel>
      </div>
    </Dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { Dialog, DialogPanel, DialogTitle } from '@headlessui/vue'
import { listMyTokens, createMyToken, revokeMyToken, type ApiToken, type CreatedToken } from '../../api/tokens'
import EmptyState from '../../components/EmptyState.vue'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'
import {
  PlusIcon,
  TrashIcon,
  XMarkIcon,
  ClipboardDocumentIcon,
} from '@heroicons/vue/24/outline'

const tokens = ref<ApiToken[]>([])
const loading = ref(false)
const creating = ref(false)
const createOpen = ref(false)
const newToken = ref({ label: '' })
const revealed = ref<CreatedToken | null>(null)
const toasts = useToastStore()

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

async function copyToken(): Promise<void> {
  if (!revealed.value) return
  try {
    await navigator.clipboard.writeText(revealed.value.token ?? '')
    toasts.success(t('settings.tokens.copied'))
  } catch {
    toasts.error(t('settings.tokens.copyFailed'))
  }
}

onMounted(load)
</script>
