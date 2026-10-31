// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, beforeEach, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import RepoActivityTab from '../RepoActivityTab.vue'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

function entry(id: string, action: string, over: Record<string, unknown> = {}) {
  return {
    id,
    timestamp: new Date().toISOString(),
    action,
    actor: 'alice',
    actor_name: 'Alice',
    actor_type: 'user',
    message: '',
    subject: { kind: 'package', name: 'lib', version: '1.0', repository: 'libs' },
    ...over,
  }
}

const page = (data: unknown[], more?: string) => ({ data, per_page: 25, has_more: !!more, ...(more ? { next_cursor: more } : {}) })

/** Stand-in IntersectionObserver the test can fire by hand. */
class FakeObserver {
  static instances: FakeObserver[] = []
  observed: Element[] = []
  disconnected = false
  constructor(private cb: IntersectionObserverCallback) {
    FakeObserver.instances.push(this)
  }
  observe(el: Element): void {
    this.observed.push(el)
  }
  disconnect(): void {
    this.disconnected = true
  }
  unobserve(): void {}
  takeRecords(): IntersectionObserverEntry[] {
    return []
  }
  fire(): void {
    this.cb([{ isIntersecting: true } as IntersectionObserverEntry], this as unknown as IntersectionObserver)
  }
  static live(): FakeObserver {
    return FakeObserver.instances.filter((o) => !o.disconnected).at(-1)!
  }
}

let spy: Mock<typeof fetch>
const original = globalThis.IntersectionObserver

beforeEach(() => {
  FakeObserver.instances = []
  globalThis.IntersectionObserver = FakeObserver as unknown as typeof IntersectionObserver
})
afterEach(() => {
  spy?.mockRestore()
  globalThis.IntersectionObserver = original
})

async function mountTab(respond: (url: string) => Response | Promise<Response>, props: { admin?: boolean } = {}) {
  spy = spyOn(globalThis, 'fetch').mockImplementation(async (input) => respond(String(input)))
  const wrapper = mount(RepoActivityTab, {
    props: { repo: 'libs', ...props },
    global: { stubs: { RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' } } },
    attachTo: document.body,
  })
  await flushPromises()
  return wrapper
}

const urls = (): string[] => spy.mock.calls.map((c) => String(c[0]))

describe('RepoActivityTab', () => {
  it('shows the actor, the action label, the package link and a relative time, grouped under a day header', async () => {
    const wrapper = await mountTab(() =>
      json(page([entry('e1', 'artifact.uploaded'), entry('e2', 'artifact.deleted', { actor_name: 'Bob', actor: 'bob' })])),
    )
    expect(urls()[0]).toBe('/api/v1/repositories/libs/activity?limit=25')

    const days = wrapper.findAll('[data-testid="activity-day"]')
    expect(days).toHaveLength(1)
    expect(days[0]!.find('h3').text()).toBe('Today')

    const rows = wrapper.findAll('[data-testid="activity-row"]')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.text()).toContain('Published')
    expect(rows[0]!.text()).toContain('Alice')
    expect(rows[0]!.text()).toContain('just now')
    expect(rows[0]!.find('a').attributes('href')).toBe('/repos/libs/packages/lib/versions/1.0')
    expect(rows[1]!.text()).toContain('Moved to trash')
    expect(rows[1]!.text()).toContain('Bob')
    expect(rows[0]!.find('time').exists()).toBe(true)
  })

  it('starts a new day header when the date changes, and shows no actor for an anonymous reader', async () => {
    const old = new Date(Date.now() - 40 * 86_400_000).toISOString()
    const wrapper = await mountTab(() =>
      json(page([entry('e1', 'artifact.uploaded', { actor: '', actor_name: '' }), entry('e2', 'artifact.deleted', { timestamp: old })])),
    )
    expect(wrapper.findAll('[data-testid="activity-day"]')).toHaveLength(2)
    expect(wrapper.findAll('[data-testid="activity-row"]')[0]!.find('[data-testid="activity-actor"]').exists()).toBe(false)
  })

  it('attributes system events and shows the detail line of a cleanup run', async () => {
    const wrapper = await mountTab(() =>
      json(page([entry('e1', 'cleanup.run_completed', { actor: 'cleanup-policy', actor_name: '', actor_type: 'system', message: 'cleanup policy keep-5 removed 5 versions', subject: null })])),
      { admin: true },
    )
    const row = wrapper.find('[data-testid="activity-row"]')
    expect(row.text()).toContain('Cleanup finished')
    expect(row.text()).toContain('cleanup-policy')
    expect(row.text()).toContain('removed 5 versions')
  })

  it('offers the operational filters only to admins', async () => {
    const reader = await mountTab(() => json(page([])))
    expect(reader.findAll('[data-category]').map((b) => b.attributes('data-category'))).toEqual(['', 'uploads', 'deletions', 'trash', 'promotions', 'quarantine'])
    reader.unmount()

    const admin = await mountTab(() => json(page([])), { admin: true })
    expect(admin.findAll('[data-category]').map((b) => b.attributes('data-category'))).toContain('settings')
    expect(admin.findAll('[data-category]').map((b) => b.attributes('data-category'))).toContain('federation')
  })

  it('filters by event type through the server and clears the filter', async () => {
    const wrapper = await mountTab((url) =>
      url.includes('category=trash') ? json(page([entry('t1', 'artifact.restored')])) : json(page([entry('e1', 'artifact.uploaded'), entry('e2', 'artifact.deleted')])),
    )
    expect(wrapper.findAll('[data-testid="activity-row"]')).toHaveLength(2)

    await wrapper.find('[data-category="trash"]').trigger('click')
    await flushPromises()
    expect(urls().at(-1)).toBe('/api/v1/repositories/libs/activity?limit=25&category=trash')
    expect(wrapper.findAll('[data-testid="activity-row"]')).toHaveLength(1)
    expect(wrapper.find('[data-category="trash"]').attributes('aria-pressed')).toBe('true')

    await wrapper.find('[data-category=""]').trigger('click')
    await flushPromises()
    expect(urls().at(-1)).toBe('/api/v1/repositories/libs/activity?limit=25')
    expect(wrapper.findAll('[data-testid="activity-row"]')).toHaveLength(2)
  })

  it('ignores the response of a filter that was replaced meanwhile', async () => {
    let releaseSlow: (r: Response) => void = () => {}
    const slow = new Promise<Response>((resolve) => (releaseSlow = resolve))
    const wrapper = await mountTab((url) => {
      if (url.includes('category=uploads')) return slow
      if (url.includes('category=deletions')) return json(page([entry('d1', 'artifact.deleted')]))
      return json(page([]))
    })
    await wrapper.find('[data-category="uploads"]').trigger('click')
    await wrapper.find('[data-category="deletions"]').trigger('click')
    await flushPromises()
    releaseSlow(json(page([entry('u1', 'artifact.uploaded'), entry('u2', 'artifact.uploaded')])))
    await flushPromises()

    const rows = wrapper.findAll('[data-testid="activity-row"]')
    expect(rows).toHaveLength(1)
    expect(rows[0]!.text()).toContain('Moved to trash')
  })

  it('loads the next page by cursor when the end of the list scrolls into view, without repeating the day header', async () => {
    const wrapper = await mountTab((url) =>
      url.includes('cursor=c2') ? json(page([entry('e2', 'artifact.deleted')])) : json(page([entry('e1', 'artifact.uploaded')], 'c2')),
    )
    expect(wrapper.find('[data-testid="activity-load-more"]').exists()).toBe(false)
    expect(urls()).toHaveLength(1)

    FakeObserver.live().fire()
    await flushPromises()

    expect(urls()[1]).toBe('/api/v1/repositories/libs/activity?cursor=c2&limit=25')
    expect(wrapper.findAll('[data-testid="activity-row"]')).toHaveLength(2)
    expect(wrapper.findAll('[data-testid="activity-day"]')).toHaveLength(1)
    expect(wrapper.find('[data-testid="activity-more"]').exists()).toBe(false)
  })

  it('keeps the filter on the next page', async () => {
    const wrapper = await mountTab((url) =>
      url.includes('cursor=c2') ? json(page([entry('e2', 'artifact.deleted')])) : json(page([entry('e1', 'artifact.deleted')], 'c2')),
    )
    await wrapper.find('[data-category="deletions"]').trigger('click')
    await flushPromises()
    FakeObserver.live().fire()
    await flushPromises()
    expect(urls().at(-1)).toBe('/api/v1/repositories/libs/activity?cursor=c2&limit=25&category=deletions')
  })

  it('does not request the same page twice while one is loading', async () => {
    let release: (r: Response) => void = () => {}
    const pending = new Promise<Response>((resolve) => (release = resolve))
    const wrapper = await mountTab((url) => (url.includes('cursor=c2') ? pending : json(page([entry('e1', 'artifact.uploaded')], 'c2'))))
    const observer = FakeObserver.live()
    observer.fire()
    observer.fire()
    await flushPromises()
    expect(urls().filter((u) => u.includes('cursor=c2'))).toHaveLength(1)
    release(json(page([entry('e2', 'artifact.deleted')])))
    await flushPromises()
    expect(wrapper.findAll('[data-testid="activity-row"]')).toHaveLength(2)
  })

  it('falls back to a button where IntersectionObserver does not exist', async () => {
    // @ts-expect-error simulating an old browser
    delete globalThis.IntersectionObserver
    const wrapper = await mountTab((url) =>
      url.includes('cursor=c2') ? json(page([entry('e2', 'artifact.deleted')])) : json(page([entry('e1', 'artifact.uploaded')], 'c2')),
    )
    await wrapper.find('[data-testid="activity-load-more"]').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('[data-testid="activity-row"]')).toHaveLength(2)
  })

  it('keeps the loaded events and offers a retry when a further page fails', async () => {
    let fail = true
    const wrapper = await mountTab((url) => {
      if (url.includes('cursor=c2')) return fail ? json({ error: 'x' }, 500) : json(page([entry('e2', 'artifact.deleted')]))
      return json(page([entry('e1', 'artifact.uploaded')], 'c2'))
    })
    FakeObserver.live().fire()
    await flushPromises()
    expect(wrapper.findAll('[data-testid="activity-row"]')).toHaveLength(1)
    expect(wrapper.find('[data-testid="activity-more"] [role="alert"]').exists()).toBe(true)

    fail = false
    await wrapper.find('[data-testid="activity-load-more"]').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('[data-testid="activity-row"]')).toHaveLength(2)
  })

  it('shows a skeleton while loading', async () => {
    let release: (r: Response) => void = () => {}
    const pending = new Promise<Response>((resolve) => (release = resolve))
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => pending)
    const wrapper = mount(RepoActivityTab, { props: { repo: 'libs' }, global: { stubs: { RouterLink: true } } })
    await flushPromises()
    expect(wrapper.find('[data-testid="activity-loading"]').exists()).toBe(true)
    release(json(page([])))
    await flushPromises()
    expect(wrapper.find('[data-testid="activity-loading"]').exists()).toBe(false)
  })

  it('says so when nothing was recorded, and offers to clear a filter that matches nothing', async () => {
    const wrapper = await mountTab(() => json(page([])))
    expect(wrapper.find('[data-testid="activity-empty"]').text()).toContain('No activity yet')

    await wrapper.find('[data-category="trash"]').trigger('click')
    await flushPromises()
    const empty = wrapper.find('[data-testid="activity-empty"]')
    expect(empty.find('[data-state="filtered"]').exists()).toBe(true)
    await empty.find('button').trigger('click')
    await flushPromises()
    expect(urls().at(-1)).toBe('/api/v1/repositories/libs/activity?limit=25')
  })

  it('shows an error with retry when the first page fails', async () => {
    let fail = true
    const wrapper = await mountTab(() => (fail ? json({ error: 'x' }, 500) : json(page([entry('e1', 'artifact.uploaded')]))))
    expect(wrapper.find('[data-testid="activity-error"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="activity-row"]').exists()).toBe(false)

    fail = false
    await wrapper.find('[data-testid="activity-error"] button').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="activity-error"]').exists()).toBe(false)
    expect(wrapper.findAll('[data-testid="activity-row"]')).toHaveLength(1)
  })

  it('starts over when the repository changes', async () => {
    const wrapper = await mountTab((url) => (url.includes('/other/') ? json(page([entry('o1', 'artifact.restored')])) : json(page([entry('e1', 'artifact.uploaded')]))))
    await wrapper.setProps({ repo: 'other' })
    await flushPromises()
    expect(urls().at(-1)).toBe('/api/v1/repositories/other/activity?limit=25')
    expect(wrapper.findAll('[data-testid="activity-row"]')).toHaveLength(1)
    expect(wrapper.find('[data-testid="activity-row"]').text()).toContain('Restored from trash')
  })
})
