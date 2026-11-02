// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, it, expect, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import DeclarativePage from '../DeclarativePage.vue'
import SettingsSectionPage from '../../SettingsSectionPage.vue'
import { useSessionStore, type User } from '../../../../stores/session'

interface Call { method: string; path: string; query: URLSearchParams; body?: string }
type Handler = (c: Call) => Response
let spy: Mock<typeof fetch> | undefined
let calls: Call[] = []
let managed: unknown[] = []

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function serve(handlers: Record<string, Handler> = {}): void {
  calls = []
  spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost')
    const call = { method: init?.method ?? 'GET', path: url.pathname, query: url.searchParams, body: init?.body as string | undefined }
    calls.push(call)
    const all: Record<string, Handler> = { 'GET /api/v1/admin/declarative': () => json({ items: managed }), ...handlers }
    return (all[`${call.method} ${call.path}`] ?? (() => json({ error: 'not found' }, 404)))(call)
  }) as never)
}

const run = (over: Record<string, unknown> = {}) => ({
  summary: { create: 1 },
  changes: true,
  items: [{ action: 'create', kind: 'Repository', name: 'new-repo', changes: [{ path: 'format', new: 'npm' }] }],
  secrets: [],
  ...over,
})

type Wrapper = Awaited<ReturnType<typeof mountPage>>

async function mountPage() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  await router.push('/settings/declarative')
  const w = mount(DeclarativePage, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return w
}

async function typeManifest(w: Wrapper, text = 'kind: Repository\nname: new-repo\n'): Promise<void> {
  await w.get('[data-testid="manifest-text"]').setValue(text)
}

async function review(w: Wrapper): Promise<void> {
  await w.get('[data-testid="review"]').trigger('click')
  await flushPromises()
}

const dialogButton = (label: string): HTMLButtonElement | undefined =>
  Array.from(document.body.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')).find((b) => b.textContent?.trim() === label)

const applyButton = (w: Wrapper) => w.get('[data-testid="apply"]')
const posted = (path: string): Call | undefined => calls.find((c) => c.method === 'POST' && c.path === path)

beforeEach(() => {
  setActivePinia(createPinia())
  document.body.innerHTML = ''
  managed = []
})
afterEach(() => spy?.mockRestore())

describe('DeclarativePage', () => {
  it('starts empty: nothing to review, apply disabled, no plan', async () => {
    serve()
    const w = await mountPage()
    expect(w.get('[data-testid="review"]').attributes('disabled')).toBeDefined()
    expect(applyButton(w).attributes('disabled')).toBeDefined()
    expect(w.find('[data-testid="declarative-plan"]').exists()).toBe(false)
    expect(w.text()).toContain('Nothing is managed yet')
    w.unmount()
  })

  it('reviews the manifest through the diff endpoint and lists the changes', async () => {
    serve({ 'POST /api/v1/admin/declarative/diff': () => json(run({ summary: { create: 1, update: 1 }, items: [
      { action: 'create', kind: 'Repository', name: 'new-repo', changes: [{ path: 'format', new: 'npm' }] },
      { action: 'update', kind: 'Project', name: 'web', changes: [{ path: 'description', old: 'old text', new: 'new text' }] },
    ] })) })
    const w = await mountPage()
    await typeManifest(w)
    await review(w)
    const sent = JSON.parse(posted('/api/v1/admin/declarative/diff')!.body!)
    expect(sent.manifest).toBe('kind: Repository\nname: new-repo\n')
    const plan = w.get('[data-testid="declarative-plan"]').text()
    expect(plan).toContain('1 create')
    expect(plan).toContain('1 update')
    expect(plan).toContain('new-repo')
    expect(plan).toContain('old text')
    expect(plan).toContain('new text')
    expect(applyButton(w).attributes('disabled')).toBeUndefined()
    w.unmount()
  })

  it('lists deletions first and flags secret values without showing them', async () => {
    serve({ 'POST /api/v1/admin/declarative/diff': () => json(run({ summary: { create: 1, delete: 1 }, items: [
      { action: 'create', kind: 'Webhook', name: 'hook', changes: [{ path: 'secret', secret: true, new: 'hunter2' }] },
      { action: 'delete', kind: 'Repository', name: 'old-repo', managed: true },
    ] })) })
    const w = await mountPage()
    await typeManifest(w)
    await review(w)
    const items = w.findAll('[data-testid="plan-items"] > li')
    expect(items.map((i) => i.attributes('data-action'))).toEqual(['delete', 'create'])
    expect(items[0]!.text()).toContain('managed by apply')
    expect(items[1]!.text()).toContain('(secret, will be sent)')
    expect(items[1]!.text()).not.toContain('hunter2')
    w.unmount()
  })

  it('shows manifest problems with their line numbers on a 422 manifest_invalid', async () => {
    serve({ 'POST /api/v1/admin/declarative/diff': () => json({
      code: 'manifest_invalid', error: 'invalid manifest', summary: {}, changes: false, items: [], secrets: [],
      problems: [{ line: 7, object: 'Repository/x', field: 'format', message: 'unknown format' }, { message: 'empty document' }],
    }, 422) })
    const w = await mountPage()
    await typeManifest(w, 'bad')
    await review(w)
    const problems = w.get('[data-testid="plan-problems"]').text()
    expect(problems).toContain('line 7')
    expect(problems).toContain('Repository/x')
    expect(problems).toContain('format: unknown format')
    expect(problems).toContain('empty document')
    expect(applyButton(w).attributes('disabled')).toBeDefined()
    w.unmount()
  })

  it('shows plan errors and keeps apply disabled', async () => {
    serve({ 'POST /api/v1/admin/declarative/diff': () => json(run({ errors: ['field "format" is immutable'] })) })
    const w = await mountPage()
    await typeManifest(w)
    await review(w)
    expect(w.get('[data-testid="plan-errors"]').text()).toContain('field "format" is immutable')
    expect(applyButton(w).attributes('disabled')).toBeDefined()
    w.unmount()
  })

  it('says the server already matches when the plan changes nothing', async () => {
    serve({ 'POST /api/v1/admin/declarative/diff': () => json(run({ summary: { unchanged: 1 }, changes: false, items: [{ action: 'unchanged', kind: 'Repository', name: 'same' }] })) })
    const w = await mountPage()
    await typeManifest(w)
    await review(w)
    expect(w.get('[data-testid="plan-nochanges"]').text()).toContain('already matches')
    expect(applyButton(w).attributes('disabled')).toBeDefined()
    expect(w.find('[data-testid="plan-items"]').text()).not.toContain('same')
    await w.get('[data-testid="toggle-unchanged"]').trigger('click')
    expect(w.get('[data-testid="plan-items"]').text()).toContain('same')
    w.unmount()
  })

  it('shows a failure that carries no plan', async () => {
    serve({ 'POST /api/v1/admin/declarative/diff': () => json({ error: 'server exploded' }, 500) })
    const w = await mountPage()
    await typeManifest(w)
    await review(w)
    expect(w.get('[data-testid="declarative-error"]').text()).toContain('server exploded')
    expect(w.find('[data-testid="declarative-plan"]').exists()).toBe(false)
    w.unmount()
  })

  it('drops the plan when the manifest is edited, so apply needs a new review', async () => {
    serve({ 'POST /api/v1/admin/declarative/diff': () => json(run()) })
    const w = await mountPage()
    await typeManifest(w)
    await review(w)
    expect(w.find('[data-testid="declarative-plan"]').exists()).toBe(true)
    await typeManifest(w, 'kind: Repository\nname: other\n')
    expect(w.find('[data-testid="declarative-plan"]').exists()).toBe(false)
    expect(applyButton(w).attributes('disabled')).toBeDefined()
    w.unmount()
  })

  it('drops the plan when an option changes', async () => {
    serve({ 'POST /api/v1/admin/declarative/diff': () => json(run()) })
    const w = await mountPage()
    await typeManifest(w)
    await review(w)
    await w.get('[data-testid="opt-prune"]').setValue(true)
    expect(w.find('[data-testid="declarative-plan"]').exists()).toBe(false)
    await review(w)
    expect(JSON.parse(calls.filter((c) => c.path.endsWith('/diff')).at(-1)!.body!).prune).toBe(true)
    w.unmount()
  })

  it('applies only after the confirmation dialog, and shows the outcome', async () => {
    serve({
      'POST /api/v1/admin/declarative/diff': () => json(run()),
      'POST /api/v1/admin/declarative/apply': () => json(run({ items: [{ action: 'create', kind: 'Repository', name: 'new-repo', result: 'applied' }] })),
    })
    const w = await mountPage()
    await typeManifest(w)
    await review(w)
    await applyButton(w).trigger('click')
    await flushPromises()
    expect(posted('/api/v1/admin/declarative/apply')).toBeUndefined()
    expect(document.body.querySelector('[role="dialog"]')?.textContent).toContain('Apply these changes?')
    dialogButton('Apply')!.click()
    await flushPromises()
    expect(posted('/api/v1/admin/declarative/apply')).toBeDefined()
    expect(w.get('[data-testid="plan-outcome"]').text()).toBe('1 applied, 0 failed, 0 skipped.')
    // an applied plan cannot be applied again
    expect(applyButton(w).attributes('disabled')).toBeDefined()
    w.unmount()
  })

  it('names every deletion in the confirmation', async () => {
    serve({ 'POST /api/v1/admin/declarative/diff': () => json(run({ summary: { delete: 2 }, items: [
      { action: 'delete', kind: 'Repository', name: 'old-repo', managed: true },
      { action: 'delete', kind: 'Project', name: 'old-proj' },
    ] })) })
    const w = await mountPage()
    await typeManifest(w)
    await review(w)
    await applyButton(w).trigger('click')
    await flushPromises()
    const list = document.body.querySelector('[data-testid="confirm-deletions"]')!
    expect(list.textContent).toContain('Repository old-repo')
    expect(list.textContent).toContain('Project old-proj')
    expect(document.body.querySelector('[role="dialog"]')?.textContent).toContain('This deletes the objects listed below')
    w.unmount()
  })

  it('does not apply when the confirmation is cancelled', async () => {
    serve({ 'POST /api/v1/admin/declarative/diff': () => json(run()) })
    const w = await mountPage()
    await typeManifest(w)
    await review(w)
    await applyButton(w).trigger('click')
    await flushPromises()
    dialogButton('Cancel')!.click()
    await flushPromises()
    expect(posted('/api/v1/admin/declarative/apply')).toBeUndefined()
    w.unmount()
  })

  it('sends the secret values it was given in the secrets map, and not empty ones', async () => {
    serve({ 'POST /api/v1/admin/declarative/diff': () => json(run({ secrets: [{ env: 'DB_PASS', provided: false }, { env: 'API_KEY', provided: false }, { file: '/etc/secret' }] })) })
    const w = await mountPage()
    await typeManifest(w)
    await review(w)
    expect(w.get('[data-testid="declarative-secrets"]').text()).toContain('/etc/secret')
    await w.get('#secret-DB_PASS').setValue('s3cret')
    await review(w)
    const sent = JSON.parse(calls.filter((c) => c.path.endsWith('/diff')).at(-1)!.body!)
    expect(sent.secrets).toEqual({ DB_PASS: 's3cret' })
    w.unmount()
  })

  it('puts the live configuration in the editor from the export endpoint', async () => {
    serve({ 'GET /api/v1/admin/declarative/export': () => new Response('kind: Group\nname: all\n', { headers: { 'Content-Type': 'application/yaml' } }) })
    const w = await mountPage()
    const load = w.findAll('button').find((b) => b.text() === 'Load current configuration')!
    await load.trigger('click')
    await flushPromises()
    expect(calls.some((c) => c.path === '/api/v1/admin/declarative/export')).toBe(true)
    expect((w.get('[data-testid="manifest-text"]').element as HTMLTextAreaElement).value).toBe('kind: Group\nname: all\n')
    w.unmount()
  })

  it('downloads the export, with members on request', async () => {
    serve({ 'GET /api/v1/admin/declarative/export': () => new Response('x', { headers: { 'Content-Disposition': 'attachment; filename="m.yaml"' } }) })
    const w = await mountPage()
    await w.get('[data-testid="opt-members"]').setValue(true)
    await w.findAll('button').find((b) => b.text() === 'Download current configuration')!.trigger('click')
    await flushPromises()
    const get = calls.find((c) => c.path === '/api/v1/admin/declarative/export')
    expect(get?.query.get('with_members')).toBe('true')
    w.unmount()
  })

  it('lists managed objects and releases one after confirmation with a DELETE', async () => {
    managed = [{ kind: 'Repository', name: 'owned-repo', object_id: 'o1', applied_hash: 'h', applied_at: '2026-01-01T00:00:00Z' }]
    serve({ 'DELETE /api/v1/admin/declarative': () => new Response(null, { status: 204 }) })
    const w = await mountPage()
    expect(w.text()).toContain('owned-repo')
    await w.get('[data-testid="action-release"]').trigger('click')
    await flushPromises()
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)
    dialogButton('Stop managing')!.click()
    await flushPromises()
    const del = calls.find((c) => c.method === 'DELETE')
    expect(del?.query.get('kind')).toBe('Repository')
    expect(del?.query.get('name')).toBe('owned-repo')
    w.unmount()
  })

  it('offers a retry when the managed list cannot be read', async () => {
    serve({ 'GET /api/v1/admin/declarative': () => json({ error: 'managed broke' }, 500) })
    const w = await mountPage()
    expect(w.text()).toContain('managed broke')
    expect(w.text()).toContain('Retry')
    w.unmount()
  })

  it('is refused for a non-administrator', async () => {
    serve()
    setActivePinia(createPinia())
    useSessionStore().user = { id: 'u', username: 'dev', is_admin: false } as User
    const r = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/settings', component: { render: () => null } },
        { path: '/settings/:section', name: 'settings-section', component: SettingsSectionPage },
      ],
    })
    await r.push('/settings/declarative')
    const w = mount(SettingsSectionPage, { attachTo: document.body, global: { plugins: [r] } })
    await flushPromises()
    expect(w.find('[data-testid="section-forbidden"]').exists()).toBe(true)
    expect(calls.some((c) => c.path.includes('/admin/declarative'))).toBe(false)
    w.unmount()
  })
})
