<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- The objects `oa apply` owns (created or adopted); a prune deletes only these. Releasing one leaves the object and ends the ownership. -->
<template>
  <CardSection :title="t('sx.declarative.managed')" :description="t('sx.declarative.managed.hint')">
    <ListTable
      :columns="columns"
      :caption="t('sx.declarative.managed')"
      :rows="rows"
      :row-key="(r) => `${(r as DeclarativeObject).kind}/${(r as DeclarativeObject).name}`"
      :loading="loading"
      :error="error"
      :empty-title="t('sx.declarative.managed.empty')"
      :empty-hint="t('sx.declarative.managed.emptyHint')"
      @retry="emit('retry')"
    >
      <template #cell-name="{ row }">
        <span class="block truncate font-mono text-sm text-foreground" :title="(row as DeclarativeObject).name">{{ (row as DeclarativeObject).name }}</span>
      </template>
      <template #cell-applied_at="{ row }">
        <RelativeTime :value="(row as DeclarativeObject).applied_at" />
      </template>
      <template #cell-actions="{ row }">
        <RowActions
          :actions="[{ key: 'release', label: t('sx.declarative.managed.release'), icon: LinkSlashIcon }]"
          :subject="(row as DeclarativeObject).name"
          @action="emit('release', row as DeclarativeObject)"
        />
      </template>
    </ListTable>
  </CardSection>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { LinkSlashIcon } from '@heroicons/vue/20/solid'
import CardSection from './ui/CardSection.vue'
import ListTable, { type ListColumn } from './ui/ListTable.vue'
import RelativeTime from './ui/RelativeTime.vue'
import RowActions from './ui/RowActions.vue'
import type { DeclarativeObject } from '../api/declarative'
import { t } from '../i18n'

defineProps<{ rows: DeclarativeObject[]; loading: boolean; error: string }>()
const emit = defineEmits<{ release: [row: DeclarativeObject]; retry: [] }>()

const columns = computed<ListColumn[]>(() => [
  { key: 'kind', label: t('sx.declarative.col.kind') },
  { key: 'name', label: t('sx.declarative.col.name'), grow: true },
  { key: 'applied_at', label: t('sx.declarative.col.applied'), dropOrder: 1 },
  { key: 'actions', label: t('common.actions'), actions: true },
])
</script>
