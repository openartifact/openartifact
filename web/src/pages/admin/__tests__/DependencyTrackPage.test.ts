// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, afterEach, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import DependencyTrackPage from '../DependencyTrackPage.vue'

interface Call {
  method: string
  path: string
  body: Record<string, unknown> | null
}

const SETTINGS = {
  enabled: true,
  base_url: 'https://dt.example.com',
  api_key_set: true,
  project_name: '{repo}/{name}',
  project_version: '{version}',
  auto_push: true,
  push_vex: false,
}

const EFFECTIVE = {
  enabled: true,
  ready: true,
  source: 'global',
  base_url: 'https://dt.example.com',
  api_key_set: true,
  project_name: '{repo}/{name}',
  project_version: '{version}',
  auto_push: true,
  push_vex: false,
}

const PUSH = {
  artifact_id: 'lib@1.0.0',
  state: 'done',
  detail: 'ok',
  project_uuid: 'u1',
  project_name: 'main/lib',
  project_version: '1.0.0',
  components: 12,
  vex_statements: 4,
  vex_applied: 3,
  bom_pushed_at: '2026-01-01T00:00:00Z',
  vex_pushed_at: null,
  updated_at: '2026-01-01T00:00:00Z',
}

let calls: Call[] = []
let pushes: unknown[] = [PUSH]
let fetchSpy: Mock<typeof fetch>

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function mockApi(): void {
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost')
    const method = init?.method ?? 'GET'
    const body = init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : null
    calls.push({ method, path: url.pathname, body })
    const p = url.pathname
    if (p === '/api/v1/admin/dependency-track' && method === 'GET') return json(SETTINGS)
    if (p === '/api/v1/admin/dependency-track' && method === 'PUT') return json({ ...SETTINGS, ...body, api_key_set: body?.api_key !== '' })
    if (p === '/api/v1/admin/dependency-track/test') {
      return json({ version: '4.12.0', permissions: ['BOM_UPLOAD'], missing_permissions: ['VULNERABILITY_ANALYSIS'] })
    }
    if (p === '/api/v1/repositories') return json({ repositories: [{ name: 'main', format: 'npm' }], total: 1 })
    if (p === '/api/v1/repositories/main/dependency-track' && method === 'GET') return json({ override: null, effective: EFFECTIVE })
    if (p === '/api/v1/repositories/main/dependency-track/pushes') return json({ pushes })
    if (p === '/api/v1/repositories/main/dependency-track/push') return json({ ...PUSH, state: 'queued' }, 202)
    return json({ error: { code: 'not_found', message: 'nope' } }, 404)
  }) as never)
}

const mountPage = (): VueWrapper => {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  return mount(DependencyTrackPage, { global: { plugins: [router] }, attachTo: document.body })
}

const buttonByText = (w: VueWrapper, text: string) => {
  const b = w.findAll('button').find((x) => x.text().includes(text))
  if (!b) throw new Error(`no button "${text}"`)
  return b
}

describe('DependencyTrackPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.body.innerHTML = ''
    calls = []
    pushes = [PUSH]
    mockApi()
  })
  afterEach(() => {
    fetchSpy.mockRestore()
  })

  it('renders the stored settings', async () => {
    const w = mountPage()
    await flushPromises()
    expect((w.get('#dt-base-url').element as HTMLInputElement).value).toBe('https://dt.example.com')
    expect((w.get('#dt-project-name').element as HTMLInputElement).value).toBe('{repo}/{name}')
    expect(w.get('[data-testid="dt-key-state"]').text()).toBe('Key stored')
    expect((w.get('#dt-api-key').element as HTMLInputElement).value).toBe('')
    w.unmount()
  })

  it('saves without api_key when the key field is untouched', async () => {
    const w = mountPage()
    await flushPromises()
    await buttonByText(w, 'Save changes').trigger('click')
    await flushPromises()
    const put = calls.find((c) => c.method === 'PUT')
    expect(put?.body).toBeTruthy()
    expect('api_key' in (put?.body ?? {})).toBe(false)
    expect(put?.body?.base_url).toBe('https://dt.example.com')
    w.unmount()
  })

  it('sends the typed api_key', async () => {
    const w = mountPage()
    await flushPromises()
    await w.get('#dt-api-key').setValue('secret')
    await buttonByText(w, 'Save changes').trigger('click')
    await flushPromises()
    expect(calls.find((c) => c.method === 'PUT')?.body?.api_key).toBe('secret')
    w.unmount()
  })

  it('sends an empty api_key on remove key', async () => {
    const w = mountPage()
    await flushPromises()
    await buttonByText(w, 'Remove key').trigger('click')
    await flushPromises()
    const put = calls.find((c) => c.method === 'PUT')
    expect(put?.body?.api_key).toBe('')
    expect(w.get('[data-testid="dt-key-state"]').text()).toBe('No key stored')
    w.unmount()
  })

  it('shows the connection test result with missing permissions', async () => {
    const w = mountPage()
    await flushPromises()
    await buttonByText(w, 'Test connection').trigger('click')
    await flushPromises()
    const res = w.get('[data-testid="dt-test-result"]')
    expect(res.text()).toContain('4.12.0')
    expect(res.text()).toContain('BOM_UPLOAD')
    expect(w.get('[data-testid="dt-test-missing"]').text()).toContain('VULNERABILITY_ANALYSIS')
    w.unmount()
  })

  it('renders the pushes table for the selected repository', async () => {
    const w = mountPage()
    await flushPromises()
    await w.get('#dt-repo').setValue('main')
    await flushPromises()
    expect(w.get('[data-testid="dt-effective"]').text()).toContain('https://dt.example.com')
    const row = w.get('[data-testid="dt-pushes"] tbody tr').text()
    expect(row).toContain('lib@1.0.0')
    expect(row).toContain('Done')
    expect(row).toContain('main/lib 1.0.0')
    expect(row).toContain('12')
    expect(row).toContain('3 / 4')
    w.unmount()
  })

  it('posts the artifact_id on manual push and polls while queued', async () => {
    const w = mountPage()
    await flushPromises()
    await w.get('#dt-repo').setValue('main')
    await flushPromises()
    pushes = [{ ...PUSH, artifact_id: 'lib@2.0.0', state: 'queued' }]
    const setIntervalSpy = spyOn(globalThis, 'setInterval')
    await w.get('#dt-artifact-id').setValue('lib@2.0.0')
    await w.get('#dt-artifact-id').element.closest('form')!.dispatchEvent(new Event('submit'))
    await flushPromises()
    const post = calls.find((c) => c.method === 'POST' && c.path.endsWith('/dependency-track/push'))
    expect(post?.body).toEqual({ artifact_id: 'lib@2.0.0' })
    expect(w.get('[data-testid="dt-pushes"]').text()).toContain('Queued')
    expect(setIntervalSpy).toHaveBeenCalledTimes(1)
    w.unmount()
    setIntervalSpy.mockRestore()
  })

  it('saves a repository override with tri-state values', async () => {
    const w = mountPage()
    await flushPromises()
    await w.get('#dt-repo').setValue('main')
    await flushPromises()
    await w.get('#dt-ovr-auto').setValue('off')
    await w.get('#dt-ovr-url').setValue('https://repo-dt.example.com')
    await w.get('#dt-ovr-url').element.closest('form')!.dispatchEvent(new Event('submit'))
    await flushPromises()
    const put = calls.find((c) => c.method === 'PUT' && c.path === '/api/v1/repositories/main/dependency-track')
    expect(put?.body?.auto_push).toBe(false)
    expect(put?.body?.push_vex).toBeNull()
    expect(put?.body?.base_url).toBe('https://repo-dt.example.com')
    expect('api_key' in (put?.body ?? {})).toBe(false)
    w.unmount()
  })
})
