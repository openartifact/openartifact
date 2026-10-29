<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <FindingDetailShell :loading="loading" :error="error">
    <div v-if="scan" class="space-y-4" data-testid="license-detail">
      <section>
        <h3 class="mb-2 text-sm font-semibold">{{ t('security.license.licenses') }}</h3>
        <div v-if="scan.licenses?.length" class="flex flex-wrap gap-1">
          <span
            v-for="lic in scan.licenses"
            :key="lic"
            class="inline-flex items-center gap-1 rounded-md border px-2 py-0.5 font-mono text-caption"
            :class="isUnresolved(lic) ? 'border-warning/40 bg-warning-soft text-warning' : 'border-border text-foreground'"
            :title="isUnresolved(lic) ? t('security.license.unresolvedHint') : undefined"
            :data-testid="isUnresolved(lic) ? 'license-unresolved' : undefined"
          >{{ lic }}<span v-if="isUnresolved(lic)" class="font-sans">· {{ t('security.license.unresolved') }}</span></span>
        </div>
        <p v-else class="text-sm text-muted">{{ t('security.license.none') }}</p>
        <p
          v-if="scan.licenses?.length && scan.license_source === 'client_declared'"
          class="mt-2 text-caption text-muted"
          data-testid="license-client-declared"
        >{{ t('security.license.clientDeclaredHint') }}</p>
      </section>
      <p v-if="state === 'quarantined'" class="text-sm text-danger" data-testid="license-quarantined">{{ t('security.license.quarantinedHint') }}</p>
      <p v-else-if="state === 'approved'" class="text-sm text-success" data-testid="license-approved">{{ t('security.license.approvedHint') }}</p>
      <p v-if="scan.scan_error" class="text-sm text-danger" role="alert">{{ scan.scan_error }}</p>
    </div>
  </FindingDetailShell>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import FindingDetailShell from './FindingDetailShell.vue'
import { getLicenseScan, type SecurityFinding } from '../../api/security'
import { useFindingScan } from '../../composables/useFindingScan'
import { licenseQuarantineState } from '../../lib/scanStatus'
import { t } from '../../i18n'

const props = defineProps<{ finding: SecurityFinding }>()

const state = ref<ReturnType<typeof licenseQuarantineState>>('none')
// A license read from the artifact that names no single SPDX license ("BSD", "BSD License"): kept raw, flagged for review.
const isUnresolved = (lic: string): boolean => scan.value?.unresolved_licenses?.includes(lic) ?? false

const { scan, loading, error } = useFindingScan(
  () => props.finding,
  (f) => getLicenseScan(f.repository, f.id),
  () => t('security.license.loadFailed'),
  (result) => {
    state.value = licenseQuarantineState(result)
  },
)
</script>
