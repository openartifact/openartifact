<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <PageHeader :title="t('adm.projects.title')" :description="t('adm.projects.description')">
      <template #actions>
        <AppButton variant="primary" size="sm" @click="openCreate">
          <PlusIcon class="w-4 h-4" aria-hidden="true" />
          {{ t('adm.projects.new') }}
        </AppButton>
      </template>
    </PageHeader>

    <!-- Self-service creation -->
    <div class="bg-surface rounded-xl border border-border p-4 flex items-start justify-between gap-4" data-testid="self-create">
      <div class="min-w-0">
        <p id="self-create-label" class="text-sm font-semibold text-foreground">{{ t('adm.projects.selfCreate.title') }}</p>
        <p class="text-xs text-muted mt-0.5">{{ t('adm.projects.selfCreate.hint') }}</p>
        <p v-if="selfCreateError" class="text-xs text-danger mt-1" role="alert">{{ selfCreateError }}</p>
      </div>
      <AppToggle
        v-model="selfCreate"
        labelledby="self-create-label"
        :disabled="selfCreateLoading || selfCreateSaving"
        @update:model-value="saveSelfCreate"
      />
    </div>

    <!-- What project roles grant -->
    <div class="bg-surface rounded-xl border border-border p-4 text-xs text-muted space-y-1.5" data-testid="project-effect">
      <p class="font-semibold text-foreground">{{ t('adm.projects.effect.title') }}</p>
      <p>{{ t('adm.projects.effect.viewer') }}</p>
      <p>{{ t('adm.projects.effect.developer') }}</p>
      <p>{{ t('adm.projects.effect.maintainer') }}</p>
      <p>{{ t('adm.projects.effect.limits') }}</p>
    </div>

    <!-- Stat Tiles -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <div class="w-9 h-9 rounded-lg flex items-center justify-center shrink-0 bg-primary/10">
          <BuildingOfficeIcon class="w-5 h-5 text-primary" aria-hidden="true" />
        </div>
        <div class="min-w-0">
          <p class="text-micro font-mono font-semibold text-muted truncate">{{ t('adm.projects.stat.projects') }}</p>
          <p class="text-xl font-bold tabular-nums text-foreground leading-tight">{{ items.length }}</p>
        </div>
      </div>
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <div class="w-9 h-9 rounded-lg flex items-center justify-center shrink-0 bg-success/10">
          <UsersIcon class="w-5 h-5 text-success" aria-hidden="true" />
        </div>
        <div class="min-w-0">
          <p class="text-micro font-mono font-semibold text-muted truncate">{{ t('adm.projects.stat.memberships') }}</p>
          <p class="text-xl font-bold tabular-nums text-foreground leading-tight">{{ totalMembers }}</p>
        </div>
      </div>
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <div class="w-9 h-9 rounded-lg flex items-center justify-center shrink-0 bg-warning-soft">
          <UserGroupIcon class="w-5 h-5 text-warning" aria-hidden="true" />
        </div>
        <div class="min-w-0">
          <p class="text-micro font-mono font-semibold text-muted truncate">{{ t('adm.projects.stat.groups') }}</p>
          <p class="text-xl font-bold tabular-nums text-foreground leading-tight">{{ totalGroups }}</p>
        </div>
      </div>
    </div>

    <!-- Loading skeleton -->
    <div v-if="loading" class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-5 py-4 border-b border-border">
        <div class="h-4 w-40 rounded skel" />
      </div>
      <div v-for="i in 4" :key="i" class="flex items-center gap-4 px-5 py-4 border-b border-border last:border-0">
        <div class="h-3 w-36 rounded skel" />
        <div class="h-5 w-12 rounded-full skel" />
        <div class="h-3 w-28 rounded skel" />
      </div>
    </div>

    <div v-else-if="loadError" class="bg-surface rounded-xl border border-border px-5 py-8 text-center text-danger" role="alert">
      {{ loadError }}
    </div>

    <!-- Table -->
    <div v-else class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-5 py-4 flex items-center gap-2 border-b border-border">
        <h2 class="text-sm font-semibold text-foreground">{{ t('adm.projects.title') }}</h2>
        <span class="px-1.5 py-0.5 rounded-md bg-surface-hover text-caption font-mono font-medium text-muted">
          {{ items.length }}
        </span>
      </div>

      <div v-if="items.length === 0" class="py-16 text-center">
        <div class="w-12 h-12 rounded-full bg-surface-hover mx-auto flex items-center justify-center mb-3">
          <BuildingOfficeIcon class="w-6 h-6 text-muted" aria-hidden="true" />
        </div>
        <p class="text-sm font-medium text-foreground">{{ t('adm.projects.empty') }}</p>
        <p class="text-xs text-muted mt-1 mb-4">{{ t('adm.projects.emptyHint') }}</p>
        <AppButton variant="primary" size="sm" @click="openCreate">
          <PlusIcon class="w-4 h-4" aria-hidden="true" />
          {{ t('adm.projects.new') }}
        </AppButton>
        <div class="mt-3"><DocsLink :url="docsUrl">{{ t('docs.projects') }}</DocsLink></div>
      </div>

      <table v-else class="w-full">
        <thead>
          <tr class="bg-surface-hover/30">
            <th scope="col" class="text-left text-caption font-semibold text-muted px-5 py-2.5">{{ t('adm.projects.column.name') }}</th>
            <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('adm.projects.column.members') }}</th>
            <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('adm.projects.column.groups') }}</th>
            <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('adm.projects.column.created') }}</th>
            <th scope="col" class="text-right text-caption font-semibold text-muted px-5 py-2.5">{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          <tr v-for="project in items" :key="project.id" class="hover:bg-surface-hover/30 transition-colors">
            <td class="px-5 py-3.5">
              <div class="flex items-center gap-2.5">
                <div class="w-8 h-8 rounded-md bg-primary/10 flex items-center justify-center shrink-0">
                  <BuildingOfficeIcon class="w-4 h-4 text-primary" aria-hidden="true" />
                </div>
                <div class="min-w-0">
                  <p class="font-mono text-ui font-medium text-foreground truncate">{{ project.name }}</p>
                  <p v-if="project.display_name && project.display_name !== project.name" class="text-caption text-muted truncate">
                    {{ project.display_name }}
                  </p>
                </div>
              </div>
            </td>
            <td class="px-3 py-3.5">
              <button
                type="button"
                :aria-label="tp('adm.projects.manageAccessAria', { name: project.name ?? '' })"
                class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-xs font-mono bg-surface-hover text-foreground hover:bg-surface-hover/80 transition-colors"
                @click="openAccess(project)"
              >
                <UsersIcon class="w-3 h-3 text-muted" aria-hidden="true" />
                {{ memberCounts[project.id ?? ''] ?? '—' }}
              </button>
            </td>
            <td class="px-3 py-3.5">
              <button
                type="button"
                :aria-label="tp('adm.projects.manageAccessAria', { name: project.name ?? '' })"
                class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-xs font-mono bg-surface-hover text-foreground hover:bg-surface-hover/80 transition-colors"
                @click="openAccess(project)"
              >
                <UserGroupIcon class="w-3 h-3 text-muted" aria-hidden="true" />
                {{ groupCounts[project.id ?? ''] ?? '—' }}
              </button>
            </td>
            <td class="px-3 py-3.5">
              <span class="font-mono text-xs text-muted">{{ formatDate(project.created_at) }}</span>
            </td>
            <td class="px-5 py-3.5">
              <div class="flex items-center gap-0.5 justify-end">
                <button
                  type="button"
                  :title="t('adm.projects.manageAccess')"
                  :aria-label="tp('adm.projects.manageAccessAria', { name: project.name ?? '' })"
                  class="p-1.5 rounded-md text-muted hover:text-primary hover:bg-primary/10 transition-colors"
                  @click="openAccess(project)"
                >
                  <UsersIcon class="w-4 h-4" aria-hidden="true" />
                </button>
                <button
                  type="button"
                  :title="t('adm.projects.edit')"
                  :aria-label="tp('adm.projects.editAria', { name: project.name ?? '' })"
                  class="p-1.5 rounded-md text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
                  @click="openEdit(project)"
                >
                  <PencilIcon class="w-4 h-4" aria-hidden="true" />
                </button>
                <button
                  type="button"
                  :title="t('adm.projects.delete')"
                  :aria-label="tp('adm.projects.deleteAria', { name: project.name ?? '' })"
                  class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors"
                  @click="openDelete(project)"
                >
                  <TrashIcon class="w-4 h-4" aria-hidden="true" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <ProjectEditDrawer
      :open="projectDrawerOpen"
      :mode="projectDrawerMode"
      :project="editingProject"
      @close="projectDrawerOpen = false"
      @saved="load"
    />

    <ProjectMembersDrawer
      :open="accessDrawerOpen"
      :project="managingProject"
      :users="users"
      :all-groups="allGroups"
      @close="accessDrawerOpen = false"
      @members-count="(id, n) => (memberCounts = { ...memberCounts, [id]: n })"
      @groups-count="(id, n) => (groupCounts = { ...groupCounts, [id]: n })"
    />

    <ProjectDeleteDialog :target="deleteTarget" @close="deleteTarget = null" @deleted="load" />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { BuildingOfficeIcon, UsersIcon, UserGroupIcon, PlusIcon, PencilIcon, TrashIcon } from '@heroicons/vue/24/outline'
import { listAllUsers, listGroups, type GroupItem, type UserItem } from '../../api/admin'
import {
  getProjectSettings,
  listProjectGroups,
  listProjectMembers,
  listProjects,
  updateProjectSettings,
  type Project,
} from '../../api/projects'
import AppButton from '../../components/AppButton.vue'
import AppToggle from '../../components/AppToggle.vue'
import DocsLink from '../../components/DocsLink.vue'
import PageHeader from '../../components/PageHeader.vue'
import ProjectDeleteDialog from '../../components/ProjectDeleteDialog.vue'
import ProjectEditDrawer from '../../components/ProjectEditDrawer.vue'
import ProjectMembersDrawer from '../../components/ProjectMembersDrawer.vue'
import { useDocsLink } from '../../composables/useDocsLink'
import { t, tp } from '../../i18n/en'
import { formatDate } from '../../lib/datetime'
import { useToastStore } from '../../stores/toast'

const toasts = useToastStore()
const docsUrl = useDocsLink('admin-projects')
const items = ref<Project[]>([])
const users = ref<UserItem[]>([])
const allGroups = ref<GroupItem[]>([])
const memberCounts = ref<Record<string, number>>({})
const groupCounts = ref<Record<string, number>>({})
const loading = ref(true)
const loadError = ref('')

const totalMembers = computed(() => Object.values(memberCounts.value).reduce((s, n) => s + n, 0))
const totalGroups = computed(() => Object.values(groupCounts.value).reduce((s, n) => s + n, 0))

// Drawers and dialog: each owns its form state, the page owns which project they target.
const projectDrawerOpen = ref(false)
const projectDrawerMode = ref<'create' | 'edit'>('create')
const editingProject = ref<Project | null>(null)
const accessDrawerOpen = ref(false)
const managingProject = ref<Project | null>(null)
const deleteTarget = ref<Project | null>(null)

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const [{ projects: list }, usersRes, groupsRes] = await Promise.all([
      listProjects({ all: true }),
      // Names fall back to ids when the user or group list cannot be read.
      listAllUsers().catch(() => ({ total: 0, items: [] as UserItem[] })),
      listGroups().catch(() => ({ total: 0, items: [] as GroupItem[] })),
    ])
    items.value = list
    users.value = usersRes.items
    allGroups.value = groupsRes.items
    const [memberLists, groupLists] = await Promise.all([
      Promise.all(list.map((p) => listProjectMembers(p.name ?? '').catch(() => null))),
      Promise.all(list.map((p) => listProjectGroups(p.name ?? '').catch(() => null))),
    ])
    const mCounts: Record<string, number> = {}
    const gCounts: Record<string, number> = {}
    list.forEach((p, i) => {
      const id = p.id ?? ''
      const m = memberLists[i]
      const g = groupLists[i]
      if (m) mCounts[id] = m.length
      if (g) gCounts[id] = g.length
    })
    memberCounts.value = mCounts
    groupCounts.value = gCounts
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : t('adm.projects.loadFailed')
    toasts.error(loadError.value)
  } finally {
    loading.value = false
  }
}

function openCreate(): void {
  projectDrawerMode.value = 'create'
  editingProject.value = null
  projectDrawerOpen.value = true
}

function openEdit(project: Project): void {
  projectDrawerMode.value = 'edit'
  editingProject.value = project
  projectDrawerOpen.value = true
}

function openAccess(project: Project): void {
  managingProject.value = project
  accessDrawerOpen.value = true
}

function openDelete(project: Project): void {
  deleteTarget.value = project
}

// Self-service project creation (global setting)
const selfCreate = ref(false)
const selfCreateLoading = ref(true)
const selfCreateSaving = ref(false)
const selfCreateError = ref('')

async function loadSelfCreate(): Promise<void> {
  try {
    selfCreate.value = (await getProjectSettings()).self_create === true
  } catch {
    selfCreateError.value = t('adm.projects.selfCreate.loadFailed')
  } finally {
    selfCreateLoading.value = false
  }
}

async function saveSelfCreate(value: boolean): Promise<void> {
  selfCreateSaving.value = true
  selfCreateError.value = ''
  try {
    selfCreate.value = (await updateProjectSettings({ self_create: value })).self_create === true
    toasts.success(t('adm.projects.selfCreate.saved'))
  } catch {
    // Show what the server still holds.
    selfCreate.value = !value
    selfCreateError.value = t('adm.projects.selfCreate.saveFailed')
  } finally {
    selfCreateSaving.value = false
  }
}

onMounted(() => {
  void load()
  void loadSelfCreate()
})
</script>
