// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import EmptyState from '../EmptyState.vue'

describe('EmptyState', () => {
  it('renders the title and description only by default', () => {
    const w = mount(EmptyState, { props: { title: 'Nothing here', description: 'Add one.' } })
    expect(w.get('h3').text()).toBe('Nothing here')
    expect(w.text()).toContain('Add one.')
    expect(w.find('[data-testid="docs-link"]').exists()).toBe(false)
    expect(w.find('[data-testid="empty-snippet"]').exists()).toBe(false)
    expect(w.find('a').exists()).toBe(false)
  })

  it('links to the docs in a new tab with the default label', () => {
    const w = mount(EmptyState, { props: { title: 'Nothing here', docsUrl: 'https://docs.example.com/v1/security/sso' } })
    const link = w.get('[data-testid="docs-link"]')
    expect(link.attributes('href')).toBe('https://docs.example.com/v1/security/sso')
    expect(link.attributes('target')).toBe('_blank')
    expect(link.attributes('rel')).toBe('noopener')
    expect(link.text()).toBe('Read the documentation')
    expect(link.get('svg').attributes('aria-hidden')).toBe('true')
  })

  it('uses a custom docs label', () => {
    const w = mount(EmptyState, { props: { title: 'x', docsUrl: 'https://docs.example.com/', docsLabel: 'Client setup' } })
    expect(w.get('[data-testid="docs-link"]').text()).toBe('Client setup')
  })

  it('omits the docs link for a null or empty url (docs links disabled)', () => {
    for (const docsUrl of [null, '', undefined]) {
      const w = mount(EmptyState, { props: { title: 'x', docsUrl } })
      expect(w.find('[data-testid="docs-link"]').exists()).toBe(false)
    }
  })

  it('shows a command snippet verbatim', () => {
    const w = mount(EmptyState, { props: { title: 'x', snippet: 'oa login --url https://oa.example.com' } })
    expect(w.get('[data-testid="empty-snippet"]').text()).toBe('oa login --url https://oa.example.com')
  })

  it('renders the default slot for extra actions', () => {
    const w = mount(EmptyState, { props: { title: 'x' }, slots: { default: '<button id="extra">Add</button>' } })
    expect(w.find('#extra').exists()).toBe(true)
    expect(mount(EmptyState, { props: { title: 'x' } }).find('button').exists()).toBe(false)
  })

  it('keeps the action button next to the docs link', async () => {
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/new', component: { template: '<div />' } }] })
    await router.push('/new')
    const w = mount(EmptyState, {
      props: { title: 'x', actionLabel: 'Create', actionTo: '/new', docsUrl: 'https://docs.example.com/' },
      global: { plugins: [router] },
    })
    expect(w.text()).toContain('Create')
    expect(w.get('[data-testid="docs-link"]').exists()).toBe(true)
  })
})
