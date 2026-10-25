// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import type { ProjectListItem } from '../../api/projects'
import { createBlockedReason, defaultScope, effectiveScope, queryProjects, quotaUsage } from '../projectList'

const P = (name: string, extra: Partial<ProjectListItem> = {}): ProjectListItem => ({ id: name, name, ...extra })
const BASE = { q: '', sort: 'name' as const, dir: 'asc' as const, page: 1, limit: 20 }

describe('project scope', () => {
  it('opens admins on all projects and everyone else on their own', () => {
    expect(defaultScope(true)).toBe('all')
    expect(defaultScope(false)).toBe('mine')
  })

  it('never lets a non-admin list every project', () => {
    expect(effectiveScope(false, 'all')).toBe('mine')
    expect(effectiveScope(true, 'all')).toBe('all')
    expect(effectiveScope(true, 'mine')).toBe('mine')
  })
})

describe('queryProjects', () => {
  const projects = [
    P('beta', { display_name: 'Platform', my_role: 'viewer', repository_count: 9, member_count: 2, created_at: '2024-01-01T00:00:00Z' }),
    P('alpha', { description: 'Billing services', my_role: 'owner', repository_count: 1, member_count: 7, usage: { size_bytes: 50 }, created_at: '2025-01-01T00:00:00Z' }),
    P('gamma', { my_role: 'maintainer', repository_count: 4, usage: { size_bytes: 500 }, created_at: '2023-01-01T00:00:00Z' }),
  ]
  const names = (q: Partial<typeof BASE> & { sort?: never }) => queryProjects(projects, { ...BASE, ...q }).rows.map((p) => p.name)

  it('searches name, display name and description case-insensitively', () => {
    expect(names({ q: 'PLAT' })).toEqual(['beta'])
    expect(names({ q: 'billing' })).toEqual(['alpha'])
    expect(names({ q: 'a' })).toEqual(['alpha', 'beta', 'gamma'])
    expect(queryProjects(projects, { ...BASE, q: 'zzz' })).toEqual({ rows: [], total: 0 })
  })

  it('sorts by every column, ties broken by name', () => {
    const by = (sort: (typeof BASE)['sort'] | 'role' | 'repositories' | 'members' | 'used' | 'created', dir: 'asc' | 'desc' = 'asc') =>
      queryProjects(projects, { ...BASE, sort, dir }).rows.map((p) => p.name)
    expect(by('name', 'desc')).toEqual(['gamma', 'beta', 'alpha'])
    expect(by('role', 'desc')).toEqual(['alpha', 'gamma', 'beta'])
    expect(by('repositories', 'desc')).toEqual(['beta', 'gamma', 'alpha'])
    expect(by('members', 'desc')).toEqual(['alpha', 'beta', 'gamma'])
    expect(by('used', 'desc')).toEqual(['gamma', 'alpha', 'beta'])
    expect(by('created')).toEqual(['gamma', 'beta', 'alpha'])
  })

  it('pages after filtering and reports the full match count', () => {
    const second = queryProjects(projects, { ...BASE, limit: 2, page: 2 })
    expect(second.rows.map((p) => p.name)).toEqual(['gamma'])
    expect(second.total).toBe(3)
  })
})

describe('quotaUsage', () => {
  it('is null without a limit', () => {
    expect(quotaUsage(100, undefined)).toBeNull()
    expect(quotaUsage(100, 0)).toBeNull()
  })

  it('warns from 80% and is full from 100%', () => {
    expect(quotaUsage(50, 100)).toEqual({ percent: 50, level: 'ok' })
    expect(quotaUsage(80, 100)).toEqual({ percent: 80, level: 'warn' })
    expect(quotaUsage(120, 100)).toEqual({ percent: 120, level: 'full' })
    expect(quotaUsage(undefined, 100)).toEqual({ percent: 0, level: 'ok' })
  })
})

describe('createBlockedReason', () => {
  it('blocks only non-admins when the server disallows self-creation', () => {
    expect(createBlockedReason(false, false)).toBe('self-create-off')
    expect(createBlockedReason(true, false)).toBeNull()
    expect(createBlockedReason(false, true)).toBeNull()
  })
})
