// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { spyOn } from 'bun:test'
import { flushPromises } from '@vue/test-utils'
import type { VueWrapper } from '@vue/test-utils'

export const REPO = 'main'

const jsonResponse = (body: unknown): Response =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

export interface RecordedCall {
  method: string
  path: string
  body: unknown
}

/**
 * Routes the API calls the security pages make: the repository list, the
 * package list and one scans endpoint. `extra` answers any other path.
 */
export function mockSecurityApi(scansPath: string, scans: unknown[], extra: (call: RecordedCall) => unknown = () => ({})) {
  const calls: RecordedCall[] = []
  spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
    const path = new URL(String(input), 'http://localhost').pathname
    const call: RecordedCall = {
      method: (init?.method ?? 'GET').toUpperCase(),
      path,
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
