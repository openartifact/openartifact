<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- The components of one SBOM and what depends on what: a circle graph plus the same data as a table. -->
<template>
  <div class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-2.5">
        <AppToggle v-model="vulnerableOnly" labelledby="sbom-vuln-only" />
        <span id="sbom-vuln-only" class="text-sm text-foreground">{{ t('artifact.graph.vulnerableOnly') }}</span>
      </div>
      <AppButton variant="secondary" :disabled="nodes.length === 0" @click="exportSvg">
        <ArrowDownTrayIcon class="h-4 w-4" aria-hidden="true" />{{ t('artifact.graph.export') }}
      </AppButton>
    </div>

    <p class="text-sm text-muted" data-testid="graph-stats">{{ tp('artifact.graph.stats', { nodes: String(nodes.length), edges: String(links.length), vulnerable: String(vulnerableCount) }) }}</p>

    <StateBlock v-if="nodes.length === 0" variant="empty" :title="t('artifact.graph.empty')" :hint="vulnerableOnly ? t('artifact.graph.noVulnerable') : t('artifact.graph.emptyHint')" />
    <div v-else class="grid grid-cols-1 gap-4 lg:grid-cols-[1fr_260px]">
      <div class="overflow-hidden rounded-lg border border-border bg-surface">
        <svg ref="svgEl" :viewBox="`0 0 ${SVG_W} ${SVG_H}`" role="img" :aria-label="t('artifact.graph.label')" class="h-auto w-full max-h-[560px]" xmlns="http://www.w3.org/2000/svg">
          <defs>
            <marker id="sbom-arrow" viewBox="0 0 10 10" refX="18" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
              <path d="M 0 0 L 10 5 L 0 10 z" :fill="BORDER_STRONG" fill-opacity="0.6" />
            </marker>
            <marker id="sbom-arrow-hl" viewBox="0 0 10 10" refX="18" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
              <path d="M 0 0 L 10 5 L 0 10 z" :fill="PRIMARY" />
            </marker>
          </defs>
          <line
            v-for="(link, i) in links"
            :key="i"
            :x1="positions[link.source]?.x ?? 0"
            :y1="positions[link.source]?.y ?? 0"
            :x2="positions[link.target]?.x ?? 0"
            :y2="positions[link.target]?.y ?? 0"
            :stroke="edgeActive(link) ? PRIMARY : BORDER_STRONG"
            :stroke-width="edgeActive(link) ? 2 : 1.2"
            :stroke-opacity="edgeActive(link) ? 0.9 : selected ? 0.15 : 0.5"
            :marker-end="edgeActive(link) ? 'url(#sbom-arrow-hl)' : 'url(#sbom-arrow)'"
          />
          <g
            v-for="node in nodes"
            :key="node.id"
            class="cursor-pointer"
            :opacity="selected && !highlighted.has(node.id) ? 0.3 : 1"
            @click="toggle(node.id)"
          >
            <circle
              :cx="positions[node.id]?.x ?? 0"
              :cy="positions[node.id]?.y ?? 0"
              r="13"
              :fill="node.vulnerable ? DANGER : PRIMARY"
              :stroke="selected === node.id ? PRIMARY : BORDER_STRONG"
              :stroke-width="selected === node.id ? 2.5 : 1.5"
            />
            <text :x="positions[node.id]?.x ?? 0" :y="(positions[node.id]?.y ?? 0) + 26" text-anchor="middle" font-size="10" font-family="ui-monospace, monospace" :fill="MUTED" class="pointer-events-none select-none">{{ node.label }}</text>
          </g>
        </svg>
      </div>

      <div class="space-y-4 text-sm">
        <div v-if="selected" class="border-l-2 border-primary pl-3" data-testid="graph-selected">
          <p class="break-all font-mono text-xs font-medium text-foreground">{{ selected }}</p>
          <p class="mt-1 text-xs text-muted">{{ tp('artifact.graph.degree', { out: String(selectedOut), in: String(selectedIn) }) }}</p>
        </div>
        <p v-else class="text-xs text-muted">{{ t('artifact.graph.hint') }}</p>
        <ul class="space-y-1 text-xs text-muted" :aria-label="t('artifact.graph.legend')">
          <li class="flex items-center gap-2"><span class="h-3 w-3 rounded-full bg-primary" aria-hidden="true" />{{ t('artifact.graph.package') }}</li>
          <li v-if="vulnerableCount > 0" class="flex items-center gap-2"><span class="h-3 w-3 rounded-full bg-danger" aria-hidden="true" />{{ t('artifact.graph.vulnerable') }}</li>
        </ul>
      </div>
    </div>

    <section v-if="packages.length > 0" aria-labelledby="sbom-components">
      <h3 id="sbom-components" class="mb-2 text-sm font-semibold text-foreground">{{ t('artifact.graph.components') }}</h3>
      <FilterBar :search="list.text.q" :search-label="t('artifact.graph.filter')" :placeholder="t('artifact.graph.filter')" :active="list.filtered.value" @update:search="list.setFilter('q', $event)" @clear="list.clear()" />
      <ListTable :columns="columns" :query="list" row-key="id" :caption="t('artifact.graph.components')" :empty-title="t('artifact.graph.noMatch')">
        <template #cell-name="{ row }">
          <span class="break-all font-mono text-xs">{{ row.name }}</span>
          <span v-if="row.vulnerable" class="ml-2 text-xs text-danger">{{ t('artifact.graph.vulnerable') }}</span>
        </template>
        <template #cell-version="{ row }"><span class="break-all font-mono text-xs">{{ row.version || '—' }}</span></template>
        <template #cell-dependsOn="{ row }"><span class="tabular-nums">{{ row.dependsOn.length }}</span></template>
      </ListTable>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ArrowDownTrayIcon } from '@heroicons/vue/24/outline'
import AppButton from '../AppButton.vue'
import AppToggle from '../AppToggle.vue'
import FilterBar from '../ui/FilterBar.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import StateBlock from '../ui/StateBlock.vue'
import { useLocalList } from '../../composables/useLocalList'
import { strParam } from '../../composables/useUrlState'
import {
  SVG_H,
  SVG_W,
  adjacency,
  circleLayout,
  dependencyRows,
  graphLinks,
  graphNodes,
  type DependencyRow,
  type GraphLink,
  type SbomPackage,
} from '../../lib/sbomGraph'
import { t, tp } from '../../i18n/en'

const props = defineProps<{
  packages: SbomPackage[]
  /** Names the exported file. */
  fileName: string
}>()

// CSS variables exist for both themes, so no dark-mode switch is needed.
const PRIMARY = 'rgb(var(--color-primary))'
const DANGER = 'rgb(var(--color-danger))'
const BORDER_STRONG = 'rgb(var(--color-border-strong))'
const MUTED = 'rgb(var(--color-muted))'

const vulnerableOnly = ref(false)
const selected = ref<string | null>(null)
const svgEl = ref<SVGSVGElement | null>(null)

const nodes = computed(() => graphNodes(props.packages, vulnerableOnly.value))
const links = computed(() => graphLinks(props.packages, nodes.value))
const positions = computed(() => circleLayout(nodes.value))
const vulnerableCount = computed(() => props.packages.filter((p) => p.vulnerable).length)
const adj = computed(() => adjacency(links.value))
const selectedOut = computed(() => (selected.value ? (adj.value.outgoing.get(selected.value)?.length ?? 0) : 0))
const selectedIn = computed(() => (selected.value ? (adj.value.incoming.get(selected.value)?.length ?? 0) : 0))
const highlighted = computed(() => {
  const id = selected.value
  return id ? new Set([id, ...(adj.value.outgoing.get(id) ?? []), ...(adj.value.incoming.get(id) ?? [])]) : new Set<string>()
})

const edgeActive = (l: GraphLink): boolean => selected.value !== null && (l.source === selected.value || l.target === selected.value)
const toggle = (id: string): void => {
  selected.value = selected.value === id ? null : id
}
watch(vulnerableOnly, () => (selected.value = null))

const columns: ListColumn[] = [
  { key: 'name', label: t('artifact.graph.column.name') },
  { key: 'version', label: t('artifact.graph.column.version') },
  { key: 'dependsOn', label: t('artifact.graph.column.dependsOn'), align: 'end' },
]

const { list, refresh } = useLocalList({
  filters: { q: strParam() },
  debounce: ['q'],
  load: async (): Promise<DependencyRow[]> => dependencyRows(props.packages),
  match: (row, { q }) => !q || row.id.toLowerCase().includes(q.toLowerCase()),
})
watch(() => props.packages, () => void refresh())

function exportSvg(): void {
  const el = svgEl.value
  if (!el) return
  // A standalone SVG cannot read the page's CSS variables: inline the resolved channels.
  const root = getComputedStyle(document.documentElement)
  const raw = new XMLSerializer()
    .serializeToString(el)
    .replace(/rgb\(var\((--color-[a-z-]+)\)\)/g, (_m, name: string) => `rgb(${root.getPropertyValue(name).trim().replace(/\s+/g, ',')})`)
  const url = URL.createObjectURL(new Blob([raw], { type: 'image/svg+xml;charset=utf-8' }))
  const link = document.createElement('a')
  link.href = url
  link.download = `${props.fileName}.svg`
  link.click()
  // Revoking in the same tick can cancel the download in some browsers.
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
</script>
