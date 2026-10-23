// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import RepoCleanupTab from '../RepoCleanupTab.vue'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let spy: Mock<typeof fetch>

afterEach(() => {
  spy?.mockRestore()
  document.body.innerHTML = ''
})

describe('RepoCleanupTab dry run', () => {
  it('lists what a run would remove without running the policy', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async (url) =>
      String(url).endsWith('/dry-run')
        ? json({
            repository: 'r',
            format: 'maven',
            dry_run: true,
            rows: 1,
            truncated: false,
            items: [{ id: 'i1', name: 'org.a:lib', version: '1.0' }],
          })
        : json({ policies: [{ id: 'c1', name: 'keep-1', enabled: true, keep_count: 1, created_at: '2026-01-01T00:00:00Z' }] }),
    )
    const wrapper = mount(RepoCleanupTab, {
      props: { repoName: 'r' },
      global: { plugins: [createPinia()] },
      attachTo: document.body,
    })
    await flushPromises()
    await wrapper.find('[data-testid="cleanup-dry-run"]').trigger('click')
    await flushPromises()

    expect(spy.mock.calls.some((c) => String(c[0]).endsWith('/cleanup-policies/c1/dry-run') && c[1]?.method === 'POST')).toBe(true)
    expect(spy.mock.calls.some((c) => String(c[0]).endsWith('/run'))).toBe(false)
    expect(document.querySelector('[data-testid="dryrun-item"]')?.textContent).toContain('org.a:lib')
  })
})
