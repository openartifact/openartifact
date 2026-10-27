<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Deleted items with restore, purge and empty actions. One list for both scopes:
  `repo` set shows that repository's trash (Repository tab and column hidden),
  without it the admin-wide trash with a Repository column. No page header: the
  host page (repository tab, settings section) provides it.
-->
<template>
  <div class="space-y-4">
    <FilterBar :active="query.filtered.value" @clear="query.clear()">
      <label class="flex items-center gap-2 text-sm text-muted">
        {{ t('trash.filter.kind') }}
        <select
          :value="query.filters.value.kind"
          class="h-9 rounded-lg border border-border-strong bg-surface px-2 text-sm text-foreground max-md:h-11"
          :aria-label="t('trash.filter.kind')"
          @change="query.setFilter('kind', ($event.target as HTMLSelectElement).value)"
        >
          <option value="">{{ t('trash.filter.all') }}</option>
          <option v-for="k in kindOptions" :key="k" :value="k">{{ kindLabel(k) }}</option>
        </select>
      </label>
      <template v-if="repo && caps.empty && query.total.value > 0" #after>
        <AppButton variant="danger-outline" size="sm" class="max-md:h-11" :disabled="busyKey !== ''" @click="emptyOpen = true">
          <TrashIcon class="h-4 w-4" aria-hidden="true" />
          {{ t('rl.trash.emptyAction') }}
        </AppButton>
      </template>
    </FilterBar>

    <p v-if="forbidden" role="alert" class="rounded-lg border border-border bg-surface px-4 py-6 text-center text-sm text-muted">
      {{ t('trash.forbidden') }}
    </p>
    <ListTable
      v-else
      :columns="columns"
      :caption="t('rl.trash.caption')"
      :query="query"
      :row-key="keyOf"
      :empty-title="t('trash.empty')"
      :empty-hint="t('trash.emptyHint')"
    >
      <template #cell-name="{ row }">
        <div class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
          <FormatBadge v-if="row.format && row.kind !== 'repository'" :format="row.format" size="sm" />
          <span class="truncate font-mono text-ui font-medium" :title="row.name">{{ row.name }}</span>
          <span class="text-xs text-muted">{{ kindLabel(row.kind) }}</span>
        </div>
      </template>
      <template #cell-version="{ row }"><span class="font-mono text-xs text-muted">{{ row.version || '—' }}</span></template>
      <template #cell-repository="{ row }"><span class="font-mono text-xs">{{ row.repository }}</span></template>
      <template #cell-size="{ row }">
        <span class="text-xs text-muted">{{ row.kind === 'repository' ? '—' : formatBytesOrDash(row.size_bytes) }}</span>
      </template>
      <template #cell-deleted="{ row }">
        <div class="text-xs text-muted">
          <RelativeTime :value="row.deleted_at" />
          <div v-if="row.deleted_by_name">{{ tp('rl.trash.deletedBy', { name: row.deleted_by_name }) }}</div>
        </div>
      </template>
      <template #cell-expires="{ row }">
        <span class="text-xs text-muted" :title="tp('rl.trash.expiresTitle', { date: formatDateTime(row.expires_at) })">{{ purgeInText(row.expires_at) }}</span>
      </template>
      <template #cell-actions="{ row }">
        <RowActions
          :actions="[
            { key: 'restore', label: t('trash.restore'), icon: ArrowUturnLeftIcon, hidden: !restoreAllowed(row), disabled: busyKey === keyOf(row) },
            { key: 'purge', label: t('trash.purge'), icon: TrashIcon, tone: 'danger', hidden: !caps.purge, disabled: busyKey === keyOf(row) },
          ]"
          :subject="row.name"
          @action="(key) => (key === 'restore' ? restore(row) : (purgeTarget = row))"
        />
      </template>
    </ListTable>

    <ConfirmDialog
      :open="purgeTarget !== null"
      :title="t('trash.purgeTitle')"
      :description="purgeDescription"
      :confirm-label="t('trash.purge')"
      :busy="busyKey !== ''"
      @confirm="doPurge"
      @close="purgeTarget = null"
    />
    <ConfirmDialog
      :open="emptyOpen"
      :title="t('trash.emptyTitle')"
      :description="tp('rl.trash.emptyBody', { name: repo ?? '' })"
      :confirm-label="t('rl.trash.emptyAction')"
      :busy="busyKey === 'empty'"
      @confirm="doEmpty"
      @close="emptyOpen = false"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { ArrowUturnLeftIcon, TrashIcon } from '@heroicons/vue/20/solid'
import AppButton from '../AppButton.vue'
import ConfirmDialog from '../ConfirmDialog.vue'
import FormatBadge from '../FormatBadge.vue'
import FilterBar from '../ui/FilterBar.vue'
import RowActions from '../ui/RowActions.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import RelativeTime from '../ui/RelativeTime.vue'
import { ApiError } from '../../api/client'
import { emptyRepoTrash, listTrash, purgeTrash, restoreTrash, type TrashItem, type TrashKind } from '../../api/trash'
import { useListQuery } from '../../composables/useListQuery'
import { strParam } from '../../composables/useUrlState'
import { formatDateTime } from '../../lib/datetime'
import { formatBytesOrDash } from '../../lib/quota'
import { emptyTrashResultText, purgeInText, trashCapabilities, type RepoPermissions } from '../../lib/trash'
import { useSessionStore } from '../../stores/session'
import { useToastStore } from '../../stores/toast'
import { t, tp, tDynamic } from '../../i18n'

const props = defineProps<{
  /** Repository whose trash to show; omit for the admin-wide trash. */
  repo?: string
  /** The caller's permissions on `repo` (drives restore and empty). */
  permissions?: Partial<RepoPermissions> | null
}>()
const emit = defineEmits<{ changed: [] }>()

const session = useSessionStore()
const toasts = useToastStore()

const ALL_KINDS: TrashKind[] = [
  'repository', 'maven_artifact', 'ivy_artifact', 'sbt_artifact', 'npm_version', 'pypi_package',
  'nuget_package', 'go_module', 'cargo_crate', 'rubygems_gem', 'conda_package', 'conan_package',
  'helm_chart', 'raw_artifact', 'docker_manifest', 'docker_tag', 'rpm_package', 'deb_package',
  'terraform_module', 'terraform_provider', 'pacman',
]
const kindOptions = computed(() => (props.repo ? ALL_KINDS.filter((k) => k !== 'repository') : ALL_KINDS))
const kindLabel = (k: string): string => tDynamic(`trash.kind.${k}`)
const keyOf = (item: TrashItem): string => `${item.kind}:${item.id}`

const forbidden = ref(false)
const busyKey = ref('')
const purgeTarget = ref<TrashItem | null>(null)
const emptyOpen = ref(false)

const caps = computed(() => trashCapabilities(props.repo ? 'repo' : 'global', session.isAdmin, props.permissions))
// Repositories are only restorable through the global admin endpoint.
const restoreAllowed = (item: TrashItem): boolean => (item.kind === 'repository' ? session.isAdmin : caps.value.restore)
const purgeDescription = computed(() => {
  const target = purgeTarget.value
  if (!target) return ''
  return tp(target.kind === 'repository' ? 'trash.purgeRepoBody' : 'trash.purgeBody', { name: target.name })
})

const columns = computed<ListColumn[]>(() => [
  { key: 'name', label: t('rl.trash.item'), grow: true },
  { key: 'version', label: t('trash.column.version'), dropOrder: 3 },
  ...(props.repo ? [] : [{ key: 'repository', label: t('trash.column.repository') }]),
  { key: 'size', label: t('trash.column.size'), dropOrder: 1 },
  { key: 'deleted', label: t('trash.column.deleted') },
  { key: 'expires', label: t('rl.trash.expires'), dropOrder: 2 },
  { key: 'actions', label: t('common.actions'), actions: true },
])

const filters = { kind: strParam() }
const query = useListQuery({
  filters,
  limit: 25,
  async fetch(req) {
    forbidden.value = false
    try {
      const res = await listTrash({ page: req.page, limit: req.limit, kind: req.filters.kind }, props.repo || undefined)
      return { rows: res.items ?? [], total: res.total ?? 0 }
    } catch (err) {
      if (err instanceof ApiError && err.status === 403) forbidden.value = true
      throw err
    }
  },
})

async function reload(): Promise<void> {
  await query.reload()
  // The last item of a page was removed: step back instead of showing an empty page.
  if (query.rows.value.length === 0 && query.page.value > 1) query.setPage(query.page.value - 1)
  emit('changed')
}

async function restore(item: TrashItem): Promise<void> {
  busyKey.value = keyOf(item)
  try {
    await restoreTrash(item, item.kind === 'repository' ? undefined : props.repo || undefined)
    toasts.success(tp('trash.restored', { name: item.name }))
    await reload()
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
    toasts.success(tp('trash.purged', { name: item.name }))
    purgeTarget.value = null
    await reload()
  } catch (err) {
    toasts.error(t('trash.purgeFailed'), err instanceof Error ? err.message : undefined)
  } finally {
    busyKey.value = ''
  }
}

async function doEmpty(): Promise<void> {
  if (!props.repo) return
  busyKey.value = 'empty'
  try {
    const res = await emptyRepoTrash(props.repo)
    toasts.success(emptyTrashResultText(res.purged, res.remaining))
    emptyOpen.value = false
    await reload()
  } catch (err) {
    toasts.error(t('trash.emptyFailed'), err instanceof Error ? err.message : undefined)
  } finally {
    busyKey.value = ''
  }
}
</script>
