// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The demo build (vite build --mode demo) answers every API call from the in-memory backend. The
// curated pages below must work completely there: this mounts the real app on each of them over a
// fresh backend and fails on any call the backend does not answer, or answers with an error. A new
// API call on a curated page fails here until the demo serves it.
import { afterEach, describe, expect, it } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { setTransport } from '../../api/client'
import App from '../../App.vue'
import router from '../../router'
import { createDemoBackend } from '../backend'

/** The pages the demo promises, with the tabs that load data of their own. */
const CURATED = [
  '/',
  '/repos',
  '/repos?format=npm',
  '/repos?type=proxy',
  '/repos?q=acme',
  '/repos/npm-internal',
  '/repos/npm-internal?tab=activity',
  '/repos/npm-internal?tab=security',
  '/repos/npm-internal?tab=settings',
  '/repos/npm',
  '/repos/npm?tab=settings',
  '/repos/npmjs',
  '/repos/npmjs?tab=settings',
  '/repos/containers',
  '/repos/npm-internal/packages/@acme/ui-kit',
  '/repos/npm-internal/packages/@acme/ui-kit?tab=versions',
  '/repos/npm-internal/packages/@acme/ui-kit?tab=files',
  '/repos/npm-internal/packages/@acme/ui-kit?tab=dependencies',
  '/repos/npm-internal/packages/@acme/ui-kit?tab=security',
  '/repos/npm-internal/packages/@acme/ui-kit?tab=activity',
  '/repos/npm-internal/packages/@acme/ui-kit/versions/2.9.1',
  '/repos/npmjs/packages/express',
  '/repos/containers/packages/payments/api',
  '/repos/containers/packages/payments/api/versions/2.3.0',
  '/repos/maven-releases/packages/com.acme.payments:payments-core',
  '/repos/debian/packages/acme-agent',
  '/packages',
  '/packages?q=acme',
  '/security',
  '/settings',
  '/settings/users',
  '/settings/groups',
  '/projects',
  '/projects/payments',
]

const UNAVAILABLE = 'Not available in the demo'
const QUIET_ROUNDS = 10
const DEADLINE_MS = 5000

afterEach(() => setTransport())

/** A fresh backend under the app's API client, recording every call it fails. */
function installBackend() {
  const backend = createDemoBackend()
  const probe = { backend, failed: [] as string[], inflight: 0, calls: 0 }
  setTransport(async (url, init) => {
    probe.calls++
    probe.inflight++
    try {
      const res = await backend.transport(url, init)
      if (!res.ok) probe.failed.push(`${init.method ?? 'GET'} ${url} → ${res.status}`)
      return res
    } finally {
      probe.inflight--
    }
  })
  return probe
}

/**
 * Waits until every call is answered and none starts for QUIET_ROUNDS polls. Real (short) timers on
 * purpose: lazy route chunks, async tabs and debounced loads give the app no single "loaded" signal.
 */
async function settle(probe: ReturnType<typeof installBackend>): Promise<boolean> {
  const deadline = Date.now() + DEADLINE_MS
  let quiet = 0
  let seen = -1
  while (quiet < QUIET_ROUNDS && Date.now() < deadline) {
    await flushPromises()
    await new Promise((resolve) => setTimeout(resolve, 10))
    quiet = probe.inflight === 0 && probe.calls === seen ? quiet + 1 : 0
    seen = probe.calls
  }
  return quiet >= QUIET_ROUNDS
}

/** Mounts the app on `path` and lets it load. */
async function visit(path: string) {
  const probe = installBackend()
  const wrapper = mount(App, { global: { plugins: [createPinia(), router] }, attachTo: document.body })
  await router.push(path)
  return { probe, wrapper, settled: await settle(probe) }
}

function expectServed(probe: ReturnType<typeof installBackend>, wrapper: ReturnType<typeof mount>): void {
  expect(probe.backend.unhandled).toEqual([])
  expect(probe.failed).toEqual([])
  expect(wrapper.text()).not.toContain(UNAVAILABLE)
}

describe('demo backend serves the curated pages', () => {
  for (const path of CURATED) {
    it(path, async () => {
      const { probe, wrapper, settled } = await visit(path)
      expect(settled).toBe(true)
      expect(router.currentRoute.value.fullPath).toBe(path)
      expectServed(probe, wrapper)
    })
  }

  // Each finding's drawer (teleported to the body) loads the scan behind it.
  it('/security finding details', async () => {
    const { probe, wrapper } = await visit('/security')
    const rows = wrapper.findAll('button[data-testid^="finding-"]')
    expect(rows.length).toBeGreaterThan(0)
    const shown = new Set<string>()
    for (const row of rows) {
      await row.trigger('click')
      expect(await settle(probe)).toBe(true)
      for (const id of ['scan-detail', 'license-detail', 'secret-detail']) if (document.querySelector(`[data-testid="${id}"]`)) shown.add(id)
    }
    expect([...shown].sort()).toEqual(['license-detail', 'scan-detail', 'secret-detail'])
    expectServed(probe, wrapper)
  })
})
