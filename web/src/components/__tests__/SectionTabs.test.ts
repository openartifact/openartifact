// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { createPinia } from 'pinia'
import SectionTabs from '../SectionTabs.vue'
import RepoHeader from '../RepoHeader.vue'
import ProjectHeader from '../ProjectHeader.vue'
import type { Project } from '../../api/projects'

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
  it('RepoHeader and ProjectHeader both render their sections through SectionTabs', () => {
    const repo = mount(RepoHeader, {
      props: { name: 'r', repo: null, active: 'main' },
      global: { plugins: [makeRouter(), createPinia()] },
    })
    expect(repo.findComponent(SectionTabs).exists()).toBe(true)

    const project: Project = { name: 'p', my_role: 'viewer' }
    const proj = mount(ProjectHeader, {
      props: { name: 'p', project, active: 'overview' },
      global: { plugins: [makeRouter(), createPinia()] },
    })
    expect(proj.findComponent(SectionTabs).exists()).toBe(true)
  })

  it('ProjectHeader lists no tabs until the project is loaded', () => {
    const wrapper = mount(ProjectHeader, {
      props: { name: 'p', project: null, active: 'overview' },
      global: { plugins: [makeRouter(), createPinia()] },
    })
    expect(wrapper.findComponent(SectionTabs).exists()).toBe(false)
  })

  it('ProjectHeader links the overview without a tab query and the rest with ?tab=', () => {
    const wrapper = mount(ProjectHeader, {
      props: { name: 'my p', project: { name: 'my p', my_role: 'owner' }, active: 'members', counts: { members: 4 } },
      global: { plugins: [makeRouter(), createPinia()] },
    })
    const hrefs = wrapper.findAll('nav a').map((a) => a.attributes('href'))
    expect(hrefs).toEqual([
      '/projects/my%20p',
      '/projects/my%20p?tab=repositories',
      '/projects/my%20p?tab=members',
      '/projects/my%20p?tab=tokens',
      '/projects/my%20p?tab=settings',
    ])
    expect(wrapper.find('nav a[aria-current="page"]').text()).toContain('Members')
    expect(wrapper.find('nav a[aria-current="page"]').text()).toContain('4')
  })
})
