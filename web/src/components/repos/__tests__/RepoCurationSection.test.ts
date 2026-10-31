// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, describe, expect, it, spyOn, type Mock } from 'bun:test'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import RepoCurationSection from '../RepoCurationSection.vue'
import { useToastStore } from '../../../stores/toast'
import type { CurationMode, CurationRepository, CurationRule } from '../../../api/curation'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const rule = (over: Partial<CurationRule> = {}): CurationRule => ({
  id: 'r1',
  action: 'block',
  package_pattern: 'lodash',
  version_pattern: '',
  reason: 'typosquat',
  created_by: 'ada',
  created_at: '2026-10-01T00:00:00Z',
  ...over,
})

interface Call {
  method: string
  path: string
  body: Record<string, unknown> | null
}

let calls: Call[]
let spy: Mock<typeof fetch>
let wrapper: VueWrapper | undefined

function serve(rules: CurationRule[], special: (c: Call) => Response | undefined = () => undefined): void {
  calls = []
  spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = init?.body
    const call: Call = {
      method: init?.method ?? 'GET',
      path: new URL(String(input), 'http://localhost').pathname,
      body: typeof raw === 'string' ? (JSON.parse(raw) as Record<string, unknown>) : null,
    }
    calls.push(call)
    return special(call) ?? (call.method === 'GET' ? json({ rules }) : json({}))
  }) as typeof fetch)
}

async function mountSection(mode: CurationMode = 'denylist'): Promise<VueWrapper> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const repo = { id: 'p1', name: 'npm-proxy', format: 'npm', repo_type: 'proxy', curation_mode: mode } as CurationRepository
  wrapper = mount(RepoCurationSection, { props: { repo }, global: { plugins: [pinia], stubs: { RouterLink: true } }, attachTo: document.body })
  await flushPromises()
  return wrapper
}

const body = (selector: string): HTMLElement | null => document.body.querySelector(selector)

async function fill(selector: string, value: string): Promise<void> {
  const input = body(selector) as HTMLInputElement
  input.value = value
  input.dispatchEvent(new Event('input'))
  await flushPromises()
}

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  spy?.mockRestore()
  document.body.innerHTML = ''
})

describe('RepoCurationSection: empty state', () => {
  it('explains the three modes and that an allow rule exempts a version from the minimum age', async () => {
    serve([])
    const w = await mountSection()
    const empty = w.get('[data-testid="curation-empty"]').text()
    expect(empty).toContain('Off')
    expect(empty).toContain('Denylist')
    expect(empty).toContain('Allowlist')
    expect(empty).toContain('exempts a version from the repository\'s minimum age')
    expect(w.find('[data-testid="curation-rules"]').exists()).toBe(false)
  })
})

describe('RepoCurationSection: rules', () => {
  it('lists the rules with their action, patterns, reason and author', async () => {
    serve([rule(), rule({ id: 'r2', action: 'allow', package_pattern: '@acme/*', version_pattern: '2.*', reason: '' })])
    const w = await mountSection()
    const rows = w.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0]?.text()).toContain('Block')
    expect(rows[0]?.text()).toContain('lodash')
    expect(rows[0]?.text()).toContain('all versions')
    expect(rows[0]?.text()).toContain('typosquat')
    expect(rows[0]?.text()).toContain('ada')
    expect(rows[1]?.text()).toContain('Allow')
    expect(rows[1]?.text()).toContain('2.*')
    expect(w.find('[data-testid="curation-empty"]').exists()).toBe(false)
  })

  it('marks the rules as inactive while curation is off, and not otherwise', async () => {
    serve([rule()])
    expect((await mountSection('off')).get('[data-testid="curation-inactive"]').text()).toBe('Rules are not applied while curation is off.')
    wrapper?.unmount()
    serve([rule()])
    expect((await mountSection('denylist')).find('[data-testid="curation-inactive"]').exists()).toBe(false)
  })

  it('shows the load error instead of the empty state', async () => {
    serve([], (c) => (c.method === 'GET' ? json({ error: 'database down' }, 500) : undefined))
    const w = await mountSection()
    expect(w.text()).toContain('database down')
    expect(w.find('[data-testid="curation-empty"]').exists()).toBe(false)
  })
})

describe('RepoCurationSection: mode', () => {
  it('patches the repository when another mode is picked and passes the answer up', async () => {
    serve([], (c) => (c.method === 'PATCH' ? json({ name: 'npm-proxy', curation_mode: 'allowlist' }) : undefined))
    const w = await mountSection('denylist')
    await w.get('[data-testid="mode-allowlist"]').trigger('click')
    await flushPromises()
    const patch = calls.find((c) => c.method === 'PATCH')
    expect(patch?.path).toBe('/api/v1/repository/npm-proxy')
    expect(patch?.body).toEqual({ curation_mode: 'allowlist' })
    expect(w.get('[data-testid="mode-allowlist"]').attributes('aria-checked')).toBe('true')
    expect(w.emitted('saved')?.[0]?.[0]).toMatchObject({ curation_mode: 'allowlist' })
  })

  it('warns that an allowlist without an allow rule serves nothing', async () => {
    serve([rule()])
    const w = await mountSection('allowlist')
    expect(w.get('[data-testid="allowlist-empty"]').text()).toContain('every version is unavailable')
  })
})

describe('RepoCurationSection: add and delete', () => {
  it('adds a rule through the drawer', async () => {
    serve([])
    const w = await mountSection('denylist')
    await w.get('[data-testid="rule-add"]').trigger('click')
    await flushPromises()
    await fill('#curation-package', '@acme/*')
    await fill('#curation-version', '2.*')
    await fill('#curation-reason', 'not vetted')
    expect(body('[data-testid="glob-help"]')?.textContent).toContain('@acme/*')
    ;(body('[data-testid="rule-save"]') as HTMLButtonElement).click()
    await flushPromises()
    const post = calls.find((c) => c.method === 'POST')
    expect(post?.path).toBe('/api/v1/repositories/npm-proxy/curation-rules')
    expect(post?.body).toEqual({ action: 'block', package_pattern: '@acme/*', version_pattern: '2.*', reason: 'not vetted' })
    // The drawer fades out, so its DOM may linger: the confirmation and the reloaded list show the rule was saved.
    expect(useToastStore().queue[0]?.title).toBe('Rule added')
    expect(calls.at(-1)?.method).toBe('GET')
  })

  it('keeps the drawer open and shows the validation error of the server', async () => {
    serve([], (c) => (c.method === 'POST' ? json({ error: 'package_pattern: invalid glob' }, 400) : undefined))
    const w = await mountSection('denylist')
    await w.get('[data-testid="rule-add"]').trigger('click')
    await flushPromises()
    await fill('#curation-package', 'lodash')
    ;(body('[data-testid="rule-save"]') as HTMLButtonElement).click()
    await flushPromises()
    expect(body('[data-testid="rule-error"]')?.textContent).toBe('package_pattern: invalid glob')
    expect(body('#curation-package')).not.toBeNull()
  })

  it('edits a rule with its current values prefilled', async () => {
    serve([rule({ id: 'r7', version_pattern: '1.*' })])
    const w = await mountSection()
    await w.get('[data-testid="action-edit"]').trigger('click')
    await flushPromises()
    expect((body('#curation-package') as HTMLInputElement).value).toBe('lodash')
    expect((body('#curation-version') as HTMLInputElement).value).toBe('1.*')
    await fill('#curation-version', '')
    ;(body('[data-testid="rule-save"]') as HTMLButtonElement).click()
    await flushPromises()
    const patch = calls.find((c) => c.method === 'PATCH')
    expect(patch?.path).toBe('/api/v1/repositories/npm-proxy/curation-rules/r7')
    expect(patch?.body).toMatchObject({ version_pattern: '' })
  })

  it('deletes a rule only after the confirmation', async () => {
    serve([rule({ id: 'r3' })])
    const w = await mountSection()
    await w.get('[data-testid="action-delete"]').trigger('click')
    await flushPromises()
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)
    const confirm = Array.from(document.body.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Delete' && b.closest('[role="dialog"]'))
    expect(confirm).toBeDefined()
    confirm?.click()
    await flushPromises()
    expect(calls.find((c) => c.method === 'DELETE')?.path).toBe('/api/v1/repositories/npm-proxy/curation-rules/r3')
  })
})
