// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import RepoFederationTab from '../RepoFederationTab.vue'
import type { FederationPeer, FederationStatus, FederationDeferredUnit } from '../../api/federation'
import type { Repository } from '../../api/repositories'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const REPO = { id: 'r1', name: 'libs', format: 'npm', repo_type: 'hosted' } as Repository

const peer = (over: Partial<FederationPeer> = {}): FederationPeer => ({
  id: 'p1',
  name: 'site-b',
  base_url: 'https://b.example.org',
  enabled: true,
  state: 'ok',
  cursor_seq: 10,
  remote_head_seq: 10,
  backlog_units: 0,
  lag_seconds: 0,
  last_error: '',
  error_count: 0,
  applied_total: 5,
  conflicts_total: 1,
  deferred_units: 0,
  resync_requested: false,
  has_ca_pem: false,
  has_client_cert: false,
  created_at: '2026-10-01T00:00:00Z',
  ...over,
})

const baseStatus = (over: Partial<FederationStatus> = {}): FederationStatus => ({
  enabled: true,
  supported: true,
  instance_id: 'inst-1',
  log_id: 'log-1',
  head_seq: 42,
  units: 40,
  tombstones: 2,
  peers: [],
  tokens: [],
  ...over,
})

interface Call {
  method: string
  path: string
  body: Record<string, unknown> | null
}

interface Server {
  status: FederationStatus
  deferred: FederationDeferredUnit[]
  calls: Call[]
}

let fetchSpy: Mock<typeof fetch>
let wrapper: VueWrapper | null = null

/** A tiny in-memory federation backend behind fetch. */
function serve(initial: FederationStatus, deferred: FederationDeferredUnit[] = []): Server {
  const server: Server = { status: structuredClone(initial), deferred, calls: [] }
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation((async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    const path = String(url).replace('/api/v1/repositories/libs/federation', '') || '/'
    const body = init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : null
    server.calls.push({ method, path, body })
    const s = server.status
    if (path === '/' && method === 'GET') return json(s)
    if (path === '/' && method === 'PUT') {
      s.enabled = body!.enabled as boolean
      if (!s.enabled) s.peers = []
      return json(s)
    }
    if (path === '/peers' && method === 'POST') {
      const created = peer({ id: 'new', name: String(body!.name), base_url: String(body!.base_url), state: 'pending' })
      s.peers = [...s.peers, created]
      return json(created, 201)
    }
    const resync = /^\/peers\/([^/]+)\/resync$/.exec(path)
    if (resync && method === 'POST') {
      s.peers = s.peers.map((p) => (p.id === resync[1] ? { ...p, resync_requested: true } : p))
      return json(s.peers.find((p) => p.id === resync[1]), 202)
    }
    if (/\/deferred$/.test(path)) return json({ items: server.deferred })
    const one = /^\/peers\/([^/]+)$/.exec(path)
    if (one && method === 'DELETE') {
      s.peers = s.peers.filter((p) => p.id !== one[1])
      return new Response(null, { status: 204 })
    }
    if (one && method === 'PATCH') {
      s.peers = s.peers.map((p) => (p.id === one[1] ? { ...p, ...(body as Partial<FederationPeer>) } : p))
      return json(s.peers.find((p) => p.id === one[1]))
    }
    if (path === '/tokens' && method === 'POST') {
      s.tokens = [...s.tokens, { id: 'tk', name: String(body!.name), created_at: '2026-10-02T00:00:00Z' }]
      return json({ id: 'tk', name: body!.name, token: 'fed_secret_value', created_at: '2026-10-02T00:00:00Z' }, 201)
    }
    const tok = /^\/tokens\/([^/]+)$/.exec(path)
    if (tok && method === 'DELETE') {
      s.tokens = s.tokens.map((x) => (x.id === tok[1] ? { ...x, revoked_at: '2026-10-03T00:00:00Z' } : x))
      return new Response(null, { status: 204 })
    }
    return json({ error: { message: 'unexpected' } }, 500)
  }) as never)
  return server
}

async function mountTab(): Promise<VueWrapper> {
  wrapper = mount(RepoFederationTab, { props: { repo: REPO }, global: { plugins: [createPinia()] }, attachTo: document.body })
  await flushPromises()
  return wrapper
}

const calls = (server: Server, method: string, path: string) =>
  server.calls.filter((c) => c.method === method && c.path === path)

// Dialogs render into a portal after the page, so the last match is the dialog's button.
const dialogButton = (label: string): HTMLButtonElement | undefined =>
  [...document.body.querySelectorAll('button')].reverse().find((b) => b.textContent?.trim() === label)

function type(selector: string, value: string): void {
  const el = document.body.querySelector<HTMLInputElement | HTMLTextAreaElement>(selector)!
  el.value = value
  el.dispatchEvent(new Event('input'))
}

afterEach(() => {
  wrapper?.unmount()
  wrapper = null
  fetchSpy?.mockRestore()
  document.body.innerHTML = ''
})

describe('RepoFederationTab', () => {
  it('explains why federation is unavailable and disables the toggle', async () => {
    serve(baseStatus({ enabled: false, supported: false }))
    const w = await mountTab()
    expect(w.get('[data-testid="federation-unsupported"]').text()).toContain('not available')
    expect(w.get('[data-testid="federation-toggle"]').attributes('disabled')).toBeDefined()
    expect(w.find('[data-testid="federation-peers"]').exists()).toBe(false)
  })

  it('enables federation with a PUT and tells the parent', async () => {
    const server = serve(baseStatus({ enabled: false }))
    const w = await mountTab()
    expect(w.find('[data-testid="federation-stats"]').exists()).toBe(false)
    await w.get('[data-testid="federation-toggle"]').trigger('click')
    await flushPromises()
    expect(calls(server, 'PUT', '/')[0]!.body).toEqual({ enabled: true })
    expect(w.emitted('toggled')).toEqual([[true]])
    expect(w.get('[data-testid="federation-stats"]').text()).toContain('inst-1')
  })

  it('asks before disabling and only then sends the request', async () => {
    const server = serve(baseStatus({ peers: [peer()] }))
    const w = await mountTab()
    await w.get('[data-testid="federation-toggle"]').trigger('click')
    await flushPromises()
    expect(calls(server, 'PUT', '/')).toHaveLength(0)
    dialogButton('Turn off')!.click()
    await flushPromises()
    expect(calls(server, 'PUT', '/')[0]!.body).toEqual({ enabled: false })
    expect(w.emitted('toggled')).toEqual([[false]])
    expect(w.find('[data-testid="federation-peers"]').exists()).toBe(false)
  })

  it('shows peer health: state, humanised lag, backlog, counts and last success', async () => {
    serve(
      baseStatus({
        peers: [
          peer({
            state: 'lagging',
            lag_seconds: 3900,
            backlog_units: 12,
            applied_total: 345,
            conflicts_total: 6,
            last_success_at: '2026-10-22T10:00:00Z',
          }),
        ],
      }),
    )
    const w = await mountTab()
    const row = w.get('[data-testid="federation-peer-site-b"]').text()
    expect(row).toContain('Lagging')
    expect(row).toContain('1 hr 5 min')
    expect(row).toContain('12 to apply')
    expect(row).toContain('345 / 6')
    expect(row).not.toContain('Never')
  })

  it('truncates a long last error and expands the full text on demand', async () => {
    const long = `dial tcp: ${'connection refused '.repeat(12)}END`
    serve(baseStatus({ peers: [peer({ state: 'error', last_error: long, error_count: 3 })] }))
    const w = await mountTab()
    const row = () => w.get('[data-testid="federation-peer-site-b"]')
    expect(row().text()).not.toContain('END')
    expect(row().text()).toContain('3 consecutive failures')
    await row().findAll('button').find((b) => b.text() === 'Show more')!.trigger('click')
    expect(row().text()).toContain('END')
    await row().findAll('button').find((b) => b.text() === 'Show less')!.trigger('click')
    expect(row().text()).not.toContain('END')
  })

  it('drills into the deferred units of a peer', async () => {
    const server = serve(baseStatus({ peers: [peer({ deferred_units: 2 })] }), [
      { unit_key: 'npm/left-pad', seq: 7, attempts: 3, reason: 'missing dependency', last_error: '', first_seen_at: '2026-10-22T09:00:00Z', next_attempt_at: '2026-10-22T10:00:00Z' },
      { unit_key: 'npm/is-odd', seq: 9, attempts: 1, reason: 'retry', last_error: 'timeout', first_seen_at: '2026-10-22T09:00:00Z', next_attempt_at: '2026-10-22T10:05:00Z' },
    ])
    const w = await mountTab()
    await w.findAll('button').find((b) => b.text() === 'View 2 deferred')!.trigger('click')
    await flushPromises()
    expect(calls(server, 'GET', '/peers/p1/deferred')).toHaveLength(1)
    const table = document.body.querySelector('[data-testid="federation-deferred"]')!.textContent!
    expect(table).toContain('npm/left-pad')
    expect(table).toContain('missing dependency')
    expect(table).toContain('timeout')
  })

  it('validates the peer form and posts the trimmed values once complete', async () => {
    const server = serve(baseStatus())
    const w = await mountTab()
    await w.findAll('button').find((b) => b.text() === 'Add peer')!.trigger('click')
    await flushPromises()

    document.body.querySelector<HTMLFormElement>('#federation-peer-form')!.dispatchEvent(new Event('submit'))
    await flushPromises()
    expect(calls(server, 'POST', '/peers')).toHaveLength(0)
    expect(document.body.textContent).toContain('Enter a name.')
    expect(document.body.textContent).toContain('Enter an http:// or https:// URL.')

    type('#fed-peer-name', ' site-c ')
    type('#fed-peer-url', 'https://c.example.org')
    type('#fed-peer-token', 'peer-token')
    document.body.querySelector<HTMLFormElement>('#federation-peer-form')!.dispatchEvent(new Event('submit'))
    await flushPromises()

    expect(calls(server, 'POST', '/peers')[0]!.body).toEqual({
      name: 'site-c',
      base_url: 'https://c.example.org',
      token: 'peer-token',
    })
    expect(w.get('[data-testid="federation-peer-site-c"]').text()).toContain('Pending')
  })

  it('edits only what changed and never sends an empty token', async () => {
    const server = serve(baseStatus({ peers: [peer()] }))
    const w = await mountTab()
    await w.get('[aria-label="Edit peer site-b"]').trigger('click')
    await flushPromises()
    expect(document.body.querySelector<HTMLInputElement>('#fed-peer-token')!.value).toBe('')
    type('#fed-peer-url', 'https://b2.example.org')
    document.body.querySelector<HTMLFormElement>('#federation-peer-form')!.dispatchEvent(new Event('submit'))
    await flushPromises()
    expect(calls(server, 'PATCH', '/peers/p1')[0]!.body).toEqual({ base_url: 'https://b2.example.org' })
  })

  it('disables a peer from its row switch', async () => {
    const server = serve(baseStatus({ peers: [peer()] }))
    const w = await mountTab()
    await w.get('[aria-label="Poll peer site-b"]').trigger('click')
    await flushPromises()
    expect(calls(server, 'PATCH', '/peers/p1')[0]!.body).toEqual({ enabled: false })
  })

  it('resyncs after confirmation and shows the pending state', async () => {
    const server = serve(baseStatus({ peers: [peer()] }))
    const w = await mountTab()
    await w.get('[aria-label="Resync peer site-b"]').trigger('click')
    await flushPromises()
    expect(calls(server, 'POST', '/peers/p1/resync')).toHaveLength(0)
    dialogButton('Resync')!.click()
    await flushPromises()
    expect(calls(server, 'POST', '/peers/p1/resync')).toHaveLength(1)
    const row = w.get('[data-testid="federation-peer-site-b"]')
    expect(row.text()).toContain('Resync pending')
    expect(row.get('[aria-label="Resync peer site-b"]').attributes('disabled')).toBeDefined()
  })

  it('deletes a peer only after confirmation', async () => {
    const server = serve(baseStatus({ peers: [peer()] }))
    const w = await mountTab()
    await w.get('[aria-label="Delete peer site-b"]').trigger('click')
    await flushPromises()
    expect(calls(server, 'DELETE', '/peers/p1')).toHaveLength(0)
    dialogButton('Delete')!.click()
    await flushPromises()
    expect(calls(server, 'DELETE', '/peers/p1')).toHaveLength(1)
    expect(w.find('[data-testid="federation-peer-site-b"]').exists()).toBe(false)
  })

  it('creates an inbound token and shows its secret once', async () => {
    const server = serve(baseStatus())
    const w = await mountTab()
    expect(w.get('#fed-token-name').exists()).toBe(true)
    await w.get('#fed-token-name').setValue('site-b')
    await w.get('#fed-token-name').element.closest('form')!.dispatchEvent(new Event('submit'))
    await flushPromises()
    expect(calls(server, 'POST', '/tokens')[0]!.body).toEqual({ name: 'site-b' })
    expect(document.body.querySelector('[data-testid="token-secret"]')!.textContent).toBe('fed_secret_value')
    expect(w.get('[data-testid="federation-tokens"]').text()).toContain('site-b')

    dialogButton('Close')!.click()
    await flushPromises()
    expect(document.body.textContent).not.toContain('fed_secret_value')
  })

  it('revokes a token after confirmation and marks it revoked', async () => {
    const server = serve(baseStatus({ tokens: [{ id: 'tk', name: 'site-b', created_at: '2026-10-02T00:00:00Z' }] }))
    const w = await mountTab()
    await w.get('[aria-label="Revoke token site-b"]').trigger('click')
    await flushPromises()
    expect(calls(server, 'DELETE', '/tokens/tk')).toHaveLength(0)
    dialogButton('Revoke')!.click()
    await flushPromises()
    expect(calls(server, 'DELETE', '/tokens/tk')).toHaveLength(1)
    expect(w.get('[data-testid="federation-tokens"]').text()).toContain('Revoked')
    expect(w.find('[aria-label="Revoke token site-b"]').exists()).toBe(false)
  })
})
