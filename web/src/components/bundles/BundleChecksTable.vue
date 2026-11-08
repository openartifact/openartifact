<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Checks tab of a bundle: what CI or policy tooling reported against it. Read only; checks are reported through the API or the CLI. -->
<template>
  <ListTable
    :columns="columns"
    :caption="t('bundles.tab.checks')"
    :rows="checks"
    row-key="name"
    :loading="loading"
    :error="error || undefined"
    :total="checks.length"
    :limit="Math.max(checks.length, 1)"
    :empty-title="t('bundles.checks.empty')"
    :empty-hint="t('bundles.checks.emptyHint')"
  >
    <template #cell-name="{ row }"><span class="block truncate font-mono text-xs" :title="row.name">{{ row.name }}</span></template>
    <template #cell-status="{ row }">
      <StatusBadge :label="checkLabel(row.status)" :variant="checkVariant(row.status)" />
      <p v-if="expired(row)" class="mt-1 text-xs text-warning">{{ t('bundles.check.expired') }}</p>
    </template>
    <template #cell-summary="{ row }">
      <span class="block truncate text-sm text-muted" :title="row.summary">{{ row.summary || '—' }}</span>
    </template>
    <template #cell-details="{ row }">
      <a v-if="detailsUrl(row)" :href="detailsUrl(row)!" target="_blank" rel="noopener noreferrer" class="text-sm text-primary hover:underline">
        {{ t('bundles.check.details') }}
      </a>
    </template>
    <template #cell-by="{ row }">{{ row.reported_by }}</template>
    <template #cell-updated="{ row }"><RelativeTime :value="row.updated_at" /></template>
    <template #cell-expires="{ row }">
      <RelativeTime v-if="row.expires_at" :value="row.expires_at" />
      <span v-else class="text-muted">—</span>
    </template>
  </ListTable>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import StatusBadge from '../StatusBadge.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import RelativeTime from '../ui/RelativeTime.vue'
import type { BundleCheck } from '../../api/bundles'
import { checkExpired, checkLabel, checkVariant, safeDetailsUrl } from '../../lib/bundles'
import { t } from '../../i18n'

withDefaults(defineProps<{ checks: BundleCheck[]; loading?: boolean; error?: string }>(), { loading: false, error: '' })

const expired = (c: BundleCheck): boolean => checkExpired(c)
const detailsUrl = (c: BundleCheck): string | null => safeDetailsUrl(c.details_url)

const columns = computed<ListColumn[]>(() => [
  { key: 'name', label: t('bundles.checks.column.name'), grow: true },
  { key: 'status', label: t('bundles.checks.column.status') },
  { key: 'summary', label: t('bundles.checks.column.summary'), dropOrder: 1 },
  { key: 'details', label: t('bundles.checks.column.details'), dropOrder: 4 },
  { key: 'by', label: t('bundles.checks.column.by'), dropOrder: 3 },
  { key: 'updated', label: t('bundles.checks.column.updated'), dropOrder: 2 },
  { key: 'expires', label: t('bundles.checks.column.expires'), dropOrder: 5 },
])
</script>
