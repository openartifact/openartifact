// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { actionLabel, auditItem, isFeedAction, isFeedEntry, mergeActivity, projectItem, publishedItem, subjectPath, verbOf } from '../activity'

describe('activity feed', () => {
  it('drops traffic actions', () => {
    expect(isFeedAction('http.request')).toBe(false)
    expect(isFeedAction('artifact.downloaded')).toBe(false)
    expect(isFeedAction('repository.created')).toBe(true)
  })

  it('keeps routine sign-ins and entries without a subject out of the feed', () => {
    expect(isFeedAction('auth.login')).toBe(false)
    expect(isFeedEntry({ action: 'repository.deleted', subject: null })).toBe(false)
    expect(isFeedEntry({ action: 'artifact.deleted', subject: { kind: 'package', name: 'left-pad' } })).toBe(true)
    expect(isFeedEntry({ action: 'auth.login_failed', subject: null })).toBe(true)
  })

  it('reads the verb off the action', () => {
    expect(verbOf('project.member_added')).toBe('member added')
    expect(verbOf('artifact.uploaded')).toBe('uploaded')
    expect(verbOf(undefined)).toBe('')
  })

  it('labels an action for a table, with a readable fallback', () => {
    expect(actionLabel('artifact.deleted')).toBe('Moved to trash')
    expect(actionLabel('artifact.released')).toBe('Released from quarantine')
    expect(actionLabel('project.member_added')).toBe('Member added')
    expect(actionLabel(undefined)).toBe('')
  })

  it('names the actor and links the resolved subject and its repository', () => {
    const item = auditItem({
      id: '1',
      timestamp: '2026-10-25T10:00:00Z',
      actor: 'u-123',
      actor_name: 'alice',
      action: 'artifact.deleted',
      subject: { kind: 'package', name: 'lodash 4.17.21', repository: 'npm-main' },
    } as never)
    expect(item.actor).toBe('alice')
    expect(item.subject).toEqual({ label: 'lodash 4.17.21', to: '/repos/npm-main/packages/lodash 4.17.21'.replace(' ', '%20') })
    expect(item.where).toEqual({ label: 'npm-main', to: '/repos/npm-main' })
  })

  it('does not repeat a repository as its own location', () => {
    const item = auditItem({ id: '2', timestamp: 't', actor: 'bob', action: 'repository.created', subject: { kind: 'repository', name: 'docker', repository: 'docker' } } as never)
    expect(item.subject).toEqual({ label: 'docker', to: '/repos/docker' })
    expect(item.where).toBeUndefined()
  })

  it('has no link when the subject was deleted', () => {
    const item = auditItem({ id: '3', timestamp: 't', actor: 'bob', action: 'user.deleted', subject: { kind: 'user', name: null } } as never)
    expect(item.subject).toBeUndefined()
    expect(subjectPath({ kind: 'mystery', name: 'x' })).toBeUndefined()
  })

  it('links a project feed entry to its project and a publish to its artifact version', () => {
    const p = projectItem({ id: 'e1', timestamp: 't', action: 'project.member_added', actor: 'carol', message: 'added dave as maintainer' }, 'storefront')
    expect(p.where).toEqual({ label: 'storefront', to: '/projects/storefront' })
    expect(p.verb).toBe('')
    expect(projectItem({ id: 'e2', timestamp: 't', action: 'project.member_added', actor: 'carol' }, 'storefront').verb).toBe('member added')
    const pub = publishedItem({ repository: 'r', format: 'npm', name: '@a/b', version: '1.0.0', created_at: 't', created_by_name: 'eve' })
    expect(pub.subject?.to).toBe('/repos/r/packages/@a/b/versions/1.0.0')
    expect(pub.actor).toBe('eve')
  })

  it('merges newest first without duplicates and caps the length', () => {
    const a = { id: 'a', at: '2026-10-25T10:00:00Z', actor: '', verb: '' }
    const b = { id: 'b', at: '2026-10-25T11:00:00Z', actor: '', verb: '' }
    const c = { id: 'c', at: 'not a date', actor: '', verb: '' }
    expect(mergeActivity([[a, c], [b, a]], 10).map((i) => i.id)).toEqual(['b', 'a', 'c'])
    expect(mergeActivity([[a, b]], 1).map((i) => i.id)).toEqual(['b'])
  })
})
