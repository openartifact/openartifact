// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import type { SbomItem } from '../../api/sbom'
import { adjacency, circleLayout, dependencyRows, graphLinks, graphNodes, packagesFromCycloneDx, reportsOfVersion } from '../sbomGraph'

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
