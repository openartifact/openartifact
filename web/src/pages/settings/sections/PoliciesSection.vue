<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Settings > Security > Policies: license policies that apply to every repository. -->
<template>
  <div>
    <SectionActions>
      <AppButton size="sm" class="max-md:h-11" data-testid="new-policy" @click="creating = true">
        <PlusIcon class="h-4 w-4" aria-hidden="true" />
        {{ t('sx.policies.new') }}
      </AppButton>
    </SectionActions>

    <p class="mb-4 text-sm text-muted">{{ t('sx.policies.scope') }}</p>

    <FilterBar
      :search="list.text.q"
      :search-label="t('sx.policies.search')"
      :placeholder="t('sx.policies.search')"
      :active="list.filtered.value"
      @update:search="list.setFilter('q', $event)"
      @clear="list.clear"
    >
      <SegmentedControl
        :options="actionOptions"
        :model-value="list.filters.value.action"
        :label="t('sx.policies.action')"
        @update:model-value="list.setFilter('action', $event as PolicyFilter)"
      />
    </FilterBar>

    <ListTable
      :columns="columns"
      :caption="t('sx.policies.table')"
      :query="list"
      row-key="id"
      :empty-title="t('sx.policies.empty')"
      :empty-hint="t('sx.policies.emptyHint')"
    >
      <template #empty-action>
        <AppButton @click="creating = true">{{ t('sx.policies.new') }}</AppButton>
      </template>

      <template #cell-name="{ row }">
        <div class="min-w-0">
          <span class="block truncate font-medium text-foreground" :title="(row as LicensePolicy).name">{{ (row as LicensePolicy).name }}</span>
          <p v-if="(row as LicensePolicy).description" class="truncate text-xs text-muted" :title="(row as LicensePolicy).description ?? undefined">{{ (row as LicensePolicy).description }}</p>
        </div>
      </template>

      <template #cell-condition="{ row }">
        <div class="max-w-xs space-y-0.5 text-xs">
          <p v-if="(row as LicensePolicy).denied_licenses?.length" class="break-words font-mono text-foreground">
            {{ tp('sx.policies.cond.denied', { licenses: (row as LicensePolicy).denied_licenses!.join(', ') }) }}
          </p>
          <p v-if="(row as LicensePolicy).allowed_licenses?.length" class="break-words font-mono text-foreground">
            {{ tp('sx.policies.cond.allowed', { licenses: (row as LicensePolicy).allowed_licenses!.join(', ') }) }}
          </p>
          <p v-if="!(row as LicensePolicy).denied_licenses?.length && !(row as LicensePolicy).allowed_licenses?.length" class="text-muted">
            {{ t('sx.policies.cond.none') }}
          </p>
        </div>
      </template>

      <template #cell-action="{ row }">
        <StatusBadge
          :label="t(`sx.policies.act.${policyAction(row as LicensePolicy)}`)"
          :variant="ACTION_VARIANT[policyAction(row as LicensePolicy)]"
        />
      </template>

      <template #cell-status="{ row }">
        <StatusBadge
          :label="(row as LicensePolicy).enabled ? t('sx.policies.enabled') : t('sx.policies.disabled')"
          :variant="(row as LicensePolicy).enabled ? 'success' : 'neutral'"
        />
      </template>

      <template #cell-updated="{ row }">
        <RelativeTime :value="(row as LicensePolicy).updated_at" />
      </template>
    </ListTable>

    <LicensePolicyDrawer :open="creating" @close="creating = false" @created="onCreated" />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { PlusIcon } from '@heroicons/vue/20/solid'
import AppButton from '../../../components/AppButton.vue'
import LicensePolicyDrawer from '../../../components/LicensePolicyDrawer.vue'
import SectionActions from '../../../components/SectionActions.vue'
import StatusBadge from '../../../components/StatusBadge.vue'
import FilterBar from '../../../components/ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../../../components/ui/ListTable.vue'
import RelativeTime from '../../../components/ui/RelativeTime.vue'
import SegmentedControl from '../../../components/ui/SegmentedControl.vue'
import { LICENSE_POLICY_ACTIONS, listGlobalLicensePolicies, type LicensePolicy, type LicensePolicyAction } from '../../../api/licensePolicies'
import { useLocalList } from '../../../composables/useLocalList'
import { enumParam, strParam } from '../../../composables/useUrlState'
import { policyAction } from '../../../lib/licensePolicy'
import { useToastStore } from '../../../stores/toast'
import { t, tp } from '../../../i18n'
import { compareText } from '../../../lib/intl'

const FILTERS = ['all', ...LICENSE_POLICY_ACTIONS] as const
type PolicyFilter = (typeof FILTERS)[number]

const ACTION_VARIANT: Record<LicensePolicyAction, 'danger' | 'warning' | 'success'> = { block: 'danger', warn: 'warning', allow: 'success' }

const toasts = useToastStore()
const creating = ref(false)

const { list, refresh } = useLocalList({
  filters: { q: strParam(), action: enumParam(FILTERS, 'all') },
  debounce: ['q'],
  sorts: ['name', 'updated'],
  limit: 25,
  load: listGlobalLicensePolicies,
  match(row, f) {
    const q = f.q.trim().toLowerCase()
    if (q && !(row.name ?? '').toLowerCase().includes(q) && !(row.description ?? '').toLowerCase().includes(q)) return false
    return f.action === 'all' || policyAction(row) === f.action
  },
  compare(a, b, sort) {
    return sort === 'updated' ? (a.updated_at ?? '').localeCompare(b.updated_at ?? '') : compareText(a.name, b.name)
  },
})

const actionOptions = computed(() => FILTERS.map((a) => ({ value: a, label: a === 'all' ? t('sx.policies.action.all') : t(`sx.policies.act.${a}`) })))

const columns: ListColumn[] = [
  { key: 'name', label: t('sx.policies.col.name'), sortable: true, grow: true },
  { key: 'condition', label: t('sx.policies.col.condition') },
  { key: 'action', label: t('sx.policies.col.action') },
  { key: 'status', label: t('sx.policies.col.status'), dropOrder: 2 },
  { key: 'updated', label: t('sx.policies.col.updated'), sortable: true, dropOrder: 1 },
]

async function onCreated(policy: LicensePolicy): Promise<void> {
  creating.value = false
  toasts.success(tp('sx.policies.created', { name: policy.name ?? '' }))
  await refresh()
}
</script>
