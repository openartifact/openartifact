<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <RepoHeader
      :name="repoName"
      :repo="repo"
      :active="activeTab === defaultTab ? 'main' : (activeTab as RepoSection)"
      :count="repo?.repo_type === 'group' ? members.length : totalPackages"
      :crumbs="crumbs"
    >
      <template v-if="repo" #actions>
        <AppButton
          v-if="canUpload"
          variant="secondary"
          :aria-expanded="showUpload"
          @click="showUpload = !showUpload"
        >
          <ArrowUpTrayIcon class="w-4 h-4 text-muted" aria-hidden="true" />
          {{ t('repoDetail.upload') }}
        </AppButton>
        <AppButton v-if="repoAdmin" variant="secondary" :to="`/repos/${encodeURIComponent(repo.name)}/edit`">
          <Cog6ToothIcon class="w-4 h-4 text-muted" aria-hidden="true" />
          {{ t('common.edit') }}
        </AppButton>
        <AppButton v-if="canDelete" variant="danger-outline" :loading="deleting" @click="showDeleteDialog = true">
          {{ deleting ? t('repoDetail.deleting') : t('common.delete') }}
        </AppButton>
      </template>
    </RepoHeader>

    <!-- Loading State -->
    <div v-if="loading" class="text-muted">{{ t('common.loading') }}</div>

    <!-- Error State -->
    <div v-else-if="loadError" class="rounded-xl border border-danger/30 bg-surface p-8 text-center">
      <p class="text-sm text-danger">{{ loadError }}</p>
      <AppButton variant="secondary" size="sm" class="mt-3" @click="load()">
        {{ t('browser.retry') }}
      </AppButton>
    </div>

    <!-- Main Content -->
    <template v-else-if="repo">
      <div v-if="activeTab === defaultTab" class="rounded-xl border border-border bg-surface overflow-hidden mb-6">
        <div class="px-4 sm:px-6 py-4">
          <p v-if="repo.description" class="text-sm text-muted max-w-2xl">{{ repo.description }}</p>
          <p v-else class="text-sm text-muted italic max-w-2xl">{{ t('repos.noDescription') }}</p>

          <!-- Registry URL -->
          <div class="mt-4 first:mt-0 flex items-center gap-2 flex-wrap">
            <span class="text-xs text-muted shrink-0">{{ t('repoDetail.registryUrl') }}</span>
            <code class="flex-1 min-w-0 rounded-md border border-border bg-surface-hover px-2.5 py-1 text-xs font-mono text-foreground break-all">{{ repoUrl }}</code>
            <AppButton variant="secondary" size="xs" class="shrink-0" @click="copyUrl">
              <ClipboardIcon class="w-3.5 h-3.5 text-muted" aria-hidden="true" />
              {{ t('repoDetail.copy') }}
            </AppButton>
          </div>

          <!-- Proxy: upstream info (credentials are never shown) -->
          <dl
            v-if="repo.repo_type === 'proxy'"
            class="mt-4 grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1.5 text-xs"
            :aria-label="t('repoDetail.proxy.title')"
          >
            <dt class="text-muted">{{ t('repoDetail.proxy.url') }}</dt>
            <dd class="min-w-0 font-mono text-foreground break-all">
              {{ repo.upstream_url || '-' }}
              <span v-if="repo.upstream_username" class="font-sans text-muted">
                · {{ tp('repoDetail.proxy.authenticatedAs', { user: repo.upstream_username }) }}
              </span>
            </dd>
            <dt class="text-muted">{{ t('repoDetail.proxy.cacheTtl') }}</dt>
            <dd class="text-foreground">{{ formatDuration(repo.cache_ttl_seconds) }}</dd>
            <dt class="text-muted">{{ t('repoDetail.proxy.negativeCacheTtl') }}</dt>
            <dd class="text-foreground">{{ formatDuration(repo.negative_cache_ttl_seconds) }}</dd>
            <dt class="text-muted" :title="t('repoDetail.proxy.maxStaleHint')">{{ t('repoDetail.proxy.maxStale') }}</dt>
            <dd class="text-foreground">
              {{ repo.max_stale_seconds ? formatDuration(repo.max_stale_seconds) : t('repoDetail.proxy.maxStaleOff') }}
            </dd>
          </dl>
        </div>
      </div>

      <div v-if="canUpload && showUpload && !(activeTab === 'packages' && packages.length === 0)" class="mb-6">
        <PackageUploadZone :repo="repo.name" :format="repo.format" @uploaded="load(true)" />
      </div>

      <!-- Tab Content -->
      <div v-if="activeTab === 'cleanup' && repoAdmin">
        <RepoCleanupTab :repo-name="repoName" />
      </div>
      <div v-else-if="activeTab === 'federation' && repoAdmin">
        <RepoFederationTab :repo="repo" @toggled="onFederationToggled" />
      </div>
      <div v-else-if="activeTab === 'configuration' && repoAdmin">
        <RepoSettingsTab :repo="repo" @saved="onSettingsSaved" />
      </div>
      <div v-else-if="activeTab === 'members'">
        <p v-if="membersError" class="text-sm text-danger">{{ membersError }}</p>
        <EmptyState
          v-else-if="members.length === 0"
          :title="t('repoDetail.group.emptyTitle')"
          :description="t('repoDetail.group.emptyHint')"
          :docs-url="repoTypesDocs"
        />
        <section v-else class="rounded-xl border border-border bg-surface overflow-hidden">
          <div class="px-4 py-3 border-b border-border">
            <h2 class="text-sm font-semibold text-foreground">{{ t('repoDetail.group.title') }}</h2>
            <p class="text-xs text-muted mt-0.5">{{ t('repoDetail.group.hint') }}</p>
          </div>
          <div class="overflow-x-auto">
            <table class="w-full">
              <thead>
                <tr class="border-b border-border bg-surface-hover text-xs font-semibold text-muted">
                  <th class="w-24 text-left px-4 py-2.5">{{ t('repoDetail.group.position') }}</th>
                  <th class="text-left px-4 py-2.5">{{ t('repos.column.name') }}</th>
                  <th class="text-left px-4 py-2.5">{{ t('repos.column.type') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(m, i) in members" :key="m.id" class="border-t border-border first:border-t-0 hover:bg-surface-hover transition-colors">
                  <td class="px-4 py-2.5 text-xs font-mono text-muted">{{ i + 1 }}</td>
                  <td class="px-4 py-2.5 text-sm font-medium">
                    <router-link
                      :to="`/repos/${encodeURIComponent(m.name)}`"
                      class="font-mono text-foreground hover:text-primary transition-colors"
                    >{{ m.name }}</router-link>
                  </td>
                  <td class="px-4 py-2.5">
                    <span class="inline-flex items-center rounded border border-border px-1.5 py-0.5 text-xs text-muted">
                      {{ m.repo_type === 'proxy' ? t('repos.new.type.proxy') : t('repos.new.type.hosted') }}
                    </span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </div>
      <div v-else-if="activeTab === 'packages'">
        <p v-if="repo.repo_type === 'proxy' && packages.length > 0" class="mb-3 text-xs text-muted">
          {{ t('repoDetail.proxy.cachedHint') }}
        </p>
        <div v-if="packages.length === 0 && repo.repo_type === 'proxy'">
          <EmptyState
            :title="t('repoDetail.proxy.emptyTitle')"
            :description="t('repoDetail.proxy.emptyHint')"
            :docs-url="repoTypesDocs"
          />
        </div>
        <div v-else-if="packages.length === 0" class="space-y-6">
          <ConnectClientPanel :format="repo.format" :repo="repo.name" />
          <PackageUploadZone v-if="canUpload" :repo="repo.name" :format="repo.format" @uploaded="load(true)" />
        </div>
        <div v-else class="overflow-x-auto rounded-xl border border-border bg-surface">
          <table class="w-full">
            <thead>
              <tr class="border-b border-border bg-surface-hover text-xs font-semibold text-muted">
                <th v-if="canDeletePackages" class="w-7 px-4 py-2.5">
                  <input
                    type="checkbox"
                    :checked="allPkgsSelected"
                    :indeterminate="somePkgsSelected && !allPkgsSelected"
                    class="w-4 h-4 rounded border-border-strong accent-[rgb(var(--color-primary))] focus:ring-primary cursor-pointer"
                    :aria-label="t('repoDetail.selectAll')"
                    @change="toggleAllPkgs"
                  />
                </th>
                <th class="text-left px-4 py-2.5">{{ t('packages.column.name') }}</th>
                <th class="text-left px-4 py-2.5">{{ t('packages.column.version') }}</th>
                <th class="text-right px-4 py-2.5">{{ t('packages.column.size') }}</th>
                <th class="text-left px-4 py-2.5">{{ t('packages.column.uploaded') }}</th>
                <th class="text-right px-4 py-2.5"><span class="sr-only">{{ t('common.actions') }}</span></th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="pkg in packages"
                :key="pkg.id"
                class="border-t border-border first:border-t-0 hover:bg-surface-hover transition-colors group"
                :class="{ 'bg-primary-soft/40': selectedIds.has(itemKey(pkg)) }"
              >
                <td v-if="canDeletePackages" class="w-7 px-4 py-2.5">
                  <input
                    type="checkbox"
                    :checked="selectedIds.has(itemKey(pkg))"
                    class="w-4 h-4 rounded border-border-strong accent-[rgb(var(--color-primary))] focus:ring-primary cursor-pointer opacity-0 group-hover:opacity-100 transition-opacity"
                    :class="{ 'opacity-100': selectedIds.has(itemKey(pkg)) }"
                    :aria-label="`${t('repoDetail.select')} ${pkg.name}`"
                    @change="togglePkg(pkg)"
                  />
                </td>
                <td class="px-4 py-2.5 text-sm font-medium">
                  <router-link
                    :to="`/repos/${encodeURIComponent(repo!.name)}/packages/${encodeURIComponent(pkg.format)}/${encodeURIComponent(pkg.name)}`"
                    class="text-foreground hover:text-primary transition-colors"
                  >{{ pkg.name }}</router-link>
                </td>
                <td class="px-4 py-2.5 text-xs font-mono text-muted">{{ pkg.version || '—' }}</td>
                <td class="px-4 py-2.5 text-xs font-mono tabular-nums text-muted text-right">{{ formatBytesOrDash(pkg.size) }}</td>
                <td class="px-4 py-2.5 text-xs text-muted">{{ formatDate(pkg.created_at) }}</td>
                <td class="px-4 py-2.5 text-right">
                  <div class="flex items-center justify-end gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
                    <button
                      class="w-7 h-7 flex items-center justify-center rounded border border-border text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
                      :title="`${t('repoDetail.download')} ${pkg.name}`"
                      :aria-label="`${t('repoDetail.download')} ${pkg.name}`"
                      @click="download(pkg)"
                    >
                      <ArrowDownTrayIcon class="w-3.5 h-3.5" aria-hidden="true" />
                    </button>
                    <button
                      v-if="canDeletePackages"
                      class="w-7 h-7 flex items-center justify-center rounded border border-border text-muted hover:text-foreground hover:bg-surface-hover transition-colors text-danger"
                      :title="`${t('common.delete')} ${pkg.name}`"
                      :aria-label="`${t('common.delete')} ${pkg.name}`"
                      @click="pendingDeletePkg = pkg"
                    >
                      <TrashIcon class="w-3.5 h-3.5" aria-hidden="true" />
                    </button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <Pagination
          v-if="totalPackages > PAGE_SIZE"
          :total="totalPackages"
          :page="page"
          :limit="PAGE_SIZE"
          @change="onPageChange"
        />

        <!-- Bulk actions bar -->
        <BulkActionsBar
          v-if="canDeletePackages"
          :selected-items="selectedItemKeys"
          :repo-name="repoName"
          @clear="clearSelection"
          @deleted="onDeleted"
          @error="msg => toasts.error(msg)"
        />
      </div>

    </template>

    <ConfirmDialog
      :open="!!pendingDeletePkg"
      :title="t('common.delete')"
      :description="tp('repoDetail.confirmDeletePkg', { name: pendingDeletePkg?.name ?? '' })"
      :confirm-label="t('common.delete')"
      :busy="deletingPkg"
      @confirm="pendingDeletePkg && deletePkg(pendingDeletePkg)"
      @close="pendingDeletePkg = null"
    />

    <RepoDeleteDialog
      v-if="repo"
      :open="showDeleteDialog"
      :name="repo.name"
      :busy="deleting"
      @close="showDeleteDialog = false"
      @confirm="deleteRepo"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  getRepository,
  listPackages,
  listRepoMembers,
  deleteRepository,
  deletePackage,
  downloadPackage,
} from '../api/repositories'
import type { Repository, RepositoryMember, PackageItem } from '../api/repositories'
import AppButton from '../components/AppButton.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import EmptyState from '../components/EmptyState.vue'
import RepoHeader, { type RepoSection } from '../components/RepoHeader.vue'
import { resolveCrumbs, type Crumb } from '../lib/crumbs'
import BulkActionsBar from '../components/BulkActionsBar.vue'
import ConnectClientPanel from '../components/ConnectClientPanel.vue'
import PackageUploadZone from '../components/PackageUploadZone.vue'
import Pagination from '../components/Pagination.vue'
import RepoDeleteDialog from '../components/RepoDeleteDialog.vue'
import { asyncTab } from '../lib/asyncTab'
import { useSessionStore } from '../stores/session'
import { useDocsLink } from '../composables/useDocsLink'
import { t, tp } from '../i18n/en'
import { useToastStore } from '../stores/toast'
import { repoBaseUrl } from '../lib/installCommands'
import { formatDate } from '../lib/datetime'
import { showsFederation } from '../lib/federation'
import { formatDuration } from '../lib/duration'
import { supportsPackageDelete } from '../lib/formats'
import { formatBytesOrDash } from '../lib/quota'
import {
  ArrowDownTrayIcon,
  ArrowUpTrayIcon,
  ClipboardIcon,
  Cog6ToothIcon,
  TrashIcon,
} from '@heroicons/vue/24/outline'

// Tab panels other than the default one load on first use, not with the page.
const RepoCleanupTab = asyncTab(() => import('./RepoCleanupTab.vue'))
const RepoFederationTab = asyncTab(() => import('./RepoFederationTab.vue'))
const RepoSettingsTab = asyncTab(() => import('./RepoSettingsTab.vue'))

const route = useRoute()
const router = useRouter()
const toasts = useToastStore()
const session = useSessionStore()
// Proxy caches and group members are explained next to the repository list, not with the client setup.
const repoTypesDocs = useDocsLink('repos')
const repo = ref<Repository | null>(null)
const packages = ref<PackageItem[]>([])
const totalPackages = ref(0)
const PAGE_SIZE = 20
const page = ref(1)
const loading = ref(true)
const loadError = ref('')
const showUpload = ref(false)
const deleting = ref(false)
const members = ref<RepositoryMember[]>([])
const membersError = ref('')

const repoName = computed(() => route.params.name as string)

function onFederationToggled(enabled: boolean): void {
  if (repo.value) repo.value = { ...repo.value, federated: enabled }
}

function onSettingsSaved(updated: Repository): void {
  repo.value = updated
}
// Global admins and maintainers/owners of the repository's project (repo.admin).
const repoAdmin = computed(() => session.isAdmin || repo.value?.permissions?.admin === true)
const canDelete = computed(() => repoAdmin.value)
// Web upload is implemented by the deb and rpm handlers only; other formats
// are published with their native client tooling. Proxy and group
// repositories are read-only.
const canUpload = computed(
  () =>
    session.isAdmin &&
    (repo.value?.repo_type ?? 'hosted') === 'hosted' &&
    (repo.value?.format === 'deb' || repo.value?.format === 'rpm'),
)

// Deleting a package row by id is implemented server-side for every hosted
// format (supportsPackageDelete), so offer it for hosted repositories only.
const canDeletePackages = computed(
  () =>
    session.isAdmin &&
    (repo.value?.repo_type ?? 'hosted') === 'hosted' &&
    supportsPackageDelete(repo.value?.format),
)

// ── Selection state ────────────────────────────────────────────────────────
const selectedIds = ref<Set<string>>(new Set())

function itemKey(pkg: PackageItem): string {
  return pkg.id
}

const selectedItemKeys = computed(() => [...selectedIds.value])

const allPkgsSelected = computed(() =>
  packages.value.length > 0 &&
  packages.value.every((p) => selectedIds.value.has(itemKey(p))),
)

const somePkgsSelected = computed(() =>
  packages.value.some((p) => selectedIds.value.has(itemKey(p))),
)

function togglePkg(pkg: PackageItem): void {
  const key = itemKey(pkg)
  const next = new Set(selectedIds.value)
  if (next.has(key)) {
    next.delete(key)
  } else {
    next.add(key)
  }
  selectedIds.value = next
}

function toggleAllPkgs(): void {
  if (allPkgsSelected.value) {
    const next = new Set(selectedIds.value)
    packages.value.forEach((p) => next.delete(itemKey(p)))
    selectedIds.value = next
  } else {
    const next = new Set(selectedIds.value)
    packages.value.forEach((p) => next.add(itemKey(p)))
    selectedIds.value = next
  }
}

function clearSelection(): void {
  selectedIds.value = new Set()
}

function onDeleted(ids: string[]): void {
  const deleted = new Set(ids)
  packages.value = packages.value.filter((p) => !deleted.has(itemKey(p)))
  totalPackages.value = Math.max(0, totalPackages.value - ids.length)
  clearSelection()
}

const defaultTab = computed(() => (repo.value?.repo_type === 'group' ? 'members' : 'packages'))

const tabs = computed(() => {
  const list: Array<{ id: string; label: string; count?: number }> = []
  if (repo.value?.repo_type === 'group') {
    list.push({ id: 'members', label: t('repoDetail.tab.members'), count: members.value.length })
  } else {
    list.push({
      id: 'packages',
      label: repo.value?.repo_type === 'proxy' ? t('repoDetail.tab.cached') : t('repoDetail.tab.packages'),
      count: totalPackages.value,
    })
  }
  if (repoAdmin.value) {
    list.push(
      { id: 'configuration', label: t('repoDetail.tab.configuration') },
      { id: 'cleanup', label: t('repoDetail.tab.cleanup') },
    )
    if (repo.value && showsFederation(repo.value)) list.push({ id: 'federation', label: t('federation.tab') })
  }
  return list
})

// The active tab lives in the URL (?tab=) so it is deep-linkable and back-button
// safe. Unknown or not-permitted values fall back to the repo's default tab.
const activeTab = computed(() => {
  const wanted = typeof route.query.tab === 'string' ? route.query.tab : ''
  return tabs.value.some((tb) => tb.id === wanted) ? wanted : defaultTab.value
})

// Breadcrumb trail: Repositories › repo, plus the tab when it is not the default.
const crumbs = computed<Crumb[]>(() => {
  const base = resolveCrumbs(route)
  const tab = tabs.value.find((tb) => tb.id === activeTab.value)
  if (!repo.value || !tab || tab.id === defaultTab.value) return base
  return [...base, { label: tab.label }]
})


const repoUrl = computed(() =>
  repo.value ? `${repoBaseUrl(repo.value.format, window.location.origin, repo.value.name)}/` : '',
)

function copyUrl(): void {
  if (!repo.value) return
  const url = repoUrl.value
  navigator.clipboard.writeText(url).then(() => {
    toasts.success(t('repoDetail.urlCopied'))
  }).catch(() => {
    toasts.error(t('repoDetail.urlCopyFailed'))
  })
}

const showDeleteDialog = ref(false)
const pendingDeletePkg = ref<PackageItem | null>(null)
const deletingPkg = ref(false)

async function deleteRepo() {
  if (!repo.value) return

  deleting.value = true
  try {
    await deleteRepository(repo.value.name)
    showDeleteDialog.value = false
    router.push('/repos')
  } catch (err) {
    toasts.error(t('repoDetail.deleteRepoFailed'), err instanceof Error ? err.message : undefined)
  } finally {
    deleting.value = false
  }
}

async function download(pkg: PackageItem): Promise<void> {
  try {
    await downloadPackage(repoName.value, pkg)
  } catch (err) {
    toasts.error(t('repoDetail.downloadFailed'), err instanceof Error ? err.message : undefined)
  }
}

async function deletePkg(pkg: PackageItem): Promise<void> {
  if (!repo.value) return
  deletingPkg.value = true
  try {
    await deletePackage(repo.value.name, pkg.id)
    packages.value = packages.value.filter((p) => p.id !== pkg.id)
    totalPackages.value = Math.max(0, totalPackages.value - 1)
    pendingDeletePkg.value = null
    toasts.success(tp('repoDetail.movedToTrash', { name: pkg.name }))
  } catch (err) {
    toasts.error(t('repoDetail.deletePkgFailed'), err instanceof Error ? err.message : undefined)
  } finally {
    deletingPkg.value = false
  }
}

let loadGeneration = 0

async function load(silent = false): Promise<void> {
  const generation = ++loadGeneration
  if (!silent) loading.value = true
  loadError.value = ''
  try {
    const repoRes = await getRepository(repoName.value)
    if (generation !== loadGeneration) return
    if (repoRes.repo_type === 'group') {
      // Groups own no packages; the page shows their ordered members instead.
      membersError.value = ''
      try {
        members.value = await listRepoMembers(repoName.value)
      } catch (err) {
        members.value = []
        membersError.value = err instanceof Error ? err.message : t('repoDetail.group.loadFailed')
      }
      if (generation !== loadGeneration) return
      packages.value = []
      totalPackages.value = 0
    } else {
      const pkgRes = await listPackages(repoName.value, { page: page.value, limit: PAGE_SIZE })
      if (generation !== loadGeneration) return
      members.value = []
      packages.value = pkgRes.packages || []
      totalPackages.value = pkgRes.total || 0
    }
    repo.value = repoRes
  } catch (err) {
    if (generation !== loadGeneration) return
    if (!silent) repo.value = null
    loadError.value = err instanceof Error ? err.message : t('repoDetail.loadFailed')
  } finally {
    if (generation === loadGeneration) loading.value = false
  }
}

async function onPageChange(next: number): Promise<void> {
  page.value = next
  clearSelection()
  await load(true)
}

// Reload (and reset per-repo state) whenever the route param changes; the
// router reuses this component between /repos/a and /repos/b.
watch(
  repoName,
  () => {
    page.value = 1
    showUpload.value = false
    clearSelection()
    void load()
  },
  { immediate: true },
)
</script>
