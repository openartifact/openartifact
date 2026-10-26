<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <PageHeader :title="t('repos.title')" :description="t('rl.description')">
      <template v-if="session.canCreateRepo" #actions>
        <AppButton variant="primary" data-testid="new-repo" @click="create.set(true)">
          <PlusIcon class="h-4 w-4" aria-hidden="true" />
          {{ t('repos.new') }}
        </AppButton>
      </template>
    </PageHeader>

    <FilterBar
      :search="list.text.q"
      :search-label="t('repos.search')"
      :placeholder="t('repos.search')"
      :active="list.filtered.value"
      @update:search="(v) => list.setFilter('q', v)"
      @clear="list.clear"
    >
      <select :value="list.filters.value.format" :class="SELECT" :aria-label="t('repos.column.format')" @change="list.setFilter('format', ($event.target as HTMLSelectElement).value)">
        <option value="">{{ t('rl.filter.allFormats') }}</option>
        <option v-for="f in ALL_FORMATS" :key="f" :value="f">{{ formatLabel(f) }}</option>
      </select>
      <select :value="list.filters.value.type" :class="SELECT" :aria-label="t('repos.column.type')" @change="list.setFilter('type', ($event.target as HTMLSelectElement).value as RepoTypeFilter)">
        <option value="">{{ t('rl.filter.allTypes') }}</option>
        <option v-for="ty in TYPES" :key="ty" :value="ty">{{ t(`repos.new.type.${ty}` as 'repos.new.type.hosted') }}</option>
      </select>
      <button
        v-for="chip in chips"
        :key="chip.key"
        type="button"
        :aria-pressed="list.filters.value[chip.key]"
        class="inline-flex h-9 items-center gap-1.5 rounded-lg border px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:h-11"
        :class="list.filters.value[chip.key] ? 'border-primary bg-primary-soft text-primary' : 'border-border-strong bg-surface text-foreground hover:bg-surface-hover'"
        :data-testid="`chip-${chip.key}`"
        @click="list.setFilter(chip.key, !list.filters.value[chip.key])"
      >{{ chip.label }}</button>
      <template #after>
        <label class="flex items-center gap-2 text-sm text-muted">
          {{ t('rl.sort.label') }}
          <select :value="list.sort.value" :class="SELECT" data-testid="sort" @change="onSort(($event.target as HTMLSelectElement).value)">
            <option v-for="s in REPO_SORTS" :key="s" :value="s">{{ t(`rl.sort.${s}` as 'rl.sort.name') }}</option>
          </select>
        </label>
      </template>
    </FilterBar>

    <ListTable
      :columns="columns"
      :caption="t('repos.title')"
      :query="list"
      :row-key="(r) => r.name ?? ''"
      :empty-title="t('repos.empty')"
      :empty-hint="session.canCreateRepo ? t('repos.emptyHint') : t('rl.emptyNoCreate')"
    >
      <template #cell-name="{ row }">
        <div class="flex min-w-0 items-center gap-2.5">
          <span class="w-[4.75rem] shrink-0"><FormatBadge :format="row.format ?? ''" size="sm" /></span>
          <div class="min-w-0">
            <router-link :to="`/repos/${encodeURIComponent(row.name ?? '')}`" class="block truncate font-mono text-sm font-semibold text-foreground hover:text-primary">{{ row.name }}</router-link>
            <p v-if="subtitle(row)" class="truncate text-xs text-muted">{{ subtitle(row) }}</p>
          </div>
        </div>
      </template>
      <template #cell-type="{ row }">{{ t(`repos.new.type.${row.repo_type ?? 'hosted'}` as 'repos.new.type.hosted') }}</template>
      <template #cell-visibility="{ row }">{{ row.public ? t('repos.public') : t('repos.private') }}</template>
      <template #cell-packages="{ row }"><span class="tabular-nums">{{ row.stats ? row.stats.package_count : '—' }}</span></template>
      <template #cell-size="{ row }"><span class="whitespace-nowrap tabular-nums">{{ formatBytesOrDash(row.stats?.size_bytes) }}</span></template>
      <template #cell-last_published="{ row }"><RelativeTime :value="row.stats?.last_published_at" class="whitespace-nowrap text-xs text-muted" /></template>
      <template #cell-security="{ row }">
        <router-link
          v-if="session.isAdmin && securityState(row).kind === 'findings'"
          :to="{ path: '/security', query: { repo: row.name } }"
          class="rounded hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
          :aria-label="tp('rl.security.open', { name: row.name ?? '' })"
        >
          <RepoSecurityCell :item="row" />
        </router-link>
        <RepoSecurityCell v-else :item="row" />
      </template>
      <template #cell-quota="{ row }"><QuotaBar :used="row.stats?.quota.used_bytes" :limit="row.stats?.quota.limit_bytes" /></template>
      <template #cell-actions="{ row }">
        <div class="flex items-center gap-0.5">
          <CopyButton :text="`${repoBaseUrl(row.format ?? '', origin, row.name ?? '')}/`" :label="`${t('repoDetail.copyUrl')}: ${row.name}`" :message="t('repoDetail.urlCopied')" />
          <router-link
            v-if="canAdmin(row)"
            :to="{ path: `/repos/${encodeURIComponent(row.name ?? '')}`, query: { tab: 'settings' } }"
            class="inline-flex h-9 w-9 items-center justify-center rounded-md text-muted hover:bg-surface-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:h-11 max-md:w-11"
            :aria-label="`${t('rl.tab.settings')}: ${row.name}`"
          >
            <Cog6ToothIcon class="h-4 w-4" aria-hidden="true" />
          </router-link>
          <button
            v-if="canAdmin(row)"
            type="button"
            class="inline-flex h-9 w-9 items-center justify-center rounded-md text-danger hover:bg-danger-soft focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:h-11 max-md:w-11"
            :aria-label="`${t('common.delete')}: ${row.name}`"
            @click="deleteTarget = row"
          >
            <TrashIcon class="h-4 w-4" aria-hidden="true" />
          </button>
        </div>
      </template>
      <template v-if="session.canCreateRepo" #empty-action>
        <AppButton variant="primary" @click="create.set(true)">{{ t('repos.new') }}</AppButton>
      </template>
    </ListTable>

    <RepoCreateDrawer :open="createOpen" :project="list.filters.value.project" @close="create.set(false)" @created="onCreated" />
    <RepoDeleteDialog v-if="deleteTarget" :open="true" :name="deleteTarget.name ?? ''" :busy="deleting" @close="deleteTarget = null" @confirm="doDelete" />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Cog6ToothIcon, PlusIcon, TrashIcon } from '@heroicons/vue/20/solid'
import AppButton from '../components/AppButton.vue'
import FormatBadge from '../components/FormatBadge.vue'
import PageHeader from '../components/PageHeader.vue'
import RepoDeleteDialog from '../components/RepoDeleteDialog.vue'
import QuotaBar from '../components/repos/QuotaBar.vue'
import RepoCreateDrawer from '../components/repos/RepoCreateDrawer.vue'
import RepoSecurityCell from '../components/repos/RepoSecurityCell.vue'
import CopyButton from '../components/ui/CopyButton.vue'
import FilterBar from '../components/ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../components/ui/ListTable.vue'
import RelativeTime from '../components/ui/RelativeTime.vue'
import { deleteRepository, listRepositories, type RepositoryListItem } from '../api/repositories'
import { useListQuery } from '../composables/useListQuery'
import { useUrlFlag } from '../composables/useUrlFlag'
import { boolParam, enumParam, strParam } from '../composables/useUrlState'
import { ALL_FORMATS, formatLabel } from '../lib/formats'
import { repoBaseUrl } from '../lib/installCommands'
import { formatBytesOrDash } from '../lib/quota'
import { REPO_SORTS, securityState } from '../lib/repoList'
import { useSessionStore } from '../stores/session'
import { useToastStore } from '../stores/toast'
import { t, tp } from '../i18n/en'

const SELECT =
  'h-9 rounded-lg border border-border-strong bg-surface px-2 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:h-11'
const TYPES = ['hosted', 'proxy', 'group'] as const
type RepoTypeFilter = '' | (typeof TYPES)[number]

const session = useSessionStore()
const toasts = useToastStore()
const router = useRouter()
const origin = window.location.origin

const list = useListQuery({
  filters: {
    q: strParam(),
    format: strParam(),
    type: enumParam(['', ...TYPES] as const, ''),
    project: strParam(),
    flagged: boolParam(),
    near_quota: boolParam(),
    unscanned: boolParam(),
  },
  debounce: ['q'],
  limit: 20,
  sorts: REPO_SORTS,
  async fetch(req) {
    const f = req.filters
    const res = await listRepositories({
      page: req.page,
      limit: req.limit,
      q: f.q || undefined,
      format: f.format || undefined,
      type: f.type || undefined,
      project: f.project || undefined,
      flagged: f.flagged || undefined,
      nearQuota: f.near_quota || undefined,
      unscanned: f.unscanned || undefined,
      sort: req.sort as (typeof REPO_SORTS)[number],
    })
    return { rows: res.repositories ?? [], total: res.total ?? 0 }
  },
})

const chips = computed(() => [
  { key: 'flagged' as const, label: t('rl.filter.flagged') },
  { key: 'near_quota' as const, label: t('rl.filter.nearQuota') },
  { key: 'unscanned' as const, label: t('rl.filter.unscanned') },
])

const columns = computed<ListColumn[]>(() => [
  { key: 'name', label: t('repos.column.name') },
  { key: 'type', label: t('repos.column.type') },
  { key: 'visibility', label: t('repos.column.visibility') },
  { key: 'packages', label: t('rl.column.packages'), align: 'end' },
  { key: 'size', label: t('packages.column.size'), align: 'end' },
  { key: 'last_published', label: t('rl.column.lastPublished') },
  { key: 'security', label: t('rl.column.security') },
  { key: 'quota', label: t('rl.column.quota') },
  { key: 'actions', label: t('common.actions'), actions: true },
])

// The sort is a choice among fixed orders (the API has no direction), so it is a select, not column headers.
function onSort(key: string): void {
  if (key !== list.sort.value) list.setSort(key)
}

function subtitle(r: RepositoryListItem): string {
  const project = r.project && r.project !== 'system' ? r.project : ''
  return [project, r.description].filter(Boolean).join(' · ')
}

const canAdmin = (r: RepositoryListItem): boolean => session.isAdmin || r.permissions?.admin === true

// Create drawer: `?new=1` (command palette, old /repos/new links) opens it.
const create = useUrlFlag('new')
const createOpen = computed(() => create.on.value && session.canCreateRepo)
watch(
  () => create.on.value && !session.canCreateRepo,
  (blocked) => {
    if (blocked) void create.set(false)
  },
  { immediate: true },
)

async function onCreated(name: string): Promise<void> {
  await router.push(`/repos/${encodeURIComponent(name)}`)
}

const deleteTarget = ref<RepositoryListItem | null>(null)
const deleting = ref(false)

async function doDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target?.name) return
  deleting.value = true
  try {
    await deleteRepository(target.name)
    toasts.success(tp('rl.deleted', { name: target.name }))
    deleteTarget.value = null
    await list.reload()
  } catch (err) {
    toasts.error(t('repoDetail.deleteRepoFailed'), err instanceof Error ? err.message : undefined)
  } finally {
    deleting.value = false
  }
}
</script>
