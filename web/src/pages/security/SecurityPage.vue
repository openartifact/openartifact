<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <PageHeader :title="t('security.page.title')" :description="t('security.page.description')">
      <template #actions>
        <ScanExportButtons
          v-if="!forbidden"
          :formats="FINDINGS_EXPORT_FORMATS"
          :url="(format) => findingsExportUrl(toFindingFilters(filters), format as FindingsExportFormat)"
          :disabled="list.loading.value"
          size="md"
        />
        <AppButton v-if="isAdmin && !forbidden" variant="primary" data-testid="run-scan" @click="scanOpen = true">
          <ShieldCheckIcon class="h-4 w-4" aria-hidden="true" />
          {{ t('security.scan.run') }}
        </AppButton>
      </template>
    </PageHeader>

    <StateBlock v-if="forbidden" variant="forbidden" :title="t('security.forbidden.title')" :hint="t('security.forbidden.hint')" data-testid="security-forbidden">
      <template #action>
        <AppButton to="/packages" variant="primary" data-testid="forbidden-packages">{{ t('security.forbidden.action') }}</AppButton>
      </template>
    </StateBlock>

    <template v-else>
      <p v-if="!isAdmin" class="mb-4 rounded-lg border border-border bg-surface-hover px-4 py-2.5 text-sm text-muted" data-testid="scope-note">
        {{ t('security.scope.note') }}
      </p>

      <SecurityHealthNotice v-if="health" :health="health" class="mb-4" />

      <SecuritySummaryStrip class="mb-4" :summary="summary" :filters="filters" :error="summaryError" @select="applyStrip" />

      <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
        <SecurityTypeSwitch :model-value="filters.type" :summary="summary" @update:model-value="(ty) => list.setFilter('type', ty)" />
      </div>

      <FilterBar
        :search="list.text.q ?? filters.q"
        :search-label="t('security.filter.search')"
        :placeholder="t('security.filter.searchPlaceholder')"
        :active="filtersActive"
        @update:search="(v) => list.setFilter('q', v)"
        @clear="clearAll"
      >
        <select
          :value="filters.severity"
          :aria-label="t('security.filter.severity')"
          :class="SELECT"
          data-testid="filter-severity"
          @change="list.setFilter('severity', ($event.target as HTMLSelectElement).value as InboxFilters['severity'])"
        >
          <option value="">{{ t('security.filter.allSeverities') }}</option>
          <option v-for="s in SEVERITY_FILTERS" :key="s" :value="s">{{ t(`ui.severity.${s}`) }}</option>
        </select>
        <select
          :value="filters.status"
          :aria-label="t('security.filter.status')"
          :class="SELECT"
          data-testid="filter-status"
          @change="list.setFilter('status', ($event.target as HTMLSelectElement).value as InboxFilters['status'])"
        >
          <option value="">{{ t('security.filter.allStatuses') }}</option>
          <option v-for="s in STATUS_FILTERS" :key="s" :value="s">{{ tDynamic(`security.status.${s}`) }}</option>
        </select>
        <select
          :value="filters.repo"
          :aria-label="t('security.filter.repo')"
          :class="SELECT"
          data-testid="filter-repo"
          @change="list.setFilter('repo', ($event.target as HTMLSelectElement).value)"
        >
          <option value="">{{ t('security.filter.allRepos') }}</option>
          <option v-if="filters.repo && !repoNames.includes(filters.repo)" :value="filters.repo">{{ filters.repo }}</option>
          <option v-for="name in repoNames" :key="name" :value="name">{{ name }}</option>
        </select>
      </FilterBar>

      <!-- Distinct non-list states: nothing scanned yet, all clear, AI off. -->
      <div v-if="blockState" class="rounded-lg border border-border bg-surface" data-testid="security-state">
        <StateBlock v-if="blockState === 'never'" variant="empty" :title="t('security.never.title')" :hint="t('security.never.hint')" :command="t('security.never.command')">
          <template v-if="isAdmin" #action>
            <AppButton variant="primary" @click="scanOpen = true">{{ t('security.scan.run') }}</AppButton>
          </template>
        </StateBlock>
        <StateBlock v-else-if="blockState === 'ai-off'" variant="off" :title="t('security.ai.offTitle')" :hint="t('security.ai.offHint')" data-testid="ai-off">
          <template #action>
            <AppButton to="/settings/ai" variant="primary">{{ t('security.ai.offAction') }}</AppButton>
          </template>
        </StateBlock>
        <StateBlock v-else variant="empty" :title="t('security.clear.title')" :hint="t('security.clear.hint')" data-testid="all-clear" />
      </div>

      <ListTable
        v-else
        :columns="columns"
        :caption="t('security.table.caption')"
        :rows="list.rows.value"
        row-key="id"
        :loading="list.loading.value"
        :error="list.error.value"
        paging="cursor"
        :has-more="list.hasMore.value"
        :filtered="filtersActive"
        :empty-title="t('security.empty.title')"
        :empty-hint="t('security.empty.hint')"
        @load-more="list.loadMore()"
        @retry="list.reload()"
        @clear="clearAll"
      >
        <template #cell-severity="{ row }">
          <SeverityBadge :severity="(row as FindingRow).severity" />
        </template>
        <template #cell-title="{ row }">
          <div class="min-w-0">
            <button
              type="button"
              class="max-w-[28rem] text-left font-medium text-foreground hover:text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:min-h-11"
              :data-testid="`finding-${(row as FindingRow).id}`"
              @click="selected = (row as FindingRow).source"
            >
              <span class="line-clamp-2 break-words">{{ (row as FindingRow).title || t('security.table.untitled') }}</span>
            </button>
            <div class="mt-0.5 flex flex-wrap items-center gap-1.5 text-caption text-muted">
              <span class="rounded border border-border px-1.5 py-px">{{ tDynamic(`security.type.${(row as FindingRow).type}`) }}</span>
              <span v-if="(row as FindingRow).vexStatus">
                <VexStatusBadge :status="(row as FindingRow).vexStatus!" />
              </span>
            </div>
          </div>
        </template>
        <template #cell-artifact="{ row }">
          <div class="min-w-0">
            <router-link :to="linkOf(row as FindingRow)" class="font-medium text-primary hover:underline">{{ (row as FindingRow).artifact || t('security.table.removedPackage') }}</router-link>
            <div class="mt-0.5 flex items-center gap-1.5 text-caption text-muted">
              <FormatBadge v-if="(row as FindingRow).format" :format="(row as FindingRow).format" size="sm" />
              {{ (row as FindingRow).repository }}
            </div>
          </div>
        </template>
        <template #cell-scanner="{ row }">
          <span class="text-sm text-muted">{{ (row as FindingRow).scanners.join(', ') || '—' }}</span>
        </template>
        <template #cell-counts="{ row }">
          <span class="text-sm tabular-nums text-muted">{{ countsText((row as FindingRow).source.counts) || '—' }}</span>
        </template>
        <template #cell-status="{ row }">
          <StatusBadge :label="tDynamic(`security.status.${(row as FindingRow).status}`)" :variant="statusVariant((row as FindingRow).status)" />
        </template>
        <template #cell-scannedAt="{ row }">
          <time class="whitespace-nowrap text-xs text-muted" :datetime="(row as FindingRow).scannedAt" :title="formatDateTime((row as FindingRow).scannedAt)">
            {{ timeAgo((row as FindingRow).scannedAt) }}
          </time>
        </template>
      </ListTable>

      <VexStatementsPanel
        v-if="isAdmin && filters.repo && (filters.type === 'all' || filters.type === 'vuln')"
        :key="vexKey"
        class="mt-6"
        :repo="filters.repo"
        :artifact-labels="vexLabels"
        @changed="vexKey++"
      />
    </template>

    <SecurityFindingDrawer :finding="selected" :can-act="isAdmin" @close="selected = null" @changed="onChanged" />
    <SecurityScanDrawer
      v-if="isAdmin"
      :open="scanOpen"
      :repo="filters.repo"
      :initial-kind="filters.type === 'all' ? 'vuln' : filters.type"
      :ai-enabled="health?.ai.enabled"
      @close="scanOpen = false"
      @started="onChanged"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ShieldCheckIcon } from '@heroicons/vue/24/outline'
import AppButton from '../../components/AppButton.vue'
import FormatBadge from '../../components/FormatBadge.vue'
import PageHeader from '../../components/PageHeader.vue'
import ScanExportButtons from '../../components/ScanExportButtons.vue'
import StatusBadge from '../../components/StatusBadge.vue'
import VexStatementsPanel from '../../components/VexStatementsPanel.vue'
import VexStatusBadge from '../../components/VexStatusBadge.vue'
import SecurityFindingDrawer from '../../components/security/SecurityFindingDrawer.vue'
import SecurityHealthNotice from '../../components/security/SecurityHealthNotice.vue'
import SecurityScanDrawer from '../../components/security/SecurityScanDrawer.vue'
import SecuritySummaryStrip from '../../components/security/SecuritySummaryStrip.vue'
import SecurityTypeSwitch from '../../components/security/SecurityTypeSwitch.vue'
import FilterBar from '../../components/ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../../components/ui/ListTable.vue'
import SeverityBadge from '../../components/ui/SeverityBadge.vue'
import StateBlock from '../../components/ui/StateBlock.vue'
import { listAllRepositories } from '../../api/repositories'
import { FINDINGS_EXPORT_FORMATS, findingsExportUrl, type FindingCounts, type FindingsExportFormat, type SecurityFinding } from '../../api/security'
import { useSecurityInbox } from '../../composables/useSecurityInbox'
import { formatDateTime, timeAgo } from '../../lib/datetime'
import {
  SEVERITY_FILTERS,
  STATUS_FILTERS,
  countsLabel,
  findingLink,
  hasNarrowingFilters,
  statusVariant,
  toFindingFilters,
  type FindingRow,
  type InboxFilters,
} from '../../lib/securityFindings'
import { useSessionStore } from '../../stores/session'
import { t, tDynamic } from '../../i18n'

const SELECT =
  'h-9 rounded-lg border border-border-strong bg-surface px-3 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary max-md:h-11'

const session = useSessionStore()
const isAdmin = computed(() => session.isAdmin)
const { list, filters, forbidden, summary, summaryError, health, vexLabels, allClear, neverScanned, applyStrip, refresh } =
  useSecurityInbox()

const selected = ref<SecurityFinding | null>(null)
const scanOpen = ref(false)
const vexKey = ref(0)
const repoNames = ref<string[]>([])

onMounted(async () => {
  try {
    repoNames.value = (await listAllRepositories()).map((r) => r.name)
  } catch {
    // The select keeps the repository from the URL; the list itself reports its own errors.
  }
})

const columns = computed<ListColumn[]>(() => [
  { key: 'severity', label: t('security.col.severity') },
  { key: 'title', label: t('security.col.finding'), hideLabel: true },
  { key: 'artifact', label: t('security.col.package'), hideLabel: true },
  ...(filters.value.type === 'all' || filters.value.type === 'vuln' ? [{ key: 'scanner', label: t('security.col.scanner') }] : []),
  { key: 'counts', label: t('security.col.counts') },
  { key: 'status', label: t('security.col.status') },
  { key: 'scannedAt', label: t('security.col.found') },
])

/** Anything beyond the type and repository narrows the list: an empty result is "no matches", not "all clear". */
const filtersActive = computed(() => hasNarrowingFilters(filters.value) || filters.value.repo !== '')

const blockState = computed<'never' | 'ai-off' | 'clear' | null>(() => {
  if (neverScanned.value) return 'never'
  if (filters.value.type === 'ai' && health.value?.ai.enabled === false && list.rows.value.length === 0 && !list.loading.value && !list.error.value) return 'ai-off'
  return allClear.value ? 'clear' : null
})

function clearAll(): void {
  list.setFilters({ severity: '', status: '', repo: '', q: '' })
}

const linkOf = (row: FindingRow): string => findingLink(row.source)

const countsText = (c: FindingCounts): string => countsLabel(c, (s) => t(`ui.severity.${s}`))

function onChanged(): void {
  vexKey.value++
  void refresh()
}
</script>
