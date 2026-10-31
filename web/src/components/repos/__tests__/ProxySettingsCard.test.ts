// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, spyOn } from 'bun:test'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ProxySettingsCard from '../ProxySettingsCard.vue'
import type { Repository } from '../../../api/repositories'
import { useSessionStore, type User } from '../../../stores/session'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

interface Write {
  method: string
  path: string
  body: Record<string, unknown>
}

/** Stubs fetch for the PATCH; `answer` builds the response from the request body. */
function serve(answer: (body: Record<string, unknown>) => Response): Write[] {
  const writes: Write[] = []
  spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const body = JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>
    writes.push({ method: init?.method ?? 'GET', path: new URL(String(input), 'http://localhost').pathname, body })
    return answer(body)
  }) as typeof fetch)
  return writes
}

const proxy = (over: Partial<Repository> = {}): Repository =>
  ({
    id: 'r1',
    name: 'npm-proxy',
    format: 'npm',
    repo_type: 'proxy',
    upstream_url: 'https://registry.npmjs.org',
    upstream_username: 'svc',
    upstream_password_set: true,
    cache_ttl_seconds: 3600,
    negative_cache_ttl_seconds: 60,
    max_stale_seconds: 604800,
    min_age_hours: 0,
    blocked: false,
    ...over,
  }) as Repository

function mountCard(repo: Repository, user: { admin?: boolean } = {}): VueWrapper {
  const pinia = createPinia()
  setActivePinia(pinia)
  useSessionStore().user = { id: 'u', username: 'ada', is_admin: user.admin ?? false } as User
  return mount(ProxySettingsCard, { props: { repo }, global: { plugins: [pinia] } })
}

const input = (w: VueWrapper, id: string) => w.find(`#${id}`)
const submitButton = (w: VueWrapper) => w.find('form button[type="submit"]')
const submitDisabled = (w: VueWrapper) => (submitButton(w).element as HTMLButtonElement).disabled
const open = async (w: VueWrapper) => {
  await w.find('[data-testid="edit-proxy-settings"]').trigger('click')
}
const submit = async (w: VueWrapper) => {
  await w.find('form').trigger('submit')
  await flushPromises()
}
const modeButton = (w: VueWrapper, label: string) => w.findAll('[data-testid="password-mode"] button').find((b) => b.text() === label)!

describe('ProxySettingsCard: who may edit', () => {
  it('shows the settings read-only to someone who may not PATCH the repository', () => {
    const w = mountCard(proxy())
    expect(w.find('[data-testid="edit-proxy-settings"]').exists()).toBe(false)
    expect(w.text()).toContain('https://registry.npmjs.org')
    expect(w.text()).toContain('1 hour')
  })

  it('offers the form to a global administrator and to a maintainer of the repository project', () => {
    expect(mountCard(proxy(), { admin: true }).find('[data-testid="edit-proxy-settings"]').exists()).toBe(true)
    expect(mountCard(proxy({ permissions: { admin: true } as Repository['permissions'] })).find('[data-testid="edit-proxy-settings"]').exists()).toBe(true)
  })

  it('lets only a global administrator change blocked', async () => {
    const maintainer = mountCard(proxy({ permissions: { admin: true } as Repository['permissions'] }))
    await open(maintainer)
    expect(maintainer.find('[data-testid="blocked-toggle"]').attributes('disabled')).toBeDefined()

    const admin = mountCard(proxy(), { admin: true })
    await open(admin)
    expect(admin.find('[data-testid="blocked-toggle"]').attributes('disabled')).toBeUndefined()
  })

  it('shows whether the repository is blocked', () => {
    expect(mountCard(proxy({ blocked: true })).find('[data-testid="proxy-blocked"]').text()).toContain('Yes')
    expect(mountCard(proxy()).find('[data-testid="proxy-blocked"]').text()).toBe('No')
  })
})

describe('ProxySettingsCard: the form', () => {
  it('is seeded from the repository, never with the password, and Save waits for a change', async () => {
    const w = mountCard(proxy(), { admin: true })
    await open(w)
    expect((input(w, 'ps-upstream-url').element as HTMLInputElement).value).toBe('https://registry.npmjs.org')
    expect((input(w, 'ps-upstream-user').element as HTMLInputElement).value).toBe('svc')
    expect((input(w, 'ps-cache-ttl').element as HTMLInputElement).value).toBe('3600')
    expect(input(w, 'ps-upstream-pass').exists()).toBe(false)
    expect(submitDisabled(w)).toBe(true)
  })

  it('shows durations next to the numeric inputs and says what an empty or zero value means', async () => {
    const w = mountCard(proxy(), { admin: true })
    await open(w)
    expect(w.text()).toContain('= 1 hour')
    await input(w, 'ps-cache-ttl').setValue('5400')
    expect(w.text()).toContain('= 1 hour 30 minutes')
    await input(w, 'ps-max-stale').setValue('')
    expect(w.text()).toContain('Default: 7 days')
    await input(w, 'ps-max-stale').setValue('0')
    expect(w.text()).toContain('Never serves a stale copy')
    await input(w, 'ps-min-age').setValue('48')
    expect(w.text()).toContain('= 2 days')
  })

  it('blocks Save and names the field when a value is outside the server range', async () => {
    const writes = serve(() => json(proxy()))
    const w = mountCard(proxy(), { admin: true })
    await open(w)
    await input(w, 'ps-min-age').setValue('87601')
    expect(submitDisabled(w)).toBe(true)
    expect(w.find('#ps-min-age-error').text()).toContain('87,600')
    await input(w, 'ps-min-age').setValue('24')
    await input(w, 'ps-upstream-url').setValue('ftp://x')
    expect(w.find('#ps-upstream-url-error').exists()).toBe(true)
    await submit(w)
    expect(writes).toHaveLength(0)
  })

  it('PATCHes only the changed fields, closes the form and reports the saved repository', async () => {
    const writes = serve((body) => json(proxy({ cache_ttl_seconds: body.cache_ttl_seconds as number, max_stale_seconds: 0 })))
    const w = mountCard(proxy(), { admin: true })
    await open(w)
    await input(w, 'ps-cache-ttl').setValue('120')
    await input(w, 'ps-max-stale').setValue('0')
    await submit(w)

    expect(writes).toEqual([{ method: 'PATCH', path: '/api/v1/repository/npm-proxy', body: { cache_ttl_seconds: 120, max_stale_seconds: 0 } }])
    expect(w.emitted('saved')).toHaveLength(1)
    expect((w.emitted('saved')![0][0] as Repository).cache_ttl_seconds).toBe(120)
    expect(w.find('form').exists()).toBe(false)
  })

  it('leaves blocked out of a maintainer request and sends it for an administrator', async () => {
    const writes = serve(() => json(proxy({ blocked: true })))
    const admin = mountCard(proxy(), { admin: true })
    await open(admin)
    await admin.find('[data-testid="blocked-toggle"]').trigger('click')
    await submit(admin)
    expect(writes[0].body).toEqual({ blocked: true })
  })

  it('cancel closes the form without a request', async () => {
    const writes = serve(() => json(proxy()))
    const w = mountCard(proxy(), { admin: true })
    await open(w)
    await input(w, 'ps-cache-ttl').setValue('120')
    await w.find('[data-testid="proxy-cancel"]').trigger('click')
    expect(w.find('form').exists()).toBe(false)
    expect(writes).toHaveLength(0)
  })
})

describe('ProxySettingsCard: the upstream password', () => {
  it('keeps the stored password by default: the request has no password field', async () => {
    const writes = serve(() => json(proxy()))
    const w = mountCard(proxy(), { admin: true })
    await open(w)
    await input(w, 'ps-cache-ttl').setValue('120')
    await submit(w)
    expect('upstream_password' in writes[0].body).toBe(false)
  })

  it('replaces it with the typed one', async () => {
    const writes = serve(() => json(proxy()))
    const w = mountCard(proxy(), { admin: true })
    await open(w)
    await modeButton(w, 'Replace').trigger('click')
    expect(submitDisabled(w)).toBe(true)
    expect(w.text()).toContain('Type the new password')
    await input(w, 'ps-upstream-pass').setValue('s3cret')
    await submit(w)
    expect(writes[0].body).toEqual({ upstream_password: 's3cret' })
  })

  it('removes it by sending an empty password', async () => {
    const writes = serve(() => json(proxy({ upstream_password_set: false })))
    const w = mountCard(proxy(), { admin: true })
    await open(w)
    await modeButton(w, 'Remove').trigger('click')
    await submit(w)
    expect(writes[0].body).toEqual({ upstream_password: '' })
  })

  it('offers just a password field when none is stored, and sets it when typed', async () => {
    const writes = serve(() => json(proxy()))
    const w = mountCard(proxy({ upstream_password_set: false }), { admin: true })
    await open(w)
    expect(w.find('[data-testid="password-mode"]').exists()).toBe(false)
    await input(w, 'ps-upstream-pass').setValue('new')
    await submit(w)
    expect(writes[0].body).toEqual({ upstream_password: 'new' })
  })

  it('warns before a new host or username drops the stored password', async () => {
    const w = mountCard(proxy(), { admin: true })
    await open(w)
    expect(w.find('[data-testid="password-drop"]').exists()).toBe(false)
    await input(w, 'ps-upstream-url').setValue('https://mirror.example.org')
    expect(w.find('[data-testid="password-drop"]').exists()).toBe(true)
    await modeButton(w, 'Replace').trigger('click')
    expect(w.find('[data-testid="password-drop"]').exists()).toBe(false)
  })
})

describe('ProxySettingsCard: server errors', () => {
  it('shows a validation error under the field the API names and keeps the form open', async () => {
    serve(() => json({ error: 'upstream_url is not allowed: host resolves to a private address', code: 'validation_failed' }, 400))
    const w = mountCard(proxy(), { admin: true })
    await open(w)
    await input(w, 'ps-upstream-url').setValue('http://10.0.0.5')
    await submit(w)
    expect(w.find('#ps-upstream-url-error').text()).toContain('private address')
    expect(w.find('[data-testid="proxy-form-error"]').exists()).toBe(false)
    expect(w.emitted('saved')).toBeUndefined()

    await input(w, 'ps-upstream-url').setValue('https://registry.example.org')
    expect(w.find('#ps-upstream-url-error').exists()).toBe(false)
  })

  it('shows an error that names no field above the buttons', async () => {
    serve(() => json({ error: 'only an administrator can block a repository', code: 'forbidden' }, 403))
    const w = mountCard(proxy(), { admin: true })
    await open(w)
    await input(w, 'ps-cache-ttl').setValue('120')
    await submit(w)
    expect(w.find('[data-testid="proxy-form-error"]').text()).toContain('only an administrator')
  })
})
