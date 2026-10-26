<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Settings > Security > Scanners: which vulnerability scanners are ready, and what the pipeline is doing. -->
<template>
  <div class="space-y-5">
    <SectionActions>
      <AppButton variant="secondary" size="sm" class="max-md:h-11" :loading="loading" @click="load">{{ t('sx.refresh') }}</AppButton>
    </SectionActions>

    <p
      v-if="health"
      class="flex items-center gap-2 text-sm"
      :class="readyCount > 0 ? 'text-foreground' : 'text-warning'"
      role="status"
      data-testid="scanner-summary"
    >
      <ExclamationTriangleIcon v-if="readyCount === 0" class="h-4 w-4 shrink-0" aria-hidden="true" />
      {{ readyCount > 0 ? tp('sx.scanners.summary.ready', { ready: String(readyCount), total: String(health.scanners.length) }) : t('sx.scanners.summary.none') }}
    </p>

    <ListTable
      :columns="columns"
      :caption="t('sx.scanners.table')"
      :rows="health?.scanners ?? []"
      row-key="name"
      :loading="loading"
      :error="error"
      :empty-title="t('sx.scanners.summary.none')"
      @retry="load"
    >
      <template #cell-scanner="{ row }">
        <span class="font-medium text-foreground">{{ vulnScannerLabel((row as Scanner).name) }}</span>
      </template>
      <template #cell-status="{ row }">
        <StatusBadge
          :label="(row as Scanner).ready ? t('sx.scanners.ready') : t('sx.scanners.notReady')"
          :variant="(row as Scanner).ready ? 'success' : 'warning'"
        />
      </template>
      <template #cell-detail="{ row }">
        <span class="text-muted">{{ (row as Scanner).message || ((row as Scanner).ready ? t('sx.scanners.readyDetail') : '') }}</span>
        <a
          v-if="safeDocsUrl((row as Scanner).docs_url)"
          :href="safeDocsUrl((row as Scanner).docs_url)"
          target="_blank"
          rel="noopener"
          class="ml-2 text-primary hover:underline"
        >{{ t('sx.scanners.guide') }}</a>
      </template>
    </ListTable>

    <section v-if="health" class="rounded-lg border border-border bg-surface p-4" data-testid="scan-activity">
      <h2 class="text-sm font-semibold text-foreground">{{ t('sx.scanners.activity.title') }}</h2>
      <dl class="mt-3 grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
        <div>
          <dt class="text-muted">{{ t('sx.scanners.lastScan') }}</dt>
          <dd class="text-foreground">
            <RelativeTime :value="health.last_scan_at" :fallback="t('sx.never')" />
          </dd>
        </div>
        <div>
          <dt class="text-muted">{{ t('sx.scanners.pending') }}</dt>
          <dd class="tabular-nums text-foreground">{{ health.scans_pending }}</dd>
        </div>
        <div>
          <dt class="text-muted">{{ t('sx.scanners.failed') }}</dt>
          <dd class="tabular-nums" :class="health.scans_failed_24h > 0 ? 'font-medium text-danger' : 'text-foreground'">{{ health.scans_failed_24h }}</dd>
        </div>
        <div>
          <dt class="text-muted">{{ t('sx.scanners.unscanned') }}</dt>
          <dd class="tabular-nums" :class="health.unscanned_repositories > 0 ? 'font-medium text-warning' : 'text-foreground'">{{ health.unscanned_repositories }}</dd>
        </div>
      </dl>
    </section>

    <section class="rounded-lg border border-border bg-surface p-4" data-testid="scanner-config">
      <h2 class="text-sm font-semibold text-foreground">{{ t('sx.scanners.configure.title') }}</h2>
      <p class="mt-0.5 text-sm text-muted">{{ t('sx.scanners.configure.body') }}</p>
      <code class="mt-2 block rounded-md bg-surface-hover px-3 py-2 font-mono text-xs text-foreground">{{ t('sx.scanners.configure.command') }}</code>
      <div class="mt-3 flex flex-wrap items-center gap-3">
        <AppButton variant="secondary" size="sm" class="max-md:h-11" to="/security">{{ t('sx.scanners.openSecurity') }}</AppButton>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ExclamationTriangleIcon } from '@heroicons/vue/20/solid'
import AppButton from '../../../components/AppButton.vue'
import SectionActions from '../../../components/SectionActions.vue'
import StatusBadge from '../../../components/StatusBadge.vue'
import ListTable, { type ListColumn } from '../../../components/ui/ListTable.vue'
import RelativeTime from '../../../components/ui/RelativeTime.vue'
import type { SecurityHealth } from '../../../api/security'
import { useSecurityHealth } from '../../../composables/useSecurityHealth'
import { safeDocsUrl, vulnScannerLabel } from '../../../lib/vulnScanners'
import { t, tp } from '../../../i18n'

type Scanner = SecurityHealth['scanners'][number]

const { health, loading, error, load } = useSecurityHealth(t('sx.scanners.loadFailed'))

const readyCount = computed(() => health.value?.scanners.filter((s) => s.ready).length ?? 0)

const columns: ListColumn[] = [
  { key: 'scanner', label: t('sx.scanners.col.scanner') },
  { key: 'status', label: t('sx.scanners.col.status') },
  { key: 'detail', label: t('sx.scanners.col.detail') },
]
</script>
