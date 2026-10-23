<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <PageHeader :title="t('bundles.title')" :description="t('bundles.description')">
      <template #actions>
        <div class="flex items-center gap-2">
          <AppButton variant="secondary" size="sm" @click="pipelineOpen = true">{{ t('bundles.pipeline.new') }}</AppButton>
          <AppButton variant="primary" size="sm" @click="createOpen = true">
            <PlusIcon class="w-4 h-4" aria-hidden="true" />
            {{ t('bundles.create') }}
          </AppButton>
        </div>
      </template>
    </PageHeader>

    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-4 py-3 border-b border-border flex items-center gap-3 flex-wrap">
        <label class="flex items-center gap-2 text-sm text-muted">
          <span class="sr-only">{{ t('bundles.filter.name') }}</span>
          <input
            v-model="nameFilter"
            type="search"
            :placeholder="t('bundles.filter.name')"
            :aria-label="t('bundles.filter.name')"
            :class="[INPUT_CLASS, 'w-56']"
          />
        </label>
        <label class="flex items-center gap-2 text-sm text-muted">
          {{ t('bundles.filter.state') }}
          <select v-model="stateFilter" :class="[INPUT_CLASS, 'w-40']">
            <option value="">{{ t('bundles.filter.allStates') }}</option>
            <option v-for="s in STATES" :key="s" :value="s">{{ bundleStateLabel(s) }}</option>
          </select>
        </label>
      </div>

      <p v-if="loading" class="p-6 text-muted">{{ t('common.loading') }}</p>
      <p v-else-if="loadError" class="p-6 text-danger" role="alert">{{ loadError }}</p>
      <EmptyState
        v-else-if="bundles.length === 0"
        :title="filtered ? t('bundles.emptyFiltered') : t('bundles.empty')"
        :description="filtered ? undefined : t('bundles.emptyHint')"
      />
      <DataTable v-else :columns="columns" :rows="bundles as unknown as Record<string, unknown>[]">
        <template #cell-name="{ row }">
          <router-link :to="bundleRoute(row as unknown as Bundle)" class="font-medium text-primary hover:underline">
            {{ row.name }}
          </router-link>
        </template>
        <template #cell-version="{ row }"><span class="font-mono text-xs">{{ row.version }}</span></template>
        <template #cell-state="{ row }">
          <StatusBadge :label="bundleStateLabel(row.state as BundleState)" :variant="bundleStateVariant(row.state as BundleState)" />
        </template>
        <template #cell-current_stage="{ row }">
          {{ row.current_stage ?? t('bundles.noStage') }}
        </template>
        <template #cell-created_at="{ row }">{{ formatDateTime(row.created_at as string) }}</template>
        <template #cell-sealed_at="{ row }">{{ formatDateTime(row.sealed_at as string | null) }}</template>
      </DataTable>
      <Pagination v-if="total > limit" :total="total" :page="page" :limit="limit" @change="goToPage" />
    </div>

    <BundleCreateDialog :open="createOpen" :pipelines="pipelines" @close="createOpen = false" @created="onCreated" />
    <BundlePipelineDialog :open="pipelineOpen" :repos="repos" @close="pipelineOpen = false" @created="onPipelineCreated" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { PlusIcon } from '@heroicons/vue/24/outline'
import AppButton from '../components/AppButton.vue'
import BundleCreateDialog from '../components/BundleCreateDialog.vue'
import BundlePipelineDialog from '../components/BundlePipelineDialog.vue'
import DataTable, { type Column } from '../components/DataTable.vue'
import EmptyState from '../components/EmptyState.vue'
import PageHeader from '../components/PageHeader.vue'
import Pagination from '../components/Pagination.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { listBundles, listPipelines, type Bundle, type BundleState, type Pipeline } from '../api/bundles'
import { listAllRepositories, type Repository } from '../api/repositories'
import { bundleRoute, bundleStateLabel, bundleStateVariant } from '../lib/bundles'
import { formatDateTime } from '../lib/datetime'
import { INPUT_CLASS } from '../lib/formClasses'
import { t } from '../i18n/en'

const STATES: BundleState[] = ['draft', 'sealed', 'archived']
const limit = 25

const router = useRouter()
const bundles = ref<Bundle[]>([])
const pipelines = ref<Pipeline[]>([])
const repos = ref<Repository[]>([])
const total = ref(0)
const page = ref(1)
const nameFilter = ref('')
const stateFilter = ref<BundleState | ''>('')
const loading = ref(false)
const loadError = ref('')
const createOpen = ref(false)
const pipelineOpen = ref(false)

const filtered = computed(() => nameFilter.value !== '' || stateFilter.value !== '')

const columns: Column[] = [
  { key: 'name', label: t('bundles.column.name') },
  { key: 'version', label: t('bundles.column.version') },
  { key: 'state', label: t('bundles.column.state') },
  { key: 'current_stage', label: t('bundles.column.stage') },
  { key: 'created_at', label: t('bundles.column.created') },
  { key: 'sealed_at', label: t('bundles.column.sealed') },
]

let seq = 0

async function load(): Promise<void> {
  const mine = ++seq
  loading.value = true
  loadError.value = ''
  try {
    const res = await listBundles({ name: nameFilter.value.trim(), state: stateFilter.value, page: page.value, limit })
    if (mine !== seq) return
    bundles.value = res.bundles ?? []
    total.value = res.total
  } catch (err) {
    if (mine !== seq) return
    loadError.value = err instanceof Error ? err.message : t('bundles.loadFailed')
  } finally {
    if (mine === seq) loading.value = false
  }
}

async function loadLookups(): Promise<void> {
  const [p, r] = await Promise.allSettled([listPipelines(), listAllRepositories()])
  if (p.status === 'fulfilled') pipelines.value = p.value
  if (r.status === 'fulfilled') repos.value = r.value
}

function goToPage(p: number): void {
  page.value = p
  void load()
}

let debounce: ReturnType<typeof setTimeout> | undefined
watch([nameFilter, stateFilter], () => {
  page.value = 1
  clearTimeout(debounce)
  debounce = setTimeout(() => void load(), 250)
})

function onCreated(bundle: Bundle): void {
  createOpen.value = false
  void router.push(bundleRoute(bundle))
}

async function onPipelineCreated(): Promise<void> {
  pipelineOpen.value = false
  pipelines.value = await listPipelines().catch(() => pipelines.value)
}

onMounted(() => {
  void load()
  void loadLookups()
})
</script>
