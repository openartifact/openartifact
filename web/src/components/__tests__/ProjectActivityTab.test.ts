// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import ProjectActivityTab from '../ProjectActivityTab.vue'
import { formatDateTime } from '../../lib/datetime'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const entry = (id: string, action: string, message: string) => ({
  id,
  timestamp: '2026-01-02T03:04:05Z',
  action,
  actor: 'alice',
  actor_type: 'user',
  message,
})

let spy: Mock<typeof fetch>
afterEach(() => spy?.mockRestore())

function mountTab(respond: (url: string) => Response) {
  spy = spyOn(globalThis, 'fetch').mockImplementation(async (input) => respond(String(input)))
  const wrapper = mount(ProjectActivityTab, { props: { project: 'p' } })
  return flushPromises().then(() => wrapper)
}

describe('ProjectActivityTab', () => {
  it('lists events with a formatted date and appends the next page', async () => {
    const wrapper = await mountTab((url) =>
      url.includes('cursor=c2')
        ? json({ data: [entry('e2', 'project.member_added', 'older')], per_page: 25, has_more: false })
        : json({ data: [entry('e1', 'project.created', 'newest')], per_page: 25, has_more: true, next_cursor: 'c2' }),
    )
    expect(String(spy.mock.calls[0]![0])).toBe('/api/v1/projects/p/activity?per_page=25')

    let rows = wrapper.findAll('[data-testid="activity-row"]')
    expect(rows).toHaveLength(1)
    expect(rows[0]!.text()).toContain(formatDateTime('2026-01-02T03:04:05Z'))
    expect(rows[0]!.text()).toContain('project.created')
    expect(rows[0]!.text()).toContain('alice')

    await wrapper.find('[data-testid="activity-more"]').trigger('click')
    await flushPromises()
    expect(String(spy.mock.calls[1]![0])).toBe('/api/v1/projects/p/activity?cursor=c2&per_page=25')
    rows = wrapper.findAll('[data-testid="activity-row"]')
    expect(rows.map((r) => r.text())).toEqual([expect.stringContaining('newest'), expect.stringContaining('older')])
    expect(wrapper.find('[data-testid="activity-more"]').exists()).toBe(false)
  })

  it('says so when nothing is recorded', async () => {
    const wrapper = await mountTab(() => json({ data: [], per_page: 25, has_more: false }))
    expect(wrapper.find('[data-testid="activity-empty"]').exists()).toBe(true)
  })

  it('shows an error with retry when loading fails', async () => {
    const wrapper = await mountTab(() => json({ error: 'x' }, 500))
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="activity-row"]').exists()).toBe(false)
  })

  /** Fetch whose responses are released manually, to force out-of-order arrival. */
  function deferredFetch() {
    const pending = new Map<string, (r: Response) => void>()
    spy = spyOn(globalThis, 'fetch').mockImplementation(
      (input) => new Promise<Response>((resolve) => pending.set(String(input), resolve)),
    )
    return pending
  }

  it('drops a slow response for the previous project after a project switch', async () => {
    const pending = deferredFetch()
    const wrapper = mount(ProjectActivityTab, { props: { project: 'old' } })
    await flushPromises()
    await wrapper.setProps({ project: 'new' })
    await flushPromises()

    pending.get('/api/v1/projects/new/activity?per_page=25')!(
      json({ data: [entry('n1', 'project.created', 'from-new')], per_page: 25, has_more: false }),
    )
    await flushPromises()
    pending.get('/api/v1/projects/old/activity?per_page=25')!(
      json({ data: [entry('o1', 'project.created', 'from-old')], per_page: 25, has_more: false }),
    )
    await flushPromises()

    const rows = wrapper.findAll('[data-testid="activity-row"]')
    expect(rows).toHaveLength(1)
    expect(rows[0]!.text()).toContain('from-new')
  })

  it('drops a load-more response that lands after the project changed', async () => {
    const pending = deferredFetch()
    const wrapper = mount(ProjectActivityTab, { props: { project: 'old' } })
    await flushPromises()
    pending.get('/api/v1/projects/old/activity?per_page=25')!(
      json({ data: [entry('o1', 'project.created', 'old-1')], per_page: 25, has_more: true, next_cursor: 'c2' }),
    )
    await flushPromises()
    await wrapper.find('[data-testid="activity-more"]').trigger('click')
    await flushPromises()

    await wrapper.setProps({ project: 'new' })
    await flushPromises()
    pending.get('/api/v1/projects/new/activity?per_page=25')!(
      json({ data: [entry('n1', 'project.created', 'new-1')], per_page: 25, has_more: false }),
    )
    await flushPromises()
    pending.get('/api/v1/projects/old/activity?cursor=c2&per_page=25')!(
      json({ data: [entry('o2', 'project.member_added', 'old-2')], per_page: 25, has_more: false }),
    )
    await flushPromises()

    const rows = wrapper.findAll('[data-testid="activity-row"]')
    expect(rows.map((r) => r.text())).toEqual([expect.stringContaining('new-1')])
    expect(wrapper.find('[data-testid="activity-more"]').exists()).toBe(false)
  })
})
