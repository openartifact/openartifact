<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <p v-if="!findings.length" class="text-sm text-muted">{{ t('security.vuln.noFindings') }}</p>
  <TableScroller v-else>
    <table class="w-full text-sm">
      <thead class="border-b border-border">
        <tr>
          <th scope="col" class="text-left py-2 pr-3 text-muted font-medium">{{ t('security.vuln.finding.id') }}</th>
          <th scope="col" class="text-left py-2 pr-3 text-muted font-medium">{{ t('security.vuln.finding.severity') }}</th>
          <th scope="col" data-drop-order="3" class="text-left py-2 pr-3 text-muted font-medium">{{ t('security.vuln.finding.exploit') }}</th>
          <th scope="col" data-drop-order="2" class="text-left py-2 pr-3 text-muted font-medium">{{ t('security.vuln.finding.cvss') }}</th>
          <th scope="col" data-drop-order="1" class="text-left py-2 pr-3 text-muted font-medium">{{ t('security.vuln.finding.fixedIn') }}</th>
          <th scope="col" class="text-left py-2 pr-3 text-muted font-medium">{{ t('security.vuln.finding.summary') }}</th>
          <th scope="col" class="text-left py-2 pr-3 text-muted font-medium">{{ t('security.vex.eyebrow') }}</th>
          <th v-if="canDecide" scope="col" class="py-2 pl-3" :class="ACTIONS_HEAD"><span class="sr-only">{{ t('common.actions') }}</span></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="f in findings" :key="f.id" class="group/row border-b border-border last:border-0 align-top" data-testid="finding-row">
          <td class="py-2 pr-3 font-mono text-xs">
            {{ f.id }}
            <div v-if="f.cve && f.cve !== f.id" class="text-muted" data-testid="finding-cve">{{ f.cve }}</div>
          </td>
          <td class="py-2 pr-3 text-xs">{{ f.severity }}</td>
          <td class="py-2 pr-3" data-testid="finding-exploit">
            <div class="flex flex-wrap items-center gap-1">
              <KevBadge v-if="f.kev" :date-added="f.kev_date_added" :due-date="f.kev_due_date" :ransomware="f.kev_ransomware" />
              <EpssBadge v-if="f.epss !== null" :score="f.epss" :percentile="f.epss_percentile" :date="f.epss_date" :elevated="f.priority === 'epss'" />
              <span v-if="!f.kev && f.epss === null" class="text-xs text-muted">—</span>
            </div>
          </td>
          <td class="py-2 pr-3 text-xs font-mono">{{ f.cvss > 0 ? f.cvss : '—' }}</td>
          <td class="py-2 pr-3 text-xs font-mono">{{ f.fixed_in || '—' }}</td>
          <td class="truncate py-2 pr-3 text-xs text-muted" :class="GROW_CELL" :title="f.summary">{{ f.summary }}</td>
          <td class="py-2 pr-3">
            <VexStatusBadge v-if="vexByVuln.get(f.id)" :status="vexByVuln.get(f.id)!.status" data-testid="finding-vex-status" />
          </td>
          <td v-if="canDecide" class="py-2 pl-3" :class="ACTIONS_CELL">
            <RowActions :actions="decisionActions(f.id)" @action="emit('decide', f.id)" />
          </td>
        </tr>
      </tbody>
    </table>
  </TableScroller>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { CheckBadgeIcon } from '@heroicons/vue/20/solid'
import RowActions, { type RowAction } from './ui/RowActions.vue'
import TableScroller from './ui/TableScroller.vue'
import { ACTIONS_CELL, ACTIONS_HEAD, GROW_CELL } from './ui/tableStyles'
import VexStatusBadge from './VexStatusBadge.vue'
import EpssBadge from './security/EpssBadge.vue'
import KevBadge from './security/KevBadge.vue'
import { vulnFindings, type VulnScan } from '../api/security'
import type { VexRecord } from '../api/vex'
import { sortVulns } from '../lib/exploitPriority'
import { t, tp } from '../i18n'

const props = withDefaults(defineProps<{
  scan: VulnScan
  /** The caller may record VEX decisions (administrators); otherwise the recorded status is shown only. */
  canDecide?: boolean
  /** Decisions of the open scan's artifact, by vulnerability id. */
  vexByVuln: Map<string, VexRecord>
}>(), { canDecide: true })

const emit = defineEmits<{ decide: [vulnId: string] }>()

function decisionActions(vulnId: string): RowAction[] {
  return [{ key: 'decide', label: tp('security.vuln.rowAction', { action: t('security.vex.decision'), name: vulnId }), icon: CheckBadgeIcon }]
}

/** Most urgent first: KEV, high EPSS, then severity and CVSS. */
const findings = computed(() => sortVulns(vulnFindings(props.scan)))
</script>
