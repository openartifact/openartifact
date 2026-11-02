// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, jest, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import ProjectCleanupDefaults from '../ProjectCleanupDefaults.vue'
import { PREVIEW_DEBOUNCE_MS } from '../../composables/useCleanupLivePreview'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const POLICIES = [
  { id: 'c1', name: 'keep-5', enabled: true, keep_last_n: 5, project_id: 'p1', created_at: '2026-01-01T00:00:00Z' },
]

let spy: Mock<typeof fetch>

function mountTab(canEdit: boolean) {
  spy = spyOn(globalThis, 'fetch').mockImplementation(async (_u, init) => {
    if (init?.method === 'POST') return json(POLICIES[0], 201)
    return json({ policies: POLICIES })
  })
  return mount(ProjectCleanupDefaults, {
    props: { project: 'p', canEdit },
    global: { plugins: [createPinia()] },
    attachTo: document.body,
  })
}

afterEach(() => {
  jest.useRealTimers()
  spy?.mockRestore()
  document.body.innerHTML = ''
})

describe('ProjectCleanupDefaults', () => {
  it('lists the policies read-only for members and states the override rule', async () => {
    const wrapper = mountTab(false)
    await flushPromises()
    expect(wrapper.findAll('[data-testid="cleanup-row"]')).toHaveLength(1)
    expect(wrapper.text()).toContain('keep-5')
    expect(wrapper.find('[data-testid="cleanup-add"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="cleanup-readonly"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="cleanup-override-note"]').text()).toContain('its own enabled cleanup policy')
    expect(wrapper.find('[data-testid="action-delete"]').exists()).toBe(false)
  })

  it('lets an owner create a default policy', async () => {
    const wrapper = mountTab(true)
    await flushPromises()
    await wrapper.find('[data-testid="cleanup-add"]').trigger('click')
    await flushPromises()
    const name = document.querySelector<HTMLInputElement>('#cleanup-name')!
    name.value = 'weekly'
    name.dispatchEvent(new Event('input'))
    await flushPromises()
    const create = [...document.querySelectorAll('button')].find((b) => b.textContent?.includes('Create policy'))!
    create.click()
    await flushPromises()

    const post = spy.mock.calls.find(([u, init]) => init?.method === 'POST' && !String(u).endsWith('/dry-run'))!
    expect(String(post[0])).toBe('/api/v1/projects/p/cleanup-policies')
    expect(JSON.parse(post[1]!.body as string)).toEqual({ name: 'weekly', enabled: true, name_pattern: '', keep_last_n: 10 })
  })

  it('previews what the default policy would remove, per repository', async () => {
    const wrapper = mountTab(true)
    await flushPromises()
    spy.mockImplementation(async (url) =>
      String(url).endsWith('/dry-run')
        ? json({
            dry_run: true,
            rows: 2,
            repositories: [
              { repository: 'npm-a', format: 'npm', dry_run: true, rows: 2, truncated: false, items: [{ id: 'i1', name: 'left-pad', version: '1.0.0' }, { id: 'i2', name: 'left-pad', version: '1.1.0' }] },
            ],
          })
        : json({ policies: POLICIES }),
    )
    await wrapper.find('[data-testid="action-dry-run"]').trigger('click')
    await flushPromises()
    const call = spy.mock.calls.find((c) => String(c[0]).endsWith('/cleanup-policies/c1/dry-run'))
    expect(call?.[1]?.method).toBe('POST')
    expect(document.querySelector('[data-testid="dryrun-total"]')?.textContent).toContain('2 items')
    expect(document.querySelectorAll('[data-testid="dryrun-item"]')).toHaveLength(2)
    expect(document.body.textContent).toContain('npm-a')
  })

  it('keeps the name pattern when a policy is edited and previews it live', async () => {
    jest.useFakeTimers()
    const wrapper = mountTab(true)
    await flushPromises()
    spy.mockImplementation(async (url, init) => {
      if (String(url).endsWith('/cleanup-policies/dry-run')) {
        return json({
          dry_run: true,
          rows: 3,
          repositories: [{ repository: 'raw-a', format: 'raw', dry_run: true, rows: 3, bytes: 30, truncated: false, items: [{ id: 'i1', name: 'app-1', version: '1' }] }],
        })
      }
      if (init?.method === 'PUT') return json(POLICIES[0])
      return json({ policies: [{ ...POLICIES[0], name_pattern: '^app-' }] })
    })
    wrapper.unmount()
    const w = mount(ProjectCleanupDefaults, { props: { project: 'p', canEdit: true }, global: { plugins: [createPinia()] }, attachTo: document.body })
    await flushPromises()

    await w.find('[data-testid="action-edit"]').trigger('click')
    await flushPromises()
    const pattern = document.querySelector<HTMLInputElement>('#cleanup-name-pattern')!
    expect(pattern.value).toBe('^app-')

    jest.advanceTimersByTime(PREVIEW_DEBOUNCE_MS)
    await flushPromises()
    const draft = spy.mock.calls.find((c) => String(c[0]).endsWith('/cleanup-policies/dry-run'))!
    expect(JSON.parse(String(draft[1]!.body))).toEqual({ keep_last_n: 5, name_pattern: '^app-' })
    expect(document.querySelector('[data-testid="cleanup-preview-rows"]')?.textContent).toBe('3')
    expect(document.querySelector('[data-testid="cleanup-preview-repos"]')?.textContent).toContain('1 repository')

    pattern.value = '('
    pattern.dispatchEvent(new Event('input'))
    await flushPromises()
    expect(document.querySelector('[data-testid="cleanup-pattern-error"]')).not.toBeNull()
    const save = [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Save')!
    expect(save.disabled).toBe(true)

    pattern.value = '^lib-'
    pattern.dispatchEvent(new Event('input'))
    await flushPromises()
    save.click()
    await flushPromises()
    const put = spy.mock.calls.find(([, init]) => init?.method === 'PUT')!
    expect(JSON.parse(String(put[1]!.body))).toEqual({ name: 'keep-5', enabled: true, name_pattern: '^lib-', keep_last_n: 5 })
  })
})
