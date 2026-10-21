<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <div>
      <div class="flex items-center gap-2 text-[11px] text-muted font-mono">
        <span>{{ repo ? repo : t('nav.admin') }}</span>
        <ChevronRightIcon class="w-3 h-3" aria-hidden="true" />
        <span class="text-foreground/70">{{ t('trash.title') }}</span>
      </div>
      <h1 class="mt-1 text-2xl font-bold tracking-tight">{{ t('trash.title') }}</h1>
      <p class="mt-1 text-sm text-muted">{{ repo ? t('trash.descriptionRepo') : t('trash.description') }}</p>
    </div>

    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-4 py-3 border-b border-border flex items-center justify-between gap-3 flex-wrap">
        <label class="flex items-center gap-2 text-sm text-muted">
          {{ t('trash.filter.kind') }}
          <select
            v-model="kind"
            class="px-2 py-1.5 border border-border rounded-md text-sm bg-bg text-foreground"
            :aria-label="t('trash.filter.kind')"
          >
            <option value="">{{ t('trash.filter.all') }}</option>
            <option v-for="k in kindOptions" :key="k" :value="k">{{ kindLabel(k) }}</option>
          </select>
        </label>
        <span class="text-[11px] font-mono text-muted">{{ total }} {{ t('adm.common.total') }}</span>
      </div>

      <div v-if="loading" class="px-4 py-8 text-center text-muted">{{ t('common.loading') }}</div>
      <div v-else-if="loadError" class="px-4 py-8 text-center text-danger" role="alert">{{ loadError }}</div>
      <div v-else-if="items.length === 0" class="px-4 py-10 text-center">
        <p class="text-foreground font-medium">{{ t('trash.empty') }}</p>
        <p class="mt-1 text-sm text-muted">{{ t('trash.emptyHint') }}</p>
      </div>
      <div v-else class="overflow-x-auto">
        <table class="w-full text-left">
          <thead>
            <tr class="border-b border-border text-xs text-muted">
              <th class="px-4 py-2 font-medium">{{ t('trash.column.type') }}</th>
              <th class="px-4 py-2 font-medium">{{ t('trash.column.name') }}</th>
              <th class="px-4 py-2 font-medium">{{ t('trash.column.version') }}</th>
              <th v-if="!repo" class="px-4 py-2 font-medium">{{ t('trash.column.repository') }}</th>
              <th class="px-4 py-2 font-medium">{{ t('trash.column.size') }}</th>
              <th class="px-4 py-2 font-medium">{{ t('trash.column.deleted') }}</th>
              <th class="px-4 py-2 font-medium">{{ t('trash.column.purge') }}</th>
              <th class="px-4 py-2 font-medium">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in items" :key="`${item.kind}:${item.id}`" class="border-b border-border/50 last:border-0">
              <td class="px-4 py-3 text-sm">
                <FormatBadge v-if="item.format && item.kind !== 'repository'" :format="item.format" size="sm" />
                <span class="ml-1 text-xs text-muted">{{ kindLabel(item.kind) }}</span>
              </td>
              <td class="px-4 py-3 font-mono text-[13px] font-medium break-all">{{ item.name }}</td>
              <td class="px-4 py-3 font-mono text-xs text-muted">{{ item.version || '—' }}</td>
              <td v-if="!repo" class="px-4 py-3 font-mono text-xs">{{ item.repository }}</td>
              <td class="px-4 py-3 text-xs text-muted">{{ item.kind === 'repository' ? '—' : formatSize(item.size_bytes) }}</td>
              <td class="px-4 py-3 text-xs text-muted">
                <div class="font-mono">{{ formatDateTime(item.deleted_at) }}</div>
                <div v-if="item.deleted_by">{{ item.deleted_by }}</div>
              </td>
              <td class="px-4 py-3 text-xs text-muted">{{ purgeInText(item.purge_at) }}</td>
              <td class="px-4 py-3">
                <div class="flex items-center gap-2">
                  <button
                    type="button"
                    class="inline-flex items-center gap-1.5 px-2 py-1 rounded-md text-[11px] font-medium text-foreground border border-border hover:bg-surface-hover disabled:opacity-50"
                    :disabled="busyKey === keyOf(item)"
                    :aria-label="`${t('trash.restore')}: ${item.name}`"
                    @click="restore(item)"
                  >
                    <ArrowUturnLeftIcon class="w-3.5 h-3.5" aria-hidden="true" />
                    {{ t('trash.restore') }}
                  </button>
                  <button
                    v-if="session.isAdmin"
                    type="button"
                    class="inline-flex items-center gap-1.5 px-2 py-1 rounded-md text-[11px] font-medium text-danger hover:bg-danger/10 disabled:opacity-50"
                    :disabled="busyKey === keyOf(item)"
                    :aria-label="`${t('trash.purge')}: ${item.name}`"
                    @click="purgeTarget = item"
                  >
                    <TrashIcon class="w-3.5 h-3.5" aria-hidden="true" />
                    {{ t('trash.purge') }}
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <Pagination v-if="total > 0" :total="total" :page="page" :limit="PAGE_SIZE" @change="page = $event" />
    </div>

    <AdminDialog :open="purgeTarget !== null" :title="t('trash.purgeTitle')" @close="purgeTarget = null">
      <p v-if="purgeTarget" class="text-sm text-foreground">
        {{
          (purgeTarget.kind === 'repository' ? t('trash.purgeRepoBody') : t('trash.purgeBody')).replace(
            '{name}',
            purgeTarget.name,
          )
        }}
      </p>
      <template #footer>
        <button
          type="button"
          class="px-4 py-2 text-sm rounded-md border border-border text-foreground hover:bg-surface-hover"
          @click="purgeTarget = null"
        >{{ t('common.cancel') }}</button>
        <button
          type="button"
          class="px-4 py-2 text-sm rounded-md bg-danger text-white hover:opacity-90 disabled:opacity-50"
          :disabled="busyKey !== ''"
          @click="doPurge"
        >{{ t('trash.purge') }}</button>
      </template>
    </AdminDialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { ArrowUturnLeftIcon, ChevronRightIcon, TrashIcon } from '@heroicons/vue/24/outline'
import AdminDialog from '../components/AdminDialog.vue'
import FormatBadge from '../components/FormatBadge.vue'
import Pagination from '../components/Pagination.vue'
import { listTrash, purgeTrash, restoreTrash, type TrashItem, type TrashKind } from '../api/trash'
import { formatDateTime } from '../lib/datetime'
import { purgeInText } from '../lib/trash'
import { useSessionStore } from '../stores/session'
import { useToastStore } from '../stores/toast'
import { t, tDynamic } from '../i18n/en'

const PAGE_SIZE = 50

const route = useRoute()
const session = useSessionStore()
const toasts = useToastStore()

// Per-repository view when the route carries :name; global admin view otherwise.
const repo = computed(() => (route.params.name ? String(route.params.name) : ''))

const items = ref<TrashItem[]>([])
const total = ref(0)
const page = ref(1)
const kind = ref('')
const loading = ref(false)
const loadError = ref('')
const busyKey = ref('')
const purgeTarget = ref<TrashItem | null>(null)

const ALL_KINDS: TrashKind[] = [
  'repository', 'maven_artifact', 'ivy_artifact', 'sbt_artifact', 'npm_version', 'pypi_package',
  'nuget_package', 'go_module', 'cargo_crate', 'rubygems_gem', 'conda_package', 'conan_package',
  'helm_chart', 'raw_artifact', 'docker_manifest', 'docker_tag', 'rpm_package', 'deb_package',
]
const kindOptions = computed(() => (repo.value ? ALL_KINDS.filter((k) => k !== 'repository') : ALL_KINDS))

const keyOf = (item: TrashItem): string => `${item.kind}:${item.id}`
const kindLabel = (k: string): string => tDynamic(`trash.kind.${k}`)

function formatSize(bytes?: number): string {
  if (!bytes) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let size = bytes
  let i = 0
  while (size >= 1024 && i < units.length - 1) {
    size /= 1024
    i++
  }
  return i === 0 ? `${size} B` : `${size.toFixed(1)} ${units[i]}`
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const res = await listTrash({ page: page.value, limit: PAGE_SIZE, kind: kind.value }, repo.value || undefined)
    items.value = res.items ?? []
    total.value = res.total ?? 0
    // Last item of a page was removed: step back instead of showing an empty page.
    if (items.value.length === 0 && page.value > 1) page.value--
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : t('trash.loadFailed')
  } finally {
    loading.value = false
  }
}

async function restore(item: TrashItem): Promise<void> {
  busyKey.value = keyOf(item)
  try {
    // Repositories are only restorable through the global admin endpoint.
    await restoreTrash(item, item.kind === 'repository' ? undefined : repo.value || undefined)
    toasts.success(t('trash.restored').replace('{name}', item.name))
    await load()
  } catch (err) {
    toasts.error(t('trash.restoreFailed'), err instanceof Error ? err.message : undefined)
  } finally {
    busyKey.value = ''
  }
}

async function doPurge(): Promise<void> {
  const item = purgeTarget.value
  if (!item) return
  busyKey.value = keyOf(item)
  try {
    await purgeTrash(item)
    toasts.success(t('trash.purged').replace('{name}', item.name))
    purgeTarget.value = null
    await load()
  } catch (err) {
    toasts.error(t('trash.purgeFailed'), err instanceof Error ? err.message : undefined)
  } finally {
    busyKey.value = ''
  }
}

watch(kind, () => {
  if (page.value === 1) void load()
  else page.value = 1
})
watch(page, () => void load())
watch(repo, () => {
  kind.value = ''
  page.value = 1
  void load()
})
onMounted(load)
</script>
