// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { buildCreateBody, buildUpdateBody, emptyRepoForm, formValid, nameError, repoFormFrom, upstreamSummary } from '../repoForm'
import type { Repository } from '../../api/repositories'

describe('repo form', () => {
  it('summarises an empty upstream as required only when the format has no default', () => {
    const form = emptyRepoForm({})
    expect(upstreamSummary(form, true)).toBe('Required, set it to continue')
    expect(upstreamSummary(form, false)).toBe('Format default')
    expect(upstreamSummary({ ...form, upstreamUrl: ' https://r.example/ ' }, true)).toBe('https://r.example/')
  })

  it('validates the name and the project prefix', () => {
    expect(nameError('', '')).toBe('')
    expect(nameError('bad name', '')).not.toBe('')
    expect(nameError('lib', 'acme-')).toContain('acme-')
    expect(nameError('acme-lib', 'acme-')).toBe('')
  })

  it('requires an upstream for proxies of formats that have no default', () => {
    const f = emptyRepoForm({ name: 'p', type: 'proxy', format: 'maven' })
    expect(formValid(f, '', true)).toBe(false)
    expect(formValid({ ...f, upstreamUrl: 'https://u.example' }, '', true)).toBe(true)
    expect(formValid(f, '', false)).toBe(true)
  })

  it('sends only what the type and role allow on create', () => {
    const body = buildCreateBody(emptyRepoForm({ name: ' g ', type: 'group', format: 'npm', members: ['a', 'b'], project: 'acme', mergeProxyVersions: true }), { isAdmin: false })
    expect(body.name).toBe('g')
    expect(body.member_repos).toEqual(['a', 'b'])
    expect(body.project).toBeUndefined()
    expect(body.merge_proxy_versions).toBeUndefined()
    expect(buildCreateBody(emptyRepoForm({ name: 'g', type: 'group', project: 'acme' }), { isAdmin: true }).project).toBe('acme')
  })

  it('sends the deploy target of a group of a supporting format only', () => {
    const group = (format: string, over = {}) => emptyRepoForm({ name: 'g', type: 'group', format: format as 'npm', members: ['a'], deployTarget: 'a', ...over })
    expect(buildCreateBody(group('npm'), { isAdmin: false }).deploy_target).toBe('a')
    expect(buildCreateBody(group('npm', { deployTarget: '' }), { isAdmin: false }).deploy_target).toBeUndefined()
    expect(buildCreateBody(group('deb'), { isAdmin: false }).deploy_target).toBeUndefined()

    const repo = { name: 'g', format: 'npm', repo_type: 'group', deploy_target: 'a', member_repos: ['a'] } as Parameters<typeof repoFormFrom>[0]
    expect(repoFormFrom(repo).deployTarget).toBe('a')
    expect(buildUpdateBody(repoFormFrom(repo), repo, { isAdmin: false }).deploy_target).toBe('a')
    // '' clears it
    expect(buildUpdateBody({ ...repoFormFrom(repo), deployTarget: '' }, repo, { isAdmin: false }).deploy_target).toBe('')
    expect('deploy_target' in buildUpdateBody(repoFormFrom({ ...repo, format: 'deb' }), { ...repo, format: 'deb' }, { isAdmin: false })).toBe(false)
  })

  it('never sends upstream fields on update: ProxySettingsForm owns them', () => {
    const repo = { name: 'p', format: 'npm', repo_type: 'proxy', upstream_url: 'https://u' } as Repository
    const body = buildUpdateBody({ ...repoFormFrom(repo), upstreamUrl: 'https://other', upstreamPassword: 'x' }, repo, { isAdmin: true })
    expect(Object.keys(body).filter((k) => k.startsWith('upstream'))).toEqual([])
  })

  it('resets trash retention with an explicit null and moves project only for admins', () => {
    const repo = { name: 'r', format: 'npm', project: 'system' } as Repository
    const form = { ...repoFormFrom(repo), project: 'acme' }
    expect(buildUpdateBody(form, repo, { isAdmin: true }).trash_retention_days).toBeNull()
    expect(buildUpdateBody(form, repo, { isAdmin: true }).project).toBe('acme')
    expect(buildUpdateBody(form, repo, { isAdmin: false }).project).toBeUndefined()
  })
})
