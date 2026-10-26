<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  One page for every format: /repos/:repo/packages/:pkg (the latest version) and
  .../versions/:v. Format differences live in the data (install command, metadata,
  files), not in separate pages.
-->
<template>
  <div>
    <p v-if="loading" role="status" class="py-10 text-center text-muted">{{ t('common.loading') }}</p>

    <StateBlock v-else-if="error" variant="error" :title="t('artifact.loadFailed')" :hint="error">
      <template #action><AppButton variant="secondary" @click="retry">{{ t('ui.state.retry') }}</AppButton></template>
    </StateBlock>

    <template v-else-if="versions.length === 0">
      <PageHeader :title="pkgName" :crumbs="crumbs" />
      <StateBlock variant="empty" :title="t('artifact.notFound.title')" :hint="tp('artifact.notFound.hint', { name: pkgName, repo: repoName })">
        <template #action>
          <AppButton :to="{ path: '/packages', query: { q: pkgName } }">{{ t('artifact.notFound.search') }}</AppButton>
          <AppButton variant="secondary" :to="`/repos/${encodeURIComponent(repoName)}`">{{ t('artifact.notFound.repo') }}</AppButton>
        </template>
      </StateBlock>
    </template>

    <template v-else-if="!entry">
      <PageHeader :title="pkgName" :crumbs="crumbs" />
      <StateBlock variant="empty" :title="t('artifact.versionNotFound.title')" :hint="tp('artifact.versionNotFound.hint', { version: requestedVersion ?? '', name: pkgName })">
        <template #action><AppButton :to="artifactPath(repoName, pkgName)">{{ t('artifact.versionNotFound.latest') }}</AppButton></template>
      </StateBlock>
    </template>

    <template v-else>
      <PageHeader :title="pkgName" :crumbs="crumbs">
        <template #meta>
          <FormatBadge :format="format" />
          <label class="sr-only" for="artifact-version">{{ t('artifact.version') }}</label>
          <select
            id="artifact-version"
            :value="entry.version"
            class="h-8 max-w-[16rem] rounded-lg border border-border-strong bg-surface px-2 font-mono text-sm text-foreground focus:ring-2 focus:ring-primary max-md:h-11"
            data-testid="version-select"
            @change="switchVersion(($event.target as HTMLSelectElement).value)"
          >
            <option v-for="v in versions" :key="v.version" :value="v.version">{{ v.version }}{{ v.latest ? ` (${t('artifact.badge.latest')})` : '' }}</option>
          </select>
          <span v-if="entry.latest" class="rounded-full border border-info/30 bg-info-soft px-2 py-0.5 text-xs text-info">{{ t('artifact.badge.latest') }}</span>
          <span v-if="entry.prerelease" class="rounded-full border border-border px-2 py-0.5 text-xs text-muted">{{ t('artifact.badge.prerelease') }}</span>
          <SeverityBadge v-if="worst !== 'none'" :severity="worst" :count="worstCount(summary?.vuln)" />
          <span v-if="summary?.quarantined" class="inline-flex items-center gap-1 rounded-full border border-warning/30 bg-warning-soft px-2 py-0.5 text-xs text-warning" data-testid="badge-quarantined">
            <LockClosedIcon class="h-3 w-3" aria-hidden="true" />{{ t('artifact.badge.quarantined') }}
          </span>
        </template>
        <template #actions><ArtifactActions /></template>
      </PageHeader>

      <div class="grid grid-cols-1 gap-8 lg:grid-cols-[minmax(0,1fr)_18rem]">
        <div class="min-w-0 space-y-6">
          <ArtifactInstall
            :format="format"
            :repo="repoName"
            :pkg="pkgName"
            :version="entry.version"
            :sha256="detail?.sha256"
            :quarantined="summary?.quarantined === true"
            :critical="summary?.vuln.critical ?? 0"
          />
          <AppTabs :key="entry.version" :tabs="tabs" :label="t('artifact.tabs')">
            <template #panel-overview><ArtifactOverviewTab /></template>
            <template #panel-versions><ArtifactVersionsTab /></template>
            <template #panel-files><ArtifactFilesTab /></template>
            <template #panel-dependencies><ArtifactDependenciesTab /></template>
            <template #panel-security><ArtifactSecurityTab /></template>
            <template #panel-sbom><ArtifactSbomTab /></template>
            <template #panel-activity><ArtifactActivityTab /></template>
          </AppTabs>
        </div>
        <ArtifactAside />
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { LockClosedIcon } from '@heroicons/vue/20/solid'
import AppButton from '../components/AppButton.vue'
import FormatBadge from '../components/FormatBadge.vue'
import PageHeader from '../components/PageHeader.vue'
import ArtifactActions from '../components/artifact/ArtifactActions.vue'
import ArtifactAside from '../components/artifact/ArtifactAside.vue'
import ArtifactInstall from '../components/artifact/ArtifactInstall.vue'
import ArtifactOverviewTab from '../components/artifact/ArtifactOverviewTab.vue'
import AppTabs from '../components/ui/AppTabs.vue'
import SeverityBadge from '../components/ui/SeverityBadge.vue'
import StateBlock from '../components/ui/StateBlock.vue'
import { useArtifact } from '../composables/useArtifact'
import { artifactPath } from '../lib/artifactPath'
import { securityFlag } from '../lib/artifactSecurity'
import { worstCount, worstSeverity } from '../lib/artifactVersions'
import { asyncTab } from '../lib/asyncTab'
import { packageTabDefs } from '../lib/packageTabs'
import { t, tp } from '../i18n/en'

const ArtifactVersionsTab = asyncTab(() => import('../components/artifact/ArtifactVersionsTab.vue'))
const ArtifactFilesTab = asyncTab(() => import('../components/artifact/ArtifactFilesTab.vue'))
const ArtifactDependenciesTab = asyncTab(() => import('../components/artifact/ArtifactDependenciesTab.vue'))
const ArtifactSecurityTab = asyncTab(() => import('../components/artifact/ArtifactSecurityTab.vue'))
const ArtifactSbomTab = asyncTab(() => import('../components/artifact/ArtifactSbomTab.vue'))
const ArtifactActivityTab = asyncTab(() => import('../components/artifact/ArtifactActivityTab.vue'))

const route = useRoute()
const router = useRouter()

const param = (name: string): string | undefined => {
  const raw = route.params[name]
  const value = Array.isArray(raw) ? raw.join('/') : raw
  return value ? String(value) : undefined
}
const requestedVersion = computed(() => param('v'))

const { repoName, pkgName, versions, entry, detail, summary, format, caps, loading, error, reload } = useArtifact({
  repo: () => param('repo') ?? '',
  pkg: () => param('pkg') ?? '',
  version: () => requestedVersion.value,
})

const worst = computed(() => worstSeverity(summary.value?.vuln))

// Package URLs with raw slashes cannot come from route records: the trail is built here.
const crumbs = computed(() => {
  const trail = [
    { label: t('nav.repositories'), to: '/repos' },
    { label: repoName.value, to: `/repos/${encodeURIComponent(repoName.value)}` },
    { label: pkgName.value, to: artifactPath(repoName.value, pkgName.value) },
  ]
  return requestedVersion.value ? [...trail, { label: requestedVersion.value }] : trail
})

const tabs = computed(() =>
  packageTabDefs({
    versionCount: versions.value.length,
    fileCount: detail.value?.files.length ?? entry.value?.files.length,
    securityFlag: securityFlag(summary.value),
    canSeeActivity: caps.value.maintain,
    canSeeSbom: caps.value.admin,
  }),
)

function switchVersion(version: string): void {
  const latest = versions.value[0]?.version === version
  void router.push({ path: artifactPath(repoName.value, pkgName.value, latest ? undefined : version), query: route.query.tab ? { tab: route.query.tab } : undefined })
}

function retry(): void {
  void reload()
}
</script>
