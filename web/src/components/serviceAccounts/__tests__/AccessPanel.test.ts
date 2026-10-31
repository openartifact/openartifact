// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import AccessPanel from '../AccessPanel.vue'

const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })

const base = {
  title: 'Groups',
  description: 'desc',
  rows: [{ id: 'g1', label: 'ci' }],
  options: [{ value: 'g2', label: 'ops' }],
  addLabel: 'Add to a group',
  removeLabel: 'Remove from group',
  emptyText: 'No groups',
}

const mountPanel = (props: Record<string, unknown> = {}) => mount(AccessPanel, { props: { ...base, ...props }, global: { plugins: [router] } })

describe('AccessPanel', () => {
  it('emits add with the chosen option and resets the select', async () => {
    const w = mountPanel()
    const add = w.find('button[type=submit]')
    expect(add.attributes('disabled')).toBeDefined()
    await w.find('select').setValue('g2')
    await add.trigger('submit')
    await w.find('form').trigger('submit')
    expect(w.emitted('add')).toEqual([['g2']])
    expect((w.find('select').element as HTMLSelectElement).value).toBe('')
  })

  it('emits remove with the row id', async () => {
    const w = mountPanel()
    await w.find('button[aria-label="Remove from group: ci"]').trigger('click')
    expect(w.emitted('remove')).toEqual([['g1']])
  })

  it('shows the empty text and hides the form when nothing can be added', () => {
    const w = mountPanel({ rows: [], options: [] })
    expect(w.find('[data-testid=access-empty]').text()).toBe('No groups')
    expect(w.find('form').exists()).toBe(false)
  })

  it('shows an error instead of the rows', () => {
    const w = mountPanel({ error: 'boom' })
    expect(w.find('[role=alert]').text()).toBe('boom')
    expect(w.find('li').exists()).toBe(false)
  })
})
