<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <PageHeader :title="project?.display_name || projectName">
      <template v-if="project" #meta>
        <ProjectRoleBadge :role="project.my_role" />
        <span v-if="project.is_system" class="text-xs text-muted">{{ t('projects.builtIn') }}</span>
        <span v-if="project.display_name && project.display_name !== projectName" class="font-mono text-xs text-muted">{{ projectName }}</span>
      </template>
    </PageHeader>

    <div v-if="loading" class="text-muted" role="status">{{ t('common.loading') }}</div>

    <div v-else-if="state !== 'ok'" class="rounded-lg border border-border bg-surface">
      <StateBlock
        :variant="state === 'forbidden' ? 'forbidden' : state === 'error' ? 'error' : 'empty'"
        :title="state === 'not-found' ? t('projects.notFound.title') : state === 'forbidden' ? t('projects.forbidden.title') : undefined"
        :hint="state === 'not-found' ? t('projects.notFound.hint') : state === 'forbidden' ? t('projects.forbidden.hint') : t('projects.detail.loadFailed')"
      >
        <template #action>
          <AppButton v-if="state === 'error'" variant="secondary" @click="load">{{ t('ui.state.retry') }}</AppButton>
          <AppButton v-else variant="secondary" to="/projects">{{ t('projects.back') }}</AppButton>
        </template>
      </StateBlock>
    </div>

    <AppTabs v-else-if="project" :tabs="tabs" :label="t('projects.tabs')">
      <template #panel-overview>
        <div class="grid gap-6 lg:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
          <div class="space-y-6">
            <section class="rounded-lg border border-border bg-surface p-4 sm:p-5" :aria-label="t('projects.overview.facts')">
              <h2 class="mb-3 text-sm font-semibold text-foreground">{{ t('projects.overview.facts') }}</h2>
              <p v-if="project.description" class="mb-4 text-sm text-foreground">{{ project.description }}</p>
              <p v-else class="mb-4 text-sm italic text-muted">{{ t('projects.overview.noDescription') }}</p>
              <dl class="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1.5 text-sm" data-testid="project-facts">
                <dt class="text-muted">{{ t('projects.overview.name') }}</dt>
                <dd class="font-mono text-foreground">{{ project.name }}</dd>
                <dt class="text-muted">{{ t('projects.overview.yourRole') }}</dt>
                <dd><ProjectRoleBadge :role="project.my_role" /></dd>
                <dt class="text-muted">{{ t('projects.overview.repositories') }}</dt>
                <dd class="tabular-nums text-foreground" data-testid="fact-repositories">{{ project.repository_count ?? repoTotal }}</dd>
                <dt class="text-muted">{{ t('projects.overview.members') }}</dt>
                <dd class="tabular-nums text-foreground" data-testid="fact-members">{{ members.length }}</dd>
                <dt class="text-muted">{{ t('projects.overview.groups') }}</dt>
                <dd class="tabular-nums text-foreground">{{ groups.length }}</dd>
                <dt class="text-muted">{{ t('projects.overview.created') }}</dt>
                <dd class="text-foreground" :title="formatDateTime(project.created_at)">{{ formatDate(project.created_at) }}</dd>
              </dl>
            </section>
            <ProjectTokensLink :project="projectName" />
          </div>

          <section class="overflow-hidden rounded-lg border border-border bg-surface" :aria-label="t('projects.overview.recent')">
            <div class="flex items-center justify-between gap-2 border-b border-border px-4 py-3">
              <h2 class="text-sm font-semibold text-foreground">{{ t('projects.overview.recent') }}</h2>
              <router-link
                v-if="repoTotal > 0"
                :to="{ path: base, query: { tab: 'repositories' } }"
                class="inline-flex items-center text-xs text-primary hover:underline max-md:min-h-11"
              >{{ t('projects.overview.viewAll') }}</router-link>
            </div>
            <p v-if="reposError" class="px-4 py-6 text-sm text-danger">{{ reposError }}</p>
            <p v-else-if="repos.length === 0" class="px-4 py-6 text-sm text-muted">{{ t('projects.overview.noRepos') }}</p>
            <ul v-else class="divide-y divide-border">
              <li v-for="r in repos.slice(0, RECENT_LIMIT)" :key="r.id" class="flex items-center justify-between gap-3 px-4 py-2.5">
                <router-link :to="`/repos/${encodeURIComponent(r.name)}`" class="truncate font-mono text-sm font-medium text-foreground hover:text-primary max-md:inline-flex max-md:min-h-11 max-md:items-center">{{ r.name }}</router-link>
                <FormatBadge :format="r.format" size="sm" />
              </li>
            </ul>
          </section>
        </div>
      </template>

      <template #panel-repositories>
        <div v-if="canCreateRepo" class="mb-4 flex justify-end">
          <AppButton :to="{ path: '/repos', query: { new: '1', project: projectName } }" data-testid="new-repo">{{ t('repos.new') }}</AppButton>
        </div>
        <ListTable
          :columns="repoColumns"
          :caption="t('projects.repos.title')"
          :rows="repos"
          :total="repoTotal"
          :page="repoPage"
          :limit="PAGE_SIZE"
          :error="reposError"
          :empty-title="t('projects.repos.empty')"
          :empty-hint="t('projects.repos.emptyHint')"
          @update:page="onRepoPage"
          @retry="loadRepos"
        >
          <template #cell-name="{ row }">
            <router-link :to="`/repos/${encodeURIComponent(row.name)}`" class="block truncate font-mono text-sm font-semibold text-foreground hover:text-primary" :title="row.name">{{ row.name }}</router-link>
          </template>
          <template #cell-format="{ row }"><FormatBadge :format="row.format" /></template>
          <template #cell-type="{ row }"><span class="text-muted">{{ row.repo_type || 'hosted' }}</span></template>
          <template #cell-visibility="{ row }"><span class="text-muted">{{ row.public ? t('repos.public') : t('repos.private') }}</span></template>
          <template #cell-created="{ row }">
            <time :datetime="row.created_at" :title="formatDateTime(row.created_at)" class="text-xs text-muted">{{ timeAgo(row.created_at) }}</time>
          </template>
          <template v-if="canCreateRepo" #empty-action>
            <AppButton :to="{ path: '/repos', query: { new: '1', project: projectName } }">{{ t('repos.new') }}</AppButton>
          </template>
        </ListTable>
      </template>

      <template #panel-members>
        <MemberTable :project="project" :members="members" :groups="groups" @changed="loadAccess" />
      </template>

      <template #panel-activity>
        <ProjectActivityTab :project="projectName" />
      </template>

      <template #panel-usage>
        <div class="space-y-6">
          <ProjectUsageTab :project="projectName" />
          <ProjectQuotaEditor :project="projectName" :editable="session.isAdmin" />
        </div>
      </template>

      <template #panel-settings>
        <ProjectSettingsTab :project="project" @saved="onSaved" @deleted="onDeleted" />
      </template>
    </AppTabs>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppButton from '../components/AppButton.vue'
import FormatBadge from '../components/FormatBadge.vue'
import PageHeader from '../components/PageHeader.vue'
import ProjectRoleBadge from '../components/ProjectRoleBadge.vue'
import ProjectTokensLink from '../components/ProjectTokensLink.vue'
import AppTabs from '../components/ui/AppTabs.vue'
import ListTable, { type ListColumn } from '../components/ui/ListTable.vue'
import StateBlock from '../components/ui/StateBlock.vue'
import {
  getProject,
  listProjectGroups,
  listProjectMembers,
  listProjectRepositories,
  type Project,
  type ProjectGroup,
  type ProjectMember,
} from '../api/projects'
import type { Repository } from '../api/repositories'
import type { TabDef } from '../composables/useUrlTab'
import { asyncTab } from '../lib/asyncTab'
import { formatDate, formatDateTime, timeAgo } from '../lib/datetime'
import { atLeast, errorStatus, visibleProjectTabs, type ProjectTab } from '../lib/projectAccess'
import { useSessionStore } from '../stores/session'
import { t } from '../i18n'

// Overview and Repositories render here; every other panel loads on first use.
const MemberTable = asyncTab(() => import('../components/MemberTable.vue'))
const ProjectActivityTab = asyncTab(() => import('../components/ProjectActivityTab.vue'))
const ProjectQuotaEditor = asyncTab(() => import('../components/ProjectQuotaEditor.vue'))
const ProjectSettingsTab = asyncTab(() => import('../components/ProjectSettingsTab.vue'))
const ProjectUsageTab = asyncTab(() => import('../components/ProjectUsageTab.vue'))

const PAGE_SIZE = 20
const RECENT_LIMIT = 5

const route = useRoute()
const router = useRouter()
const session = useSessionStore()

const project = ref<Project | null>(null)
const loading = ref(true)
const state = ref<'ok' | 'not-found' | 'forbidden' | 'error'>('ok')

const repos = ref<Repository[]>([])
const repoTotal = ref(0)
const repoPage = ref(1)
const reposError = ref('')
const members = ref<ProjectMember[]>([])
const groups = ref<ProjectGroup[]>([])

// Maintainers and owners create repositories in the project (admins reach it as owner).
const canCreateRepo = computed(() => atLeast(project.value?.my_role, 'maintainer'))
const projectName = computed(() => route.params.project as string)
const base = computed(() => `/projects/${encodeURIComponent(projectName.value)}`)

const tabs = computed<TabDef[]>(() => {
  const allowed = visibleProjectTabs(project.value?.my_role)
  const defs: Record<ProjectTab, TabDef> = {
    overview: { id: 'overview', label: t('projects.tab.overview') },
    repositories: { id: 'repositories', label: t('projects.tab.repositories'), count: repoTotal.value },
    members: { id: 'members', label: t('projects.tab.members'), count: members.value.length + groups.value.length },
    activity: { id: 'activity', label: t('projects.tab.activity') },
    usage: { id: 'usage', label: t('projects.tab.usage') },
    settings: { id: 'settings', label: t('projects.tab.settings') },
  }
  return allowed.map((id) => defs[id])
})

const repoColumns = computed<ListColumn[]>(() => [
  { key: 'name', label: t('repos.column.name'), grow: true },
  { key: 'format', label: t('repos.column.format') },
  { key: 'type', label: t('repos.column.type') },
  { key: 'visibility', label: t('repos.column.visibility'), dropOrder: 2 },
  { key: 'created', label: t('projects.repos.column.created'), dropOrder: 1 },
])

async function loadRepos(): Promise<void> {
  try {
    const res = await listProjectRepositories(projectName.value, { page: repoPage.value, limit: PAGE_SIZE })
    repos.value = res.repositories
    repoTotal.value = res.total
    reposError.value = ''
  } catch {
    repos.value = []
    reposError.value = t('projects.repos.loadFailed')
  }
}

async function loadAccess(): Promise<void> {
  try {
    const [m, g] = await Promise.all([listProjectMembers(projectName.value), listProjectGroups(projectName.value)])
    members.value = m
    groups.value = g
  } catch {
    members.value = []
    groups.value = []
  }
}

let loadGeneration = 0

async function load(): Promise<void> {
  const generation = ++loadGeneration
  loading.value = true
  state.value = 'ok'
  try {
    const p = await getProject(projectName.value)
    if (generation !== loadGeneration) return
    project.value = p
    await Promise.all([loadRepos(), loadAccess()])
  } catch (err) {
    if (generation !== loadGeneration) return
    project.value = null
    const status = errorStatus(err)
    state.value = status === 404 ? 'not-found' : status === 403 ? 'forbidden' : 'error'
  } finally {
    if (generation === loadGeneration) loading.value = false
  }
}

async function onRepoPage(next: number): Promise<void> {
  repoPage.value = next
  await loadRepos()
}

function onSaved(saved: Project): void {
  if (project.value) project.value = { ...project.value, ...saved }
}

async function onDeleted(): Promise<void> {
  await router.push('/projects')
}

// The router reuses this component between /projects/a and /projects/b.
watch(
  projectName,
  () => {
    repoPage.value = 1
    repos.value = []
    repoTotal.value = 0
    members.value = []
    groups.value = []
    void load()
  },
  { immediate: true },
)
</script>
