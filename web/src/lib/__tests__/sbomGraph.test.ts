// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import type { SbomItem } from '../../api/sbom'
import { SVG_H, SVG_W, adjacency, circleLayout, dependencyRows, graphLinks, graphNodes, packagesFromCycloneDx, reportsOfVersion } from '../sbomGraph'

const doc = {
  components: [
    { 'bom-ref': 'a', name: 'app', version: '1.0.0' },
    { 'bom-ref': 'b', name: 'lib', version: '2.0.0' },
    { 'bom-ref': 'c', name: 'a-very-long-component-name', version: '3.0.0' },
  ],
  dependencies: [{ ref: 'a', dependsOn: ['b', 'c', 'missing'] }],
  vulnerabilities: [{ affects: [{ ref: 'b' }] }],
}

describe('packagesFromCycloneDx', () => {
  const packages = packagesFromCycloneDx(doc)
  it('resolves direct dependencies by reference and drops unknown ones', () => {
    expect(packages[0]!.dependencies.map((d) => d.name)).toEqual(['lib', 'a-very-long-component-name'])
  })
  it('flags components named by a vulnerability', () => {
    expect(packages.map((p) => p.vulnerable)).toEqual([false, true, false])
  })
  it('tolerates an empty document', () => {
    expect(packagesFromCycloneDx({})).toEqual([])
  })

  it('shortens long labels and filters to vulnerable nodes', () => {
    expect(graphNodes(packages, false)[2]!.label).toHaveLength(14)
    expect(graphNodes(packages, true).map((n) => n.name)).toEqual(['lib'])
  })

  it('links only visible nodes and indexes neighbours', () => {
    const nodes = graphNodes(packages, false)
    const links = graphLinks(packages, nodes)
    expect(links).toHaveLength(2)
    expect(graphLinks(packages, graphNodes(packages, true))).toEqual([])
    const adj = adjacency(links)
    expect(adj.outgoing.get('app@1.0.0')).toHaveLength(2)
    expect(adj.incoming.get('lib@2.0.0')).toEqual(['app@1.0.0'])
  })

  it('places every node and lists table rows', () => {
    const nodes = graphNodes(packages, false)
    expect(Object.keys(circleLayout(nodes))).toHaveLength(3)
    expect(dependencyRows(packages)[0]).toMatchObject({ id: 'app@1.0.0', dependsOn: ['lib@2.0.0', 'a-very-long-component-name@3.0.0'] })
  })
})

describe('reportsOfVersion', () => {
  it('keeps the reports of the version files, newest first', () => {
    const reports = [
      { id: '1', artifact_id: 'f1', format: 'cyclonedx', generated_at: '2025-01-01T00:00:00Z' },
      { id: '2', artifact_id: 'other', format: 'cyclonedx' },
      { id: '3', artifact_id: 'f2', format: 'spdx', generated_at: '2025-02-01T00:00:00Z' },
    ] as SbomItem[]
    expect(reportsOfVersion(reports, ['f1', 'f2']).map((r) => r.id)).toEqual(['3', '1'])
  })
})

describe('graph data edge cases', () => {
  it('draws a dependency cycle and a self-dependency once per edge without looping', () => {
    const packages = packagesFromCycloneDx({
      components: [
        { 'bom-ref': 'a', name: 'a', version: '1' },
        { 'bom-ref': 'b', name: 'b', version: '1' },
      ],
      dependencies: [
        { ref: 'a', dependsOn: ['b', 'a'] },
        { ref: 'b', dependsOn: ['a'] },
      ],
    })
    const links = graphLinks(packages, graphNodes(packages, false))
    expect(links).toEqual([
      { source: 'a@1', target: 'b@1' },
      { source: 'a@1', target: 'a@1' },
      { source: 'b@1', target: 'a@1' },
    ])
    const adj = adjacency(links)
    expect(adj.incoming.get('a@1')).toEqual(['a@1', 'b@1'])
  })

  it('keeps a component without a bom-ref but cannot link to or from it', () => {
    const packages = packagesFromCycloneDx({
      components: [{ name: 'orphan', version: '1' }, { 'bom-ref': 'x', name: 'x' }],
      dependencies: [{ ref: 'x', dependsOn: ['orphan', 'x'] }],
      vulnerabilities: [{ affects: [{ ref: 'orphan' }] }],
    })
    expect(packages.map((p) => [p.name, p.version, p.vulnerable])).toEqual([
      ['orphan', '1', false],
      ['x', '', false],
    ])
    expect(packages[1]!.dependencies).toEqual([{ name: 'x', version: '' }])
  })

  it('tolerates components without dependency, affects or dependsOn lists', () => {
    const packages = packagesFromCycloneDx({
      components: [{ 'bom-ref': 'a', name: 'a' }],
      dependencies: [{ ref: 'a' }],
      vulnerabilities: [{}],
    })
    expect(packages).toEqual([{ name: 'a', version: '', vulnerable: false, dependencies: [] }])
  })

  it('draws one node per component even when two share name and version', () => {
    const packages = packagesFromCycloneDx({
      components: [
        { 'bom-ref': 'p1', name: 'dup', version: '1' },
        { 'bom-ref': 'p2', name: 'dup', version: '1' },
        { 'bom-ref': 'p3', name: 'top', version: '1' },
      ],
      dependencies: [{ ref: 'p3', dependsOn: ['p1', 'p2'] }],
      vulnerabilities: [{ affects: [{ ref: 'p2' }] }],
    })
    const nodes = graphNodes(packages, false)
    expect(nodes.map((n) => n.id)).toEqual(['dup@1', 'top@1'])
    // The duplicate keeps the vulnerable flag of either copy.
    expect(nodes[0]!.vulnerable).toBe(true)
    expect(graphLinks(packages, nodes)).toEqual([{ source: 'top@1', target: 'dup@1' }])
    expect(dependencyRows(packages).map((r) => r.id)).toEqual(['dup@1', 'top@1'])
  })

  it('caps the circle radius so a large graph stays inside the drawing and keeps nodes apart', () => {
    const nodes = Array.from({ length: 500 }, (_, i) => ({ id: `n${i}`, name: `n${i}`, version: '', label: '', vulnerable: false }))
    const positions = circleLayout(nodes)
    expect(Object.keys(positions)).toHaveLength(500)
    for (const p of Object.values(positions)) {
      expect(Math.hypot(p.x - SVG_W / 2, p.y - SVG_H / 2)).toBeCloseTo(220, 5)
      expect(p.x).toBeGreaterThan(0)
      expect(p.x).toBeLessThan(SVG_W)
      expect(p.y).toBeGreaterThan(0)
      expect(p.y).toBeLessThan(SVG_H)
    }
    expect(positions.n0).toEqual({ x: SVG_W / 2, y: SVG_H / 2 - 220 })
  })

  it('lays out nothing for no nodes and a single node at the top', () => {
    expect(circleLayout([])).toEqual({})
    const one = circleLayout([{ id: 'a', name: 'a', version: '', label: 'a', vulnerable: false }])
    expect(one.a).toEqual({ x: SVG_W / 2, y: SVG_H / 2 - 100 })
  })

  it('builds the graph of a large generated document in reasonable time', () => {
    const n = 3000
    const doc = {
      components: Array.from({ length: n }, (_, i) => ({ 'bom-ref': `r${i}`, name: `c${i}`, version: '1' })),
      dependencies: Array.from({ length: n }, (_, i) => ({ ref: `r${i}`, dependsOn: [`r${(i + 1) % n}`, `r${(i + 7) % n}`] })),
    }
    const start = performance.now()
    const packages = packagesFromCycloneDx(doc)
    const nodes = graphNodes(packages, false)
    const links = graphLinks(packages, nodes)
    adjacency(links)
    expect(links).toHaveLength(2 * n)
    expect(performance.now() - start).toBeLessThan(2000)
  })
})

describe('reportsOfVersion ordering', () => {
  it('sorts reports without a timestamp after dated ones, whatever the input order', () => {
    const dated = { id: 'd', artifact_id: 'f', format: 'cyclonedx', generated_at: '2025-01-01T00:00:00Z' }
    const created = { id: 'c', artifact_id: 'f', format: 'spdx', created_at: '2025-03-01T00:00:00Z' }
    const undated = { id: 'u', artifact_id: 'f', format: 'spdx' }
    const forward = reportsOfVersion([undated, dated, created] as SbomItem[], ['f']).map((r) => r.id)
    const backward = reportsOfVersion([created, dated, undated] as SbomItem[], ['f']).map((r) => r.id)
    expect(forward).toEqual(['c', 'd', 'u'])
    expect(backward).toEqual(['c', 'd', 'u'])
  })

  it('returns nothing when no file of the version has a report', () => {
    expect(reportsOfVersion([{ id: '1', artifact_id: 'x', format: 'spdx' }] as SbomItem[], [])).toEqual([])
  })
})
