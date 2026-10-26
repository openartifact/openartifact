// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { CycloneDxDocument, SbomItem } from '../api/sbom'

export interface SbomPackage {
  name: string
  version: string
  vulnerable: boolean
  dependencies: Array<{ name: string; version: string }>
}

/** Components of a CycloneDX document with their direct dependencies and vulnerability flag. */
export function packagesFromCycloneDx(doc: CycloneDxDocument): SbomPackage[] {
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
  // Two components can share name and version (a different purl or hash): the drawing and the
  // table identify a package by `name@version`, so they are merged into one.
  const merged = new Map<string, SbomPackage>()
  for (const c of components) {
    const ref = c['bom-ref']
    const pkg: SbomPackage = { name: c.name, version: c.version ?? '', vulnerable: ref ? vulnerable.has(ref) : false, dependencies: [] }
    const id = nodeId(pkg)
    const known = merged.get(id) ?? pkg
    known.vulnerable ||= pkg.vulnerable
    for (const r of ref ? (deps.get(ref) ?? []) : []) {
      const dep = byRef.get(r)
      if (dep && !known.dependencies.some((d) => nodeId(d) === nodeId(dep))) known.dependencies.push(dep)
    }
    merged.set(id, known)
  }
  return [...merged.values()]
}

export interface GraphNode {
  id: string
  name: string
  version: string
  /** Shortened `name@version` for the drawing. */
  label: string
  vulnerable: boolean
}

export interface GraphLink {
  source: string
  target: string
}

export interface Position {
  x: number
  y: number
}

export const SVG_W = 620
export const SVG_H = 540

const nodeId = (pkg: { name: string; version: string }): string => `${pkg.name}@${pkg.version}`

export function graphNodes(packages: readonly SbomPackage[], vulnerableOnly: boolean): GraphNode[] {
  const nodes = packages.map((pkg): GraphNode => {
    const id = nodeId(pkg)
    return { id, name: pkg.name, version: pkg.version, label: id.length > 16 ? `${id.slice(0, 13)}…` : id, vulnerable: pkg.vulnerable }
  })
  return vulnerableOnly ? nodes.filter((n) => n.vulnerable) : nodes
}

/** Edges between the visible nodes only. */
export function graphLinks(packages: readonly SbomPackage[], visible: readonly GraphNode[]): GraphLink[] {
  const ids = new Set(visible.map((n) => n.id))
  const links: GraphLink[] = []
  for (const pkg of packages) {
    const source = nodeId(pkg)
    if (!ids.has(source)) continue
    for (const dep of pkg.dependencies) {
      const target = nodeId(dep)
      if (ids.has(target)) links.push({ source, target })
    }
  }
  return links
}

/** Nodes on a circle whose radius grows with their number. */
export function circleLayout(nodes: readonly GraphNode[]): Record<string, Position> {
  const positions: Record<string, Position> = {}
  const count = nodes.length
  const r = Math.min(220, Math.max(100, count * 14))
  nodes.forEach((node, i) => {
    const angle = (2 * Math.PI * i) / Math.max(count, 1) - Math.PI / 2
    positions[node.id] = { x: SVG_W / 2 + r * Math.cos(angle), y: SVG_H / 2 + r * Math.sin(angle) }
  })
  return positions
}

/** Direct neighbours per node, built once so selecting a node costs its degree. */
export function adjacency(links: readonly GraphLink[]): { outgoing: Map<string, string[]>; incoming: Map<string, string[]> } {
  const outgoing = new Map<string, string[]>()
  const incoming = new Map<string, string[]>()
  for (const l of links) {
    const out = outgoing.get(l.source)
    if (out) out.push(l.target)
    else outgoing.set(l.source, [l.target])
    const inc = incoming.get(l.target)
    if (inc) inc.push(l.source)
    else incoming.set(l.target, [l.source])
  }
  return { outgoing, incoming }
}

/** Rows of the accessible table next to the drawing: each component with what it depends on. */
export interface DependencyRow {
  id: string
  name: string
  version: string
  vulnerable: boolean
  dependsOn: string[]
}

export function dependencyRows(packages: readonly SbomPackage[]): DependencyRow[] {
  return packages.map((pkg) => ({
    id: nodeId(pkg),
    name: pkg.name,
    version: pkg.version,
    vulnerable: pkg.vulnerable,
    dependsOn: pkg.dependencies.map(nodeId),
  }))
}

/** The reports that describe one of the version's files (every file of a version has its own SBOM). */
export function reportsOfVersion(reports: readonly SbomItem[], fileIds: readonly string[]): SbomItem[] {
  const ids = new Set(fileIds)
  return reports
    .filter((r) => ids.has(r.artifact_id))
    .sort((a, b) => reportTime(b) - reportTime(a))
}

/** Generation time in ms; a report without a usable timestamp counts as the oldest. */
function reportTime(r: SbomItem): number {
  const ms = Date.parse(r.generated_at ?? r.created_at ?? '')
  return Number.isNaN(ms) ? Number.NEGATIVE_INFINITY : ms
}
