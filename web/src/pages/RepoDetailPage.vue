<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <PageHeader :title="repoName" :description="repo?.description || undefined">
      <template v-if="repo" #meta>
        <FormatBadge :format="repo.format" />
        <span class="text-xs text-muted">{{ t(`repos.new.type.${repo.repo_type ?? 'hosted'}` as 'repos.new.type.hosted') }}</span>
        <span class="text-xs text-muted" aria-hidden="true">·</span>
        <span class="inline-flex items-center gap-1 text-xs text-muted">
          <EyeIcon v-if="repo.public" class="h-3.5 w-3.5" aria-hidden="true" />
          <EyeSlashIcon v-else class="h-3.5 w-3.5" aria-hidden="true" />
          {{ repo.public ? t('repos.public') : t('repos.private') }}
        </span>
        <span v-if="repo.federated" class="inline-flex items-center gap-1 rounded-md bg-surface-hover px-2 py-0.5 text-xs font-medium text-muted" :title="t('federation.badgeTitle')" data-testid="federated-badge">
          <ArrowsRightLeftIcon class="h-3 w-3" aria-hidden="true" />{{ t('federation.badge') }}
        </span>
      </template>
      <template v-if="repo && canUpload" #actions>
        <AppButton variant="secondary" :aria-expanded="showUpload" @click="showUpload = !showUpload">
          <ArrowUpTrayIcon class="h-4 w-4 text-muted" aria-hidden="true" />
          {{ t('repoDetail.upload') }}
        </AppButton>
      </template>
    </PageHeader>

    <div v-if="loading" role="status" class="space-y-3" data-testid="repo-loading">
      <span class="sr-only">{{ t('common.loading') }}</span>
      <div class="h-24 animate-pulse rounded-lg bg-surface-hover" aria-hidden="true" />
      <div class="h-48 animate-pulse rounded-lg bg-surface-hover" aria-hidden="true" />
    </div>

    <div v-else-if="loadError" class="rounded-lg border border-border bg-surface">
      <StateBlock variant="error" :title="notFound ? t('rl.detail.notFound') : undefined" :hint="loadError">
        <template #action>
          <AppButton v-if="!notFound" variant="secondary" @click="load()">{{ t('ui.state.retry') }}</AppButton>
          <AppButton variant="secondary" to="/repos">{{ t('rl.detail.backToList') }}</AppButton>
        </template>
      </StateBlock>
    </div>

    <template v-else-if="repo">
      <div class="mb-6 space-y-4">
        <ConnectClientPanel :format="repo.format" :repo="repo.name" :read-only="repo.repo_type === 'proxy' || repo.repo_type === 'group'" />
        <PackageUploadZone v-if="canUpload && showUpload" :repo="repo.name" :format="repo.format" @uploaded="onUploaded" />
        <dl
          v-if="repo.repo_type === 'proxy'"
          class="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-1.5 rounded-lg border border-border bg-surface px-4 py-3 text-xs"
          :aria-label="t('repoDetail.proxy.title')"
        >
          <dt class="text-muted">{{ t('repoDetail.proxy.url') }}</dt>
          <dd class="min-w-0 break-all font-mono text-foreground">
            {{ repo.upstream_url || '—' }}
            <span v-if="repo.upstream_username" class="font-sans text-muted">· {{ tp('repoDetail.proxy.authenticatedAs', { user: repo.upstream_username }) }}</span>
          </dd>
          <dt class="text-muted">{{ t('repoDetail.proxy.cacheTtl') }}</dt>
          <dd class="text-foreground">{{ formatDuration(repo.cache_ttl_seconds) }}</dd>
          <dt class="text-muted">{{ t('repoDetail.proxy.negativeCacheTtl') }}</dt>
          <dd class="text-foreground">{{ formatDuration(repo.negative_cache_ttl_seconds) }}</dd>
          <dt class="text-muted" :title="t('repoDetail.proxy.maxStaleHint')">{{ t('repoDetail.proxy.maxStale') }}</dt>
          <dd class="text-foreground">{{ repo.max_stale_seconds ? formatDuration(repo.max_stale_seconds) : t('repoDetail.proxy.maxStaleOff') }}</dd>
        </dl>
      </div>

      <AppTabs :key="repoName" :tabs="tabs" :label="t('repoDetail.tabs')">
        <template #panel-members><RepoMembersTab :repo="repo.name" /></template>
        <template #panel-packages><RepoPackagesTab :key="uploads" :repo="repo" /></template>
        <template #panel-activity><RepoActivityTab :repo="repo.name" /></template>
        <template #panel-security><RepoSecurityTab :repo="repo.name" /></template>
        <template #panel-trash><TrashList :repo="repo.name" :permissions="repo.permissions" /></template>
        <template #panel-settings><RepoSettings :repo="repo" @saved="(r) => (repo = r)" /></template>
      </AppTabs>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { ArrowsRightLeftIcon, ArrowUpTrayIcon, EyeIcon, EyeSlashIcon } from '@heroicons/vue/24/outline'
import AppButton from '../components/AppButton.vue'
import ConnectClientPanel from '../components/ConnectClientPanel.vue'
import FormatBadge from '../components/FormatBadge.vue'
import PackageUploadZone from '../components/PackageUploadZone.vue'
import PageHeader from '../components/PageHeader.vue'
import RepoActivityTab from '../components/repos/RepoActivityTab.vue'
import RepoMembersTab from '../components/repos/RepoMembersTab.vue'
import RepoPackagesTab from '../components/repos/RepoPackagesTab.vue'
import RepoSecurityTab from '../components/repos/RepoSecurityTab.vue'
import RepoSettings from '../components/repos/RepoSettings.vue'
import TrashList from '../components/trash/TrashList.vue'
import AppTabs from '../components/ui/AppTabs.vue'
import StateBlock from '../components/ui/StateBlock.vue'
import { ApiError } from '../api/client'
import { getRepository, type Repository } from '../api/repositories'
import type { TabDef } from '../composables/useUrlTab'
import { formatDuration } from '../lib/duration'
import { useSessionStore } from '../stores/session'
import { t, tp } from '../i18n'

const route = useRoute()
const session = useSessionStore()

const repoName = computed(() => route.params.repo as string)
const repo = ref<Repository | null>(null)
const loading = ref(true)
const loadError = ref('')
const notFound = ref(false)
const showUpload = ref(false)
// Bumped after an upload so the package list refetches.
const uploads = ref(0)

// Global admins and maintainers/owners of the repository's project (repo.permissions.admin).
const repoAdmin = computed(() => session.isAdmin || repo.value?.permissions?.admin === true)
// Web upload is implemented by the deb and rpm handlers only; other formats are published with their
// native client tooling. Proxy and group repositories are read-only.
const canUpload = computed(
  () =>
    session.isAdmin &&
    (repo.value?.repo_type ?? 'hosted') === 'hosted' &&
    (repo.value?.format === 'deb' || repo.value?.format === 'rpm'),
)

const tabs = computed<TabDef[]>(() => {
  const type = repo.value?.repo_type ?? 'hosted'
  return [
    { id: 'members', label: t('repoDetail.tab.members'), hidden: type !== 'group' || !repoAdmin.value },
    { id: 'packages', label: type === 'proxy' ? t('repoDetail.tab.cached') : t('repoDetail.tab.packages'), hidden: type === 'group' },
    { id: 'activity', label: t('rl.tab.activity'), hidden: !repoAdmin.value },
    { id: 'security', label: t('rl.tab.security') },
    // Repo-scoped permissions come from the server, so principals with a delete grant also see the trash.
    { id: 'trash', label: t('trash.title'), hidden: !(session.isAdmin || repo.value?.permissions?.delete === true) },
    { id: 'settings', label: t('rl.tab.settings'), hidden: !repoAdmin.value },
  ]
})

function onUploaded(): void {
  uploads.value++
}

let generation = 0

async function load(): Promise<void> {
  const mine = ++generation
  loading.value = true
  loadError.value = ''
  notFound.value = false
  try {
    const res = await getRepository(repoName.value)
    if (mine === generation) repo.value = res
  } catch (err) {
    if (mine !== generation) return
    repo.value = null
    notFound.value = err instanceof ApiError && err.status === 404
    loadError.value = err instanceof Error ? err.message : t('repoDetail.loadFailed')
  } finally {
    if (mine === generation) loading.value = false
  }
}

// The router reuses this component between /repos/a and /repos/b.
watch(
  repoName,
  () => {
    showUpload.value = false
    void load()
  },
  { immediate: true },
)
</script>
