// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import DashboardPage from '../DashboardPage.vue'
import { useSessionStore, type User } from '../../stores/session'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const DAY = 86_400_000
const inDays = (n: number): string => new Date(Date.now() + n * DAY).toISOString()

const counts = (o: Partial<Record<'total' | 'critical' | 'high' | 'medium' | 'low' | 'quarantined', number>> = {}) => ({
  total: 0, critical: 0, high: 0, medium: 0, low: 0, quarantined: 0, ...o,
})
const summary = (vuln = counts(), secret = counts()) => ({
  by_type: { vuln, license: counts(), secret, ai: counts() },
  scanned_repositories: 1,
  total_repositories: 1,
})

const repo = (name: string, format = 'npm', extra: Record<string, unknown> = {}) => ({ name, format, repo_type: 'hosted', public: false, ...extra })

interface RepoSets { all?: unknown[]; total?: number; flagged?: number; unscanned?: number; nearQuota?: unknown[] }

/** `GET /repositories`: each filter answers its own set, as the server does. */
const repos = ({ all = [repo('libs')], total = all.length, flagged = 0, unscanned = 0, nearQuota = [] }: RepoSets = {}) =>
  (url: URL): Response => {
    const q = url.searchParams
    if (q.get('flagged')) return json({ repositories: [], total: flagged })
    if (q.get('unscanned')) return json({ repositories: [], total: unscanned })
    if (q.get('near_quota')) return json({ repositories: nearQuota, total: nearQuota.length })
    return json({ repositories: all, total })
  }

type Handler = (url: URL) => Response | Promise<Response>
type Routes = Record<string, Handler | unknown>

/** Answers each API path from `routes` (value or handler); an unlisted path is an empty object. */
function answer(routes: Routes) {
  return async (input: unknown): Promise<Response> => {
    const url = new URL(String(input), 'http://localhost')
    const hit = routes[url.pathname]
    if (typeof hit === 'function') return (hit as Handler)(url)
    return hit === undefined ? json({}) : json(hit)
  }
}

const ADMIN = { id: 'a', username: 'root', is_admin: true } as User
const MEMBER = { id: 'u', username: 'ann', projects: [{ name: 'web', role: 'developer' }] } as User

async function mountPage(user: User, routes: Routes) {
  setActivePinia(createPinia())
  useSessionStore().user = user
  const spy = spyOn(globalThis, 'fetch').mockImplementation(answer(routes) as never)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: DashboardPage }] })
  await router.push('/')
  await router.isReady()
  const w = mount(DashboardPage, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return { w, spy }
}

const paths = (spy: ReturnType<typeof spyOn>): string[] => spy.mock.calls.map((c) => new URL(String(c[0]), 'http://localhost').pathname)
const attention = (w: ReturnType<typeof mount>): string[] =>
  w.findAll('[data-testid="attention-list"] li').map((li) => li.find('a').text())

const HEALTHY_ADMIN: Routes = {
  '/api/v1/admin/system/info': { db_status: 'ok', uptime: '3h' },
  '/api/v1/admin/storage': { items: [{ name: 'main', status: 'healthy' }] },
  '/api/v1/security/health': { scanners: [{ name: 'grype', ready: true }], scans_failed_24h: 0 },
  '/api/v1/security/findings/summary': summary(),
  '/api/v1/repositories': repos(),
  '/api/v1/packages/recent': { packages: [{ repository: 'libs', format: 'npm', name: 'left-pad', version: '1.0.0', created_at: inDays(-1) }] },
  '/api/v1/users': { total: 3 },
  '/api/v1/tokens': { tokens: [], total: 2 },
}

describe('DashboardPage attention', () => {
  it('raises an item per failing condition, most severe first, in source order within a severity', async () => {
    const { w } = await mountPage(ADMIN, {
      ...HEALTHY_ADMIN,
      '/api/v1/admin/storage': { items: [{ name: 'cold', status: 'unreachable' }, { name: 'main', status: 'healthy' }] },
      '/api/v1/security/health': {
        scanners: [{ name: 'grype', ready: true }, { name: 'trivy', ready: false, message: 'db missing' }],
        scans_failed_24h: 4,
        dependency_track: { enabled: true, last_push_ok: false },
      },
      '/api/v1/security/findings/summary': summary(counts({ critical: 2, quarantined: 1 }), counts({ critical: 1 })),
      '/api/v1/tokens': {
        total: 2,
        tokens: [
          { id: 't1', type: 'personal', name: 'ci', owner: 'root', created_at: inDays(-90), expires_at: inDays(10), expired: false },
          { id: 't2', type: 'personal', name: 'old', owner: 'root', created_at: inDays(-90), expires_at: inDays(-2), expired: true },
        ],
      },
      '/api/v1/repositories': repos({
        flagged: 2,
        unscanned: 5,
        nearQuota: [repo('big', 'npm', { stats: { package_count: 1, size_bytes: 1, security: {}, quota: { limit_bytes: 100, used_bytes: 96 } } })],
      }),
    })
    expect(w.find('[aria-label="11 items need attention"]').exists()).toBe(true)
    // Five rows show first; the rest is behind "Show more" rather than dropped.
    expect(attention(w)).toEqual([
      'Storage backend cold is unreachable',
      'Critical findings: 3',
      'Scanner trivy is not ready',
      'Failed scans in the last 24 hours: 4',
      'Repositories with open critical or high findings: 2',
    ])
    await w.get('[data-testid="attention-list"]').element.parentElement!.querySelector('button')!.click()
    const all = attention(w)
    expect(all).toHaveLength(11)
    expect(all.slice(5, 7)).toEqual(['Repository big is near its quota (96% used)', 'Token old has expired'])
    expect(all.slice(7, 9)).toEqual(['Quarantined findings: 1', 'The last push to Dependency-Track failed'])
    expect(all[9]).toStartWith('Token ci expires on')
    expect(all[10]).toBe('Repositories never scanned: 5')
    w.unmount()
  })

  it('says all clear when nothing needs a decision', async () => {
    const { w } = await mountPage(ADMIN, HEALTHY_ADMIN)
    expect(w.text()).toContain('All clear')
    expect(w.find('[data-testid="attention-list"]').exists()).toBe(false)
    expect(w.text()).not.toContain('may be incomplete')
    w.unmount()
  })

  it('does not vouch for a failed source: it warns that the list may be incomplete', async () => {
    const { w } = await mountPage(ADMIN, {
      ...HEALTHY_ADMIN,
      '/api/v1/security/findings/summary': () => json({ error: 'boom' }, 500),
    })
    expect(w.get('[role="status"]').text()).toContain('Some checks could not be loaded')
    // The full "nothing close to a limit" claim is withheld while a check is missing.
    expect(w.text()).not.toContain('nothing close to a limit')
    w.unmount()
  })

  it('treats a forbidden source as not for this caller, not as a failure', async () => {
    const { w } = await mountPage(ADMIN, {
      ...HEALTHY_ADMIN,
      '/api/v1/security/findings/summary': () => json({ error: 'no' }, 403),
    })
    expect(w.text()).toContain('All clear')
    expect(w.text()).not.toContain('may be incomplete')
    w.unmount()
  })

  it('lists a token that expires within three days and one that expires later, with the days kept at least one', async () => {
    const { w } = await mountPage(ADMIN, {
      ...HEALTHY_ADMIN,
      '/api/v1/tokens': {
        total: 2,
        tokens: [
          { id: 'a', type: 'personal', name: 'later', owner: 'root', created_at: inDays(-1), expires_at: inDays(20), expired: false },
          { id: 'b', type: 'personal', name: 'soon', owner: 'root', created_at: inDays(-1), expires_at: new Date(Date.now() + 3600_000).toISOString(), expired: false },
        ],
      },
    })
    const rows = w.findAll('[data-testid="attention-list"] li').map((li) => li.text())
    expect(rows[0]).toContain('Token soon expires')
    expect(rows[0]).toContain('High')
    expect(rows[1]).toContain('Token later expires')
    expect(rows[1]).toContain('Medium')
    w.unmount()
  })

  it('asks the server only for tokens expiring inside the horizon', async () => {
    const { spy } = await mountPage(ADMIN, HEALTHY_ADMIN)
    const call = spy.mock.calls.map((c) => new URL(String(c[0]), 'http://localhost')).find((u) => u.pathname === '/api/v1/tokens' && u.searchParams.has('expires_before'))!
    const horizon = Date.parse(call.searchParams.get('expires_before')!)
    expect(Math.abs(horizon - (Date.now() + 30 * DAY))).toBeLessThan(60_000)
  })
})

describe('DashboardPage roles', () => {
  it('shows an admin the health strip and calls the admin endpoints', async () => {
    const { w, spy } = await mountPage(ADMIN, HEALTHY_ADMIN)
    expect(w.find('[data-testid="health-strip"]').exists()).toBe(true)
    expect(paths(spy)).toEqual(expect.arrayContaining(['/api/v1/admin/system/info', '/api/v1/admin/storage', '/api/v1/security/health', '/api/v1/users']))
    expect(w.get('[data-testid="totals"]').text()).toContain('3')
    w.unmount()
  })

  it('never asks a member for admin-only data and greets them by name', async () => {
    const { w, spy } = await mountPage(MEMBER, {
      '/api/v1/repositories': repos(),
      '/api/v1/packages/recent': (url: URL) =>
        json({ packages: [{ repository: 'libs', format: 'npm', name: url.searchParams.get('mine') === 'true' ? 'mine-pkg' : 'any-pkg', version: '2.0.0', created_at: inDays(-1), created_by_name: 'ann' }] }),
      '/api/v1/projects': { projects: [{ name: 'web' }] },
      '/api/v1/projects/web/activity': { data: [{ id: 'e1', timestamp: inDays(-2), action: 'project.member_added', actor: 'bob', message: 'added carol' }], per_page: 8, has_more: false },
    })
    const called = paths(spy)
    for (const p of called) {
      expect(p.startsWith('/api/v1/admin/')).toBe(false)
      expect(p).not.toBe('/api/v1/security/health')
      expect(p).not.toBe('/api/v1/users')
    }
    expect(w.find('[data-testid="health-strip"]').exists()).toBe(false)
    expect(w.text()).toContain('Welcome back, ann')
    expect(w.text()).not.toContain('View audit log')
    expect(w.get('[data-testid="my-projects"]').text()).toContain('web')
    // The feed merges project activity with the member's own publishes, newest first.
    const feed = w.get('[data-testid="activity-feed"]').text()
    expect(feed).toContain('added carol')
    expect(feed).toContain('mine-pkg 2.0.0')
    expect(feed.indexOf('mine-pkg')).toBeLessThan(feed.indexOf('added carol'))
    // Recently published lists everyone's packages.
    expect(w.text()).toContain('any-pkg')
    w.unmount()
  })

  it('offers repository creation only to those who may create one', async () => {
    const admin = await mountPage(ADMIN, HEALTHY_ADMIN)
    expect(admin.w.findAll('a').some((a) => a.attributes('href') === '/repos?new=1')).toBe(true)
    admin.w.unmount()
    const member = await mountPage(MEMBER, { '/api/v1/repositories': repos() })
    expect(member.w.findAll('a').some((a) => a.attributes('href') === '/repos?new=1')).toBe(false)
    member.w.unmount()
  })

  it('reads only the first four projects of a member for the feed', async () => {
    const projects = ['a', 'b', 'c', 'd', 'e'].map((name) => ({ name, role: 'viewer' }))
    const { spy } = await mountPage({ ...MEMBER, projects } as User, {})
    const feeds = paths(spy).filter((p) => p.endsWith('/activity'))
    expect(feeds.sort()).toEqual(['/api/v1/projects/a/activity', '/api/v1/projects/b/activity', '/api/v1/projects/c/activity', '/api/v1/projects/d/activity'])
  })
})

describe('DashboardPage activity (admin)', () => {
  it('leaves requests, downloads, sign-ins and subject-less entries out of the feed', async () => {
    const at = inDays(-1)
    const { w } = await mountPage(ADMIN, {
      ...HEALTHY_ADMIN,
      '/api/v1/admin/audit': {
        data: [
          { id: '1', timestamp: at, action: 'http.request', actor: 'x', subject: { name: 'noise' } },
          { id: '2', timestamp: at, action: 'artifact.downloaded', actor: 'x', subject: { name: 'dl' } },
          { id: '3', timestamp: at, action: 'auth.login', actor: 'x' },
          { id: '4', timestamp: at, action: 'artifact.deleted', actor: 'x' },
          { id: '5', timestamp: at, action: 'auth.login_failed', actor_name: 'mallory' },
          { id: '6', timestamp: at, action: 'artifact.uploaded', actor_name: 'Alice', subject: { kind: 'artifact', name: 'left-pad', repository: 'libs' } },
        ],
        has_more: false,
      },
    })
    const rows = w.findAll('[data-testid="activity-feed"] li').map((li) => li.text())
    expect(rows).toHaveLength(2)
    expect(rows.join('|')).toContain('mallory')
    expect(rows.join('|').replace(/\s+/g, ' ')).toContain('Alice uploaded left-pad')
    expect(w.text()).toContain('View audit log')
    w.unmount()
  })

  it('shows the error of the failed feed with a retry that reloads it', async () => {
    let fail = true
    const { w } = await mountPage(ADMIN, {
      ...HEALTHY_ADMIN,
      '/api/v1/admin/audit': () => (fail ? json({ error: 'audit down' }, 500) : json({ data: [], has_more: false })),
    })
    const alert = w.findAll('[role="alert"]').find((a) => a.text().includes('audit down'))!
    expect(alert).toBeDefined()
    fail = false
    await alert.get('button').trigger('click')
    await flushPromises()
    expect(w.text()).not.toContain('audit down')
    expect(w.text()).toContain('No recent activity')
    w.unmount()
  })
})

describe('DashboardPage first run', () => {
  const FRESH: Routes = {
    ...HEALTHY_ADMIN,
    '/api/v1/repositories': repos({ all: [] }),
    '/api/v1/packages/recent': { packages: [] },
    '/api/v1/users': { total: 1 },
    '/api/v1/tokens': { tokens: [], total: 0 },
    '/api/v1/security/health': { scanners: [], scans_failed_24h: 0 },
  }

  it('shows the checklist with the real progress and empty blocks on a fresh instance', async () => {
    const { w } = await mountPage(ADMIN, FRESH)
    const list = w.get('[data-testid="checklist"]')
    expect(list.text()).toContain('0 of 5')
    expect(list.text()).toContain('No repository exists yet')
    expect(list.text()).toContain('You are the only user')
    expect(w.text()).toContain('All clear')
    w.unmount()
  })

  it('marks steps done from counts and hides the checklist once complete', async () => {
    const partial = await mountPage(ADMIN, { ...FRESH, '/api/v1/repositories': repos() })
    expect(partial.w.get('[data-testid="checklist"]').text()).toContain('1 of 5')
    partial.w.unmount()
    const done = await mountPage(ADMIN, {
      ...FRESH,
      '/api/v1/repositories': repos(),
      '/api/v1/packages/recent': HEALTHY_ADMIN['/api/v1/packages/recent'],
      '/api/v1/users': { total: 2 },
      '/api/v1/tokens': { tokens: [], total: 1 },
      '/api/v1/security/health': { scanners: [{ name: 'grype', ready: true }] },
    })
    expect(done.w.find('[data-testid="checklist"]').exists()).toBe(false)
    done.w.unmount()
  })

  it('does not show the checklist on an established instance', async () => {
    const { w } = await mountPage(ADMIN, HEALTHY_ADMIN)
    expect(w.find('[data-testid="checklist"]').exists()).toBe(false)
    w.unmount()
  })

  it('remembers a dismissed checklist across page views', async () => {
    const first = await mountPage(ADMIN, FRESH)
    await first.w.get('[data-testid="checklist"] button').trigger('click')
    expect(first.w.find('[data-testid="checklist"]').exists()).toBe(false)
    first.w.unmount()
    const second = await mountPage(ADMIN, FRESH)
    expect(second.w.find('[data-testid="checklist"]').exists()).toBe(false)
    second.w.unmount()
  })

  it('leaves the member steps for a member: no repository, scanner or team step', async () => {
    const { w } = await mountPage(MEMBER, { '/api/v1/repositories': repos({ all: [] }), '/api/v1/packages/recent': { packages: [] } })
    const text = w.get('[data-testid="checklist"]').text()
    expect(text).toContain('Publish a package')
    expect(text).toContain('Create an access token')
    expect(text).not.toContain('Create a repository')
    expect(text).not.toContain('Enable a scanner')
    expect(text).not.toContain('teammate')
    w.unmount()
  })
})

describe('DashboardPage totals', () => {
  it('counts formats only when the loaded page holds every repository', async () => {
    const all = await mountPage(ADMIN, { ...HEALTHY_ADMIN, '/api/v1/repositories': repos({ all: [repo('a', 'npm'), repo('b', 'npm'), repo('c', 'pypi')] }) })
    const allText = all.w.get('[data-testid="totals"]').text()
    expect(allText).toMatch(/3\s*Repositories/)
    expect(allText).toMatch(/2\s*Formats/)
    all.w.unmount()
    const partial = await mountPage(ADMIN, { ...HEALTHY_ADMIN, '/api/v1/repositories': repos({ all: [repo('a', 'npm')], total: 250 }) })
    const text = partial.w.get('[data-testid="totals"]').text()
    expect(text).toMatch(/250\s*Repositories/)
    expect(text).not.toContain('Formats')
    partial.w.unmount()
  })
})

describe('DashboardPage loading and refresh', () => {
  it('shows a loading state until the sources answer', async () => {
    setActivePinia(createPinia())
    useSessionStore().user = ADMIN
    const { promise: gate, resolve: release } = Promise.withResolvers<Response>()
    spyOn(globalThis, 'fetch').mockImplementation((async () => gate) as never)
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: DashboardPage }] })
    await router.push('/')
    await router.isReady()
    const w = mount(DashboardPage, { attachTo: document.body, global: { plugins: [router] } })
    await flushPromises()
    expect(w.findAll('[aria-busy="true"]').length).toBeGreaterThan(0)
    expect(w.text()).not.toContain('All clear')
    release(json({}))
    await flushPromises()
    expect(w.text()).toContain('All clear')
    w.unmount()
  })

  it('re-reads every source on Refresh and shows what changed', async () => {
    let critical = 0
    const { w, spy } = await mountPage(ADMIN, {
      ...HEALTHY_ADMIN,
      '/api/v1/security/findings/summary': () => json(summary(counts({ critical }))),
    })
    expect(w.text()).toContain('All clear')
    const before = spy.mock.calls.length
    critical = 7
    const refresh = w.findAll('button').find((b) => b.text().includes('Refresh'))!
    await refresh.trigger('click')
    await flushPromises()
    expect(spy.mock.calls.length).toBeGreaterThanOrEqual(before * 2 - 1)
    expect(attention(w)).toEqual(['Critical findings: 7'])
    expect(refresh.attributes('disabled')).toBeUndefined()
    w.unmount()
  })
})
