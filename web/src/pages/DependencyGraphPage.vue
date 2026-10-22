<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <PageHeader :title="t('graph.title')" :description="t('graph.subtitle').replace('{repo}', repoName)">
      <template #actions>
        <!-- SBOM selector -->
        <div class="relative">
          <select
            v-model="selectedSbomId"
            :disabled="loadingSboms || sboms.length === 0"
            class="appearance-none pl-3 pr-8 py-1.5 rounded-lg border border-border bg-surface text-sm text-foreground font-medium focus:outline-none focus:ring-2 focus:ring-primary transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            @change="onSbomChange"
          >
            <option value="">
              {{ sboms.length === 0 ? t('graph.noSboms') : t('graph.selectSbom') }}
            </option>
            <option v-for="sbom in sboms" :key="sbom.id" :value="sbom.id">
              {{ sbomLabel(sbom) }}
            </option>
          </select>
          <ChevronDownIcon class="pointer-events-none absolute right-2.5 top-1/2 -translate-y-1/2 w-4 h-4 text-muted" />
        </div>

        <!-- Export SVG -->
        <button
          type="button"
          :disabled="visibleNodes.length === 0"
          class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium border border-border bg-surface text-foreground hover:bg-surface-hover transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          @click="exportSvg"
        >
          <ArrowDownTrayIcon class="w-4 h-4" />
          {{ t('graph.exportSvg') }}
        </button>
      </template>
    </PageHeader>

    <!-- Main grid: graph + sidebar -->
    <div class="grid grid-cols-1 lg:grid-cols-[1fr_300px] gap-4">

      <!-- ── Graph canvas ────────────────────────────────────────────────── -->
      <div class="bg-surface rounded-xl border border-border overflow-hidden min-h-[520px] flex items-center justify-center relative">

        <!-- Loading spinner -->
        <div v-if="loadingSboms || loadingGraph" class="flex flex-col items-center gap-3 text-center">
          <ArrowPathIcon class="w-8 h-8 text-muted animate-spin" />
          <p class="text-sm text-muted">{{ t('graph.loading') }}</p>
        </div>

        <!-- No SBOMs available -->
        <div v-else-if="sboms.length === 0" class="text-center px-6">
          <div class="w-16 h-16 rounded-full bg-surface-hover flex items-center justify-center mx-auto mb-4">
            <ShareIcon class="w-8 h-8 text-muted" />
          </div>
          <p class="text-base font-semibold text-foreground">{{ t('graph.noSboms') }}</p>
          <p class="text-sm text-muted mt-1 mb-4">{{ t('graph.noSbomsHint') }}</p>
          <router-link
            :to="`/repos/${repoName}`"
            class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium bg-primary text-primary-contrast hover:opacity-90 transition-opacity"
          >
            Go to repository
          </router-link>
        </div>

        <!-- Select a SBOM first -->
        <div v-else-if="!selectedSbomId" class="text-center px-6">
          <div class="w-16 h-16 rounded-full bg-surface-hover flex items-center justify-center mx-auto mb-4">
            <ShareIcon class="w-8 h-8 text-muted" />
          </div>
          <p class="text-base font-semibold text-foreground">{{ t('graph.emptyTitle') }}</p>
          <p class="text-sm text-muted mt-1">{{ t('graph.emptyHint') }}</p>
        </div>

        <!-- No packages in SBOM -->
        <div v-else-if="visibleNodes.length === 0 && !loadingGraph" class="text-center px-6">
          <div class="w-16 h-16 rounded-full bg-surface-hover flex items-center justify-center mx-auto mb-4">
            <ShareIcon class="w-8 h-8 text-muted" />
          </div>
          <p class="text-base font-semibold text-foreground">{{ t('graph.noPackages') }}</p>
          <p class="text-sm text-muted mt-1">
            {{ showVulnerableOnly ? t('graph.noVulnerable') : t('graph.noPackagesHint') }}
          </p>
        </div>

        <!-- SVG Graph -->
        <svg
          v-else
          ref="svgEl"
          :viewBox="`0 0 ${SVG_W} ${SVG_H}`"
          class="w-full h-full"
          xmlns="http://www.w3.org/2000/svg"
          style="max-height: 560px"
        >
          <defs>
            <!-- Arrowhead marker -->
            <marker
              id="arrow"
              viewBox="0 0 10 10"
              refX="18"
              refY="5"
              markerWidth="6"
              markerHeight="6"
              orient="auto-start-reverse"
            >
              <path d="M 0 0 L 10 5 L 0 10 z" :fill="borderColor" fill-opacity="0.6" />
            </marker>
            <!-- Highlighted arrowhead -->
            <marker
              id="arrow-hl"
              viewBox="0 0 10 10"
              refX="18"
              refY="5"
              markerWidth="6"
              markerHeight="6"
              orient="auto-start-reverse"
            >
              <path d="M 0 0 L 10 5 L 0 10 z" fill="#6366f1" />
            </marker>
          </defs>

          <!-- Edges -->
          <g>
            <line
              v-for="(link, i) in links"
              :key="`link-${i}`"
              :x1="nodePositions[link.source]?.x ?? 0"
              :y1="nodePositions[link.source]?.y ?? 0"
              :x2="nodePositions[link.target]?.x ?? 0"
              :y2="nodePositions[link.target]?.y ?? 0"
              :stroke="isEdgeHighlighted(link) ? '#6366f1' : borderColor"
              :stroke-width="isEdgeHighlighted(link) ? 2 : 1.2"
              :stroke-opacity="isEdgeHighlighted(link) ? 0.9 : (selectedNodeId ? 0.15 : 0.5)"
              :marker-end="isEdgeHighlighted(link) ? 'url(#arrow-hl)' : 'url(#arrow)'"
              class="transition-all duration-150"
            />
          </g>

          <!-- Nodes -->
          <g>
            <g
              v-for="node in visibleNodes"
              :key="node.id"
              class="cursor-pointer"
              :style="{ opacity: selectedNodeId && !isNodeHighlighted(node.id) ? 0.3 : 1 }"
              style="transition: opacity 0.15s"
              @click="toggleNode(node.id)"
            >
              <!-- Node circle -->
              <circle
                :cx="nodePositions[node.id]?.x ?? 0"
                :cy="nodePositions[node.id]?.y ?? 0"
                r="13"
                :fill="nodeColor(node)"
                :stroke="selectedNodeId === node.id ? '#6366f1' : borderColor"
                :stroke-width="selectedNodeId === node.id ? 2.5 : 1.5"
                class="transition-all duration-100"
              />
              <!-- Label below node -->
              <text
                :x="nodePositions[node.id]?.x ?? 0"
                :y="(nodePositions[node.id]?.y ?? 0) + 26"
                text-anchor="middle"
                font-size="10"
                font-family="ui-monospace, monospace"
                :fill="textColor"
                class="pointer-events-none select-none"
              >
                {{ node.label }}
              </text>
            </g>
          </g>
        </svg>

        <!-- Click hint -->
        <p
          v-if="visibleNodes.length > 0 && !loadingGraph"
          class="absolute bottom-3 left-0 right-0 text-center text-[11px] text-muted pointer-events-none"
        >
          {{ t('graph.clickHint') }}
        </p>
      </div>

      <!-- ── Sidebar ──────────────────────────────────────────────────────── -->
      <div class="bg-surface rounded-xl border border-border p-5 space-y-5">

        <!-- Stats -->
        <div>
          <h3 class="text-xs font-semibold text-muted mb-3">{{ t('graph.stats') }}</h3>
          <div class="space-y-2">
            <div class="flex items-center justify-between text-sm">
              <span class="text-muted">{{ t('graph.nodes') }}</span>
              <span class="font-mono font-medium text-foreground tabular-nums">{{ visibleNodes.length }}</span>
            </div>
            <div class="flex items-center justify-between text-sm">
              <span class="text-muted">{{ t('graph.edges') }}</span>
              <span class="font-mono font-medium text-foreground tabular-nums">{{ links.length }}</span>
            </div>
            <div v-if="vulnerableCount > 0" class="flex items-center justify-between text-sm">
              <span class="text-muted">{{ t('graph.vulnerable') }}</span>
              <span class="font-mono font-medium text-danger tabular-nums">{{ vulnerableCount }}</span>
            </div>
          </div>
        </div>

        <!-- Selected node info -->
        <div v-if="selectedNodeId" class="rounded-lg border border-primary/30 bg-primary/5 p-3">
          <p class="text-[10px] font-mono font-semibold text-primary mb-1">{{ t('graph.selected') }}</p>
          <p class="text-xs font-mono font-medium text-foreground break-all">{{ selectedNodeId }}</p>
          <div class="mt-2 space-y-1">
            <p class="text-[11px] text-muted">
              <span class="font-semibold text-foreground">{{ outgoingEdges(selectedNodeId).length }}</span>
              {{ t('graph.outgoing') }}
            </p>
            <p class="text-[11px] text-muted">
              <span class="font-semibold text-foreground">{{ incomingEdges(selectedNodeId).length }}</span>
              {{ t('graph.dependents') }}
            </p>
          </div>
        </div>

        <!-- Legend -->
        <div>
          <h3 class="text-xs font-semibold text-muted mb-3">{{ t('graph.legend') }}</h3>
          <div class="space-y-2">
            <div class="flex items-center gap-2 text-sm">
              <span class="w-3 h-3 rounded-full bg-[#6366f1]" />
              <span class="text-muted">{{ t('graph.package') }}</span>
            </div>
            <div v-if="vulnerableCount > 0" class="flex items-center gap-2 text-sm">
              <span class="w-3 h-3 rounded-full bg-danger" />
              <span class="text-muted">{{ t('graph.vulnerable') }}</span>
            </div>
          </div>
        </div>

        <!-- Controls -->
        <div class="pt-3 border-t border-border space-y-3">
          <!-- Show vulnerable only -->
          <label class="flex items-center gap-2.5 cursor-pointer">
            <button
              type="button"
              role="switch"
              :aria-checked="showVulnerableOnly"
              class="relative inline-flex h-5 w-9 shrink-0 rounded-full transition-colors duration-200 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary"
              :class="showVulnerableOnly ? 'bg-primary' : 'bg-border'"
              @click="showVulnerableOnly = !showVulnerableOnly"
            >
              <span
                class="inline-block h-4 w-4 rounded-full bg-white shadow transition-transform duration-200 mt-0.5"
                :class="showVulnerableOnly ? 'translate-x-4' : 'translate-x-0.5'"
              />
            </button>
            <span class="text-sm text-foreground">{{ t('graph.showVulnerableOnly') }}</span>
          </label>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import PageHeader from '../components/PageHeader.vue'
import { formatDate } from '../lib/datetime'
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute } from 'vue-router'
import { request } from '../api/client'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'
import {
  ChevronDownIcon,
  ShareIcon,
  ArrowDownTrayIcon,
  ArrowPathIcon,
} from '@heroicons/vue/24/outline'

// ─── Types ─────────────────────────────────────────────────────────────────────

interface SbomItem {
  id: string
  format: string
  spec_version?: string
  component_count?: number
  created_at?: string
}

interface SbomPackage {
  name: string
  version: string
  vulnerable?: boolean
  dependencies?: Array<{ name: string; version: string }>
}

// Subset of a CycloneDX JSON document, as streamed by the /download route.
interface CycloneDxDocument {
  components?: Array<{ 'bom-ref'?: string; name: string; version?: string }>
  dependencies?: Array<{ ref: string; dependsOn?: string[] }>
  vulnerabilities?: Array<{ affects?: Array<{ ref: string }> }>
}

function packagesFromCycloneDx(doc: CycloneDxDocument): SbomPackage[] {
  const components = doc.components ?? []
  const byRef = new Map<string, { name: string; version: string }>()
  for (const c of components) {
    if (c['bom-ref']) byRef.set(c['bom-ref'], { name: c.name, version: c.version ?? '' })
  }
  const deps = new Map<string, string[]>()
  for (const d of doc.dependencies ?? []) deps.set(d.ref, d.dependsOn ?? [])
  const vulnerable = new Set<string>()
  for (const v of doc.vulnerabilities ?? []) {
    for (const a of v.affects ?? []) vulnerable.add(a.ref)
  }
  return components.map((c) => ({
    name: c.name,
    version: c.version ?? '',
    vulnerable: c['bom-ref'] ? vulnerable.has(c['bom-ref']) : false,
    dependencies: (c['bom-ref'] ? (deps.get(c['bom-ref']) ?? []) : [])
      .map((ref) => byRef.get(ref))
      .filter((x): x is { name: string; version: string } => x !== undefined),
  }))
}

function sbomLabel(sbom: SbomItem): string {
  const when = sbom.created_at ? formatDate(sbom.created_at) : sbom.id.slice(0, 8)
  return `${sbom.format}${sbom.spec_version ? ' ' + sbom.spec_version : ''} · ${when}`
}

interface GraphNode {
  id: string
  name: string
  version: string
  label: string
  vulnerable: boolean
}

interface GraphLink {
  source: string
  target: string
}

interface Position {
  x: number
  y: number
}

// ─── Constants ─────────────────────────────────────────────────────────────────

const SVG_W = 620
const SVG_H = 540

// ─── Route & Router ────────────────────────────────────────────────────────────

const route = useRoute()
const repoName = computed(() => route.params.name as string)
const toasts = useToastStore()

// ─── Theme helpers ─────────────────────────────────────────────────────────────

const isDark = computed(() =>
  typeof document !== 'undefined'
    ? document.documentElement.classList.contains('dark')
    : false,
)
const borderColor = computed(() => (isDark.value ? '#475569' : '#cbd5e1'))
const textColor = computed(() => (isDark.value ? '#94a3b8' : '#64748b'))

// ─── State ─────────────────────────────────────────────────────────────────────

const sboms = ref<SbomItem[]>([])
const loadingSboms = ref(true)
const loadingGraph = ref(false)

const selectedSbomId = ref<string>('')
const allPackages = ref<SbomPackage[]>([])

const showVulnerableOnly = ref(false)
const selectedNodeId = ref<string | null>(null)

const svgEl = ref<SVGSVGElement | null>(null)
const loadError = ref('')
let detailGeneration = 0

// ─── Derived Graph Data ─────────────────────────────────────────────────────────

const allNodes = computed<GraphNode[]>(() =>
  allPackages.value.map((pkg) => {
    const id = `${pkg.name}@${pkg.version}`
    return {
      id,
      name: pkg.name,
      version: pkg.version,
      label: id.length > 16 ? id.slice(0, 13) + '…' : id,
      vulnerable: pkg.vulnerable ?? false,
    }
  }),
)

const visibleNodes = computed<GraphNode[]>(() =>
  showVulnerableOnly.value ? allNodes.value.filter((n) => n.vulnerable) : allNodes.value,
)

const visibleNodeIds = computed(() => new Set(visibleNodes.value.map((n) => n.id)))

const links = computed<GraphLink[]>(() => {
  const result: GraphLink[] = []
  for (const pkg of allPackages.value) {
    const srcId = `${pkg.name}@${pkg.version}`
    if (!visibleNodeIds.value.has(srcId)) continue
    for (const dep of pkg.dependencies ?? []) {
      const tgtId = `${dep.name}@${dep.version}`
      if (visibleNodeIds.value.has(tgtId)) {
        result.push({ source: srcId, target: tgtId })
      }
    }
  }
  return result
})

const nodePositions = computed<Record<string, Position>>(() => {
  const positions: Record<string, Position> = {}
  const nodes = visibleNodes.value
  const count = nodes.length
  const cx = SVG_W / 2
  const cy = SVG_H / 2
  // Scale radius with node count for readability
  const r = Math.min(220, Math.max(100, count * 14))

  nodes.forEach((node, i) => {
    const angle = (2 * Math.PI * i) / Math.max(count, 1) - Math.PI / 2
    positions[node.id] = {
      x: cx + r * Math.cos(angle),
      y: cy + r * Math.sin(angle),
    }
  })

  return positions
})

const vulnerableCount = computed(() => allNodes.value.filter((n) => n.vulnerable).length)

// ─── Highlight Helpers ─────────────────────────────────────────────────────────

function outgoingEdges(nodeId: string): GraphLink[] {
  return links.value.filter((l) => l.source === nodeId)
}

function incomingEdges(nodeId: string): GraphLink[] {
  return links.value.filter((l) => l.target === nodeId)
}

function isEdgeHighlighted(link: GraphLink): boolean {
  if (!selectedNodeId.value) return false
  return link.source === selectedNodeId.value || link.target === selectedNodeId.value
}

function isNodeHighlighted(nodeId: string): boolean {
  if (!selectedNodeId.value) return true
  if (nodeId === selectedNodeId.value) return true
  return links.value.some(
    (l) =>
      (l.source === selectedNodeId.value && l.target === nodeId) ||
      (l.target === selectedNodeId.value && l.source === nodeId),
  )
}

function toggleNode(nodeId: string): void {
  selectedNodeId.value = selectedNodeId.value === nodeId ? null : nodeId
}

function nodeColor(node: GraphNode): string {
  if (node.vulnerable) return 'rgb(var(--color-danger))'
  if (selectedNodeId.value === node.id) return '#6366f1'
  return isDark.value ? '#4f46e5' : '#6366f1'
}

// ─── Data Loading ──────────────────────────────────────────────────────────────

async function loadSboms(): Promise<void> {
  loadingSboms.value = true
  loadError.value = ''
  try {
    const res = await request<{ reports?: SbomItem[] }>(
      `/api/v1/repositories/${encodeURIComponent(repoName.value)}/sbom`,
    )
    sboms.value = res?.reports ?? []
  } catch (err) {
    sboms.value = []
    loadError.value = err instanceof Error ? err.message : 'Failed to load SBOMs'
    toasts.error(loadError.value)
  } finally {
    loadingSboms.value = false
  }
}

async function loadSbomDetail(id: string): Promise<void> {
  loadingGraph.value = true
  allPackages.value = []
  selectedNodeId.value = null
  loadError.value = ''
  const generation = ++detailGeneration
  try {
    const doc = await request<CycloneDxDocument>(
      `/api/v1/repositories/${encodeURIComponent(repoName.value)}/sbom/${encodeURIComponent(id)}/download`,
    )
    if (generation !== detailGeneration) return
    allPackages.value = packagesFromCycloneDx(doc ?? {})
  } catch (err) {
    if (generation !== detailGeneration) return
    allPackages.value = []
    loadError.value = err instanceof Error ? err.message : 'Failed to load SBOM'
    toasts.error(loadError.value)
  } finally {
    if (generation === detailGeneration) loadingGraph.value = false
  }
}

function onSbomChange(): void {
  if (selectedSbomId.value) {
    loadSbomDetail(selectedSbomId.value)
  } else {
    allPackages.value = []
    selectedNodeId.value = null
  }
}

// ─── Export SVG ────────────────────────────────────────────────────────────────

function exportSvg(): void {
  const el = svgEl.value
  if (!el) return

  const serializer = new XMLSerializer()
  const raw = serializer.serializeToString(el)
  const blob = new Blob([raw], { type: 'image/svg+xml;charset=utf-8' })
  const url = URL.createObjectURL(blob)

  const link = document.createElement('a')
  link.href = url
  link.download = `${repoName.value}-dependency-graph.svg`
  link.click()
  URL.revokeObjectURL(url)
}

// ─── Watchers ──────────────────────────────────────────────────────────────────

watch(showVulnerableOnly, () => {
  selectedNodeId.value = null
})

// ─── Lifecycle ─────────────────────────────────────────────────────────────────

onMounted(loadSboms)

watch(repoName, () => {
  selectedSbomId.value = ''
  allPackages.value = []
  selectedNodeId.value = null
  void loadSboms()
})
</script>
