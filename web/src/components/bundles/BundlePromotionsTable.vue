<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Promotions tab of a bundle: one row per promotion between stages. -->
<template>
  <ListTable
    :columns="columns"
    :caption="t('bundles.tab.promotions')"
    :rows="promotions"
    :total="promotions.length"
    :limit="Math.max(promotions.length, 1)"
    :empty-title="t('bundles.promotions.empty')"
    :empty-hint="t('bundles.promotions.emptyHint')"
  >
    <template #cell-stages="{ row }">
      <span class="font-mono text-xs">{{ row.from_stage || t('bundles.noStage') }} → {{ row.to_stage }}</span>
    </template>
    <template #cell-status="{ row }">
      <StatusBadge :label="promotionLabel(row.status)" :variant="promotionVariant(row.status)" />
      <p v-if="row.error_message" class="mt-1 max-w-xs text-xs text-danger">{{ row.error_message }}</p>
    </template>
    <template #cell-by="{ row }">{{ row.promoted_by }}</template>
    <template #cell-items="{ row }"><span class="tabular-nums">{{ row.item_count }}</span></template>
    <template #cell-started="{ row }"><RelativeTime :value="row.started_at" /></template>
    <template #cell-notes="{ row }"><span class="block truncate text-sm text-muted" :title="row.notes">{{ row.notes || '—' }}</span></template>
  </ListTable>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import StatusBadge from '../StatusBadge.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import RelativeTime from '../ui/RelativeTime.vue'
import type { BundlePromotion } from '../../api/bundles'
import { promotionLabel, promotionVariant } from '../../lib/bundles'
import { t } from '../../i18n'

defineProps<{ promotions: BundlePromotion[] }>()

const columns = computed<ListColumn[]>(() => [
  { key: 'stages', label: t('bundles.promotions.column.stages') },
  { key: 'status', label: t('bundles.promotions.column.status') },
  { key: 'by', label: t('bundles.promotions.column.by'), dropOrder: 2 },
  { key: 'items', label: t('bundles.promotions.column.items'), align: 'end' },
  { key: 'started', label: t('bundles.promotions.column.started') },
  { key: 'notes', label: t('bundles.promotions.column.notes'), grow: true, dropOrder: 1 },
])
</script>
