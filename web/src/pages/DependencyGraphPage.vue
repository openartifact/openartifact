<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <RepoHeader :name="repoName" :repo="repoInfo" active="graph" />

    <div class="!mt-0 flex flex-wrap items-center justify-between gap-3">
      <p class="text-sm text-muted">{{ tp('graph.subtitle', { repo: repoName }) }}</p>
      <div class="flex flex-wrap items-center gap-2">
        <!-- SBOM selector -->
        <div class="relative">
          <select
            v-model="selectedSbomId"
            :disabled="loadingSboms || sboms.length === 0"
            :aria-label="t('graph.sbomList')"
            class="appearance-none pl-3 pr-8 py-1.5 rounded-lg border border-border-strong bg-surface text-sm text-foreground font-medium focus:ring-2 focus:ring-primary transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            @change="onSbomChange"
          >
            <option value="">
              {{ sboms.length === 0 ? t('graph.noSboms') : t('graph.selectSbom') }}
            </option>
            <option v-for="sbom in sboms" :key="sbom.id" :value="sbom.id">
              {{ sbomLabel(sbom) }}
            </option>
          </select>
          <ChevronDownIcon class="pointer-events-none absolute right-2.5 top-1/2 -translate-y-1/2 w-4 h-4 text-muted" aria-hidden="true" />
        </div>

        <!-- Export SVG -->
        <AppButton variant="secondary" :disabled="visibleNodes.length === 0" @click="exportSvg">
          <ArrowDownTrayIcon class="w-4 h-4" aria-hidden="true" />
          {{ t('graph.exportSvg') }}
        </AppButton>
      </div>
    </div>

    <!-- Main grid: graph + sidebar -->
    <div class="grid grid-cols-1 lg:grid-cols-[1fr_300px] gap-4">

      <!-- ── Graph canvas ────────────────────────────────────────────────── -->
      <div class="bg-surface rounded-xl border border-border overflow-hidden min-h-[520px] flex items-center justify-center relative">

        <!-- Loading spinner -->
        <div v-if="loadingSboms || loadingGraph" class="flex flex-col items-center gap-3 text-center">
          <ArrowPathIcon class="w-8 h-8 text-muted animate-spin" aria-hidden="true" />
          <p class="text-sm text-muted">{{ t('graph.loading') }}</p>
        </div>

        <!-- No SBOMs available -->
        <div v-else-if="sboms.length === 0" class="text-center px-6">
          <div class="w-16 h-16 rounded-full bg-surface-hover flex items-center justify-center mx-auto mb-4">
            <ShareIcon class="w-8 h-8 text-muted" aria-hidden="true" />
          </div>
          <p class="text-base font-semibold text-foreground">{{ t('graph.noSboms') }}</p>
          <p class="text-sm text-muted mt-1 mb-4">{{ t('graph.noSbomsHint') }}</p>
          <AppButton size="sm" :to="`/repos/${encodeURIComponent(repoName)}`">{{ t('graph.goToRepo') }}</AppButton>
        </div>

        <!-- Select a SBOM first -->
        <div v-else-if="!selectedSbomId" class="text-center px-6">
          <div class="w-16 h-16 rounded-full bg-surface-hover flex items-center justify-center mx-auto mb-4">
            <ShareIcon class="w-8 h-8 text-muted" aria-hidden="true" />
          </div>
          <p class="text-base font-semibold text-foreground">{{ t('graph.emptyTitle') }}</p>
          <p class="text-sm text-muted mt-1">{{ t('graph.emptyHint') }}</p>
        </div>

        <!-- No packages in SBOM -->
        <div v-else-if="visibleNodes.length === 0 && !loadingGraph" class="text-center px-6">
          <div class="w-16 h-16 rounded-full bg-surface-hover flex items-center justify-center mx-auto mb-4">
            <ShareIcon class="w-8 h-8 text-muted" aria-hidden="true" />
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
              <path d="M 0 0 L 10 5 L 0 10 z" :fill="BORDER_STRONG" fill-opacity="0.6" />
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
              <path d="M 0 0 L 10 5 L 0 10 z" fill="rgb(var(--color-primary))" />
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
              :stroke="isEdgeHighlighted(link) ? PRIMARY : BORDER_STRONG"
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
                :stroke="selectedNodeId === node.id ? PRIMARY : BORDER_STRONG"
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
                :fill="MUTED"
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
          class="absolute bottom-3 left-0 right-0 text-center text-caption text-muted pointer-events-none"
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
          <p class="text-micro font-mono font-semibold text-primary mb-1">{{ t('graph.selected') }}</p>
          <p class="text-xs font-mono font-medium text-foreground break-all">{{ selectedNodeId }}</p>
          <div class="mt-2 space-y-1">
            <p class="text-caption text-muted">
              <span class="font-semibold text-foreground">{{ selectedOutgoing }}</span>
              {{ t('graph.outgoing') }}
            </p>
            <p class="text-caption text-muted">
              <span class="font-semibold text-foreground">{{ selectedIncoming }}</span>
              {{ t('graph.dependents') }}
            </p>
          </div>
        </div>

        <!-- Legend -->
        <div>
          <h3 class="text-xs font-semibold text-muted mb-3">{{ t('graph.legend') }}</h3>
          <div class="space-y-2">
            <div class="flex items-center gap-2 text-sm">
              <span class="w-3 h-3 rounded-full bg-primary" />
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
          <div class="flex items-center gap-2.5">
            <AppToggle v-model="showVulnerableOnly" labelledby="graph-vuln-only-label" />
            <span id="graph-vuln-only-label" class="text-sm text-foreground">{{ t('graph.showVulnerableOnly') }}</span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import AppButton from '../components/AppButton.vue'
import AppToggle from '../components/AppToggle.vue'
import RepoHeader from '../components/RepoHeader.vue'
import { getRepository, type Repository } from '../api/repositories'
import { formatDate } from '../lib/datetime'
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute } from 'vue-router'
import { getSbomDocument, listSboms, type CycloneDxDocument, type SbomItem } from '../api/sbom'
import { useToastStore } from '../stores/toast'
import { t, tp } from '../i18n/en'
import {
  ChevronDownIcon,
  ShareIcon,
  ArrowDownTrayIcon,
  ArrowPathIcon,
} from '@heroicons/vue/24/outline'

// ─── Types ─────────────────────────────────────────────────────────────────────

interface SbomPackage {
  name: string
  version: string
  vulnerable?: boolean
  dependencies?: Array<{ name: string; version: string }>
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

// ─── Theme colours ─────────────────────────────────────────────────────────────
// CSS variables are defined for both themes, so no dark-mode switch is needed.

const PRIMARY = 'rgb(var(--color-primary))'
const BORDER_STRONG = 'rgb(var(--color-border-strong))'
const MUTED = 'rgb(var(--color-muted))'

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
let sbomsGeneration = 0

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

// Adjacency is built once per graph, so selecting a node costs O(its degree)
// instead of a scan of every edge per node and per render.
const adjacency = computed(() => {
  const outgoing = new Map<string, string[]>()
  const incoming = new Map<string, string[]>()
  for (const l of links.value) {
    let out = outgoing.get(l.source)
    if (!out) outgoing.set(l.source, (out = []))
    out.push(l.target)
    let inc = incoming.get(l.target)
    if (!inc) incoming.set(l.target, (inc = []))
    inc.push(l.source)
  }
  return { outgoing, incoming }
})

const selectedOutgoing = computed(() =>
  selectedNodeId.value ? (adjacency.value.outgoing.get(selectedNodeId.value)?.length ?? 0) : 0,
)
const selectedIncoming = computed(() =>
  selectedNodeId.value ? (adjacency.value.incoming.get(selectedNodeId.value)?.length ?? 0) : 0,
)

// The selected node and its direct neighbours stay fully opaque.
const highlightedNodeIds = computed<Set<string> | null>(() => {
  const id = selectedNodeId.value
  if (!id) return null
  return new Set([
    id,
    ...(adjacency.value.outgoing.get(id) ?? []),
    ...(adjacency.value.incoming.get(id) ?? []),
  ])
})

function isEdgeHighlighted(link: GraphLink): boolean {
  if (!selectedNodeId.value) return false
  return link.source === selectedNodeId.value || link.target === selectedNodeId.value
}

function isNodeHighlighted(nodeId: string): boolean {
  return highlightedNodeIds.value?.has(nodeId) ?? true
}

function toggleNode(nodeId: string): void {
  selectedNodeId.value = selectedNodeId.value === nodeId ? null : nodeId
}

function nodeColor(node: GraphNode): string {
  if (node.vulnerable) return 'rgb(var(--color-danger))'
  return PRIMARY
}

// ─── Data Loading ──────────────────────────────────────────────────────────────

async function loadSboms(): Promise<void> {
  const generation = ++sbomsGeneration
  loadingSboms.value = true
  loadError.value = ''
  try {
    const reports = await listSboms(repoName.value)
    if (generation !== sbomsGeneration) return
    sboms.value = reports
  } catch (err) {
    if (generation !== sbomsGeneration) return
    sboms.value = []
    loadError.value = err instanceof Error ? err.message : t('graph.sbomsLoadFailed')
    toasts.error(loadError.value)
  } finally {
    if (generation === sbomsGeneration) loadingSboms.value = false
  }
}

async function loadSbomDetail(id: string): Promise<void> {
  loadingGraph.value = true
  allPackages.value = []
  selectedNodeId.value = null
  loadError.value = ''
  const generation = ++detailGeneration
  try {
    const doc = await getSbomDocument(repoName.value, id)
    if (generation !== detailGeneration) return
    allPackages.value = packagesFromCycloneDx(doc ?? {})
  } catch (err) {
    if (generation !== detailGeneration) return
    allPackages.value = []
    loadError.value = err instanceof Error ? err.message : t('graph.sbomLoadFailed')
    toasts.error(loadError.value)
  } finally {
    if (generation === detailGeneration) loadingGraph.value = false
  }
}

function onSbomChange(): void {
  if (selectedSbomId.value) {
    loadSbomDetail(selectedSbomId.value)
  } else {
    // A detail request still in flight must not repaint the cleared graph.
    detailGeneration++
    loadingGraph.value = false
    loadError.value = ''
    allPackages.value = []
    selectedNodeId.value = null
  }
}

// ─── Export SVG ────────────────────────────────────────────────────────────────

function exportSvg(): void {
  const el = svgEl.value
  if (!el) return

  // A standalone SVG has no access to the page's CSS variables: inline the resolved channels.
  const rootStyle = getComputedStyle(document.documentElement)
  const raw = new XMLSerializer()
    .serializeToString(el)
    .replace(/rgb\(var\((--color-[a-z-]+)\)\)/g, (_m, name: string) => `rgb(${rootStyle.getPropertyValue(name).trim().replace(/\s+/g, ',')})`)
  const blob = new Blob([raw], { type: 'image/svg+xml;charset=utf-8' })
  const url = URL.createObjectURL(blob)

  const link = document.createElement('a')
  link.href = url
  link.download = `${repoName.value}-dependency-graph.svg`
  link.click()
  // Revoking in the same tick can cancel the download in some browsers.
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

// ─── Watchers ──────────────────────────────────────────────────────────────────

watch(showVulnerableOnly, () => {
  selectedNodeId.value = null
})

// ─── Lifecycle ─────────────────────────────────────────────────────────────────

const repoInfo = ref<Repository | null>(null)

async function loadRepo(): Promise<void> {
  const name = repoName.value
  try {
    const r = await getRepository(name)
    if (name === repoName.value) repoInfo.value = r
  } catch {
    // the SBOM list reports access problems; the header falls back to the bare name
  }
}

onMounted(() => {
  void loadRepo()
  void loadSboms()
})

watch(repoName, () => {
  repoInfo.value = null
  selectedSbomId.value = ''
  detailGeneration++
  loadingGraph.value = false
  allPackages.value = []
  selectedNodeId.value = null
  void loadRepo()
  void loadSboms()
})
</script>
