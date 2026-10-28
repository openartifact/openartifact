<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Quick look at one finding: what it is, where it is, the type's own details
  (VEX decisions of a vulnerability, license list, secret masks, AI notes) and
  the reversible quarantine toggle. Mutations are offered to administrators only.
-->
<template>
  <Drawer
    :open="finding !== null"
    wide
    :title="finding?.title ?? ''"
    :eyebrow="finding?.repository"
    :description="finding ? artifactLabel : undefined"
    :busy="busy"
    @close="emit('close')"
  >
    <div v-if="finding" class="space-y-5" data-testid="finding-drawer">
      <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
        <SeverityBadge :severity="finding.severity" />
        <span class="rounded border border-border px-1.5 py-0.5 text-xs text-muted">{{ tDynamic(`security.type.${finding.type}`) }}</span>
        <StatusBadge :label="tDynamic(`security.status.${quarantined ? 'quarantined' : finding.status}`)" :variant="statusVariant(quarantined ? 'quarantined' : finding.status)" />
        <VexStatusBadge v-if="finding.vex_status" :status="finding.vex_status" />
      </div>

      <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
        <dt class="text-muted">{{ t('security.detail.package') }}</dt>
        <dd class="min-w-0 break-words">
          <router-link :to="artifactLink" class="font-medium text-primary hover:underline" data-testid="finding-artifact-link">{{ artifactLabel }}</router-link>
        </dd>
        <dt class="text-muted">{{ t('security.detail.repository') }}</dt>
        <dd class="font-mono text-xs">{{ finding.repository }}</dd>
        <template v-if="scanners.length">
          <dt class="text-muted">{{ t('security.detail.scanners') }}</dt>
          <dd>{{ scanners.join(', ') }}</dd>
        </template>
        <template v-if="finding.type === 'vuln'">
          <dt class="text-muted">{{ t('security.detail.priority') }}</dt>
          <dd data-testid="finding-priority"><PriorityBadge :priority="finding.priority" /></dd>
          <template v-if="finding.kev_count > 0 || finding.max_epss !== null">
            <dt class="text-muted">{{ t('security.detail.exploit') }}</dt>
            <dd class="flex flex-wrap items-center gap-1.5" data-testid="finding-exploit">
              <KevBadge v-if="finding.kev_count > 0" :count="finding.kev_count" />
              <EpssBadge v-if="finding.max_epss !== null" :score="finding.max_epss" :elevated="finding.priority === 'epss'" />
            </dd>
          </template>
        </template>
        <dt class="text-muted">{{ t('security.detail.found') }}</dt>
        <dd><time :datetime="finding.scanned_at" :title="formatDateTime(finding.scanned_at)">{{ timeAgo(finding.scanned_at) }}</time></dd>
      </dl>

      <section aria-live="polite" class="border-t border-border pt-4">
        <component :is="detail" :key="finding.id" :finding="finding" :can-act="canAct" @changed="emit('changed')" />
      </section>

      <ConfirmDialog
        :open="confirmDelete"
        :title="t('security.delete.title')"
        :description="`${finding.repository} · ${artifactLabel}`"
        :confirm-label="t('security.delete.confirm')"
        :busy="deleting"
        @confirm="removeScan"
        @close="confirmDelete = false"
      >
        {{ t('security.delete.description') }}
      </ConfirmDialog>
    </div>

    <template v-if="finding" #footer>
      <AppButton
        v-if="canAct && canDelete"
        variant="danger"
        class="mr-auto"
        :disabled="busy"
        data-testid="finding-delete"
        @click="confirmDelete = true"
      >
        {{ t('security.delete.action') }}
      </AppButton>
      <AppButton
        v-if="canAct && finding.type !== 'ai'"
        variant="secondary"
        :loading="toggling"
        :disabled="busy"
        data-testid="finding-quarantine"
        @click="toggleQuarantine"
      >
        {{ quarantined ? t('security.quarantine.release') : t('security.quarantine.action') }}
      </AppButton>
      <AppButton variant="primary" :to="`${artifactLink}`" data-testid="finding-open-artifact">
        {{ t('security.detail.openPackage') }}
      </AppButton>
    </template>
  </Drawer>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, ref, watch } from 'vue'
import AppButton from '../AppButton.vue'
import ConfirmDialog from '../ConfirmDialog.vue'
import StatusBadge from '../StatusBadge.vue'
import VexStatusBadge from '../VexStatusBadge.vue'
import Drawer from '../ui/Drawer.vue'
import SeverityBadge from '../ui/SeverityBadge.vue'
import EpssBadge from './EpssBadge.vue'
import FindingAIDetail from './FindingAIDetail.vue'
import FindingLicenseDetail from './FindingLicenseDetail.vue'
import FindingSecretDetail from './FindingSecretDetail.vue'
import KevBadge from './KevBadge.vue'
import PriorityBadge from './PriorityBadge.vue'
import {
  deleteSecretScan,
  deleteVulnScan,
  setLicenseQuarantine,
  setSecretQuarantine,
  setVulnQuarantine,
  type SecurityFinding,
} from '../../api/security'
import { formatDateTime, timeAgo } from '../../lib/datetime'
import { findingArtifact, findingLink, scannerLabels, statusVariant } from '../../lib/securityFindings'
import { useToastStore } from '../../stores/toast'
import { t, tDynamic } from '../../i18n'

// The vulnerability detail carries the VEX dialog and the export buttons: its own chunk.
const FindingVulnDetail = defineAsyncComponent(() => import('./FindingVulnDetail.vue'))

const props = defineProps<{
  finding: SecurityFinding | null
  /** The caller may change state (administrators). */
  canAct: boolean
}>()

const emit = defineEmits<{ close: []; changed: [] }>()

const toasts = useToastStore()
const toggling = ref(false)
const deleting = ref(false)
const confirmDelete = ref(false)
const quarantined = ref(false)
const busy = computed(() => toggling.value || deleting.value)

watch(
  () => props.finding,
  (f) => {
    quarantined.value = f?.quarantined ?? false
    confirmDelete.value = false
  },
  { immediate: true },
)

const artifactLabel = computed(() => (props.finding ? findingArtifact(props.finding) || t('security.table.removedPackage') : ''))
const artifactLink = computed(() => (props.finding ? findingLink(props.finding) : ''))
const scanners = computed(() => (props.finding ? scannerLabels(props.finding) : []))
const canDelete = computed(() => props.finding?.type === 'vuln' || props.finding?.type === 'secret')

const detail = computed(() => {
  switch (props.finding?.type) {
    case 'vuln':
      return FindingVulnDetail
    case 'license':
      return FindingLicenseDetail
    case 'secret':
      return FindingSecretDetail
    default:
      return FindingAIDetail
  }
})

async function toggleQuarantine(): Promise<void> {
  const f = props.finding
  if (!f || busy.value) return
  const next = !quarantined.value
  toggling.value = true
  try {
    const set = f.type === 'vuln' ? setVulnQuarantine : f.type === 'license' ? setLicenseQuarantine : setSecretQuarantine
    const updated = await set(f.repository, f.id, next)
    quarantined.value = updated.quarantined
    toasts.success(updated.quarantined ? t('security.quarantine.done') : t('security.quarantine.released'))
    emit('changed')
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('security.quarantine.failed'))
  } finally {
    toggling.value = false
  }
}

async function removeScan(): Promise<void> {
  const f = props.finding
  if (!f || deleting.value) return
  deleting.value = true
  try {
    if (f.type === 'vuln') await deleteVulnScan(f.repository, f.id)
    else await deleteSecretScan(f.repository, f.id)
    toasts.success(t('security.delete.done'))
    confirmDelete.value = false
    emit('changed')
    emit('close')
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('security.delete.failed'))
  } finally {
    deleting.value = false
  }
}
</script>
