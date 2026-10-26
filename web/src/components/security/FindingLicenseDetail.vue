<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <div v-if="loading" class="flex justify-center py-8 text-muted" role="status">
      <ArrowPathIcon class="h-5 w-5 animate-spin" aria-hidden="true" />
      <span class="sr-only">{{ t('security.detail.loading') }}</span>
    </div>
    <p v-else-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    <div v-else-if="scan" class="space-y-4" data-testid="license-detail">
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
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { ArrowPathIcon } from '@heroicons/vue/24/outline'
import { getLicenseScan, type LicenseScan, type SecurityFinding } from '../../api/security'
import { licenseQuarantineState } from '../../lib/scanStatus'
import { t } from '../../i18n/en'

const props = defineProps<{ finding: SecurityFinding }>()

const scan = ref<LicenseScan | null>(null)
const loading = ref(false)
const error = ref('')
const state = ref<ReturnType<typeof licenseQuarantineState>>('none')
let seq = 0

// A license read from the artifact that names no single SPDX license ("BSD", "BSD License"): kept raw, flagged for review.
const isUnresolved = (lic: string): boolean => scan.value?.unresolved_licenses?.includes(lic) ?? false

watch(
  () => props.finding.id,
  async () => {
    const mine = ++seq
    scan.value = null
    error.value = ''
    loading.value = true
    try {
      const result = await getLicenseScan(props.finding.repository, props.finding.id)
      if (mine !== seq) return
      scan.value = result
      state.value = licenseQuarantineState(result)
    } catch (err) {
      if (mine === seq) error.value = err instanceof Error ? err.message : t('security.license.loadFailed')
    } finally {
      if (mine === seq) loading.value = false
    }
  },
  { immediate: true },
)
</script>
