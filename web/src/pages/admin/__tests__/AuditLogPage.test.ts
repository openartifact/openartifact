// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import AuditLogPage from '../AuditLogPage.vue'

const PAGES: Record<string, { data: { id: string; action: string }[]; has_more: boolean; next_cursor?: string }> = {
  '': { data: [{ id: 'a1', action: 'auth.login' }], has_more: true, next_cursor: 'c2' },
  c2: { data: [{ id: 'b1', action: 'auth.logout' }], has_more: true, next_cursor: 'c3' },
  c3: { data: [{ id: 'c1', action: 'repository.created' }], has_more: false },
}

function mountPage() {
  const fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const cursor = new URL(String(input), 'http://localhost').searchParams.get('cursor') ?? ''
    return new Response(JSON.stringify(PAGES[cursor]), { status: 200, headers: { 'Content-Type': 'application/json' } })
  })
  const wrapper = mount(AuditLogPage, { global: { plugins: [createPinia()], stubs: { PageHeader: PageHeaderStub } } })
  const cursors = () => fetchSpy.mock.calls.map((c) => new URL(String(c[0]), 'http://localhost').searchParams.get('cursor'))
  const button = (label: string) => wrapper.findAll('button').find((b) => b.text() === label)!.element as HTMLButtonElement

  return { wrapper, cursors, button }
}

// PageHeader reads the router; the page tests only need its action slot.
const PageHeaderStub = { template: '<div><slot name="actions" /></div>' }

describe('AuditLogPage keyset paging', () => {
  it('walks forward with next_cursor, back from the cursor stack, and stops at the last page', async () => {
    const { wrapper, cursors, button } = mountPage()
    await flushPromises()
    expect(wrapper.text()).toContain('auth.login')
    expect(button('Previous page').disabled).toBe(true)
    expect(button('Next page').disabled).toBe(false)

    button('Next page').click()
    await flushPromises()
    expect(wrapper.text()).toContain('auth.logout')
    expect(wrapper.text()).toContain('Page 2')

    button('Next page').click()
    await flushPromises()
    expect(wrapper.text()).toContain('repository.created')
    expect(button('Next page').disabled).toBe(true)

    button('Previous page').click()
    await flushPromises()
    expect(wrapper.text()).toContain('auth.logout')
    expect(button('Next page').disabled).toBe(false)

    expect(cursors()).toEqual(['', 'c2', 'c3', 'c2'])
  })
})
