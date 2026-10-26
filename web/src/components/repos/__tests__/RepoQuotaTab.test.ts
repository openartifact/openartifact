// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, afterEach } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import RepoQuotaTab from '../RepoQuotaTab.vue'

const realFetch = globalThis.fetch

afterEach(() => {
  globalThis.fetch = realFetch
})

function mockQuota(body: Record<string, unknown>): void {
  globalThis.fetch = (async () =>
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })) as typeof fetch
}

describe('RepoQuotaTab', () => {
  it('shows a quota that only limits the artifact count', async () => {
    mockQuota({
      max_size_bytes: 0,
      max_artifact_count: 50,
      current_size_bytes: 2048,
      current_artifact_count: 10,
      is_exceeded: false,
    })
    const wrapper = mount(RepoQuotaTab, { props: { repoName: 'r' }, global: { plugins: [createPinia()] } })
    await flushPromises()
    expect(wrapper.text()).not.toContain('No quota configured')
    expect(wrapper.text()).toContain('Artifact count')
    expect(wrapper.text()).toContain('/ 50')
    expect(wrapper.text()).toContain('Unlimited')
    expect(wrapper.findAll('[role="progressbar"]')).toHaveLength(1)
    wrapper.unmount()
  })

  it('treats a quota with no limits as not configured', async () => {
    mockQuota({ max_size_bytes: 0, max_artifact_count: 0, current_size_bytes: 0, current_artifact_count: 0 })
    const wrapper = mount(RepoQuotaTab, { props: { repoName: 'r' }, global: { plugins: [createPinia()] } })
    await flushPromises()
    expect(wrapper.text()).toContain('No quota configured')
    wrapper.unmount()
  })
})
