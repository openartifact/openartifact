<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- The signatures, attestations and SBOMs attached to one docker image manifest, with their verification verdict. -->
<template>
  <section class="space-y-3" aria-labelledby="image-attestations">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h2 id="image-attestations" class="text-base font-semibold text-foreground">{{ t('artifact.attestations.title') }}</h2>
      <AppButton v-if="canAdmin && subject" variant="secondary" size="sm" :loading="reverifying" data-testid="reverify" @click="reverify">
        {{ t('artifact.attestations.reverify') }}
      </AppButton>
    </div>

    <StateBlock v-if="!subject" variant="empty" :title="t('artifact.attestations.noDigest')" />
    <StateBlock v-else-if="error" variant="error" :title="t('artifact.attestations.loadFailed')" :hint="error">
      <template #action><AppButton variant="secondary" @click="load">{{ t('ui.state.retry') }}</AppButton></template>
    </StateBlock>
    <ListTable
      v-else
      :columns="columns"
      :rows="rows"
      row-key="digest"
      :loading="loading"
      :caption="t('artifact.attestations.title')"
      :empty-title="t('artifact.attestations.empty')"
      :empty-hint="t('artifact.attestations.emptyHint')"
    >
      <template #cell-kind="{ row }">{{ t(`artifact.attestations.kind.${(row as ImageAttestation).kind}`) }}</template>
      <template #cell-predicate="{ row }">
        <span class="block truncate" :title="(row as ImageAttestation).predicate_type">{{ predicateText(row as ImageAttestation) }}</span>
      </template>
      <template #cell-verified="{ row }">
        <div class="flex flex-col gap-1">
          <StatusBadge
            :label="(row as ImageAttestation).verified ? t('artifact.attestations.verified') : t('artifact.attestations.notVerified')"
            :variant="(row as ImageAttestation).verified ? 'success' : 'warning'"
          />
          <span v-if="!(row as ImageAttestation).verified && (row as ImageAttestation).verify_error" class="text-xs text-muted" data-testid="verify-error">
            {{ (row as ImageAttestation).verify_error }}
          </span>
        </div>
      </template>
      <template #cell-signer="{ row }">
        <div v-if="(row as ImageAttestation).signer_identity || (row as ImageAttestation).issuer" class="min-w-0">
          <span class="block break-all text-xs text-foreground">{{ (row as ImageAttestation).signer_identity }}</span>
          <span class="block break-all text-xs text-muted">{{ (row as ImageAttestation).issuer }}</span>
        </div>
        <span v-else class="text-muted">—</span>
      </template>
      <template #cell-created="{ row }">{{ formatDateTime((row as ImageAttestation).created_at) }}</template>
      <template #cell-actions="{ row }">
        <RowActions
          :actions="[{ key: 'view', label: t('artifact.attestations.view'), icon: EyeIcon }]"
          :subject="predicateText(row as ImageAttestation)"
          @action="view(row as ImageAttestation)"
        />
      </template>
    </ListTable>

    <Drawer :open="viewing !== null" wide :title="t('artifact.attestations.statement')" :description="viewing ? predicateText(viewing) : undefined" @close="closeStatement">
      <p v-if="statementLoading" class="text-sm text-muted">{{ t('common.loading') }}</p>
      <p v-else-if="statementError" role="alert" class="text-sm text-danger">{{ statementError }}</p>
      <pre v-else class="overflow-auto rounded-md bg-surface-hover p-3 font-mono text-xs text-foreground" data-testid="statement-json">{{ statement }}</pre>
    </Drawer>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { EyeIcon } from '@heroicons/vue/20/solid'
import AppButton from './AppButton.vue'
import StatusBadge from './StatusBadge.vue'
import Drawer from './ui/Drawer.vue'
import ListTable, { type ListColumn } from './ui/ListTable.vue'
import RowActions from './ui/RowActions.vue'
import StateBlock from './ui/StateBlock.vue'
import type { ImageAttestation } from '../api/attestations'
import { useImageAttestations } from '../composables/useImageAttestations'
import { formatDateTime } from '../lib/datetime'
import { predicateLabel } from '../lib/attestations'
import { t } from '../i18n'

const props = defineProps<{
  repo: string
  image: string
  /** `sha256:<hex>` of the image manifest, or null when unknown. */
  subject: string | null
  canAdmin: boolean
}>()

const { rows, loading, error, reverifying, viewing, statement, statementLoading, statementError, load, reverify, view, closeStatement } = useImageAttestations({
  repo: () => props.repo,
  image: () => props.image,
  subject: () => props.subject,
})

const columns = computed<ListColumn[]>(() => [
  { key: 'kind', label: t('artifact.attestations.col.kind') },
  { key: 'predicate', label: t('artifact.attestations.col.predicate'), grow: true },
  { key: 'verified', label: t('artifact.attestations.col.verified') },
  { key: 'signer', label: t('artifact.attestations.col.signer'), dropOrder: 2 },
  { key: 'created', label: t('artifact.attestations.col.created'), dropOrder: 1 },
  { key: 'actions', label: t('common.actions'), actions: true },
])

function predicateText(row: ImageAttestation): string {
  const label = predicateLabel(row.predicate_type)
  return label === 'other' ? (row.predicate_type ?? t(`artifact.attestations.kind.${row.kind}`)) : t(`artifact.attestations.predicate.${label}`)
}
</script>
