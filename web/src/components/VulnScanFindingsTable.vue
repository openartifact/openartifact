<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <p v-if="!findings.length" class="text-sm text-muted">{{ t('security.vuln.noFindings') }}</p>
  <div v-else class="overflow-x-auto">
    <table class="w-full text-sm">
      <thead class="border-b border-border">
        <tr>
          <th scope="col" class="text-left py-2 pr-3 text-muted font-medium">{{ t('security.vuln.finding.id') }}</th>
          <th scope="col" class="text-left py-2 pr-3 text-muted font-medium">{{ t('security.vuln.finding.severity') }}</th>
          <th scope="col" class="text-left py-2 pr-3 text-muted font-medium">{{ t('security.vuln.finding.cvss') }}</th>
          <th scope="col" class="text-left py-2 pr-3 text-muted font-medium">{{ t('security.vuln.finding.fixedIn') }}</th>
          <th scope="col" class="text-left py-2 pr-3 text-muted font-medium">{{ t('security.vuln.finding.summary') }}</th>
          <th scope="col" class="text-left py-2 text-muted font-medium">{{ t('security.vex.eyebrow') }}</th>
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
                v-if="canDecide"
                variant="secondary"
                size="xs"
                :aria-label="tp('security.vuln.rowAction', { action: t('security.vex.decision'), name: f.id })"
                data-testid="vex-decision-open"
                @click="emit('decide', f.id)"
              >
                {{ t('security.vex.decision') }}
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
import { t, tp } from '../i18n'

const props = withDefaults(defineProps<{
  scan: VulnScan
  /** The caller may record VEX decisions (administrators); otherwise the recorded status is shown only. */
  canDecide?: boolean
  /** Decisions of the open scan's artifact, by vulnerability id. */
  vexByVuln: Map<string, VexRecord>
}>(), { canDecide: true })

const emit = defineEmits<{ decide: [vulnId: string] }>()

const findings = computed(() => vulnFindings(props.scan))
</script>
