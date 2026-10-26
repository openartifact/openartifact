<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <PageHeader :title="t('bundles.title')" :description="t('bundles.description')">
      <template #actions>
        <div class="flex items-center gap-2">
          <AppButton variant="secondary" class="max-md:h-11" data-testid="open-pipelines" @click="pipelineOpen = true">
            {{ t('bundles.pipelines.open') }}
          </AppButton>
          <AppButton variant="primary" class="max-md:h-11" data-testid="new-bundle" @click="create.set(true)">
            <PlusIcon class="h-4 w-4" aria-hidden="true" />
            {{ t('bundles.create') }}
          </AppButton>
        </div>
      </template>
    </PageHeader>

    <FilterBar
      :search="list.text.q"
      :search-label="t('bundles.filter.name')"
      :placeholder="t('bundles.filter.name')"
      :active="list.filtered.value"
      @update:search="(v) => list.setFilter('q', v)"
      @clear="list.clear"
    >
      <SegmentedControl
        :options="stateOptions"
        :model-value="list.filters.value.state"
        :label="t('bundles.filter.state')"
        @update:model-value="(v) => list.setFilter('state', v as StateFilter)"
      />
    </FilterBar>

    <ListTable
      :columns="columns"
      :caption="t('bundles.title')"
      :query="list"
      :empty-title="t('bundles.empty')"
      :empty-hint="t('bundles.emptyHint')"
    >
      <template #cell-bundle="{ row }">
        <router-link :to="bundleRoute(row)" class="font-mono text-sm font-semibold text-foreground hover:text-primary">
          {{ row.name }} <span class="font-normal text-muted">{{ row.version }}</span>
        </router-link>
      </template>
      <template #cell-state="{ row }">
        <StatusBadge :label="bundleStateLabel(row.state)" :variant="bundleStateVariant(row.state)" />
      </template>
      <template #cell-signed="{ row }">
        <span v-if="row.gpg_key_id" class="text-sm text-foreground">{{ t('bundles.signed') }}</span>
        <span v-else class="text-muted"><span aria-hidden="true">—</span><span class="sr-only">{{ t('bundles.notSigned') }}</span></span>
      </template>
      <template #cell-stage="{ row }">
        <span v-if="row.current_stage" class="font-mono text-sm">{{ row.current_stage }}</span>
        <span v-else class="text-sm text-muted">{{ t('bundles.noStage') }}</span>
      </template>
      <template #cell-created="{ row }">
        <div class="text-sm">
          <RelativeTime :value="row.created_at" />
          <p v-if="row.created_by" class="text-xs text-muted">{{ tp('bundles.createdBy', { user: row.created_by }) }}</p>
        </div>
      </template>
      <template #empty-action>
        <AppButton variant="primary" class="max-md:h-11" @click="create.set(true)">{{ t('bundles.create') }}</AppButton>
      </template>
    </ListTable>

    <BundleCreateDrawer :open="create.on.value" :pipelines="pipelines" @close="create.set(false)" @created="onCreated" />
    <BundlePipelinesDrawer :open="pipelineOpen" :pipelines="pipelines" @close="pipelineOpen = false" @created="loadPipelines" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { PlusIcon } from '@heroicons/vue/20/solid'
import AppButton from '../components/AppButton.vue'
import BundleCreateDrawer from '../components/bundles/BundleCreateDrawer.vue'
import BundlePipelinesDrawer from '../components/bundles/BundlePipelinesDrawer.vue'
import PageHeader from '../components/PageHeader.vue'
import StatusBadge from '../components/StatusBadge.vue'
import FilterBar from '../components/ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../components/ui/ListTable.vue'
import RelativeTime from '../components/ui/RelativeTime.vue'
import SegmentedControl from '../components/ui/SegmentedControl.vue'
import { listBundles, listPipelines, type Bundle, type Pipeline } from '../api/bundles'
import { useListQuery } from '../composables/useListQuery'
import { enumParam, strParam } from '../composables/useUrlState'
import { useUrlFlag } from '../composables/useUrlFlag'
import { bundleRoute, bundleStateLabel, bundleStateVariant } from '../lib/bundles'
import { t, tp } from '../i18n/en'

const STATES = ['all', 'draft', 'sealed', 'archived'] as const
type StateFilter = (typeof STATES)[number]

const router = useRouter()
const create = useUrlFlag('new')
const pipelineOpen = ref(false)
const pipelines = ref<Pipeline[]>([])

const list = useListQuery({
  filters: { state: enumParam(STATES, 'all'), q: strParam() },
  debounce: ['q'],
  limit: 25,
  fetch: async (req) => {
    const res = await listBundles({
      name: req.filters.q.trim(),
      state: req.filters.state === 'all' ? '' : req.filters.state,
      page: req.page,
      limit: req.limit,
    })
    return { rows: res.bundles, total: res.total }
  },
})

const stateOptions = computed(() =>
  STATES.map((s) => ({ value: s, label: s === 'all' ? t('bundles.filter.allStates') : bundleStateLabel(s) })),
)

const columns = computed<ListColumn[]>(() => [
  { key: 'bundle', label: t('bundles.column.bundle') },
  { key: 'state', label: t('bundles.column.state') },
  { key: 'signed', label: t('bundles.column.signed') },
  { key: 'stage', label: t('bundles.column.stage') },
  { key: 'created', label: t('bundles.column.created') },
])

// The pipelines feed the create drawer and the pipelines drawer; a bundle can be made without one.
async function loadPipelines(): Promise<void> {
  pipelines.value = await listPipelines().catch(() => [] as Pipeline[])
}
onMounted(loadPipelines)

async function onCreated(bundle: Bundle): Promise<void> {
  await router.push(bundleRoute(bundle))
}
</script>
