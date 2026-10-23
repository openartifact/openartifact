// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { spyOn } from 'bun:test'
import { flushPromises } from '@vue/test-utils'
import type { VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'

export const REPO = 'main'

const jsonResponse = (body: unknown): Response =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

export interface RecordedCall {
  method: string
  path: string
  search: URLSearchParams
  body: unknown
}

/** A router for a security page, already navigated to `path` (the pages read and write the URL query). */
export async function makeRouter(path: string): Promise<Router> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }],
  })
  await router.push(path)
  await router.isReady()
  return router
}

/**
 * Routes the API calls the security pages make: the repository list, the
 * package list and one scans endpoint. `extra` answers any other path.
 */
export function mockSecurityApi(scansPath: string, scans: unknown[], extra: (call: RecordedCall) => unknown = () => ({})) {
  const calls: RecordedCall[] = []
  spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
    const url = new URL(String(input), 'http://localhost')
    const path = url.pathname
    const call: RecordedCall = {
      method: (init?.method ?? 'GET').toUpperCase(),
      path,
      search: url.searchParams,
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    }
    calls.push(call)
    if (path === '/api/v1/repositories') return jsonResponse({ total: 1, repositories: [{ name: REPO }] })
    if (path === `/api/v1/repositories/${REPO}/packages`) {
      return jsonResponse({ total: 1, packages: [{ id: 'art-1', name: 'left-pad', version: '1.0.0', format: 'npm' }] })
    }
    if (path === scansPath && call.method === 'GET') return jsonResponse({ scans })
    return jsonResponse(extra(call))
  })
  return calls
}

/** Picks the repository in the page's repository selector and waits for the scans to load. */
export async function selectRepo(wrapper: VueWrapper): Promise<void> {
  await flushPromises()
  const select = wrapper.get('select')
  await select.setValue(REPO)
  await flushPromises()
}
