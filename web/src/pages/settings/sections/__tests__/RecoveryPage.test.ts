// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, it, expect, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import RecoveryPage from '../RecoveryPage.vue'
import SettingsSectionPage from '../../SettingsSectionPage.vue'
import { useSessionStore, type User } from '../../../../stores/session'

const HEALTHY = {
  enabled: true,
  backlog: 0,
  backlog_cap: 1000,
  retrying_rows: 0,
  sidecars_tracked: 1234,
  tables_reconciled: 7,
  tables_total: 9,
  schema_version: 42,
}

let spy: Mock<typeof fetch> | undefined
let paths: string[] = []

function serve(body: unknown, status = 200): void {
  paths = []
  spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL) => {
    paths.push(new URL(String(input), 'http://localhost').pathname)
    return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
  }) as never)
}

async function mountPage() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push('/settings/recovery')
  const w = mount(RecoveryPage, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return w
}

const summary = (w: Awaited<ReturnType<typeof mountPage>>) => w.get('[data-testid="recovery-summary"]').text()

beforeEach(() => {
  setActivePinia(createPinia())
  document.body.innerHTML = ''
})
afterEach(() => spy?.mockRestore())

describe('RecoveryPage', () => {
  it('shows a loading state before the status arrives', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation((() => new Promise(() => {})) as never)
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
    await router.push('/settings/recovery')
    const w = mount(RecoveryPage, { global: { plugins: [router] } })
    expect(w.find('[role="status"]').text()).toContain('Loading')
    w.unmount()
  })

  it('reads the status of the admin endpoint and reports a healthy journal', async () => {
    serve(HEALTHY)
    const w = await mountPage()
    expect(paths).toContain('/api/v1/admin/recovery/status')
    expect(summary(w)).toContain('Healthy')
    expect(w.get('[data-testid="recovery-tiles"]').text()).toContain('7/9')
    expect(w.find('[data-testid="recovery-last-error"]').exists()).toBe(false)
    expect(w.get('[data-testid="recovery-details"]').text()).toContain('42')
    w.unmount()
  })

  it('reports a backlog as catching up', async () => {
    serve({ ...HEALTHY, backlog: 12, oldest_dirty_age_seconds: 90 })
    const w = await mountPage()
    expect(summary(w)).toContain('Catching up')
    expect(w.get('[data-testid="recovery-tiles"]').text()).toContain('12')
    w.unmount()
  })

  it('shows a backlog at its cap as a lower bound', async () => {
    serve({ ...HEALTHY, backlog: 1000, backlog_cap: 1000 })
    const w = await mountPage()
    expect(w.get('[data-testid="recovery-tiles"]').text()).toMatch(/≥\s*1,?000/)
    w.unmount()
  })

  it('reports retrying rows as failing and shows the last error', async () => {
    serve({ ...HEALTHY, backlog: 3, retrying_rows: 2, last_error: 'put sidecar: access denied', last_error_at: '2026-01-01T00:00:00Z' })
    const w = await mountPage()
    expect(summary(w)).toContain('Failing')
    const err = w.get('[data-testid="recovery-last-error"]')
    expect(err.text()).toContain('Last error')
    expect(err.text()).toContain('put sidecar: access denied')
    w.unmount()
  })

  it('explains a switched-off journal and still offers the rebuild commands', async () => {
    serve({ enabled: false })
    const w = await mountPage()
    expect(summary(w)).toContain('Off')
    expect(w.get('[data-testid="recovery-off"]').text()).toContain('The recovery journal is off')
    expect(w.find('[data-testid="recovery-tiles"]').exists()).toBe(false)
    expect(w.get('[data-testid="recovery-rebuild"]').text()).toContain('openartifact rebuild --dry-run')
    w.unmount()
  })

  it('shows the error and a retry when the status cannot be read', async () => {
    serve({ error: 'boom' }, 500)
    const w = await mountPage()
    const block = w.get('[data-testid="recovery-error"]')
    expect(block.text()).toContain('Could not read the recovery journal')
    expect(block.text()).toContain('Retry')
    w.unmount()
  })

  it('is refused for a non-administrator', async () => {
    serve(HEALTHY)
    setActivePinia(createPinia())
    useSessionStore().user = { id: 'u', username: 'dev', is_admin: false } as User
    const r = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/settings', component: { render: () => null } },
        { path: '/settings/:section', name: 'settings-section', component: SettingsSectionPage },
      ],
    })
    await r.push('/settings/recovery')
    const w = mount(SettingsSectionPage, { attachTo: document.body, global: { plugins: [r] } })
    await flushPromises()
    expect(w.find('[data-testid="section-forbidden"]').exists()).toBe(true)
    expect(paths).not.toContain('/api/v1/admin/recovery/status')
    w.unmount()
  })
})
