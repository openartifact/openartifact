<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <ProjectHeader
      :name="projectName"
      :project="project"
      :active="activeTab"
      :counts="counts"
      :crumbs="crumbs"
    />

    <div v-if="loading" class="text-muted">{{ t('common.loading') }}</div>

    <EmptyState
      v-else-if="state === 'not-found'"
      :title="t('projects.notFound.title')"
      :description="t('projects.notFound.hint')"
      :action-label="t('projects.back')"
      action-to="/projects"
    />
    <EmptyState
      v-else-if="state === 'forbidden'"
      :title="t('projects.forbidden.title')"
      :description="t('projects.forbidden.hint')"
      :action-label="t('projects.back')"
      action-to="/projects"
    />
    <div v-else-if="state === 'error'" class="rounded-xl border border-danger/30 bg-surface p-8 text-center" role="alert">
      <p class="text-sm text-danger">{{ t('projects.detail.loadFailed') }}</p>
      <AppButton variant="secondary" size="sm" class="mt-3" @click="load">{{ t('browser.retry') }}</AppButton>
    </div>

    <template v-else-if="project">
      <!-- Overview -->
      <div v-if="activeTab === 'overview'" class="grid gap-6 lg:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
        <section class="rounded-xl border border-border bg-surface p-4 sm:p-5" :aria-label="t('projects.overview.facts')">
          <h2 class="text-sm font-semibold text-foreground mb-3">{{ t('projects.overview.facts') }}</h2>
          <p v-if="project.description" class="text-sm text-foreground mb-4">{{ project.description }}</p>
          <p v-else class="text-sm text-muted italic mb-4">{{ t('projects.overview.noDescription') }}</p>
          <dl class="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1.5 text-sm" data-testid="project-facts">
            <dt class="text-muted">{{ t('projects.overview.name') }}</dt>
            <dd class="font-mono text-foreground">{{ project.name }}</dd>
            <dt class="text-muted">{{ t('projects.overview.yourRole') }}</dt>
            <dd><ProjectRoleBadge :role="project.my_role" /></dd>
            <dt class="text-muted">{{ t('projects.overview.repositories') }}</dt>
            <dd class="text-foreground tabular-nums" data-testid="fact-repositories">{{ project.repository_count ?? repoTotal }}</dd>
            <dt class="text-muted">{{ t('projects.overview.members') }}</dt>
            <dd class="text-foreground tabular-nums" data-testid="fact-members">{{ members.length }}</dd>
            <dt class="text-muted">{{ t('projects.overview.groups') }}</dt>
            <dd class="text-foreground tabular-nums">{{ groups.length }}</dd>
            <dt class="text-muted">{{ t('projects.overview.created') }}</dt>
            <dd class="text-foreground">{{ formatDate(project.created_at) }}</dd>
          </dl>
        </section>

        <section class="rounded-xl border border-border bg-surface overflow-hidden" :aria-label="t('projects.overview.recent')">
          <div class="px-4 py-3 border-b border-border flex items-center justify-between gap-2">
            <h2 class="text-sm font-semibold text-foreground">{{ t('projects.overview.recent') }}</h2>
            <router-link
              v-if="repoTotal > 0"
              :to="{ path: base, query: { tab: 'repositories' } }"
              class="text-xs text-primary hover:underline"
            >{{ t('projects.overview.viewAll') }}</router-link>
          </div>
          <p v-if="reposError" class="px-4 py-6 text-sm text-danger">{{ reposError }}</p>
          <p v-else-if="repos.length === 0" class="px-4 py-6 text-sm text-muted">{{ t('projects.overview.noRepos') }}</p>
          <ul v-else class="divide-y divide-border">
            <li v-for="r in repos.slice(0, RECENT_LIMIT)" :key="r.id" class="flex items-center justify-between gap-3 px-4 py-2.5">
              <router-link :to="`/repos/${encodeURIComponent(r.name)}`" class="font-mono text-sm font-medium text-foreground hover:text-primary truncate">{{ r.name }}</router-link>
              <FormatBadge :format="r.format" size="sm" />
            </li>
          </ul>
        </section>
      </div>

      <!-- Repositories -->
      <template v-else-if="activeTab === 'repositories'">
        <div v-if="canCreateRepo" class="mb-4 flex justify-end">
          <AppButton :to="{ path: '/repos/new', query: { project: projectName } }" data-testid="new-repo">{{ t('repos.new') }}</AppButton>
        </div>
        <p v-if="reposError" class="text-sm text-danger" role="alert">{{ reposError }}</p>
        <EmptyState
          v-else-if="repoTotal === 0"
          :title="t('projects.repos.empty')"
          :description="t('projects.repos.emptyHint')"
        />
        <section v-else class="rounded-xl border border-border bg-surface overflow-hidden" :aria-label="t('projects.repos.title')">
          <div class="overflow-x-auto">
            <table class="w-full">
              <thead>
                <tr class="border-b border-border bg-surface-hover text-xs font-semibold text-muted">
                  <th scope="col" class="text-left px-4 py-2.5">{{ t('repos.column.name') }}</th>
                  <th scope="col" class="text-left px-4 py-2.5">{{ t('repos.column.format') }}</th>
                  <th scope="col" class="text-left px-4 py-2.5">{{ t('repos.column.type') }}</th>
                  <th scope="col" class="text-left px-4 py-2.5">{{ t('repos.column.visibility') }}</th>
                  <th scope="col" class="text-left px-4 py-2.5">{{ t('projects.repos.column.created') }}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-border">
                <tr v-for="r in repos" :key="r.id" class="hover:bg-surface-hover/40 transition-colors" data-testid="repo-row">
                  <td class="px-4 py-3">
                    <router-link :to="`/repos/${encodeURIComponent(r.name)}`" class="font-mono text-sm font-semibold text-foreground hover:text-primary">{{ r.name }}</router-link>
                  </td>
                  <td class="px-4 py-3"><FormatBadge :format="r.format" /></td>
                  <td class="px-4 py-3 text-sm text-muted">{{ r.repo_type || 'hosted' }}</td>
                  <td class="px-4 py-3 text-sm text-muted">{{ r.public ? t('repos.public') : t('repos.private') }}</td>
                  <td class="px-4 py-3 text-xs text-muted">{{ formatDate(r.created_at) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <Pagination :total="repoTotal" :page="repoPage" :limit="PAGE_SIZE" @change="onRepoPage" />
        </section>
      </template>

      <!-- Members -->
      <MemberTable
        v-else-if="activeTab === 'members'"
        :project="project"
        :members="members"
        :groups="groups"
        @changed="loadAccess"
      />

      <!-- Settings -->
      <ProjectSettingsTab
        v-else-if="activeTab === 'settings'"
        :project="project"
        @saved="(p) => (project = { ...project, ...p })"
        @deleted="onDeleted"
      />
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppButton from '../components/AppButton.vue'
import EmptyState from '../components/EmptyState.vue'
import FormatBadge from '../components/FormatBadge.vue'
import MemberTable from '../components/MemberTable.vue'
import Pagination from '../components/Pagination.vue'
import ProjectHeader from '../components/ProjectHeader.vue'
import ProjectRoleBadge from '../components/ProjectRoleBadge.vue'
import ProjectSettingsTab from '../components/ProjectSettingsTab.vue'
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
import { resolveCrumbs, type Crumb } from '../lib/crumbs'
import { formatDate } from '../lib/datetime'
import { atLeast, errorStatus, visibleProjectTabs, type ProjectTab } from '../lib/projectAccess'
import { t } from '../i18n/en'

const PAGE_SIZE = 20
const RECENT_LIMIT = 5

const route = useRoute()
const router = useRouter()

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

const counts = computed<Partial<Record<ProjectTab, number>>>(() =>
  project.value ? { repositories: repoTotal.value, members: members.value.length } : {},
)

// The active tab lives in the URL (?tab=) so it is deep-linkable and back-button
// safe. Unknown or not-permitted values fall back to the overview.
const activeTab = computed<ProjectTab>(() => {
  const wanted = typeof route.query.tab === 'string' ? route.query.tab : ''
  const allowed = visibleProjectTabs(project.value?.my_role)
  return (allowed as string[]).includes(wanted) ? (wanted as ProjectTab) : 'overview'
})

const crumbs = computed<Crumb[]>(() => {
  const trail = resolveCrumbs(route)
  if (!project.value || activeTab.value === 'overview') return trail
  return [...trail, { label: t(`projects.tab.${activeTab.value}`) }]
})

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
