<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <div class="flex items-start justify-between flex-wrap gap-3">
      <div>
        <div class="flex items-center gap-2 text-[11px] uppercase tracking-[0.14em] text-muted font-mono">
          <span>{{ t('nav.admin') }}</span>
          <ChevronRightIcon class="w-3 h-3" aria-hidden="true" />
          <span class="text-foreground/70">{{ t('nav.tokens') }}</span>
        </div>
        <h1 class="mt-1 text-2xl font-bold tracking-tight">{{ t('admin.tokens.title') }}</h1>
        <p class="mt-1 text-sm text-muted">{{ t('adm.tokens.description') }}</p>
      </div>
    </div>

    <!-- StatRow -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <span class="w-9 h-9 rounded-lg flex items-center justify-center text-primary bg-primary-soft">
          <KeyIcon class="w-4 h-4" aria-hidden="true" />
        </span>
        <div>
          <div class="text-[10px] uppercase tracking-[0.14em] font-semibold text-muted">{{ t('adm.tokens.total') }}</div>
          <div class="text-xl font-bold tabular-nums">{{ total }}</div>
        </div>
      </div>
    </div>

    <!-- TableCard -->
    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-4 py-3 border-b border-border flex items-center justify-between">
        <h3 class="text-sm font-semibold tracking-tight">{{ t('adm.tokens.list') }}</h3>
        <span class="text-[11px] font-mono text-muted">{{ total }} {{ t('adm.common.total') }}</span>
      </div>
      <div v-if="loading" class="px-4 py-8 text-center text-muted">{{ t('common.loading') }}</div>
      <div v-else-if="loadError" class="px-4 py-8 text-center text-danger" role="alert">{{ loadError }}</div>
      <div v-else-if="tokens.length === 0" class="px-4 py-8 text-center text-muted">{{ t('adm.tokens.empty') }}</div>
      <div v-else class="overflow-x-auto">
        <DataTable :columns="columns" :rows="tokens as Record<string, unknown>[]" row-key="id">
          <template #cell-name="{ value }">
            <span class="font-mono text-[13px] font-medium">{{ value }}</span>
          </template>
          <template #cell-user_id="{ value }">
            <span class="text-sm text-muted font-mono">{{ ownerName(value as string | undefined) }}</span>
          </template>
          <template #cell-created_at="{ value }">
            <span class="text-xs text-muted font-mono">{{ formatDate(value as string) }}</span>
          </template>
          <template #cell-last_used_at="{ value }">
            <span class="text-xs text-muted font-mono">{{ formatDate(value as string) || t('admin.tokens.never') }}</span>
          </template>
          <template #cell-expires_at="{ value }">
            <span :class="['text-xs font-mono', isExpired(value as string) ? 'text-danger' : 'text-muted']">
              {{ formatDate(value as string) || t('admin.tokens.never') }}
            </span>
          </template>
          <template #cell-actions="{ row }">
            <button
              type="button"
              :aria-label="`${t('admin.tokens.revoke')}: ${row.name}`"
              class="inline-flex items-center gap-1.5 px-2 py-1 rounded-md text-[11px] font-medium text-danger hover:bg-danger/10 transition-colors"
              @click="revoke(row.id as string)"
            >
              <TrashIcon class="w-3.5 h-3.5" aria-hidden="true" />
              {{ t('admin.tokens.revoke') }}
            </button>
          </template>
        </DataTable>
      </div>
    </div>

    <!-- Pagination -->
    <div v-if="tokens.length" class="flex items-center justify-between">
      <span class="text-sm text-muted">{{ t('adm.audit.page') }} {{ page }} / {{ pageCount }}</span>
      <div class="flex items-center gap-2">
        <button
          type="button"
          @click="page--"
          :disabled="page <= 1"
          class="px-3 py-1.5 text-sm rounded-lg border border-border text-foreground hover:bg-surface-hover transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {{ t('adm.audit.previous') }}
        </button>
        <button
          type="button"
          @click="page++"
          :disabled="page >= pageCount"
          class="px-3 py-1.5 text-sm rounded-lg border border-border text-foreground hover:bg-surface-hover transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {{ t('adm.audit.next') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import DataTable, { type Column } from '../../components/DataTable.vue'
import { listTokens, revokeToken, type AdminToken } from '../../api/tokens'
import { listAllUsers, type UserItem } from '../../api/admin'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'
import { ChevronRightIcon, KeyIcon, TrashIcon } from '@heroicons/vue/24/outline'

type ApiToken = AdminToken & { id: string }

const columns: Column[] = [
  { key: 'name', label: t('admin.tokens.column.label') },
  { key: 'user_id', label: t('admin.tokens.column.owner') },
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

const pageCount = computed(() => Math.max(1, Math.ceil(total.value / PAGE_SIZE)))

// The token payload carries only the owner's user id; show the username when known.
function ownerName(userId?: string): string {
  if (!userId) return '—'
  return users.value.find((u) => u.id === userId)?.username ?? userId
}

function formatDate(date?: string): string {
  if (!date) return ''
  const d = new Date(date)
  if (Number.isNaN(d.getTime())) return date
  return d.toLocaleDateString(undefined, { year: 'numeric', month: '2-digit', day: '2-digit' })
}

function isExpired(date?: string): boolean {
  if (!date) return false
  const d = new Date(date)
  return !Number.isNaN(d.getTime()) && d < new Date()
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const res = await listTokens({ page: page.value, limit: PAGE_SIZE })
    tokens.value = res.items as ApiToken[]
    total.value = res.total
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : t('adm.tokens.loadFailed')
    toasts.error(loadError.value)
  } finally {
    loading.value = false
  }
}

async function revoke(id: string): Promise<void> {
  if (!window.confirm(t('adm.tokens.confirmRevoke'))) return
  try {
    await revokeToken(id)
    toasts.success(t('adm.tokens.revoked'))
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.tokens.revokeFailed'))
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
