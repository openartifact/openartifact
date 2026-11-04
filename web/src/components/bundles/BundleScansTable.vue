<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Security tab of a bundle: vulnerability totals per source repository. -->
<template>
  <ListTable
    :columns="columns"
    :caption="t('bundles.tab.security')"
    :rows="rows"
    :loading="loading"
    row-key="repo"
    :total="rows.length"
    :limit="Math.max(rows.length, 1)"
  >
    <template #cell-repo="{ row }">
      <router-link :to="{ path: '/security', query: { type: 'vuln', repo: row.repo } }" class="text-primary hover:underline">{{ row.repo }}</router-link>
    </template>
    <template #cell-total="{ row }">
      <span v-if="row.failed" class="text-xs text-warning">{{ t('bundles.scans.failed') }}</span>
      <span v-else class="tabular-nums">{{ row.total }}</span>
    </template>
    <template #cell-critical="{ row }">
      <span v-if="!row.failed" class="tabular-nums" :class="row.critical > 0 ? 'font-medium text-danger' : ''">{{ row.critical }}</span>
    </template>
    <template #cell-high="{ row }"><span v-if="!row.failed" class="tabular-nums">{{ row.high }}</span></template>
  </ListTable>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import type { BundleScanRow } from '../../composables/useBundleScans'
import { t } from '../../i18n'

defineProps<{ rows: BundleScanRow[]; loading: boolean }>()

const columns = computed<ListColumn[]>(() => [
  { key: 'repo', label: t('bundles.scans.column.repo') },
  { key: 'total', label: t('bundles.scans.column.total'), align: 'end' },
  { key: 'critical', label: t('bundles.scans.critical'), align: 'end' },
  { key: 'high', label: t('bundles.scans.high'), align: 'end' },
])
</script>
