// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { defineComponent } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { preloadRouteChunks } from '../startup'

const Eager = defineComponent({ render: () => null })

function routerWith(loads: string[]) {
  const lazy = (name: string) => () => {
    loads.push(name)
    return Promise.resolve(Eager)
  }
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: lazy('home') },
      { path: '/repos/:name', component: lazy('repo') },
      { path: '/static', component: Eager },
      { path: '/broken', component: () => Promise.reject(new Error('chunk 404')) },
    ],
  })
}

describe('preloadRouteChunks', () => {
  it('starts the code of the page the location resolves to, and no other', async () => {
    const loads: string[] = []
    await preloadRouteChunks(routerWith(loads), '/repos/libs?tab=packages')
    expect(loads).toEqual(['repo'])
  })

  it('starts the chunk before the navigation, so the two share one download', async () => {
    const loads: string[] = []
    const router = routerWith(loads)
    const started = preloadRouteChunks(router, '/')
    expect(loads).toEqual(['home'])
    await started
  })

  it('ignores components that are not lazy and locations that match nothing', async () => {
    const loads: string[] = []
    const router = routerWith(loads)
    await preloadRouteChunks(router, '/static')
    await preloadRouteChunks(router, '/no/such/page')
    expect(loads).toEqual([])
  })

  it('never rejects: a failed chunk is reported by the navigation that needs it', async () => {
    await expect(preloadRouteChunks(routerWith([]), '/broken')).resolves.toBeUndefined()
  })
})
