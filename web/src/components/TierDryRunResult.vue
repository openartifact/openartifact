<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-4" data-testid="dry-run-result">
    <div class="flex items-start gap-2.5 rounded-lg border border-border bg-surface-hover/40 p-3" role="status">
      <InformationCircleIcon class="w-4 h-4 text-muted mt-0.5 shrink-0" aria-hidden="true" />
      <p class="text-xs text-foreground" data-testid="dry-run-no-changes">{{ t('tiering.dryRun.noChanges') }}</p>
    </div>

    <div
      v-if="result.truncated"
      class="flex items-start gap-2.5 rounded-lg border border-warning/30 bg-warning-soft p-3"
      role="alert"
      data-testid="dry-run-truncated"
    >
      <ExclamationTriangleIcon class="w-4 h-4 text-warning mt-0.5 shrink-0" aria-hidden="true" />
      <p class="text-xs text-warning">{{ t('tiering.dryRun.truncated') }}</p>
    </div>

    <dl class="grid grid-cols-3 gap-3">
      <div v-for="stat in stats" :key="stat.label" class="rounded-lg border border-border p-3">
        <dt class="text-caption text-muted">{{ stat.label }}</dt>
        <dd class="text-xl font-bold tabular-nums text-foreground" :data-testid="stat.testid">{{ stat.value }}</dd>
      </div>
    </dl>

    <section v-for="group in groups" :key="group.title" :aria-label="group.title">
      <h3 class="text-xs font-semibold text-foreground mb-1.5">{{ group.title }}</h3>
      <TableScroller>
        <table class="w-full text-sm">
          <thead>
            <tr class="text-left text-caption font-semibold text-muted">
              <th scope="col" class="py-1.5 pr-3">{{ group.keyHeading }}</th>
              <th scope="col" class="py-1.5 px-3 text-right">{{ t('tiering.dryRun.blobs') }}</th>
              <th scope="col" class="py-1.5 pl-3 text-right">{{ t('tiering.dryRun.size') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            <tr v-for="row in group.rows" :key="row.key" class="group/row" :data-testid="group.rowTestid">
              <td class="py-1.5 pr-3" :class="row.mono ? 'font-mono text-xs' : undefined">{{ row.label }}</td>
              <td class="py-1.5 px-3 text-right tabular-nums">{{ formatCount(row.count) }}</td>
              <td class="py-1.5 pl-3 text-right tabular-nums">{{ formatBytes(row.bytes ?? 0) }}</td>
            </tr>
          </tbody>
        </table>
      </TableScroller>
    </section>

    <section v-if="sample.length" :aria-label="t('tiering.dryRun.sample')">
      <h3 class="text-xs font-semibold text-foreground mb-1.5">{{ t('tiering.dryRun.sample') }}</h3>
      <TierBlobTable :rows="sample" row-testid="dry-run-sample">
        <template #head>
          <th scope="col" class="py-1.5 px-3">{{ t('tiering.dryRun.policy') }}</th>
          <th scope="col" class="py-1.5 pl-3">{{ t('tiering.dryRun.lastAccessed') }}</th>
        </template>
        <template #cells="{ row }">
          <td class="py-1.5 px-3">{{ row.policy || t('tiering.dryRun.promoteOnRead') }}</td>
          <td class="py-1.5 pl-3 text-muted whitespace-nowrap">
            {{ formatDate(row.last_accessed_at, t('tiering.dryRun.neverAccessed')) }}
          </td>
        </template>
      </TierBlobTable>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ExclamationTriangleIcon, InformationCircleIcon } from '@heroicons/vue/24/outline'
import TierBlobTable from './TierBlobTable.vue'
import TableScroller from './ui/TableScroller.vue'
import type { TierDryRun } from '../api/tiering'
import { t } from '../i18n'
import { formatDate } from '../lib/datetime'
import { formatBytes } from '../lib/quota'
import { formatCount } from '../lib/tiering'

const props = defineProps<{ result: TierDryRun }>()

const sample = computed(() => props.result.sample ?? [])

const stats = computed(() => [
  { label: t('tiering.dryRun.wouldMove'), value: formatCount(props.result.would_move?.count), testid: 'dry-run-count' },
  { label: t('tiering.dryRun.totalSize'), value: formatBytes(props.result.would_move?.bytes ?? 0), testid: 'dry-run-bytes' },
  { label: t('tiering.dryRun.scanned'), value: formatCount(props.result.scanned), testid: undefined },
])

/** The blob count and size per route and per policy, shown as two small tables. */
const groups = computed(() =>
  [
    {
      title: t('tiering.dryRun.routes'),
      keyHeading: t('tiering.dryRun.route'),
      rowTestid: 'dry-run-route',
      rows: (props.result.routes ?? []).map((r) => ({
        key: `${r.from}>${r.to}`,
        label: `${r.from} → ${r.to}`,
        mono: true,
        count: r.count,
        bytes: r.bytes,
      })),
    },
    {
      title: t('tiering.dryRun.policies'),
      keyHeading: t('tiering.policy.column.name'),
      rowTestid: 'dry-run-policy',
      rows: (props.result.policies ?? []).map((p) => ({
        key: p.policy ?? '',
        label: p.policy || t('tiering.dryRun.promoteOnRead'),
        mono: false,
        count: p.count,
        bytes: p.bytes,
      })),
    },
  ].filter((g) => g.rows.length > 0),
)
</script>
