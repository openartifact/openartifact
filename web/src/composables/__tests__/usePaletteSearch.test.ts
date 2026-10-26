// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, spyOn, type Mock } from 'bun:test'
import { ApiError } from '../../api/client'
import * as searchApi from '../../api/search'
import * as paletteApi from '../../api/paletteSearch'
import * as projectsApi from '../../api/projects'
import * as bundlesApi from '../../api/bundles'
import { usePaletteSearch } from '../usePaletteSearch'

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms))

let repos: Mock<typeof paletteApi.searchRepositories>
let users: Mock<typeof paletteApi.searchUsers>
let pkgs: Mock<typeof searchApi.search>
let projects: Mock<typeof projectsApi.listProjects>
let bundles: Mock<typeof bundlesApi.listBundles>

function make(isAdmin = true) {
  return usePaletteSearch({ debounceMs: 5, isAdmin: () => isAdmin })
}

beforeEach(() => {
  repos = spyOn(paletteApi, 'searchRepositories').mockResolvedValue([])
  users = spyOn(paletteApi, 'searchUsers').mockResolvedValue([])
  pkgs = spyOn(searchApi, 'search').mockResolvedValue({ packages: [], repositories: [] })
  projects = spyOn(projectsApi, 'listProjects').mockResolvedValue({ projects: [], can_create: false })
  bundles = spyOn(bundlesApi, 'listBundles').mockResolvedValue({ total: 0, page: 1, limit: 5, bundles: [] })
})

describe('usePaletteSearch', () => {
  it('maps every source to palette items with working destinations', async () => {
    repos.mockResolvedValue([{ id: '1', name: 'web npm', format: 'npm', repo_type: 'hosted' }] as never)
    pkgs.mockResolvedValue({ packages: [{ name: '@acme/ui-kit', version: '4.2.0', format: 'npm', repository: 'web-npm' }], repositories: [] })
    projects.mockResolvedValue({ projects: [{ name: 'storefront', display_name: 'Storefront' }], can_create: false })
    bundles.mockResolvedValue({ total: 1, page: 1, limit: 5, bundles: [{ name: 'shop', version: '2.5.0', state: 'sealed' }] as never })
    users.mockResolvedValue([{ username: 'chen_wei', email: 'c@x.io' }])
    const s = make()
    s.setQuery('shop')
    await wait(40)
    expect(s.status.value).toBe('ready')
    expect(s.entities.value.repositories[0]).toMatchObject({ to: '/repos/web%20npm', format: 'npm' })
    expect(s.entities.value.packages[0]!.to).toBe('/repos/web-npm/packages/%40acme/ui-kit')
    expect(s.entities.value.projects[0]).toMatchObject({ to: '/projects/storefront', title: 'Storefront' })
    expect(s.entities.value.bundles[0]!.to).toBe('/bundles/shop/2.5.0')
    expect(s.entities.value.users[0]!.to).toBe('/settings/users?q=chen_wei')
  })

  it('treats a null bundle list (no match) as empty, not as a failed source', async () => {
    bundles.mockResolvedValue({ total: 0, page: 1, limit: 5, bundles: null } as never)
    const s = make()
    s.setQuery('nothing')
    await wait(40)
    expect(s.status.value).toBe('ready')
    expect(s.entities.value.bundles).toEqual([])
  })

  it('does not ask for bundles or users when the caller is not an administrator', async () => {
    const s = make(false)
    s.setQuery('shop')
    await wait(40)
    expect(bundles).not.toHaveBeenCalled()
    expect(users).not.toHaveBeenCalled()
    expect(repos).toHaveBeenCalled()
  })

  it('debounces typing into a single request', async () => {
    const s = make()
    s.setQuery('sh')
    s.setQuery('sho')
    s.setQuery('shop')
    await wait(40)
    expect(repos).toHaveBeenCalledTimes(1)
    expect(repos.mock.calls[0]![0]).toBe('shop')
  })

  it('does not call the API below the minimum length, for a checksum, or for an actions-only query', async () => {
    const s = make()
    s.setQuery('s')
    s.setQuery('a'.repeat(40), true)
    s.setQuery('create', true)
    await wait(40)
    expect(repos).not.toHaveBeenCalled()
    expect(s.status.value).toBe('idle')
  })

  it('aborts the previous request and drops its late answer', async () => {
    let signalOfFirst: AbortSignal | undefined
    let release!: (v: never[]) => void
    repos.mockImplementationOnce((_q, _l, signal) => {
      signalOfFirst = signal
      return new Promise((resolve) => { release = resolve as never })
    })
    const s = make()
    s.setQuery('first')
    await wait(20)
    repos.mockResolvedValueOnce([{ id: '2', name: 'second-repo', format: 'npm' }] as never)
    s.setQuery('second')
    expect(signalOfFirst?.aborted).toBe(true)
    await wait(40)
    expect(s.entities.value.repositories.map((r) => r.title)).toEqual(['second-repo'])
    release([{ id: '1', name: 'stale-repo', format: 'npm' }] as never)
    await wait(10)
    expect(s.entities.value.repositories.map((r) => r.title)).toEqual(['second-repo'])
    expect(s.status.value).toBe('ready')
  })

  it('keeps the sources that answered when one fails, and retries', async () => {
    repos.mockResolvedValue([{ id: '1', name: 'shop-repo', format: 'npm' }] as never)
    users.mockRejectedValueOnce(new Error('boom'))
    const s = make()
    s.setQuery('shop')
    await wait(40)
    expect(s.status.value).toBe('error')
    expect(s.entities.value.repositories).toHaveLength(1)
    s.retry()
    await wait(20)
    expect(s.status.value).toBe('ready')
  })

  it('reports a too-broad package query without failing the rest', async () => {
    pkgs.mockRejectedValue(new ApiError('too broad', 400, searchApi.QUERY_TOO_BROAD))
    repos.mockResolvedValue([{ id: '1', name: 'shop-repo', format: 'npm' }] as never)
    const s = make()
    s.setQuery('shop')
    await wait(40)
    expect(s.status.value).toBe('tooBroad')
    expect(s.entities.value.repositories).toHaveLength(1)
  })

  it('clears everything on reset', async () => {
    repos.mockResolvedValue([{ id: '1', name: 'shop-repo', format: 'npm' }] as never)
    const s = make()
    s.setQuery('shop')
    await wait(40)
    s.reset()
    expect(s.entities.value.repositories).toHaveLength(0)
    expect(s.status.value).toBe('idle')
  })
})
