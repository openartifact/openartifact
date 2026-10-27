// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, beforeEach, type Mock } from 'bun:test'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { defineComponent, h } from 'vue'
import ArtifactSbomTab from '../artifact/ArtifactSbomTab.vue'
import { useArtifact } from '../../composables/useArtifact'
import { SBOM_FORMATS } from '../../api/sbom'
import { useToastStore } from '../../stores/toast'
import { t, tp } from '../../i18n'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const PACKAGES = [
  { id: 'f-amd64', name: 'widget', version: '2.0.0', format: 'deb', filename: 'widget_2.0.0_amd64.deb', created_at: '2026-02-01T00:00:00Z' },
  { id: 'f-arm64', name: 'widget', version: '2.0.0', format: 'deb', filename: 'widget_2.0.0_arm64.deb', created_at: '2026-02-01T00:00:00Z' },
  { id: 'f-old', name: 'widget', version: '1.0.0', format: 'deb', filename: 'widget_1.0.0_amd64.deb', created_at: '2026-01-01T00:00:00Z' },
]

const DOC = {
  components: [
    { 'bom-ref': 'app', name: 'app', version: '1.0.0' },
    { 'bom-ref': 'lib', name: 'lib', version: '2.0.0' },
    { 'bom-ref': 'leaf', name: 'leaf', version: '3.0.0' },
  ],
  dependencies: [
    { ref: 'app', dependsOn: ['lib', 'leaf', 'ghost'] },
    { ref: 'lib', dependsOn: ['app'] },
  ],
  vulnerabilities: [{ affects: [{ ref: 'lib' }] }],
}

interface Report {
  id: string
  artifact_id: string
  format: string
  spec_version?: string
  component_count?: number
  generated_at?: string
}

let reports: Report[]
let docs: Record<string, () => Response | Promise<Response>>
let fail: Record<string, Response>
let fetchSpy: Mock<typeof fetch>
let clicked: { href: string; download: string }[]

const calls = (): string[] => fetchSpy.mock.calls.map((c) => `${(c[1] as RequestInit | undefined)?.method ?? 'GET'} ${String(c[0])}`)
const bodies = (needle: string): unknown[] =>
  fetchSpy.mock.calls.filter((c) => String(c[0]).includes(needle)).map((c) => JSON.parse(String((c[1] as RequestInit).body)))

beforeEach(() => {
  reports = []
  docs = {}
  fail = {}
  clicked = []
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    const key = `${method} ${url}`
    const failure = Object.entries(fail).find(([k]) => key.startsWith(k))
    if (failure) return failure[1].clone()
    if (url === '/api/v1/repository/deb-repo') return json({ name: 'deb-repo', format: 'deb', repo_type: 'hosted', permissions: { admin: true } })
    if (url.startsWith('/api/v1/repositories/deb-repo/packages?')) return json({ packages: PACKAGES, total: PACKAGES.length })
    if (url.includes('/security/summary') || url.includes('/packages/f-')) return json({})
    if (method === 'GET' && url === '/api/v1/repositories/deb-repo/sbom') return json({ reports })
    if (method === 'POST' && url.endsWith('/sbom/generate')) {
      const body = JSON.parse(String(init?.body)) as { artifact_id: string; format: string }
      reports = [...reports, { id: `r-${reports.length + 1}`, artifact_id: body.artifact_id, format: body.format, generated_at: '2026-03-01T00:00:00Z' }]
      return new Response(null, { status: 204 })
    }
    if (method === 'DELETE') {
      const id = decodeURIComponent(url.split('/').pop()!)
      reports = reports.filter((r) => r.id !== id)
      return new Response(null, { status: 204 })
    }
    const doc = url.match(/\/sbom\/([^/]+)\/download$/)
    if (doc) {
      const id = decodeURIComponent(doc[1]!)
      return docs[id] ? docs[id]!() : json(DOC)
    }
    return json({}, 404)
  })
  spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
    clicked.push({ href: this.getAttribute('href') ?? '', download: this.download })
  })
})
afterEach(() => fetchSpy?.mockRestore())

async function mountTab(): Promise<VueWrapper> {
  setActivePinia(createPinia())
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { template: '<div/>' } }] })
  await router.push('/')
  await router.isReady()
  const Host = defineComponent({
    setup() {
      useArtifact({ repo: () => 'deb-repo', pkg: () => 'widget', version: () => '2.0.0' })
      return () => h(ArtifactSbomTab)
    },
  })
  const wrapper = mount(Host, { global: { plugins: [router] }, attachTo: document.body })
  await flushPromises()
  return wrapper
}

const rows = (w: VueWrapper) => w.findAll('tbody tr')
const dlLabel = (file: string, format: string): string => `Download: ${file} (${format})`
const delLabel = (file: string, format: string): string => `Delete: ${file} (${format})`
const stats = (w: VueWrapper): string => w.get('[data-testid=graph-stats]').text()
const lastToast = () => useToastStore().queue.at(-1)

describe('ArtifactSbomTab', () => {
  it('offers every supported format and shows the empty state while no SBOM exists', async () => {
    const w = await mountTab()
    const select = w.get('select')
    expect(select.findAll('option').map((o) => o.element.value)).toEqual([...SBOM_FORMATS])
    expect((select.element as HTMLSelectElement).value).toBe('cyclonedx')
    expect(w.text()).toContain(t('artifact.sbom.empty'))
    expect(rows(w)).toHaveLength(0)
    expect(w.text()).not.toContain(t('artifact.graph.title'))
  })

  it('lists only the reports of the shown version, newest first, with their format and counts', async () => {
    reports = [
      { id: 'r-old', artifact_id: 'f-amd64', format: 'spdx', spec_version: '2.3', component_count: 12, generated_at: '2026-01-01T00:00:00Z' },
      { id: 'r-other', artifact_id: 'f-old', format: 'cyclonedx', generated_at: '2026-04-01T00:00:00Z' },
      { id: 'r-new', artifact_id: 'f-arm64', format: 'spdx-json', generated_at: '2026-02-01T00:00:00Z' },
    ]
    const w = await mountTab()
    const r = rows(w)
    expect(r).toHaveLength(2)
    expect(r[0]!.text()).toContain('widget_2.0.0_arm64.deb')
    expect(r[0]!.text()).toContain('spdx-json')
    expect(r[0]!.text()).toContain('—')
    expect(r[1]!.text()).toContain('widget_2.0.0_amd64.deb')
    expect(r[1]!.text()).toContain('spdx 2.3')
    expect(r[1]!.text()).toContain('12')
    // Neither report is CycloneDX: no graph button, no graph section and no document is fetched.
    expect(w.findAll('button').some((b) => b.text() === t('artifact.sbom.graph'))).toBe(false)
    expect(calls().some((c) => c.endsWith('/download'))).toBe(false)
  })

  it('generates the chosen format for every file of the version and lists the result', async () => {
    const w = await mountTab()
    await w.get('select').setValue('spdx-json')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(bodies('/sbom/generate')).toEqual([
      { artifact_id: 'f-amd64', format: 'spdx-json' },
      { artifact_id: 'f-arm64', format: 'spdx-json' },
    ])
    expect(rows(w)).toHaveLength(2)
    expect(lastToast()).toMatchObject({ level: 'success', title: tp('artifact.sbom.generated', { format: 'spdx-json' }) })
  })

  it('reports a failed generation with the server message and keeps the list as it was', async () => {
    fail['POST /api/v1/repositories/deb-repo/sbom/generate'] = json({ error: 'scanner offline' }, 500)
    const w = await mountTab()
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(lastToast()).toMatchObject({ level: 'error', title: t('artifact.sbom.generateFailed'), description: 'scanner offline' })
    expect(rows(w)).toHaveLength(0)
    // The button is usable again after the failure.
    expect(w.get('form button').attributes('disabled')).toBeUndefined()
  })

  it('shows a retryable error instead of the table when the reports cannot load', async () => {
    fail['GET /api/v1/repositories/deb-repo/sbom'] = json({ error: 'db down' }, 500)
    const w = await mountTab()
    const alert = w.get('[role=alert]')
    expect(alert.text()).toContain(t('artifact.sbom.loadFailed'))
    expect(alert.text()).toContain('db down')
    expect(w.find('table').exists()).toBe(false)

    delete fail['GET /api/v1/repositories/deb-repo/sbom']
    reports = [{ id: 'r1', artifact_id: 'f-amd64', format: 'spdx', generated_at: '2026-02-01T00:00:00Z' }]
    await alert.get('button').trigger('click')
    await flushPromises()
    expect(w.find('[role=alert]').exists()).toBe(false)
    expect(rows(w)).toHaveLength(1)
  })

  it('draws the newest CycloneDX report: counts, vulnerable filter, selection with degrees', async () => {
    reports = [
      { id: 'cdx-old', artifact_id: 'f-amd64', format: 'cyclonedx', generated_at: '2026-01-01T00:00:00Z' },
      { id: 'cdx-new', artifact_id: 'f-arm64', format: 'cyclonedx', generated_at: '2026-02-01T00:00:00Z' },
    ]
    docs['cdx-old'] = () => json({ components: [{ 'bom-ref': 'x', name: 'stale' }] })
    const w = await mountTab()
    expect(calls().filter((c) => c.endsWith('/download'))).toEqual(['GET /api/v1/repositories/deb-repo/sbom/cdx-new/download'])
    expect(stats(w)).toBe(tp('artifact.graph.stats', { nodes: '3', edges: '3', vulnerable: '1' }))
    expect(w.findAll('svg[role=img] circle')).toHaveLength(3)
    expect(w.findAll('svg[role=img] line')).toHaveLength(3)
    // The component table lists every package with its dependency count (unknown refs dropped).
    const comp = rows(w).filter((r) => /^(app|lib|leaf)/.test(r.text()))
    expect(comp.map((r) => r.findAll('td').at(-1)!.text())).toEqual(['2', '1', '0'])
    expect(comp[1]!.text()).toContain(t('artifact.graph.vulnerable'))

    await w.findAll('svg[role=img] g')[0]!.trigger('click')
    expect(w.get('[data-testid=graph-selected]').text()).toContain('app@1.0.0')
    expect(w.get('[data-testid=graph-selected]').text()).toContain(tp('artifact.graph.degree', { out: '2', in: '1' }))
    await w.findAll('svg[role=img] g')[0]!.trigger('click')
    expect(w.find('[data-testid=graph-selected]').exists()).toBe(false)

    await w.get('[role=switch]').trigger('click')
    expect(stats(w)).toBe(tp('artifact.graph.stats', { nodes: '1', edges: '0', vulnerable: '1' }))
    expect(w.findAll('svg[role=img] circle')).toHaveLength(1)
  })

  it('says so when the document has no components, and when no component is vulnerable', async () => {
    reports = [{ id: 'cdx', artifact_id: 'f-amd64', format: 'cyclonedx', generated_at: '2026-01-01T00:00:00Z' }]
    docs.cdx = () => json({})
    let w = await mountTab()
    expect(w.text()).toContain(t('artifact.graph.empty'))
    expect(w.text()).toContain(t('artifact.graph.emptyHint'))
    expect(w.find('svg[role=img]').exists()).toBe(false)
    expect(w.findAll('button').find((b) => b.text().includes(t('artifact.graph.export')))!.attributes('disabled')).toBeDefined()
    w.unmount()

    docs.cdx = () => json({ components: [{ 'bom-ref': 'a', name: 'a', version: '1' }] })
    w = await mountTab()
    await w.get('[role=switch]').trigger('click')
    expect(w.text()).toContain(t('artifact.graph.noVulnerable'))
    expect(stats(w)).toBe(tp('artifact.graph.stats', { nodes: '0', edges: '0', vulnerable: '0' }))
  })

  it('shows an error in place of the graph when the document cannot be read', async () => {
    reports = [{ id: 'cdx', artifact_id: 'f-amd64', format: 'cyclonedx', generated_at: '2026-01-01T00:00:00Z' }]
    docs.cdx = () => json({ error: 'document missing' }, 404)
    const w = await mountTab()
    const alert = w.findAll('[role=alert]').find((a) => a.text().includes(t('artifact.graph.loadFailed')))
    expect(alert?.text()).toContain('document missing')
    expect(rows(w)).toHaveLength(1)
  })

  it('shows a loading note while the document is in flight and ignores a superseded response', async () => {
    reports = [
      { id: 'a', artifact_id: 'f-amd64', format: 'cyclonedx', generated_at: '2026-02-01T00:00:00Z' },
      { id: 'b', artifact_id: 'f-arm64', format: 'cyclonedx', generated_at: '2026-01-01T00:00:00Z' },
    ]
    let releaseA!: () => void
    docs.a = () => new Promise((resolve) => (releaseA = () => resolve(json({ components: [{ 'bom-ref': 'x', name: 'from-a' }] }))))
    docs.b = () => json({ components: [{ 'bom-ref': 'y', name: 'from-b' }, { 'bom-ref': 'z', name: 'from-b2' }] })
    const w = await mountTab()
    expect(w.text()).toContain(t('common.loading'))
    expect(w.find('svg[role=img]').exists()).toBe(false)

    const graphButtons = w.findAll('[data-testid="action-graph"]')
    await graphButtons[1]!.trigger('click')
    await flushPromises()
    expect(stats(w)).toContain('2 ')
    releaseA()
    await flushPromises()
    expect(w.text()).toContain('from-b2')
    expect(w.text()).not.toContain('from-a')
    expect(graphButtons[1]!.attributes('aria-pressed')).toBe('true')
    expect(graphButtons[0]!.attributes('aria-pressed')).toBe('false')
  })

  it('deletes a report, clearing its graph and falling back to another CycloneDX report', async () => {
    reports = [
      { id: 'a', artifact_id: 'f-amd64', format: 'cyclonedx', generated_at: '2026-02-01T00:00:00Z' },
      { id: 'b', artifact_id: 'f-arm64', format: 'cyclonedx', generated_at: '2026-01-01T00:00:00Z' },
    ]
    docs.a = () => json({ components: [{ 'bom-ref': 'x', name: 'from-a' }] })
    docs.b = () => json({ components: [{ 'bom-ref': 'y', name: 'from-b' }] })
    const w = await mountTab()
    expect(w.text()).toContain('from-a')
    await w.get(`[aria-label="${delLabel('widget_2.0.0_amd64.deb', 'cyclonedx')}"]`).trigger('click')
    await flushPromises()
    expect(calls()).toContain('DELETE /api/v1/repositories/deb-repo/sbom/a')
    expect(lastToast()).toMatchObject({ level: 'success', title: t('artifact.sbom.deleted') })
    expect(rows(w).filter((r) => r.text().includes('widget_'))).toHaveLength(1)
    expect(w.text()).toContain('from-b')
    expect(w.text()).not.toContain('from-a')

    await w.get(`[aria-label="${delLabel('widget_2.0.0_arm64.deb', 'cyclonedx')}"]`).trigger('click')
    await flushPromises()
    expect(w.text()).not.toContain(t('artifact.graph.title'))
    expect(w.text()).toContain(t('artifact.sbom.empty'))
  })

  it('keeps the report and tells the user when a deletion is refused', async () => {
    reports = [{ id: 'a', artifact_id: 'f-amd64', format: 'spdx', generated_at: '2026-02-01T00:00:00Z' }]
    fail['DELETE '] = json({ error: 'forbidden here' }, 403)
    const w = await mountTab()
    await w.get(`[aria-label="${delLabel('widget_2.0.0_amd64.deb', 'spdx')}"]`).trigger('click')
    await flushPromises()
    expect(lastToast()).toMatchObject({ level: 'error', title: t('artifact.sbom.deleteFailed'), description: 'forbidden here' })
    expect(rows(w)).toHaveLength(1)
  })

  it('downloads a stored document through the browser and reports a refusal', async () => {
    reports = [{ id: 'a b', artifact_id: 'f-amd64', format: 'spdx-json', generated_at: '2026-02-01T00:00:00Z' }]
    const w = await mountTab()
    const button = w.get(`[aria-label="${dlLabel('widget_2.0.0_amd64.deb', 'spdx-json')}"]`)
    await button.trigger('click')
    await flushPromises()
    expect(clicked).toEqual([{ href: '/api/v1/repositories/deb-repo/sbom/a%20b/download', download: 'sbom-spdx-json.json' }])

    fail['GET /api/v1/repositories/deb-repo/sbom/a%20b/download'] = json({ error: 'quarantined' }, 403)
    await button.trigger('click')
    await flushPromises()
    expect(clicked).toHaveLength(1)
    expect(lastToast()).toMatchObject({ level: 'error', title: t('artifact.sbom.downloadFailed'), description: 'quarantined' })
  })

  it('exports the drawing as a standalone SVG named after the package and version', async () => {
    reports = [{ id: 'a', artifact_id: 'f-amd64', format: 'cyclonedx', generated_at: '2026-02-01T00:00:00Z' }]
    document.documentElement.style.setProperty('--color-primary', '1 2 3')
    let blob: Blob | undefined
    spyOn(URL, 'createObjectURL').mockImplementation((b) => {
      blob = b as Blob
      return 'blob:svg'
    })
    spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    const w = await mountTab()
    await w.findAll('button').find((b) => b.text().includes(t('artifact.graph.export')))!.trigger('click')
    expect(clicked).toEqual([{ href: 'blob:svg', download: 'widget-2.0.0-dependency-graph.svg' }])
    const svg = await blob!.text()
    expect(svg).toContain('<svg')
    // The page's CSS variables are resolved so the file renders on its own.
    expect(svg).not.toContain('var(--color')
    expect(svg).toContain('rgb(1,2,3)')
  })
})
