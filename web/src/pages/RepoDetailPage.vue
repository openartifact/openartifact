<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <!-- Loading State -->
    <div v-if="loading" class="text-muted">{{ t('common.loading') }}</div>

    <!-- Error State -->
    <div v-else-if="loadError" class="rounded-xl border border-danger/30 bg-surface p-8 text-center">
      <p class="text-sm text-danger">{{ loadError }}</p>
      <button
        class="mt-3 px-3 py-1.5 text-sm rounded-md border border-border hover:bg-surface-hover"
        @click="load()"
      >
        {{ t('browser.retry') }}
      </button>
    </div>

    <!-- Main Content -->
    <template v-else-if="repo">
      <div class="rounded-xl border border-border bg-surface overflow-hidden">
        <!-- Header -->
        <div class="px-6 pt-5 pb-4 border-b border-border">
          <div class="flex items-start justify-between gap-4 flex-wrap">
            <div class="min-w-0">
              <div class="flex items-center gap-2.5 flex-wrap">
                <h1 class="text-xl font-semibold font-mono text-foreground break-all">{{ repo.name }}</h1>
                <FormatBadge :format="repo.format" />
                <span class="text-xs text-muted">{{ repo.repo_type || 'hosted' }}</span>
                <span class="text-xs text-muted">·</span>
                <span class="inline-flex items-center gap-1 text-xs text-muted">
                  <EyeIcon v-if="repo.public" class="w-3.5 h-3.5" aria-hidden="true" />
                  <EyeSlashIcon v-else class="w-3.5 h-3.5" aria-hidden="true" />
                  {{ repo.public ? t('repos.public') : t('repos.private') }}
                </span>
              </div>
              <p v-if="repo.description" class="text-sm text-muted max-w-2xl mt-1.5">{{ repo.description }}</p>
            </div>

            <div class="flex items-center gap-2 shrink-0">
              <button
                v-if="canUpload"
                class="inline-flex items-center gap-1.5 px-3 py-1.5 text-sm rounded-md border border-border text-foreground hover:bg-surface-hover"
                :aria-expanded="showUpload"
                @click="showUpload = !showUpload"
              >
                <ArrowUpTrayIcon class="w-4 h-4 text-muted" aria-hidden="true" />
                {{ t('repoDetail.upload') }}
              </button>
              <router-link
                :to="`/repos/${repo.name}/browse`"
                class="inline-flex items-center px-3 py-1.5 text-sm rounded-md border border-border text-foreground hover:bg-surface-hover"
              >
                {{ t('browser.title') }}
              </router-link>
              <router-link
                v-if="session.isAdmin"
                :to="`/repos/${repo.name}/edit`"
                class="inline-flex items-center gap-1.5 px-3 py-1.5 text-sm rounded-md border border-border text-foreground hover:bg-surface-hover"
              >
                <Cog6ToothIcon class="w-4 h-4 text-muted" aria-hidden="true" />
                {{ t('common.edit') }}
              </router-link>
              <button
                v-if="canDelete"
                :disabled="deleting"
                class="px-3 py-1.5 text-sm rounded-md border border-danger/40 text-danger hover:bg-danger/10 disabled:opacity-50"
                @click="deleteRepo"
              >
                {{ deleting ? t('repoDetail.deleting') : t('common.delete') }}
              </button>
            </div>
          </div>

          <!-- Registry URL -->
          <div class="mt-4 flex items-center gap-2 flex-wrap">
            <span class="text-xs text-muted shrink-0">{{ t('repoDetail.registryUrl') }}</span>
            <code class="flex-1 min-w-0 rounded-md border border-border bg-surface-hover px-2.5 py-1 text-xs font-mono text-foreground break-all">{{ repoUrl }}</code>
            <button
              class="inline-flex items-center gap-1.5 px-2.5 py-1 text-xs rounded-md border border-border text-foreground hover:bg-surface-hover shrink-0"
              @click="copyUrl"
            >
              <ClipboardIcon class="w-3.5 h-3.5 text-muted" aria-hidden="true" />
              {{ t('repoDetail.copy') }}
            </button>
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
                · {{ t('repoDetail.proxy.authenticatedAs').replace('{user}', repo.upstream_username) }}
              </span>
            </dd>
            <dt class="text-muted">{{ t('repoDetail.proxy.cacheTtl') }}</dt>
            <dd class="text-foreground">{{ formatTtl(repo.cache_ttl_seconds) }}</dd>
            <dt class="text-muted">{{ t('repoDetail.proxy.negativeCacheTtl') }}</dt>
            <dd class="text-foreground">{{ formatTtl(repo.negative_cache_ttl_seconds) }}</dd>
            <dt class="text-muted" :title="t('repoDetail.proxy.maxStaleHint')">{{ t('repoDetail.proxy.maxStale') }}</dt>
            <dd class="text-foreground">
              {{ repo.max_stale_seconds ? formatTtl(repo.max_stale_seconds) : t('repoDetail.proxy.maxStaleOff') }}
            </dd>
          </dl>
        </div>

        <!-- Tabs -->
        <div class="flex items-center gap-1 px-3 overflow-x-auto">
          <button
            v-for="tab in tabs"
            :key="tab.id"
            class="relative h-11 px-3 text-sm font-medium transition-colors cursor-pointer shrink-0"
            :class="activeTab === tab.id ? 'text-foreground' : 'text-muted hover:text-foreground'"
            @click="activeTab = tab.id"
          >
            <span class="flex items-center gap-1.5">
              {{ tab.label }}
              <span
                v-if="tab.count !== undefined"
                class="text-[10px] font-mono px-1.5 py-0.5 rounded"
                :class="activeTab === tab.id
                  ? 'bg-primary-soft text-primary'
                  : 'bg-surface-hover text-muted'
                "
              >
                {{ tab.count }}
              </span>
            </span>
            <span
              v-if="activeTab === tab.id"
              class="absolute left-2 right-2 -bottom-px h-[2px] bg-primary rounded-full"
            />
          </button>
        </div>
      </div>

      <div v-if="canUpload && showUpload && !(activeTab === 'packages' && packages.length === 0)" class="mt-6">
        <PackageUploadZone :repo="repo.name" :format="repo.format" @uploaded="load(true)" />
      </div>

      <!-- Tab Content -->
      <div v-if="activeTab === 'cleanup' && session.isAdmin" class="mt-6">
        <RepoCleanupTab :repo-name="repoName" />
      </div>
      <div v-else-if="activeTab === 'configuration' && session.isAdmin" class="mt-6">
        <RepoSettingsTab :repo="repo" @saved="(r) => (repo = r)" />
      </div>
      <div v-else-if="activeTab === 'members'" class="mt-6">
        <p v-if="membersError" class="text-sm text-danger">{{ membersError }}</p>
        <EmptyState
          v-else-if="members.length === 0"
          :title="t('repoDetail.group.emptyTitle')"
          :description="t('repoDetail.group.emptyHint')"
        />
        <section v-else class="rounded-lg border border-border bg-surface">
          <div class="px-4 py-3 border-b border-border">
            <h2 class="text-sm font-semibold text-foreground">{{ t('repoDetail.group.title') }}</h2>
            <p class="text-xs text-muted mt-0.5">{{ t('repoDetail.group.hint') }}</p>
          </div>
          <div class="overflow-x-auto">
            <table class="w-full">
              <thead>
                <tr class="border-b border-border">
                  <th class="w-24 text-left px-4 py-3 text-sm font-medium text-muted">{{ t('repoDetail.group.position') }}</th>
                  <th class="text-left px-4 py-3 text-sm font-medium text-muted">{{ t('repos.column.name') }}</th>
                  <th class="text-left px-4 py-3 text-sm font-medium text-muted">{{ t('repos.column.format') }}</th>
                  <th class="text-left px-4 py-3 text-sm font-medium text-muted">{{ t('repos.column.type') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(m, i) in members" :key="m.id" class="border-t border-border hover:bg-surface-hover transition-colors">
                  <td class="px-4 py-3 text-xs font-mono text-muted">{{ i + 1 }}</td>
                  <td class="px-4 py-3 text-sm font-medium">
                    <router-link
                      :to="`/repos/${encodeURIComponent(m.name)}`"
                      class="font-mono text-foreground hover:text-primary transition-colors"
                    >{{ m.name }}</router-link>
                  </td>
                  <td class="px-4 py-3 text-sm"><FormatBadge :format="m.format" /></td>
                  <td class="px-4 py-3">
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
      <div v-else-if="activeTab === 'packages'" class="mt-6">
        <p v-if="repo.repo_type === 'proxy' && packages.length > 0" class="mb-3 text-xs text-muted">
          {{ t('repoDetail.proxy.cachedHint') }}
        </p>
        <div v-if="packages.length === 0 && repo.repo_type === 'proxy'">
          <EmptyState
            :title="t('repoDetail.proxy.emptyTitle')"
            :description="t('repoDetail.proxy.emptyHint')"
          />
        </div>
        <div v-else-if="packages.length === 0" class="space-y-6">
          <ConnectClientPanel :format="repo.format" :repo="repo.name" />
          <PackageUploadZone v-if="canUpload" :repo="repo.name" :format="repo.format" @uploaded="load(true)" />
        </div>
        <div v-else class="overflow-x-auto">
          <table class="w-full">
            <thead>
              <tr class="border-b border-border">
                <th v-if="!isProxy" class="w-7 px-4 py-3 text-sm font-medium text-muted">
                  <input
                    type="checkbox"
                    :checked="allPkgsSelected"
                    :indeterminate="somePkgsSelected && !allPkgsSelected"
                    class="w-4 h-4 rounded border-border text-primary focus:ring-primary cursor-pointer"
                    :aria-label="t('repoDetail.selectAll')"
                    @change="toggleAllPkgs"
                  />
                </th>
                <th class="text-left px-4 py-3 text-sm font-medium text-muted">{{ t('packages.column.name') }}</th>
                <th class="text-left px-4 py-3 text-sm font-medium text-muted">{{ t('packages.column.version') }}</th>
                <th class="text-left px-4 py-3 text-sm font-medium text-muted">{{ t('packages.column.format') }}</th>
                <th class="text-left px-4 py-3 text-sm font-medium text-muted">{{ t('packages.column.size') }}</th>
                <th class="text-left px-4 py-3 text-sm font-medium text-muted">{{ t('packages.column.uploaded') }}</th>
                <th class="text-right px-4 py-3 text-sm font-medium text-muted"></th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="pkg in packages"
                :key="pkg.id"
                class="border-t border-border hover:bg-surface-hover transition-colors cursor-pointer group"
                :class="{ 'bg-primary-soft/40': selectedIds.has(itemKey(pkg)) }"
              >
                <td v-if="!isProxy" class="w-7 px-4 py-3" @click.stop>
                  <input
                    type="checkbox"
                    :checked="selectedIds.has(itemKey(pkg))"
                    class="w-4 h-4 rounded border-border text-primary focus:ring-primary cursor-pointer opacity-0 group-hover:opacity-100 transition-opacity"
                    :class="{ 'opacity-100': selectedIds.has(itemKey(pkg)) }"
                    :aria-label="`${t('repoDetail.select')} ${pkg.name}`"
                    @change="togglePkg(pkg)"
                  />
                </td>
                <td class="px-4 py-3 text-sm font-medium">
                  <router-link
                    :to="`/repos/${repo!.name}/packages/${encodeURIComponent(pkg.format)}/${encodeURIComponent(pkg.name)}`"
                    class="text-foreground hover:text-primary transition-colors"
                    @click.stop
                  >{{ pkg.name }}</router-link>
                </td>
                <td class="px-4 py-3 text-xs font-mono text-muted">{{ pkg.version }}</td>
                <td class="px-4 py-3 text-sm">
                  <FormatBadge :format="pkg.format" />
                </td>
                <td class="px-4 py-3 text-sm text-muted">{{ formatSize(pkg.size) }}</td>
                <td class="px-4 py-3 text-sm text-muted">{{ formatDate(pkg.created_at) }}</td>
                <td class="px-4 py-3 text-right" @click.stop>
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
                      v-if="session.isAdmin && !isProxy"
                      class="w-7 h-7 flex items-center justify-center rounded border border-border text-muted hover:text-foreground hover:bg-surface-hover transition-colors text-danger"
                      :title="`${t('common.delete')} ${pkg.name}`"
                      :aria-label="`${t('common.delete')} ${pkg.name}`"
                      @click="deletePkg(pkg)"
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
          v-if="session.isAdmin && !isProxy"
          :selected-items="selectedItemKeys"
          :repo-name="repoName"
          @clear="clearSelection"
          @deleted="onDeleted"
          @error="msg => toasts.error(msg)"
        />
      </div>

    </template>
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
import EmptyState from '../components/EmptyState.vue'
import FormatBadge from '../components/FormatBadge.vue'
import BulkActionsBar from '../components/BulkActionsBar.vue'
import ConnectClientPanel from '../components/ConnectClientPanel.vue'
import PackageUploadZone from '../components/PackageUploadZone.vue'
import Pagination from '../components/Pagination.vue'
import RepoCleanupTab from './RepoCleanupTab.vue'
import RepoSettingsTab from './RepoSettingsTab.vue'
import { useSessionStore } from '../stores/session'
import { t } from '../i18n/en'
import { useToastStore } from '../stores/toast'
import { repoBaseUrl } from '../lib/installCommands'
import { formatDate } from '../lib/datetime'
import { formatTtl } from '../lib/repoTypes'
import {
  ArrowDownTrayIcon,
  ArrowUpTrayIcon,
  ClipboardIcon,
  Cog6ToothIcon,
  EyeIcon,
  EyeSlashIcon,
  TrashIcon,
} from '@heroicons/vue/24/outline'

const route = useRoute()
const router = useRouter()
const toasts = useToastStore()
const session = useSessionStore()
const repo = ref<Repository | null>(null)
const packages = ref<PackageItem[]>([])
const totalPackages = ref(0)
const PAGE_SIZE = 20
const page = ref(1)
const loading = ref(true)
const loadError = ref('')
const showUpload = ref(false)
const deleting = ref(false)
const activeTab = ref('packages')
const members = ref<RepositoryMember[]>([])
const membersError = ref('')

const repoName = computed(() => route.params.name as string)
const isProxy = computed(() => repo.value?.repo_type === 'proxy')
const canDelete = computed(() => session.isAdmin)
// Web upload is implemented by the deb and rpm handlers only; other formats
// are published with their native client tooling. Proxy and group
// repositories are read-only.
const canUpload = computed(
  () =>
    session.isAdmin &&
    (repo.value?.repo_type ?? 'hosted') === 'hosted' &&
    (repo.value?.format === 'deb' || repo.value?.format === 'rpm'),
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
  if (session.isAdmin) {
    list.push(
      { id: 'configuration', label: t('repoDetail.tab.configuration') },
      { id: 'cleanup', label: t('repoDetail.tab.cleanup') },
    )
  }
  return list
})

function formatSize(bytes?: number): string {
  if (!bytes) return '-'
  const units = ['B', 'KB', 'MB', 'GB']
  let size = bytes
  let unitIndex = 0
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024
    unitIndex++
  }
  return unitIndex === 0 ? `${size} B` : `${size.toFixed(1)} ${units[unitIndex]}`
}

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

async function deleteRepo() {
  if (!repo.value) return
  if (!confirm(t('repoDetail.confirmDeleteRepo').replace('{name}', repo.value.name))) return

  deleting.value = true
  try {
    await deleteRepository(repo.value.name)
    router.push('/repos')
  } catch {
    toasts.error(t('repoDetail.deleteRepoFailed'))
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
  if (!confirm(t('repoDetail.confirmDeletePkg').replace('{name}', pkg.name))) return
  try {
    await deletePackage(repo.value.name, pkg.id)
    packages.value = packages.value.filter((p) => p.id !== pkg.id)
    totalPackages.value = Math.max(0, totalPackages.value - 1)
    toasts.success(`${t('repoDetail.deleted')} ${pkg.name}`)
  } catch (err) {
    toasts.error(t('repoDetail.deletePkgFailed'), err instanceof Error ? err.message : undefined)
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
    if (!silent) activeTab.value = repoRes.repo_type === 'group' ? 'members' : 'packages'
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
    activeTab.value = 'packages'
    showUpload.value = false
    clearSelection()
    void load()
  },
  { immediate: true },
)
</script>
