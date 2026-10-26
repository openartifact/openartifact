// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import RepoSettings from '../RepoSettings.vue'
import type { Repository } from '../../../api/repositories'
import { useSessionStore, type User } from '../../../stores/session'
import { useToastStore } from '../../../stores/toast'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const USAGE = { package_count: 1, version_count: 1, size_bytes: 1, quota: { used_bytes: 1 } }

interface Call {
  method: string
  path: string
  query: URLSearchParams
  body: Record<string, unknown> | null
}

type Handler = (call: Call) => Response | undefined

/** Stubs fetch; `handler` answers the calls a test cares about, everything else is an empty 200. */
function serve(handler: Handler = () => undefined): Call[] {
  const calls: Call[] = []
  spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost')
    const raw = init?.body
    const call: Call = {
      method: init?.method ?? 'GET',
      path: url.pathname,
      query: url.searchParams,
      body: typeof raw === 'string' ? (JSON.parse(raw) as Record<string, unknown>) : null,
    }
    calls.push(call)
    if (call.path.endsWith('/usage')) return handler(call) ?? json(USAGE)
    return handler(call) ?? json({})
  }) as typeof fetch)
  return calls
}

const writes = (calls: Call[]) => calls.filter((c) => c.method !== 'GET')

const repo = (over: Partial<Repository> = {}): Repository =>
  ({ id: 'r1', name: 'libs', format: 'npm', repo_type: 'hosted', project: 'system', ...over }) as Repository

let router: Router

async function mountSettings(r: Repository, opts: { admin?: boolean } = {}): Promise<VueWrapper> {
  const pinia = createPinia()
  setActivePinia(pinia)
  useSessionStore().user = { id: 'u', username: 'ada', is_admin: opts.admin ?? false } as User
  router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { render: () => null } }] })
  await router.push('/repos/' + r.name)
  const wrapper = mount(RepoSettings, {
    props: { repo: r },
    global: {
      plugins: [pinia, router],
      // These tabs have their own tests; here only their presence matters.
      stubs: { RepoCleanupTab: true, RepoQuotaTab: true, RepoFederationTab: true },
    },
  })
  await flushPromises()
  return wrapper
}

const saveButton = (w: VueWrapper) => w.find('form button[type="submit"]')
const saveDisabled = (w: VueWrapper) => (saveButton(w).element as HTMLButtonElement).disabled
const row = (w: VueWrapper, id: string) => w.find(`[data-row="${id}"]`)

async function save(w: VueWrapper): Promise<void> {
  await w.find('form').trigger('submit')
  await flushPromises()
}

describe('RepoSettings: which fields each repository type offers', () => {
  it('a hosted repository has no upstream or members rows and its type is locked', async () => {
    const w = await mountSettings(repo())
    expect(row(w, 'upstream').exists()).toBe(false)
    expect(row(w, 'members').exists()).toBe(false)
    expect(row(w, 'type').find('[role="radiogroup"]').exists()).toBe(false)
    expect(w.find('#rl-upstream-url').exists()).toBe(false)
  })

  it('a proxy shows its upstream URL and username but never the stored password', async () => {
    const w = await mountSettings(
      repo({ repo_type: 'proxy', format: 'maven', upstream_url: 'https://repo.example.org/maven2', upstream_username: 'svc' }),
    )
    expect((w.find('#rl-upstream-url').element as HTMLInputElement).value).toBe('https://repo.example.org/maven2')
    expect((w.find('#rl-upstream-user').element as HTMLInputElement).value).toBe('svc')
    expect((w.find('#rl-upstream-pass').element as HTMLInputElement).value).toBe('')
  })

  it('a group lists as candidates only same-format non-group repositories other than itself', async () => {
    serve((c) =>
      c.path === '/api/v1/repositories'
        ? json({
            repositories: [
              repo({ id: 'a', name: 'npm-a' }),
              repo({ id: 'b', name: 'npm-b' }),
              repo({ id: 'c', name: 'npm-group2', repo_type: 'group' }),
              repo({ id: 'd', name: 'pypi-a', format: 'pypi' }),
              repo({ id: 'e', name: 'all-npm', repo_type: 'group' }),
            ],
            total: 5,
          })
        : undefined,
    )
    const w = await mountSettings(repo({ name: 'all-npm', repo_type: 'group', member_repos: ['npm-a'] }))
    const available = w.findAll('[data-testid="members-available"] li').map((l) => l.text())
    expect(available).toEqual(['npm-b'])
    expect(w.findAll('[data-testid="members-selected"] li').map((l) => l.text())).toEqual([expect.stringContaining('npm-a')])
  })

  it('shows the merge-proxy-versions policy on groups only, and locked for non-admins', async () => {
    serve()
    const hosted = await mountSettings(repo())
    expect(hosted.text()).not.toContain('Merge')
    const asMember = await mountSettings(repo({ repo_type: 'group' }))
    const box = asMember.findAll('input[type="checkbox"]').filter((c) => (c.element as HTMLInputElement).disabled)
    expect(box).toHaveLength(1)
    const asAdmin = await mountSettings(repo({ repo_type: 'group' }), { admin: true })
    expect(asAdmin.findAll('input[type="checkbox"]').some((c) => (c.element as HTMLInputElement).disabled)).toBe(false)
  })

  it('offers package signing only for hosted rpm repositories', async () => {
    serve()
    const labels = async (r: Repository) => (await mountSettings(r)).findAll('[data-row="policies"] input[type="checkbox"]').length
    expect(await labels(repo({ format: 'rpm' }))).toBe(2)
    expect(await labels(repo({ format: 'deb' }))).toBe(1)
    expect(await labels(repo({ format: 'rpm', repo_type: 'proxy', upstream_url: 'https://x.example' }))).toBe(1)
  })

  it('shows the federation section to admins for capable formats only', async () => {
    serve()
    expect((await mountSettings(repo(), { admin: true })).find('[data-testid="federation-off"]').exists()).toBe(true)
    expect((await mountSettings(repo(), { admin: false })).find('[data-testid="federation-off"]').exists()).toBe(false)
    expect((await mountSettings(repo({ format: 'rpm' }), { admin: true })).find('[data-testid="federation-off"]').exists()).toBe(false)
    const enabled = await mountSettings(repo({ format: 'rpm', federated: true }), { admin: true })
    expect(enabled.find('[data-testid="federation-off"]').exists()).toBe(false)
  })
})

describe('RepoSettings: saving', () => {
  it('keeps save disabled until something changes, and sends only hosted-allowed fields', async () => {
    const calls = serve((c) => (c.method === 'PATCH' ? json(repo({ description: 'new' })) : undefined))
    const w = await mountSettings(repo({ description: 'old' }))
    expect(saveDisabled(w)).toBe(true)
    await w.find('#rl-description').setValue('new')
    expect(saveDisabled(w)).toBe(false)
    await save(w)
    const [patch, ...rest] = writes(calls)
    expect(rest).toEqual([])
    expect(patch.method).toBe('PATCH')
    expect(patch.path).toBe('/api/v1/repository/libs')
    expect(patch.body).toEqual({
      description: 'new',
      public: false,
      presign_downloads: null,
      trash_retention_days: null,
      immutable_releases: false,
    })
    expect(w.emitted('saved')![0][0]).toMatchObject({ description: 'new' })
    expect(useToastStore().queue.map((t) => t.level)).toEqual(['success'])
  })

  it('sends sign_packages only for a hosted rpm repository', async () => {
    const calls = serve((c) => (c.method === 'PATCH' ? json(repo()) : undefined))
    const w = await mountSettings(repo({ format: 'rpm' }))
    await w.find('[data-row="policies"] input[type="checkbox"]').setValue(true)
    await save(w)
    expect(writes(calls)[0].body).toMatchObject({ sign_packages: true })
  })

  it('maps the trash retention input: empty resets to the default (null), 0 and N are sent, text is refused', async () => {
    const calls = serve((c) => (c.method === 'PATCH' ? json(repo()) : undefined))
    const w = await mountSettings(repo({ trash_retention_days: 7 }))
    expect((w.find('#rl-retention').element as HTMLInputElement).value).toBe('7')

    await w.find('#rl-retention').setValue('0')
    await save(w)
    expect(writes(calls)[0].body).toMatchObject({ trash_retention_days: 0 })

    await w.find('#rl-retention').setValue('12')
    await save(w)
    expect(writes(calls)[1].body).toMatchObject({ trash_retention_days: 12 })

    await w.find('#rl-retention').setValue('')
    await save(w)
    expect(writes(calls)[2].body).toMatchObject({ trash_retention_days: null })

    await w.find('#rl-retention').setValue('1.5')
    expect(saveDisabled(w)).toBe(true)
    await save(w)
    expect(writes(calls)).toHaveLength(3)
    await w.find('#rl-retention').setValue('-3')
    expect(saveDisabled(w)).toBe(true)
  })

  it('maps the presign choice to true, false or null', async () => {
    const calls = serve((c) => (c.method === 'PATCH' ? json(repo()) : undefined))
    const edit = async (stored: boolean | null, choice: string): Promise<void> => {
      const w = await mountSettings(repo({ presign_downloads: stored }))
      await w.find('#rl-presign').setValue(choice)
      await save(w)
    }
    await edit(null, 'always')
    await edit(null, 'never')
    await edit(true, 'inherit')
    expect(writes(calls).map((c) => c.body!.presign_downloads)).toEqual([true, false, null])
  })

  it('surfaces a failed save and keeps the edit', async () => {
    serve((c) => (c.method === 'PATCH' ? json({ error: 'description too long', code: 'validation_failed' }, 400) : undefined))
    const w = await mountSettings(repo())
    await w.find('#rl-description').setValue('x')
    await save(w)
    expect(w.find('[role="alert"]').text()).toBe('description too long')
    expect(w.emitted('saved')).toBeUndefined()
    expect(saveDisabled(w)).toBe(false)
    expect((w.find('#rl-description').element as HTMLTextAreaElement).value).toBe('x')
  })

  it('shows a saved-error again after a retry succeeds: the alert is cleared', async () => {
    let fail = true
    serve((c) => (c.method === 'PATCH' ? (fail ? json({ error: 'boom' }, 500) : json(repo())) : undefined))
    const w = await mountSettings(repo())
    await w.find('#rl-description').setValue('x')
    await save(w)
    expect(w.find('[role="alert"]').exists()).toBe(true)
    fail = false
    await save(w)
    expect(w.find('[role="alert"]').exists()).toBe(false)
    expect(w.emitted('saved')).toHaveLength(1)
  })

  it('re-seeds the form and clears the dirty state when the repository prop changes', async () => {
    serve()
    const w = await mountSettings(repo({ description: 'old' }))
    await w.find('#rl-description').setValue('typed')
    expect(saveDisabled(w)).toBe(false)
    await w.setProps({ repo: repo({ description: 'from server' }) })
    expect((w.find('#rl-description').element as HTMLTextAreaElement).value).toBe('from server')
    expect(saveDisabled(w)).toBe(true)
  })
})

describe('RepoSettings: proxy payload', () => {
  const proxy = (over: Partial<Repository> = {}) =>
    repo({ repo_type: 'proxy', format: 'maven', upstream_url: 'https://repo.example.org', upstream_username: 'svc', ...over })

  it('sends the trimmed upstream URL and username, and no password unless one was typed', async () => {
    const calls = serve((c) => (c.method === 'PATCH' ? json(proxy()) : undefined))
    const w = await mountSettings(proxy())
    await w.find('#rl-upstream-url').setValue('  https://repo.example.org/new  ')
    await save(w)
    const body = writes(calls)[0].body!
    expect(body).toMatchObject({ upstream_url: 'https://repo.example.org/new', upstream_username: 'svc' })
    expect('upstream_password' in body).toBe(false)
    expect('sign_packages' in body).toBe(false)
    expect('merge_proxy_versions' in body).toBe(false)

    await w.find('#rl-upstream-pass').setValue('s3cret')
    await save(w)
    expect(writes(calls)[1].body).toMatchObject({ upstream_password: 's3cret' })
  })

  it('refuses to save a proxy whose upstream URL was emptied (the server rejects it for every proxy)', async () => {
    const calls = serve((c) => (c.method === 'PATCH' ? json(proxy()) : undefined))
    const w = await mountSettings(proxy())
    await w.find('#rl-upstream-url').setValue('   ')
    expect(saveDisabled(w)).toBe(true)
    await save(w)
    expect(writes(calls)).toHaveLength(0)
  })

  it('refuses the same for formats with a default upstream, where the stored URL is still required on update', async () => {
    const calls = serve((c) => (c.method === 'PATCH' ? json(proxy()) : undefined))
    const w = await mountSettings(proxy({ format: 'pypi', upstream_url: 'https://pypi.org/simple' }))
    await w.find('#rl-upstream-url').setValue('')
    expect(saveDisabled(w)).toBe(true)
    await save(w)
    expect(writes(calls)).toHaveLength(0)
  })
})

describe('RepoSettings: group payload', () => {
  const group = (over: Partial<Repository> = {}) => repo({ name: 'all', repo_type: 'group', member_repos: ['a', 'b'], ...over })
  const candidates = () => (c: Call) =>
    c.path === '/api/v1/repositories'
      ? json({ repositories: [repo({ name: 'a' }), repo({ name: 'b' }), repo({ name: 'c' })], total: 3 })
      : c.method === 'PATCH'
        ? json(group())
        : c.method === 'PUT'
          ? json({ members: [] })
          : undefined

  it('does not touch the members endpoint when only the description changed', async () => {
    const calls = serve(candidates())
    const w = await mountSettings(group())
    await w.find('#rl-description').setValue('x')
    await save(w)
    expect(writes(calls).map((c) => c.method)).toEqual(['PATCH'])
    expect('member_repos' in writes(calls)[0].body!).toBe(false)
    expect('upstream_url' in writes(calls)[0].body!).toBe(false)
  })

  it('saves reordered/added members through the members endpoint after the settings, and reports them', async () => {
    const calls = serve(candidates())
    const w = await mountSettings(group())
    await w.findAll('[data-testid="members-available"] button')[0].trigger('click')
    await w.findAll('[data-testid="members-selected"] li')[1].findAll('button')[0].trigger('click')
    await save(w)
    expect(writes(calls).map((c) => `${c.method} ${c.path}`)).toEqual(['PATCH /api/v1/repository/all', 'PUT /api/v1/repositories/all/members'])
    expect(writes(calls)[1].body).toEqual({ member_repos: ['b', 'a', 'c'] })
    expect((w.emitted('saved')![0][0] as Repository).member_repos).toEqual(['b', 'a', 'c'])
  })

  it('removing every member is saved as an empty list', async () => {
    const calls = serve(candidates())
    const w = await mountSettings(group())
    for (let i = 0; i < 2; i++) await w.find('[data-testid="members-selected"] li').findAll('button')[2].trigger('click')
    await save(w)
    expect(writes(calls)[1].body).toEqual({ member_repos: [] })
  })

  it('reports a failed member update and does not announce a save', async () => {
    const calls = serve((c) => (c.method === 'PUT' ? json({ error: 'member c is a group' }, 400) : candidates()(c)))
    const w = await mountSettings(group())
    await w.findAll('[data-testid="members-available"] button')[0].trigger('click')
    await save(w)
    expect(writes(calls)).toHaveLength(2)
    expect(w.find('[role="alert"]').text()).toBe('member c is a group')
    expect(w.emitted('saved')).toBeUndefined()
  })

  it('sends merge_proxy_versions only for admins', async () => {
    const asAdmin = serve(candidates())
    const a = await mountSettings(group(), { admin: true })
    await a.findAll('[data-row="policies"] input[type="checkbox"]')[1].setValue(true)
    await save(a)
    expect(writes(asAdmin)[0].body).toMatchObject({ merge_proxy_versions: true })

    const asMember = serve(candidates())
    const m = await mountSettings(group())
    await m.find('#rl-description').setValue('x')
    await save(m)
    expect('merge_proxy_versions' in writes(asMember)[0].body!).toBe(false)
  })
})

describe('RepoSettings: project move', () => {
  it('admins move a repository by sending the project, a cleared project means the system project', async () => {
    const calls = serve((c) => {
      if (c.path === '/api/v1/projects') return json({ projects: [{ name: 'team-a' }, { name: 'system' }], total: 2 })
      if (c.method === 'PATCH') return json(repo())
      return undefined
    })
    const w = await mountSettings(repo({ project: 'team-a' }), { admin: true })
    await w.find('#rl-project').setValue('')
    await save(w)
    expect(writes(calls)[0].body).toMatchObject({ project: 'system' })

    await w.find('#rl-project').setValue('team-a')
    // Back to the original project: nothing to move, nothing else changed.
    expect(saveDisabled(w)).toBe(true)
  })

  it('does not send a project for non-admins and does not let them pick one', async () => {
    const calls = serve((c) => (c.method === 'PATCH' ? json(repo()) : undefined))
    const w = await mountSettings(repo({ project: 'team-a' }))
    expect(w.find('#rl-project').exists()).toBe(false)
    await w.find('#rl-description').setValue('x')
    await save(w)
    expect('project' in writes(calls)[0].body!).toBe(false)
  })
})

describe('RepoSettings: usage and deletion', () => {
  it('shows package, version and size totals, and degrades when usage cannot be loaded', async () => {
    serve((c) =>
      c.path.endsWith('/usage')
        ? json({ package_count: 4, version_count: 9, size_bytes: 2048, quota: { used_bytes: 2048, limit_bytes: 4096 } })
        : undefined,
    )
    const ok = await mountSettings(repo())
    expect(ok.find('[data-testid="usage-line"]').text()).toContain('4')
    expect(ok.find('[data-testid="usage-line"]').text()).toContain('9')
    expect(ok.find('[data-testid="usage-line"]').text()).toContain('2.0 KB')

    serve((c) => (c.path.endsWith('/usage') ? json({ error: 'down' }, 500) : undefined))
    const bad = await mountSettings(repo())
    expect(bad.find('[data-testid="usage-line"]').exists()).toBe(false)
  })

  it('deletes only after the repository name is typed, with the name as explicit confirmation, then leaves to the list', async () => {
    const calls = serve((c) => (c.method === 'DELETE' ? new Response(null, { status: 204 }) : undefined))
    const w = await mountSettings(repo({ name: 'libs' }))
    const push = spyOn(router, 'push')
    await w.find('[data-testid="delete-repo"]').trigger('click')
    await flushPromises()
    const input = document.body.querySelector<HTMLInputElement>('#repo-delete-confirm')!
    expect(input).not.toBeNull()
    const confirm = [...document.body.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Delete' && b.closest('[role="dialog"]'))!
    expect(confirm.disabled).toBe(true)
    input.value = 'libs'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await flushPromises()
    expect(confirm.disabled).toBe(false)
    confirm.click()
    await flushPromises()
    const del = writes(calls)[0]
    expect(del.method).toBe('DELETE')
    expect(del.path).toBe('/api/v1/repository/libs')
    expect(del.query.get('confirm')).toBe('libs')
    expect(push).toHaveBeenCalledWith('/repos')
  })

  it('stays on the page and toasts when the delete fails', async () => {
    serve((c) => (c.method === 'DELETE' ? json({ error: 'repository is locked' }, 409) : undefined))
    const w = await mountSettings(repo({ name: 'libs' }))
    const push = spyOn(router, 'push')
    await w.find('[data-testid="delete-repo"]').trigger('click')
    await flushPromises()
    const input = document.body.querySelector<HTMLInputElement>('#repo-delete-confirm')!
    input.value = 'libs'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await flushPromises()
    ;[...document.body.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Delete' && b.closest('[role="dialog"]'))!.click()
    await flushPromises()
    expect(push).not.toHaveBeenCalled()
    expect(useToastStore().queue.map((t) => [t.level, t.description])).toEqual([['error', 'repository is locked']])
  })
})
