<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <PageHeader :title="t('projects.title')" :description="session.isAdmin ? t('projects.descriptionAdmin') : t('projects.description')">
      <template #actions>
        <AppButton
          variant="primary"
          data-testid="new-project"
          :disabled="blocked"
          :aria-describedby="blocked ? 'project-create-blocked' : undefined"
          @click="openCreate"
        >
          <PlusIcon class="h-4 w-4" aria-hidden="true" />
          {{ t('projects.new') }}
        </AppButton>
      </template>
    </PageHeader>
    <p v-if="blocked" id="project-create-blocked" class="-mt-3 mb-4 text-sm text-muted" data-testid="create-blocked">
      {{ t('projects.createBlocked') }}
    </p>

    <FilterBar
      :search="list.text.q"
      :search-label="t('projects.search')"
      :placeholder="t('projects.search')"
      :active="list.filtered.value"
      @update:search="(v) => list.setFilter('q', v)"
      @clear="list.clear"
    >
      <SegmentedControl
        v-if="session.isAdmin"
        :options="scopeOptions"
        :model-value="list.filters.value.scope"
        :label="t('projects.scope')"
        @update:model-value="(v) => list.setFilter('scope', v as ProjectScope)"
      />
    </FilterBar>

    <ListTable
      :columns="columns"
      :caption="t('projects.title')"
      :query="list"
      :empty-title="emptyTitle"
      :empty-hint="emptyHint"
    >
      <template #cell-name="{ row }">
        <div class="min-w-0">
          <div class="flex min-w-0 items-center gap-2">
            <router-link
              :to="`/projects/${encodeURIComponent(row.name ?? '')}`"
              class="truncate font-mono text-sm font-semibold text-foreground hover:text-primary"
              :title="row.name"
            >{{ row.name }}</router-link>
            <span v-if="row.is_system" class="text-xs text-muted">{{ t('projects.builtIn') }}</span>
          </div>
          <p v-if="subtitle(row)" class="truncate text-xs text-muted" :title="subtitle(row)">{{ subtitle(row) }}</p>
        </div>
      </template>
      <template #cell-role="{ row }">
        <ProjectRoleBadge v-if="row.my_role" :role="row.my_role" />
        <span v-else class="text-xs text-muted">{{ t('projects.notMember') }}</span>
      </template>
      <template #cell-members="{ row }">
        <span class="tabular-nums">{{ row.member_count ?? '—' }}</span>
      </template>
      <template #cell-repositories="{ row }">
        <router-link
          v-if="row.repository_count"
          :to="{ path: `/projects/${encodeURIComponent(row.name ?? '')}`, query: { tab: 'repositories' } }"
          class="tabular-nums text-primary hover:underline"
        >{{ row.repository_count }}</router-link>
        <span v-else class="tabular-nums">{{ row.repository_count ?? 0 }}</span>
      </template>
      <template #cell-used="{ row }">
        <ProjectQuotaCell :usage="row.usage" :quota="row.quota" />
      </template>
      <template #cell-created="{ row }">
        <time :datetime="row.created_at" :title="formatDateTime(row.created_at)" class="text-xs text-muted">{{ timeAgo(row.created_at) }}</time>
      </template>
      <template #empty-action>
        <div class="flex flex-col items-center gap-3">
          <AppButton v-if="canCreate" variant="primary" @click="openCreate">{{ t('projects.new') }}</AppButton>
          <DocsLink :url="docsUrl">{{ t('docs.projects') }}</DocsLink>
        </div>
      </template>
    </ListTable>

    <ProjectCreateDrawer :open="createOpen" @close="closeCreate" @created="onCreated" />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { PlusIcon } from '@heroicons/vue/20/solid'
import AppButton from '../components/AppButton.vue'
import DocsLink from '../components/DocsLink.vue'
import PageHeader from '../components/PageHeader.vue'
import ProjectCreateDrawer from '../components/ProjectCreateDrawer.vue'
import ProjectQuotaCell from '../components/ProjectQuotaCell.vue'
import ProjectRoleBadge from '../components/ProjectRoleBadge.vue'
import FilterBar from '../components/ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../components/ui/ListTable.vue'
import SegmentedControl from '../components/ui/SegmentedControl.vue'
import { listProjects, type ProjectListItem } from '../api/projects'
import { useDocsLink } from '../composables/useDocsLink'
import { useListQuery } from '../composables/useListQuery'
import { enumParam, strParam } from '../composables/useUrlState'
import { formatDateTime, timeAgo } from '../lib/datetime'
import {
  PROJECT_SCOPES,
  PROJECT_SORTS,
  createBlockedReason,
  defaultScope,
  effectiveScope,
  queryProjects,
  type ProjectScope,
  type ProjectSort,
} from '../lib/projectList'
import { useSessionStore } from '../stores/session'
import { t } from '../i18n'

const route = useRoute()
const router = useRouter()
const session = useSessionStore()

const docsUrl = useDocsLink('projects')
const canCreate = ref(false)
const loaded = ref(false)
// The API list carries no paging or search: one fetch per scope, the rest is client side.
let fetched: { scope: ProjectScope; items: ProjectListItem[] } | null = null

const list = useListQuery({
  filters: { scope: enumParam(PROJECT_SCOPES, defaultScope(session.isAdmin)), q: strParam() },
  debounce: ['q'],
  limit: 20,
  sorts: PROJECT_SORTS,
  fetch: async (req) => {
    const scope = effectiveScope(session.isAdmin, req.filters.scope)
    if (!fetched || fetched.scope !== scope) {
      const res = await listProjects({ all: scope === 'all' })
      fetched = { scope, items: res.projects }
      canCreate.value = res.can_create
      loaded.value = true
    }
    const { rows, total } = queryProjects(fetched.items, {
      q: req.filters.q,
      sort: req.sort as ProjectSort,
      dir: req.dir,
      page: req.page,
      limit: req.limit,
    })
    return { rows, total }
  },
})

const blocked = computed(() => loaded.value && createBlockedReason(canCreate.value, session.isAdmin) !== null)

const scopeOptions = computed(() => [
  { value: 'mine', label: t('projects.scope.mine') },
  { value: 'all', label: t('projects.scope.all') },
])

const columns = computed<ListColumn[]>(() => [
  { key: 'name', label: t('projects.column.name'), sortable: true, grow: true },
  { key: 'role', label: t('projects.column.role'), sortable: true, dropOrder: 3 },
  { key: 'members', label: t('projects.column.members'), sortable: true, align: 'end', dropOrder: 2 },
  { key: 'repositories', label: t('projects.column.repositories'), sortable: true, align: 'end' },
  { key: 'used', label: t('projects.column.used'), sortable: true },
  { key: 'created', label: t('projects.column.created'), sortable: true, dropOrder: 1 },
])

const mineOnly = computed(() => effectiveScope(session.isAdmin, list.filters.value.scope) === 'mine')
const emptyTitle = computed(() => (mineOnly.value && !session.isAdmin ? t('projects.emptyMember') : t('projects.empty')))
const emptyHint = computed(() =>
  mineOnly.value
    ? canCreate.value ? t('projects.emptyCanCreate') : t('projects.emptyHint')
    : t('projects.emptyAll'),
)

function subtitle(p: ProjectListItem): string {
  const display = p.display_name && p.display_name !== p.name ? p.display_name : ''
  return display || p.description || ''
}

// Create drawer: `?new=1` (command palette) opens it; closing drops the parameter.
const createOpen = ref(false)

function dropNewParam(): void {
  if (route.query.new === undefined) return
  const { new: _drop, ...query } = route.query
  void router.replace({ query })
}

function openCreate(): void {
  createOpen.value = true
}

function closeCreate(): void {
  createOpen.value = false
  dropNewParam()
}

async function onCreated(name: string): Promise<void> {
  createOpen.value = false
  await router.push(`/projects/${encodeURIComponent(name)}`)
}

// The create permission arrives with the first list response, so `?new=1` waits for it.
if (route.query.new !== undefined) {
  const stop = watch(
    loaded,
    (ready) => {
      if (!ready) return
      stop()
      if (canCreate.value || session.isAdmin) createOpen.value = true
      else dropNewParam()
    },
    { immediate: true },
  )
}
</script>
