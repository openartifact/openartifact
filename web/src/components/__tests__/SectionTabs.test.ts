// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { createPinia } from 'pinia'
import SectionTabs from '../SectionTabs.vue'
import RepoHeader from '../RepoHeader.vue'

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }],
  })
}

const items = [
  { id: 'one', label: 'One', to: '/x', count: 3 },
  { id: 'two', label: 'Two', to: { path: '/x', query: { tab: 'two' } } },
]

describe('SectionTabs', () => {
  it('renders one link per item with the count and marks the active one', () => {
    const wrapper = mount(SectionTabs, {
      props: { items, active: 'two', label: 'Sections' },
      global: { plugins: [makeRouter()] },
    })
    expect(wrapper.find('nav').attributes('aria-label')).toBe('Sections')
    const links = wrapper.findAll('a')
    expect(links.map((a) => a.text())).toEqual(['One 3', 'Two'])
    expect(links[0]!.attributes('href')).toBe('/x')
    expect(links[1]!.attributes('href')).toBe('/x?tab=two')
    expect(links[0]!.attributes('aria-current')).toBeUndefined()
    expect(links[1]!.attributes('aria-current')).toBe('page')
  })

  it('shows no count badge when the item has none', () => {
    const wrapper = mount(SectionTabs, {
      props: { items, active: 'one', label: 'Sections' },
      global: { plugins: [makeRouter()] },
    })
    expect(wrapper.findAll('a')[1]!.find('.font-mono').exists()).toBe(false)
  })
})

describe('the one tab convention', () => {
  it('RepoHeader renders its sections through SectionTabs', () => {
    const repo = mount(RepoHeader, {
      props: { name: 'r', repo: null, active: 'main' },
      global: { plugins: [makeRouter(), createPinia()] },
    })
    expect(repo.findComponent(SectionTabs).exists()).toBe(true)
  })
})
