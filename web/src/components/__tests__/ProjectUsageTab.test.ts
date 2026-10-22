// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { mount, flushPromises, RouterLinkStub } from '@vue/test-utils'
import ProjectUsageTab from '../ProjectUsageTab.vue'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const GB = 1024 ** 3

const USAGE = {
  project: 'p',
  size_bytes: 9 * GB,
  artifact_count: 40,
  quota: { project: 'p', max_size_bytes: 10 * GB, max_artifact_count: 0, warn_threshold_percent: 80, enforced: true },
  repositories: [
    { name: 'hosted-deb', format: 'deb', repo_type: 'hosted', size_bytes: 9 * GB, artifact_count: 40, max_size_bytes: 5 * GB, max_artifact_count: 0, quota_enforced: true },
    { name: 'npm-proxy', format: 'npm', repo_type: 'proxy', size_bytes: 0, artifact_count: 0, quota_enforced: false },
  ],
}

let spy: Mock<typeof fetch>
afterEach(() => spy?.mockRestore())

async function mountTab(body: unknown, status = 200) {
  spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json(body, status))
  const wrapper = mount(ProjectUsageTab, { props: { project: 'p' }, global: { stubs: { RouterLink: RouterLinkStub } } })
  await flushPromises()
  return wrapper
}

describe('ProjectUsageTab', () => {
  it('shows totals with the quota bar in the warning colour and flags unmetered repositories', async () => {
    const wrapper = await mountTab(USAGE)
    expect(String(spy.mock.calls[0]![0])).toBe('/api/v1/projects/p/usage')
    expect(wrapper.find('[data-testid="usage-size"]').text()).toContain('9.0 GB')
    expect(wrapper.find('[data-testid="usage-size"]').text()).toContain('10.0 GB')
    expect(wrapper.find('[data-testid="size-bar"]').classes()).toContain('bg-warning')
    expect(wrapper.find('[data-testid="count-bar"]').exists()).toBe(false)

    const rows = wrapper.findAll('[data-testid="usage-row"]')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.find('[data-testid="usage-limit"]').text()).toBe('5.0 GB')
    expect(rows[1]!.find('[data-testid="usage-limit"]').text()).toBe('Not metered')
  })

  it('turns the bar red at the limit', async () => {
    const wrapper = await mountTab({ ...USAGE, size_bytes: 10 * GB })
    expect(wrapper.find('[data-testid="size-bar"]').classes()).toContain('bg-danger')
  })

  it('explains that nothing is metered without a project limit', async () => {
    const wrapper = await mountTab({ ...USAGE, quota: { enforced: false, max_size_bytes: 0, max_artifact_count: 0 } })
    expect(wrapper.find('[data-testid="usage-no-limit"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="size-bar"]').exists()).toBe(false)
  })

  it('shows an error with a retry when the usage cannot be loaded', async () => {
    const wrapper = await mountTab({ error: 'x' }, 500)
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="usage-totals"]').exists()).toBe(false)
  })
})
