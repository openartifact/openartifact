// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import SearchPage from '../SearchPage.vue'
import * as searchApi from '../../api/search'

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/search', component: SearchPage },
      { path: '/:rest(.*)*', component: { template: '<div/>' } },
    ],
  })
}

describe('SearchPage ?q=', () => {
  it('runs the search carried by the URL and shows it in the box', async () => {
    const search = spyOn(searchApi, 'search').mockResolvedValue({
      repositories: [],
      packages: [{ id: '1', name: 'lodash', version: '4.17.21', format: 'npm', description: '', repository: 'npmjs' }],
    })
    const router = makeRouter()
    await router.push('/search?q=lodash')
    const wrapper = mount(SearchPage, { global: { plugins: [router] } })
    await flushPromises()

    expect(search).toHaveBeenCalledTimes(1)
    expect(search.mock.calls[0]![0]).toMatchObject({ q: 'lodash' })
    expect((wrapper.get('input').element as HTMLInputElement).value).toBe('lodash')
    expect(wrapper.text()).toContain('4.17.21')
  })

  it('follows a query-only navigation without remounting', async () => {
    const search = spyOn(searchApi, 'search').mockResolvedValue({ repositories: [], packages: [] })
    const router = makeRouter()
    await router.push('/search?q=one')
    mount(SearchPage, { global: { plugins: [router] } })
    await flushPromises()

    await router.push('/search?q=two')
    await flushPromises()

    expect(search.mock.calls.map((c) => c[0].q)).toEqual(['one', 'two'])
  })

  it('does not search without a query', async () => {
    const search = spyOn(searchApi, 'search').mockResolvedValue({ repositories: [], packages: [] })
    const router = makeRouter()
    await router.push('/search')
    mount(SearchPage, { global: { plugins: [router] } })
    await flushPromises()

    expect(search).not.toHaveBeenCalled()
  })
})
