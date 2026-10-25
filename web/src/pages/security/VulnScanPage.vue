<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- Header -->
    <PageHeader :title="t('sec.vuln.title')" :description="t('sec.vuln.description')">
      <template #actions>
        <div class="flex items-center gap-3 flex-wrap">
          <ScanExportButtons :url="exportUrl" />
          <AppButton variant="primary" size="md" @click="scanDialog?.open(filters.repo)">
            <ShieldCheckIcon class="w-4 h-4" aria-hidden="true" />
            {{ t('sec.vuln.scanPackage') }}
          </AppButton>
        </div>
      </template>
    </PageHeader>

    <!-- Summary cards (respect the repository filter only; click to filter) -->
    <VulnScanSummaryCards :summary="summary" :error="summaryError" :filters="filters" @filter="setFilter" />

    <!-- Filter toolbar -->
    <VulnScanFilterBar
      :filters="filters"
      :repos="repos"
      :has-filters="hasFilters"
      :loading="loading"
      @select="setFilter"
      @clear="clearFilters"
      @refresh="refresh()"
    />

    <!-- Scans table -->
    <VulnScanTable
      :scans="scans"
      :has-more="hasMore"
      :has-filters="hasFilters"
      :loading="loading"
      :loading-more="loadingMore"
      :load-error="loadError"
      :quarantining="quarantining"
      @refresh="refresh()"
      @clear-filters="clearFilters"
      @load-more="loadMore"
      @details="openDetail"
      @quarantine="toggleQuarantine"
      @delete="deleteTarget = $event"
    />

    <!-- VEX decisions of the filtered repository -->
    <VexStatementsPanel ref="vexPanel" :repo="filters.repo" :artifact-labels="artifactLabels" @changed="reloadDetailVex" />

    <!-- Scan details (full findings, loaded from the scan's repository) -->
    <VulnScanDetailDialog
      :row="detailRow"
      :detail="detail"
      :loading="detailLoading"
      :error="detailError"
      :vex-error="vexError"
      :vex-by-vuln="vexByVuln"
      :vex-vuln-id="vexVulnId"
      @close="closeDetail"
      @decide="openVexDecision"
      @close-decision="closeVexDecision"
      @saved="onVexSaved"
      @deleted="onVexDeleted"
    />

    <ConfirmDialog
      :open="deleteTarget !== null"
      :title="t('sec.vuln.deleteTitle')"
      :description="deleteTarget ? `${deleteTarget.repository_name} · ${deleteTarget.package_name}@${deleteTarget.package_version}` : ''"
      :confirm-label="t('sec.vuln.delete')"
      :busy="deleting"
      @confirm="confirmDelete"
      @close="deleteTarget = null"
    />

    <!-- Scan drawer -->
    <VulnScanDialog
      ref="scanDialog"
      v-model:repo="drawerRepo"
      :repos="repos"
      :packages="packages"
      :find-package="findPackage"
      @started="refresh()"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { ShieldCheckIcon } from '@heroicons/vue/24/outline'
import AppButton from '../../components/AppButton.vue'
import ConfirmDialog from '../../components/ConfirmDialog.vue'
import PageHeader from '../../components/PageHeader.vue'
import ScanExportButtons from '../../components/ScanExportButtons.vue'
import VexStatementsPanel from '../../components/VexStatementsPanel.vue'
import VulnScanDetailDialog from '../../components/VulnScanDetailDialog.vue'
import VulnScanDialog from '../../components/VulnScanDialog.vue'
import VulnScanFilterBar from '../../components/VulnScanFilterBar.vue'
import VulnScanSummaryCards from '../../components/VulnScanSummaryCards.vue'
import VulnScanTable from '../../components/VulnScanTable.vue'
import { useRepoPackages } from '../../composables/useRepoPackages'
import { useVulnScanDetail } from '../../composables/useVulnScanDetail'
import { useVulnScans } from '../../composables/useVulnScans'
import { t } from '../../i18n/en'

// The composable's selection is the scan drawer's repository: the overview itself is cross-repository.
const { repos, reposLoaded, selectedRepo: drawerRepo, packages, findPackage } = useRepoPackages({ selection: 'manual' })

const vexPanel = ref<InstanceType<typeof VexStatementsPanel> | null>(null)
const scanDialog = ref<InstanceType<typeof VulnScanDialog> | null>(null)

const {
  detailRow,
  detail,
  detailLoading,
  detailError,
  vexError,
  vexVulnId,
  vexByVuln,
  openDetail,
  closeDetail,
  reloadDetailVex,
  openVexDecision,
  closeVexDecision,
  onVexSaved,
  onVexDeleted,
} = useVulnScanDetail(() => void vexPanel.value?.reload())

const {
  filters,
  hasFilters,
  exportUrl,
  setFilter,
  clearFilters,
  summary,
  summaryError,
  scans,
  hasMore,
  loading,
  loadingMore,
  loadError,
  artifactLabels,
  refresh,
  loadMore,
  quarantining,
  toggleQuarantine,
  deleteTarget,
  deleting,
  confirmDelete,
} = useVulnScans((deleted) => {
  if (detailRow.value?.id === deleted.id) closeDetail()
})

// A repository that does not exist is dropped from the filter once the list is known.
watch(reposLoaded, () => {
  const repo = filters.value.repo
  if (repo && repos.value.length > 0 && !repos.value.some((r) => r.name === repo)) setFilter({ repo: '' })
})
</script>
