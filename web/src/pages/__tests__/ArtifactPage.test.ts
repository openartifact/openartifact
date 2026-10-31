// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, afterEach, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import ArtifactPage from '../ArtifactPage.vue'
import { useSessionStore } from '../../stores/session'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const SHA = 'b'.repeat(64)
const PACKAGES = [
  { id: 'f-new-amd64', name: '@acme/widget', version: '2.0.0', format: 'npm', size: 2048, created_at: '2026-02-01T00:00:00Z', security: { critical: 2, high: 0, medium: 0, low: 0 }, scanned_at: '2026-02-02T00:00:00Z' },
  { id: 'f-new-arm64', name: '@acme/widget', version: '2.0.0', format: 'npm', size: 1024, created_at: '2026-02-01T00:00:00Z' },
  { id: 'f-old', name: '@acme/widget', version: '1.0.0', format: 'npm', size: 512, created_at: '2026-01-01T00:00:00Z' },
]
const SUMMARY = {
  scanned_at: '2026-02-02T00:00:00Z',
  vuln: { critical: 2, high: 0, medium: 0, low: 0 },
  license: { violations: 0 },
  secret: { count: 0 },
  ai: { quarantine_recommended: 0, review_required: 0 },
  quarantined: false,
}

let fetchSpy: Mock<typeof fetch>
const sent = (): string[] => fetchSpy.mock.calls.map((c) => `${(c[1] as RequestInit | undefined)?.method ?? 'GET'} ${decodeURIComponent(String(c[0]))}`)

function serve(permissions: Record<string, boolean>, packages: unknown[] = PACKAGES, summary: unknown = SUMMARY): void {
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    if (method === 'DELETE') return new Response(null, { status: 204 })
    if (url === '/api/v1/repository/web') return json({ name: 'web', format: 'npm', repo_type: 'hosted', permissions })
    if (url.startsWith('/api/v1/repositories/web/packages?')) return json({ packages, total: packages.length })
    if (url.includes('/artifacts/f-new-amd64/properties')) return json({ channel: 'stable' })
    if (url.includes('/packages/f-')) {
      return json({ id: 'f-new-amd64', filename: 'widget-2.0.0.tgz', sha256: SHA, license: 'MIT', summary: 'A widget', files: [{ id: 'f-new-amd64', filename: 'widget-2.0.0.tgz', size: 2048, sha256: SHA }], metadata: [{ key: 'engines', value: 'node>=20' }], dependencies: [{ name: 'left-pad', version: '^1.0.0', kind: 'runtime' }], version_count: 2, latest_version: '2.0.0' })
    }
    if (url.startsWith('/api/v1/repositories/web/security/summary')) return json(summary)
    if (url.includes('/vuln/scans')) return json({ scans: [] })
    if (url.includes('/activity')) return json({ data: [], has_more: false })
    return json({}, 404)
  })
}

async function mountAt(path: string, admin = false) {
  setActivePinia(createPinia())
  useSessionStore().user = { id: 'u1', username: 'jo', is_admin: admin } as never
  const stub = { template: '<div/>' }
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/repos/:repo/packages/:pkg(.+)/versions/:v(.+)', component: ArtifactPage },
      { path: '/repos/:repo/packages/:pkg(.+)', component: ArtifactPage },
      { path: '/:rest(.*)*', component: stub },
    ],
  })
  await router.push(path)
  await router.isReady()
  const wrapper = mount(ArtifactPage, { global: { plugins: [router] }, attachTo: document.body })
  await flushPromises()
  return { wrapper, router }
}

const tabNames = (w: { findAll: (s: string) => Array<{ text: () => string }> }): string[] => w.findAll('[role=tab]').map((t) => t.text())

describe('ArtifactPage', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })
  afterEach(() => fetchSpy?.mockRestore())

  it('shows the latest version of a scoped package with its install command and facts', async () => {
    serve({ write: false, delete: false, manage: false, admin: false })
    const { wrapper } = await mountAt('/repos/web/packages/@acme/widget')
    expect(wrapper.get('h1').text()).toBe('@acme/widget')
    expect((wrapper.get('[data-testid=version-select]').element as HTMLSelectElement).value).toBe('2.0.0')
    expect(wrapper.get('[data-testid=install-command]').text()).toContain('@acme/widget')
    expect(wrapper.get('[data-testid=artifact-aside]').text()).toContain('MIT')
    expect(wrapper.text()).toContain('A widget')
    expect(wrapper.get('[data-testid=install-critical]').text()).toContain('2 critical')
    // Both files of the version count; the other version is only in the select.
    expect(wrapper.text()).toContain('3.0 KB')
    wrapper.unmount()
  })

  it('never shows the cached upstream metadata row of a proxy package as a version', async () => {
    const meta = { id: 'f-meta', name: '@acme/widget', version: '', format: 'npm', size: 9, created_at: '2026-03-01T00:00:00Z' }
    serve({ write: false, delete: false, manage: false, admin: false }, [meta, ...PACKAGES])
    const { wrapper } = await mountAt('/repos/web/packages/@acme/widget')
    const select = wrapper.get('[data-testid=version-select]').element as HTMLSelectElement
    expect(select.value).toBe('2.0.0')
    expect([...select.options].map((o) => o.value)).toEqual(['2.0.0', '1.0.0'])
    wrapper.unmount()
  })

  it('explains a proxy package that is known only from cached metadata', async () => {
    const meta = { id: 'f-meta', name: '@acme/widget', version: '', format: 'npm', size: 9, created_at: '2026-03-01T00:00:00Z' }
    serve({ write: false, delete: false, manage: false, admin: false }, [meta])
    const { wrapper } = await mountAt('/repos/web/packages/@acme/widget')
    expect(wrapper.get('[data-testid=metadata-only]').text()).toContain('Proxied package')
    expect(wrapper.find('[data-testid=version-select]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows a proxy package with no row at all as proxied when the gate holds versions back', async () => {
    fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = String(input)
      if (url === '/api/v1/repository/web') return json({ name: 'web', format: 'pypi', repo_type: 'proxy', permissions: {} })
      if (url.startsWith('/api/v1/repositories/web/packages?')) return json({ packages: [], total: 0 })
      if (url.startsWith('/api/v1/repositories/web/held-back-versions')) {
        return json({ supported: true, versions: [{ version: '2.0.0', reasons: [{ source: 'min_age', detail: 'too new', until: '2099-01-01T00:00:00Z' }] }] })
      }
      return json({}, 404)
    })
    const { wrapper } = await mountAt('/repos/web/packages/widget')
    expect(wrapper.get('[data-testid=metadata-only]').text()).toContain('Proxied package')
    expect(wrapper.get('[data-testid=held-back-list]').text()).toContain('2.0.0')
    expect(wrapper.text()).not.toContain('Package not found')
    wrapper.unmount()
  })

  it('hides the activity tab from readers and shows only counts on the security tab', async () => {
    serve({ write: false, delete: false, manage: false, admin: false })
    const { wrapper } = await mountAt('/repos/web/packages/@acme/widget?tab=security')
    expect(tabNames(wrapper).some((n) => n.startsWith('Activity'))).toBe(false)
    expect(wrapper.get('[data-testid=sec-restricted]').text()).toContain('administrators')
    expect(sent().some((c) => c.includes('/vuln/scans'))).toBe(false)
    wrapper.unmount()
  })

  it('lets a project maintainer read the activity but requests no scan results or SBOMs', async () => {
    serve({ write: true, delete: true, manage: false, admin: true })
    const { wrapper } = await mountAt('/repos/web/packages/@acme/widget?tab=security')
    expect(tabNames(wrapper).some((n) => n.startsWith('Activity'))).toBe(true)
    expect(tabNames(wrapper).some((n) => n.startsWith('SBOM'))).toBe(false)
    expect(wrapper.get('[data-testid=sec-inbox-link]').attributes('href')).toContain('/security?repo=web')
    expect(sent().some((c) => c.includes('/vuln/scans') || c.includes('/sbom'))).toBe(false)
    wrapper.unmount()
  })

  it('offers activity and loads findings for an admin', async () => {
    serve({ write: true, delete: true, manage: true, admin: true })
    const { wrapper } = await mountAt('/repos/web/packages/@acme/widget?tab=security', true)
    expect(tabNames(wrapper).some((n) => n.startsWith('Activity'))).toBe(true)
    expect(sent().some((c) => c.startsWith('GET /api/v1/repositories/web/vuln/scans'))).toBe(true)
    expect(wrapper.find('[data-testid=sec-no-scans]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('shows no finding count for a version nobody scanned', async () => {
    serve({ write: false, delete: false, manage: false, admin: false }, PACKAGES, { ...SUMMARY, scanned_at: null, vuln: { critical: 0, high: 0, medium: 0, low: 0 } })
    const { wrapper } = await mountAt('/repos/web/packages/@acme/widget')
    const glance = wrapper.get('[aria-label="At a glance"]').text()
    expect(glance).toContain('–')
    expect(glance).not.toContain('0Open findings')
    wrapper.unmount()
  })

  it('opens a requested older version and switches versions from the header', async () => {
    serve({ write: false, delete: false, manage: false, admin: false })
    const { wrapper, router } = await mountAt('/repos/web/packages/@acme/widget/versions/1.0.0')
    expect((wrapper.get('[data-testid=version-select]').element as HTMLSelectElement).value).toBe('1.0.0')
    const select = wrapper.get('[data-testid=version-select]')
    ;(select.element as HTMLSelectElement).value = '2.0.0'
    await select.trigger('change')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/repos/web/packages/@acme/widget')
    wrapper.unmount()
  })

  it('says so when the package or the version does not exist', async () => {
    serve({ write: false, delete: false, manage: false, admin: false })
    const missingVersion = await mountAt('/repos/web/packages/@acme/widget/versions/9.9.9')
    expect(missingVersion.wrapper.text()).toContain('Version not found')
    missingVersion.wrapper.unmount()
    fetchSpy.mockRestore()
    serve({ write: false, delete: false, manage: false, admin: false }, [])
    const missingPackage = await mountAt('/repos/web/packages/@acme/widget')
    expect(missingPackage.wrapper.text()).toContain('Package not found')
    missingPackage.wrapper.unmount()
  })

  it('deletes every file of the version after confirmation', async () => {
    serve({ write: true, delete: true, manage: true, admin: true })
    const { wrapper } = await mountAt('/repos/web/packages/@acme/widget', true)
    await wrapper.get('[data-testid=artifact-menu]').trigger('click')
    await flushPromises()
    document.body.querySelector<HTMLElement>('[data-testid=action-delete]')!.click()
    await flushPromises()
    expect(sent().some((c) => c.startsWith('DELETE'))).toBe(false)
    const confirm = [...document.body.querySelectorAll('button')].filter((b) => b.textContent?.trim() === 'Delete version').pop()!
    confirm.click()
    await flushPromises()
    const deletes = sent().filter((c) => c.startsWith('DELETE'))
    expect(deletes.sort()).toEqual(['DELETE /api/v1/repositories/web/packages/f-new-amd64', 'DELETE /api/v1/repositories/web/packages/f-new-arm64'])
    wrapper.unmount()
  })

  it('lets a writer edit properties in a drawer and saves only the change', async () => {
    serve({ write: true, delete: false, manage: false, admin: false })
    const { wrapper } = await mountAt('/repos/web/packages/@acme/widget')
    expect(wrapper.text()).toContain('stable')
    await wrapper.get('[data-testid=props-edit]').trigger('click')
    await flushPromises()
    const value = document.body.querySelector('input[aria-label="Property 1 value"]') as HTMLInputElement
    value.value = 'beta'
    value.dispatchEvent(new Event('input'))
    const save = [...document.body.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Save properties')!
    save.click()
    await flushPromises()
    const patch = fetchSpy.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === 'PATCH')
    expect(String(patch![0])).toBe('/api/v1/repositories/web/artifacts/f-new-amd64/properties')
    expect(JSON.parse(String((patch![1] as RequestInit).body))).toEqual({ properties: { channel: 'beta' } })
    expect(wrapper.text()).toContain('beta')
    wrapper.unmount()
  })
})
