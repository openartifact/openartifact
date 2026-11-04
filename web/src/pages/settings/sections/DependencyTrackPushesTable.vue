<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <TableScroller class="rounded-lg border border-border">
    <table class="w-full text-xs" data-testid="dt-pushes">
      <thead class="bg-surface-hover/50 text-muted text-left">
        <tr>
          <th scope="col" class="px-3 py-2 font-medium">{{ t('adm.dt.col.artifact') }}</th>
          <th scope="col" class="px-3 py-2 font-medium">{{ t('adm.dt.col.state') }}</th>
          <th scope="col" class="px-3 py-2 font-medium">{{ t('adm.dt.col.detail') }}</th>
          <th scope="col" data-drop-order="6" class="px-3 py-2 font-medium">{{ t('adm.dt.col.project') }}</th>
          <th scope="col" data-drop-order="5" class="px-3 py-2 font-medium">{{ t('adm.dt.col.components') }}</th>
          <th scope="col" data-drop-order="4" class="px-3 py-2 font-medium">{{ t('adm.dt.col.vex') }}</th>
          <th scope="col" data-drop-order="3" class="px-3 py-2 font-medium">{{ t('adm.dt.col.bomPushed') }}</th>
          <th scope="col" data-drop-order="2" class="px-3 py-2 font-medium">{{ t('adm.dt.col.vexPushed') }}</th>
          <th scope="col" data-drop-order="1" class="px-3 py-2 font-medium">{{ t('adm.dt.col.updated') }}</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-border">
        <tr v-for="p in pushes" :key="p.artifact_id" class="group/row">
          <td class="truncate px-3 py-2 font-mono text-foreground" :class="GROW_CELL" :title="p.artifact_id">{{ p.artifact_id }}</td>
          <td class="px-3 py-2"><StatusBadge :label="stateLabel(p.state)" :variant="stateVariant(p.state)" /></td>
          <td class="px-3 py-2 text-foreground break-words max-w-xs">{{ p.detail || '—' }}</td>
          <td class="px-3 py-2 font-mono text-foreground">
            {{ p.project_name ? `${p.project_name} ${p.project_version ?? ''}`.trim() : '—' }}
          </td>
          <td class="px-3 py-2 text-foreground">{{ p.components ?? 0 }}</td>
          <td class="px-3 py-2 text-foreground">{{ p.vex_applied ?? 0 }} / {{ p.vex_statements ?? 0 }}</td>
          <td class="px-3 py-2 text-muted whitespace-nowrap">{{ formatDateTime(p.bom_pushed_at) }}</td>
          <td class="px-3 py-2 text-muted whitespace-nowrap">{{ formatDateTime(p.vex_pushed_at) }}</td>
          <td class="px-3 py-2 text-muted whitespace-nowrap">{{ formatDateTime(p.updated_at) }}</td>
        </tr>
      </tbody>
    </table>
  </TableScroller>
</template>

<script setup lang="ts">
import StatusBadge from '../../../components/StatusBadge.vue'
import TableScroller from '../../../components/ui/TableScroller.vue'
import { GROW_CELL } from '../../../components/ui/tableStyles'
import type { DependencyTrackPush } from '../../../api/dependencyTrack'
import { formatDateTime } from '../../../lib/datetime'
import { t } from '../../../i18n'

defineProps<{ pushes: DependencyTrackPush[] }>()

function stateLabel(state: DependencyTrackPush['state']): string {
  return t(`adm.dt.state.${state ?? 'queued'}`)
}
function stateVariant(state: DependencyTrackPush['state']): 'success' | 'warning' | 'neutral' | 'danger' {
  switch (state) {
    case 'done': return 'success'
    case 'failed': return 'danger'
    case 'skipped': return 'neutral'
    default: return 'warning'
  }
}
</script>
