<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <PageHeader :title="t('projects.title')" :description="t('projects.description')">
      <template #actions>
        <label v-if="session.isAdmin" class="inline-flex items-center gap-2 text-sm text-foreground" :title="t('projects.allHint')">
          <AppToggle v-model="showAll" :label="t('projects.all')" />
          <span>{{ t('projects.all') }}</span>
        </label>
        <AppButton v-if="canCreate" variant="primary" data-testid="new-project" @click="openCreate">
          {{ t('projects.new') }}
        </AppButton>
      </template>
    </PageHeader>

    <div v-if="loading" class="text-muted">{{ t('common.loading') }}</div>

    <div v-else-if="loadError" class="rounded-xl border border-danger/30 bg-surface p-8 text-center" role="alert">
      <p class="text-sm text-danger">{{ loadError }}</p>
      <AppButton variant="secondary" size="sm" class="mt-3" @click="load">{{ t('browser.retry') }}</AppButton>
    </div>

    <EmptyState
      v-else-if="projects.length === 0"
      :title="t('projects.empty')"
      :description="showAll ? t('projects.emptyAll') : canCreate ? t('projects.emptyCanCreate') : t('projects.emptyHint')"
      :docs-url="docsUrl"
      :docs-label="t('docs.projects')"
    />

    <div v-else class="rounded-xl border border-border bg-surface overflow-hidden">
      <div class="overflow-x-auto">
        <table class="w-full" :aria-label="t('projects.title')">
          <thead>
            <tr class="border-b border-border bg-surface-hover text-xs font-semibold text-muted">
              <th scope="col" class="text-left px-4 py-2.5">{{ t('projects.column.name') }}</th>
              <th scope="col" class="text-left px-4 py-2.5">{{ t('projects.column.displayName') }}</th>
              <th scope="col" class="text-left px-4 py-2.5">{{ t('projects.column.role') }}</th>
              <th scope="col" class="text-right px-4 py-2.5">{{ t('projects.column.repositories') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            <tr v-for="p in projects" :key="p.id ?? p.name" class="hover:bg-surface-hover/40 transition-colors" data-testid="project-row">
              <td class="px-4 py-3">
                <router-link
                  :to="`/projects/${encodeURIComponent(p.name ?? '')}`"
                  class="font-mono text-sm font-semibold text-foreground hover:text-primary transition-colors"
                >{{ p.name }}</router-link>
                <span v-if="p.is_system" class="ml-2 text-xs text-muted">{{ t('projects.builtIn') }}</span>
              </td>
              <td class="px-4 py-3 text-sm text-muted">{{ p.display_name && p.display_name !== p.name ? p.display_name : '' }}</td>
              <td class="px-4 py-3"><ProjectRoleBadge :role="p.my_role" /></td>
              <td class="px-4 py-3 text-right font-mono text-sm tabular-nums text-foreground">{{ p.repository_count ?? 0 }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <AdminDialog :open="createOpen" max-width="max-w-md" :title="t('projects.create.title')" @close="createOpen = false">
      <form id="project-create-form" class="space-y-4" @submit.prevent="create">
        <div>
          <label for="new-project-name" class="block text-xs font-semibold text-foreground mb-1.5">
            {{ t('projects.create.name') }} <span class="text-danger ml-0.5">*</span>
          </label>
          <input
            id="new-project-name"
            v-model="form.name"
            type="text"
            required
            placeholder="my-project"
            class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary"
          />
          <p class="text-caption text-muted mt-1">{{ t('projects.create.nameHint') }}</p>
        </div>
        <div>
          <label for="new-project-display-name" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('projects.create.displayName') }}</label>
          <input
            id="new-project-display-name"
            v-model="form.display_name"
            type="text"
            class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm focus:ring-2 focus:ring-primary"
          />
        </div>
        <div>
          <label for="new-project-description" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('projects.create.description') }}</label>
          <textarea
            id="new-project-description"
            v-model="form.description"
            rows="3"
            class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm focus:ring-2 focus:ring-primary resize-none"
          />
        </div>
        <p v-if="createError" class="text-sm text-danger" role="alert">{{ createError }}</p>
      </form>
      <template #footer>
        <AppButton variant="secondary" size="sm" @click="createOpen = false">{{ t('common.cancel') }}</AppButton>
        <AppButton variant="primary" size="sm" type="submit" form="project-create-form" :loading="creating" :disabled="!form.name.trim()">
          {{ t('projects.create.action') }}
        </AppButton>
      </template>
    </AdminDialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import AdminDialog from '../components/AdminDialog.vue'
import AppButton from '../components/AppButton.vue'
import AppToggle from '../components/AppToggle.vue'
import EmptyState from '../components/EmptyState.vue'
import PageHeader from '../components/PageHeader.vue'
import ProjectRoleBadge from '../components/ProjectRoleBadge.vue'
import { createProject, listProjects, type Project } from '../api/projects'
import { errorStatus } from '../lib/projectAccess'
import { useSessionStore } from '../stores/session'
import { useDocsLink } from '../composables/useDocsLink'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const router = useRouter()
const session = useSessionStore()
const toasts = useToastStore()
const docsUrl = useDocsLink('projects')

const projects = ref<Project[]>([])
const canCreate = ref(false)
const showAll = ref(false)
const loading = ref(true)
const loadError = ref('')

let loadGeneration = 0

async function load(): Promise<void> {
  const generation = ++loadGeneration
  loading.value = true
  loadError.value = ''
  try {
    const res = await listProjects({ all: session.isAdmin && showAll.value })
    if (generation !== loadGeneration) return
    projects.value = res.projects
    canCreate.value = res.can_create
  } catch {
    if (generation !== loadGeneration) return
    loadError.value = t('projects.loadFailed')
  } finally {
    if (generation === loadGeneration) loading.value = false
  }
}

// Create dialog
const createOpen = ref(false)
const creating = ref(false)
const createError = ref('')
const form = ref({ name: '', display_name: '', description: '' })

function openCreate(): void {
  form.value = { name: '', display_name: '', description: '' }
  createError.value = ''
  createOpen.value = true
}

async function create(): Promise<void> {
  const name = form.value.name.trim()
  if (!name || creating.value) return
  creating.value = true
  createError.value = ''
  try {
    // Only the fields a non-admin may send: policy stays with global admins.
    await createProject({
      name,
      display_name: form.value.display_name.trim() || undefined,
      description: form.value.description.trim() || undefined,
    })
    toasts.success(t('projects.create.done'))
    createOpen.value = false
    await router.push(`/projects/${encodeURIComponent(name)}`)
  } catch (err) {
    const status = errorStatus(err)
    createError.value =
      status === 409 ? t('projects.create.conflict')
      : status === 403 ? t('projects.create.forbidden')
      : t('projects.create.failed')
  } finally {
    creating.value = false
  }
}

watch(showAll, () => void load())
onMounted(load)
</script>
