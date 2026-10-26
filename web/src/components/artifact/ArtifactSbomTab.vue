<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-8">
    <section aria-labelledby="artifact-sbom">
      <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
        <h2 id="artifact-sbom" class="text-base font-semibold text-foreground">{{ t('artifact.sbom.title') }}</h2>
        <form v-if="ctx.caps.value.write" class="flex items-center gap-2" @submit.prevent="generate">
          <select v-model="format" :aria-label="t('artifact.sbom.format')" class="h-9 rounded-lg border border-border-strong bg-surface px-2 text-sm text-foreground focus:ring-2 focus:ring-primary max-md:h-11">
            <option v-for="f in SBOM_FORMATS" :key="f" :value="f">{{ f }}</option>
          </select>
          <AppButton type="submit" :loading="generating" class="max-md:h-11">{{ t('artifact.sbom.generate') }}</AppButton>
        </form>
      </div>

      <StateBlock v-if="error" variant="error" :title="t('artifact.sbom.loadFailed')" :hint="error">
        <template #action><AppButton variant="secondary" @click="refresh">{{ t('ui.state.retry') }}</AppButton></template>
      </StateBlock>
      <ListTable
        v-else
        :columns="columns"
        :query="list"
        row-key="id"
        :caption="t('artifact.sbom.title')"
        :empty-title="t('artifact.sbom.empty')"
        :empty-hint="ctx.caps.value.write ? t('artifact.sbom.emptyHint') : t('artifact.sbom.emptyReadOnly')"
      >
        <template #cell-file="{ row }"><span class="break-all font-mono text-xs">{{ fileName(row.artifact_id) }}</span></template>
        <template #cell-format="{ row }"><span class="font-mono text-xs">{{ row.format }}{{ row.spec_version ? ` ${row.spec_version}` : '' }}</span></template>
        <template #cell-components="{ row }"><span class="tabular-nums">{{ row.component_count ?? '—' }}</span></template>
        <template #cell-generated="{ row }"><RelativeTime :value="row.generated_at ?? row.created_at" /></template>
        <template #cell-actions="{ row }">
          <div class="flex items-center gap-1">
            <AppButton v-if="row.format === 'cyclonedx'" variant="ghost" size="sm" class="max-md:h-11" :aria-pressed="graphId === row.id" @click="showGraph(row.id)">
              <ShareIcon class="h-4 w-4" aria-hidden="true" />{{ t('artifact.sbom.graph') }}
            </AppButton>
            <AppButton variant="ghost" size="sm" class="max-md:h-11" :aria-label="tp('artifact.sbom.download', { file: fileName(row.artifact_id), format: row.format })" @click="download(row)">
              <ArrowDownTrayIcon class="h-4 w-4" aria-hidden="true" /><span class="md:sr-only">{{ t('artifact.action.download') }}</span>
            </AppButton>
            <AppButton v-if="ctx.caps.value.write" variant="ghost" size="sm" class="max-md:h-11" :aria-label="tp('artifact.sbom.delete', { file: fileName(row.artifact_id), format: row.format })" @click="remove(row)">
              <TrashIcon class="h-4 w-4" aria-hidden="true" /><span class="md:sr-only">{{ t('common.delete') }}</span>
            </AppButton>
          </div>
        </template>
      </ListTable>
    </section>

    <section v-if="graphId" aria-labelledby="artifact-graph">
      <h2 id="artifact-graph" class="mb-3 text-base font-semibold text-foreground">{{ t('artifact.graph.title') }}</h2>
      <p v-if="graphLoading" class="text-sm text-muted">{{ t('common.loading') }}</p>
      <StateBlock v-else-if="graphError" variant="error" :title="t('artifact.graph.loadFailed')" :hint="graphError" />
      <SbomGraph v-else :packages="packages" :file-name="`${ctx.pkgName.value.replace(/[^\w.-]+/g, '-')}-${ctx.entry.value?.version ?? ''}-dependency-graph`" />
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ArrowDownTrayIcon, ShareIcon, TrashIcon } from '@heroicons/vue/24/outline'
import AppButton from '../AppButton.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import RelativeTime from '../ui/RelativeTime.vue'
import StateBlock from '../ui/StateBlock.vue'
import SbomGraph from './SbomGraph.vue'
import { SBOM_FORMATS, deleteSbom, downloadSbom, generateSbom, getSbomDocument, listSboms, type SbomFormat, type SbomItem } from '../../api/sbom'
import { useArtifactContext } from '../../composables/useArtifact'
import { useLocalList } from '../../composables/useLocalList'
import { packagesFromCycloneDx, reportsOfVersion, type SbomPackage } from '../../lib/sbomGraph'
import { useToastStore } from '../../stores/toast'
import { t, tp } from '../../i18n/en'

const ctx = useArtifactContext()
const toasts = useToastStore()

const columns: ListColumn[] = [
  { key: 'file', label: t('artifact.sbom.column.file') },
  { key: 'format', label: t('artifact.sbom.column.format') },
  { key: 'components', label: t('artifact.sbom.column.components'), align: 'end' },
  { key: 'generated', label: t('artifact.sbom.column.generated') },
  { key: 'actions', label: t('artifact.sbom.column.actions'), actions: true },
]

const error = ref('')
const fileIds = computed(() => ctx.entry.value?.files.map((f) => f.id) ?? [])
const fileNames = computed(() => new Map((ctx.entry.value?.files ?? []).map((f) => [f.id, f.filename || ctx.pkgName.value])))
const fileName = (id: string): string => fileNames.value.get(id) ?? id

const { list, all, refresh: reload } = useLocalList({
  filters: {},
  load: async (): Promise<SbomItem[]> => {
    error.value = ''
    try {
      return reportsOfVersion(await listSboms(ctx.repoName.value), fileIds.value)
    } catch (err) {
      error.value = err instanceof Error ? err.message : String(err)
      return []
    }
  },
})
const refresh = reload

const format = ref<SbomFormat>('cyclonedx')
const generating = ref(false)

async function generate(): Promise<void> {
  generating.value = true
  try {
    await Promise.all(fileIds.value.map((id) => generateSbom(ctx.repoName.value, id, format.value)))
    toasts.success(tp('artifact.sbom.generated', { format: format.value }))
    await reload()
  } catch (err) {
    toasts.error(t('artifact.sbom.generateFailed'), err instanceof Error ? err.message : undefined)
  } finally {
    generating.value = false
  }
}

async function download(report: SbomItem): Promise<void> {
  try {
    await downloadSbom(ctx.repoName.value, report)
  } catch (err) {
    toasts.error(t('artifact.sbom.downloadFailed'), err instanceof Error ? err.message : undefined)
  }
}

// Reports can be regenerated at any time, so removing one needs no confirmation.
async function remove(report: SbomItem): Promise<void> {
  try {
    await deleteSbom(ctx.repoName.value, report.id)
    if (graphId.value === report.id) graphId.value = ''
    toasts.success(t('artifact.sbom.deleted'))
    await reload()
  } catch (err) {
    toasts.error(t('artifact.sbom.deleteFailed'), err instanceof Error ? err.message : undefined)
  }
}

const graphId = ref('')
const graphLoading = ref(false)
const graphError = ref('')
const packages = ref<SbomPackage[]>([])

let generation = 0
async function showGraph(id: string): Promise<void> {
  const mine = ++generation
  graphId.value = id
  graphLoading.value = true
  graphError.value = ''
  packages.value = []
  try {
    const doc = await getSbomDocument(ctx.repoName.value, id)
    if (mine === generation) packages.value = packagesFromCycloneDx(doc ?? {})
  } catch (err) {
    if (mine === generation) graphError.value = err instanceof Error ? err.message : String(err)
  } finally {
    if (mine === generation) graphLoading.value = false
  }
}

watch(() => fileIds.value.join(','), () => void reload())

// The newest CycloneDX report is drawn first: the graph is the reason most people open this tab.
watch(all, (reports) => {
  if (graphId.value && reports.some((r) => r.id === graphId.value)) return
  const first = reports.find((r) => r.format === 'cyclonedx')
  if (first) void showGraph(first.id)
  else graphId.value = ''
})
</script>
