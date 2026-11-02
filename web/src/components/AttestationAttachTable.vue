<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- The hosted docker repositories, each with the switch that attaches signed SBOM/VEX attestations to its images. -->
<template>
  <CardSection :title="t('sx.attestation.attach')" :description="t('sx.attestation.attach.hint')">
    <p v-if="error" class="px-5 py-4 text-sm text-danger" role="alert" data-testid="attach-error">{{ error }}</p>
    <ListTable
      v-else
      :columns="columns"
      :caption="t('sx.attestation.attach')"
      :rows="rows"
      row-key="name"
      :empty-title="t('sx.attestation.attach.empty')"
      :empty-hint="t('sx.attestation.attach.emptyHint')"
    >
      <template #cell-name="{ row }">
        <router-link :to="{ name: 'repo-detail', params: { repo: (row as AttachRow).name } }" class="font-medium text-foreground hover:text-primary">
          {{ (row as AttachRow).name }}
        </router-link>
      </template>
      <template #cell-state="{ row }">
        <StatusBadge
          v-if="(row as AttachRow).enabled !== undefined"
          :label="(row as AttachRow).enabled ? t('sx.attestation.attach.on') : t('sx.attestation.attach.off')"
          :variant="(row as AttachRow).enabled ? 'success' : 'neutral'"
        />
        <span v-else class="text-muted">{{ t('sx.attestation.attach.unknown') }}</span>
      </template>
      <template #cell-actions="{ row }">
        <div class="flex justify-end">
          <AppToggle
            :model-value="(row as AttachRow).enabled === true"
            :disabled="(row as AttachRow).busy || (row as AttachRow).enabled === undefined"
            :label="tp('sx.attestation.attach.toggle', { name: (row as AttachRow).name })"
            @update:model-value="emit('toggle', row as AttachRow, $event)"
          />
        </div>
      </template>
    </ListTable>
  </CardSection>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import AppToggle from './AppToggle.vue'
import StatusBadge from './StatusBadge.vue'
import CardSection from './ui/CardSection.vue'
import ListTable, { type ListColumn } from './ui/ListTable.vue'
import type { AttachRow } from '../composables/useAttestation'
import { t, tp } from '../i18n'

defineProps<{ rows: AttachRow[]; error: string }>()
const emit = defineEmits<{ toggle: [row: AttachRow, enabled: boolean] }>()

const columns = computed<ListColumn[]>(() => [
  { key: 'name', label: t('sx.attestation.col.repository'), grow: true },
  { key: 'state', label: t('sx.attestation.col.attach') },
  { key: 'actions', label: t('common.actions'), actions: true },
])
</script>
