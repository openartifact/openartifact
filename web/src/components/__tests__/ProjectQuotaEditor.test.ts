// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import ProjectQuotaEditor from '../ProjectQuotaEditor.vue'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const GB = 1024 ** 3
const QUOTA = { project: 'p', max_size_bytes: 10 * GB, max_artifact_count: 0, warn_threshold_percent: 80, enforced: true }

let spy: Mock<typeof fetch>
afterEach(() => spy?.mockRestore())

async function mountEditor(editable: boolean) {
  spy = spyOn(globalThis, 'fetch').mockImplementation(async (_u, init) =>
    init?.method === 'PUT' ? json({ ...QUOTA, ...JSON.parse(init.body as string) }) : json(QUOTA),
  )
  const wrapper = mount(ProjectQuotaEditor, { props: { project: 'p', editable }, global: { plugins: [createPinia()] } })
  await flushPromises()
  return wrapper
}

describe('ProjectQuotaEditor', () => {
  it('is read-only for non-admins', async () => {
    const wrapper = await mountEditor(false)
    expect(wrapper.find('[data-testid="quota-form"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="quota-readonly"]').text()).toContain('10.0 GB')
    expect(wrapper.find('[data-testid="quota-readonly"]').text()).toContain('Unlimited')
  })

  it('lets an admin edit and saves the limits in bytes', async () => {
    const wrapper = await mountEditor(true)
    const size = wrapper.find<HTMLInputElement>('#pq-size')
    expect(size.element.value).toBe('10')
    const save = wrapper.find('button[type="submit"]')
    expect(save.attributes('disabled')).toBeDefined()

    await size.setValue('20')
    await wrapper.find('#pq-count').setValue('500')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    const put = spy.mock.calls.find(([, init]) => init?.method === 'PUT')!
    expect(String(put[0])).toBe('/api/v1/projects/p/quota')
    expect(JSON.parse(put[1]!.body as string)).toEqual({
      max_size_bytes: 20 * GB,
      max_artifact_count: 500,
      warn_threshold_percent: 80,
    })
  })

  it('rejects a threshold above 100', async () => {
    const wrapper = await mountEditor(true)
    await wrapper.find('#pq-warn').setValue('150')
    expect(wrapper.find('button[type="submit"]').attributes('disabled')).toBeDefined()
  })
})
