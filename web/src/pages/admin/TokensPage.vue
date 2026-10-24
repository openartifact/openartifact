<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <PageHeader :title="t('admin.tokens.title')" :description="t('adm.tokens.description')" />

    <!-- StatRow -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <span class="w-9 h-9 rounded-lg flex items-center justify-center text-primary bg-primary-soft">
          <KeyIcon class="w-4 h-4" aria-hidden="true" />
        </span>
        <div>
          <div class="text-micro font-semibold text-muted">{{ t('adm.tokens.total') }}</div>
          <div class="text-xl font-bold tabular-nums">{{ total }}</div>
        </div>
      </div>
    </div>

    <!-- TableCard -->
    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-4 py-3 border-b border-border flex items-center justify-between">
        <h3 class="text-sm font-semibold tracking-tight">{{ t('adm.tokens.list') }}</h3>
        <span class="text-caption font-mono text-muted">{{ total }} {{ t('adm.common.total') }}</span>
      </div>
      <div v-if="loading" class="px-4 py-8 text-center text-muted">{{ t('common.loading') }}</div>
      <div v-else-if="loadError" class="px-4 py-8 text-center text-danger" role="alert">{{ loadError }}</div>
      <EmptyState
        v-else-if="tokens.length === 0"
        :title="t('adm.tokens.empty')"
        :description="t('adm.tokens.emptyHint')"
        :docs-url="docsUrl"
        :docs-label="t('docs.tokens')"
      />
      <div v-else class="overflow-x-auto">
        <DataTable :columns="columns" :rows="tokens as Record<string, unknown>[]" row-key="id">
          <template #cell-name="{ value }">
            <span class="font-mono text-ui font-medium">{{ value }}</span>
          </template>
          <template #cell-user_id="{ value }">
            <span class="text-sm text-muted font-mono">{{ ownerName(value as string | undefined) }}</span>
          </template>
          <template #cell-project="{ value }">
            <span class="text-sm font-mono text-muted">{{ value || '—' }}</span>
          </template>
          <template #cell-created_by="{ value }">
            <span class="text-sm font-mono text-muted">{{ value || '—' }}</span>
          </template>
          <template #cell-created_at="{ value }">
            <span class="text-xs text-muted font-mono">{{ formatDate(value as string) }}</span>
          </template>
          <template #cell-last_used_at="{ value }">
            <span class="text-xs text-muted font-mono">{{ formatDate(value as string, '') || t('admin.tokens.never') }}</span>
          </template>
          <template #cell-expires_at="{ value }">
            <span :class="['text-xs font-mono', isExpired(value as string) ? 'text-danger' : 'text-muted']">
              {{ formatDate(value as string, '') || t('admin.tokens.never') }}
            </span>
          </template>
          <template #cell-actions="{ row }">
            <AppButton
              variant="danger-outline"
              size="xs"
              :aria-label="`${t('admin.tokens.revoke')}: ${row.name}`"
              @click="revokeTarget = row as unknown as ApiToken"
            >
              <TrashIcon class="w-3.5 h-3.5" aria-hidden="true" />
              {{ t('admin.tokens.revoke') }}
            </AppButton>
          </template>
        </DataTable>
      </div>
    </div>

    <!-- Pagination -->
    <div v-if="tokens.length" class="flex items-center justify-between">
      <span class="text-sm text-muted">{{ t('adm.audit.page') }} {{ page }} / {{ pageCount }}</span>
      <div class="flex items-center gap-2">
        <AppButton variant="secondary" size="sm" :disabled="page <= 1" @click="page--">
          {{ t('adm.audit.previous') }}
        </AppButton>
        <AppButton variant="secondary" size="sm" :disabled="page >= pageCount" @click="page++">
          {{ t('adm.audit.next') }}
        </AppButton>
      </div>
    </div>

    <ConfirmDialog
      :open="revokeTarget !== null"
      :title="t('adm.tokens.confirmRevoke')"
      :description="revokeTarget ? `${revokeTarget.name}` : ''"
      :confirm-label="t('admin.tokens.revoke')"
      :busy="revoking"
      @confirm="confirmRevoke"
      @close="revokeTarget = null"
    />
  </div>
</template>

<script setup lang="ts">
import PageHeader from '../../components/PageHeader.vue'
import { formatDate } from '../../lib/datetime'
import { ref, computed, onMounted, watch } from 'vue'
import AppButton from '../../components/AppButton.vue'
import ConfirmDialog from '../../components/ConfirmDialog.vue'
import DataTable, { type Column } from '../../components/DataTable.vue'
import EmptyState from '../../components/EmptyState.vue'
import { useDocsLink } from '../../composables/useDocsLink'
import { listTokens, revokeToken, type AdminToken } from '../../api/tokens'
import { listAllUsers, type UserItem } from '../../api/admin'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'
import { KeyIcon, TrashIcon } from '@heroicons/vue/24/outline'

type ApiToken = AdminToken & { id: string }

const columns: Column[] = [
  { key: 'name', label: t('admin.tokens.column.label') },
  { key: 'user_id', label: t('admin.tokens.column.owner') },
  { key: 'project', label: t('admin.tokens.column.project') },
  { key: 'created_by', label: t('admin.tokens.column.createdBy') },
  { key: 'created_at', label: t('admin.tokens.column.created') },
  { key: 'last_used_at', label: t('admin.tokens.column.lastUsed') },
  { key: 'expires_at', label: t('admin.tokens.column.expires') },
  { key: 'actions', label: t('common.actions') },
]

const PAGE_SIZE = 25

const tokens = ref<ApiToken[]>([])
const users = ref<UserItem[]>([])
const total = ref(0)
const loading = ref(false)
const loadError = ref('')
const page = ref(1)
const toasts = useToastStore()
const docsUrl = useDocsLink('admin-tokens')

const pageCount = computed(() => Math.max(1, Math.ceil(total.value / PAGE_SIZE)))

// The token payload carries only the owner's user id; show the username when known.
function ownerName(userId?: string): string {
  if (!userId) return '—'
  return users.value.find((u) => u.id === userId)?.username ?? userId
}


function isExpired(date?: string): boolean {
  if (!date) return false
  const d = new Date(date)
  return !Number.isNaN(d.getTime()) && d < new Date()
}

let loadSeq = 0

async function load(): Promise<void> {
  // Fast paging can finish out of order; only the latest request may apply.
  const seq = ++loadSeq
  loading.value = true
  loadError.value = ''
  try {
    const res = await listTokens({ page: page.value, limit: PAGE_SIZE })
    if (seq !== loadSeq) return
    // The last token of the last page was revoked: step back to a page that exists.
    if (res.items.length === 0 && res.total > 0 && page.value > 1) {
      page.value = Math.max(1, Math.ceil(res.total / PAGE_SIZE))
      return
    }
    tokens.value = res.items as ApiToken[]
    total.value = res.total
  } catch (err) {
    if (seq !== loadSeq) return
    loadError.value = err instanceof Error ? err.message : t('adm.tokens.loadFailed')
    toasts.error(loadError.value)
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

const revokeTarget = ref<ApiToken | null>(null)
const revoking = ref(false)

async function confirmRevoke(): Promise<void> {
  const target = revokeTarget.value
  if (!target) return
  revoking.value = true
  try {
    await revokeToken(target.id)
    toasts.success(t('adm.tokens.revoked'))
    revokeTarget.value = null
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.tokens.revokeFailed'))
  } finally {
    revoking.value = false
  }
}

watch(page, () => void load())

onMounted(async () => {
  // Usernames are a convenience; failing to list users must not block the page.
  listAllUsers()
    .then((res) => {
      users.value = res.items
    })
    .catch(() => undefined)
  await load()
})
</script>
