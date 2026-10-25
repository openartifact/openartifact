<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <p v-if="!findings.length" class="text-sm text-muted">{{ t('sec.vuln.noFindings') }}</p>
  <div v-else class="overflow-x-auto">
    <table class="w-full text-sm">
      <thead class="border-b border-border">
        <tr>
          <th scope="col" class="text-left py-2 pr-3 text-muted font-medium">{{ t('sec.vuln.finding.id') }}</th>
          <th scope="col" class="text-left py-2 pr-3 text-muted font-medium">{{ t('sec.vuln.finding.severity') }}</th>
          <th scope="col" class="text-left py-2 pr-3 text-muted font-medium">{{ t('sec.vuln.finding.cvss') }}</th>
          <th scope="col" class="text-left py-2 pr-3 text-muted font-medium">{{ t('sec.vuln.finding.fixedIn') }}</th>
          <th scope="col" class="text-left py-2 pr-3 text-muted font-medium">{{ t('sec.vuln.finding.summary') }}</th>
          <th scope="col" class="text-left py-2 text-muted font-medium">{{ t('sec.vex.eyebrow') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="f in findings" :key="f.id" class="border-b border-border last:border-0 align-top" data-testid="finding-row">
          <td class="py-2 pr-3 font-mono text-xs">{{ f.id }}</td>
          <td class="py-2 pr-3 text-xs">{{ f.severity }}</td>
          <td class="py-2 pr-3 text-xs font-mono">{{ f.cvss > 0 ? f.cvss : '—' }}</td>
          <td class="py-2 pr-3 text-xs font-mono">{{ f.fixed_in || '—' }}</td>
          <td class="py-2 pr-3 text-xs text-muted">{{ f.summary }}</td>
          <td class="py-2">
            <div class="flex flex-col items-start gap-1.5">
              <VexStatusBadge v-if="vexByVuln.get(f.id)" :status="vexByVuln.get(f.id)!.status" data-testid="finding-vex-status" />
              <AppButton
                variant="secondary"
                size="xs"
                :aria-label="tp('sec.vuln.rowAction', { action: t('sec.vex.decision'), name: f.id })"
                data-testid="vex-decision-open"
                @click="emit('decide', f.id)"
              >
                {{ t('sec.vex.decision') }}
              </AppButton>
            </div>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import AppButton from './AppButton.vue'
import VexStatusBadge from './VexStatusBadge.vue'
import { vulnFindings, type VulnScan } from '../api/security'
import type { VexRecord } from '../api/vex'
import { t, tp } from '../i18n/en'

const props = defineProps<{
  scan: VulnScan
  /** Decisions of the open scan's artifact, by vulnerability id. */
  vexByVuln: Map<string, VexRecord>
}>()

const emit = defineEmits<{ decide: [vulnId: string] }>()

const findings = computed(() => vulnFindings(props.scan))
</script>
