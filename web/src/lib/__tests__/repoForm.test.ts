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

  it('keeps the stored password on update unless a new one is typed', () => {
    const repo = { name: 'p', format: 'npm', repo_type: 'proxy', upstream_url: 'https://u' } as Repository
    const form = repoFormFrom(repo)
    expect('upstream_password' in buildUpdateBody(form, repo, { isAdmin: true })).toBe(false)
    expect(buildUpdateBody({ ...form, upstreamPassword: 'x' }, repo, { isAdmin: true }).upstream_password).toBe('x')
  })

  it('resets trash retention with an explicit null and moves project only for admins', () => {
    const repo = { name: 'r', format: 'npm', project: 'system' } as Repository
    const form = { ...repoFormFrom(repo), project: 'acme' }
    expect(buildUpdateBody(form, repo, { isAdmin: true }).trash_retention_days).toBeNull()
    expect(buildUpdateBody(form, repo, { isAdmin: true }).project).toBe('acme')
    expect(buildUpdateBody(form, repo, { isAdmin: false }).project).toBeUndefined()
  })
})
