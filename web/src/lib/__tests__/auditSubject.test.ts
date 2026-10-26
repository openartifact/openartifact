// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { auditSubject, auditSubjectView, isHttpNoise } from '../auditSubject'

const ID = '5da92cd2-eb09-4369-bbf4-8a590cd1f08e'

describe('auditSubject', () => {
  it('prefers the description over the resource id', () => {
    expect(auditSubject({ target: ID, details: { message: 'moved to trash: repo/base/alpine v3' } })).toBe(
      'moved to trash: repo/base/alpine v3',
    )
  })

  it('reads the request path out of an object message', () => {
    expect(auditSubject({ details: { message: { method: 'POST', path: '/api/v1/repositories', status: 201 } } })).toBe(
      '/api/v1/repositories',
    )
  })

  it('never shows a bare UUID target', () => {
    expect(auditSubject({ target: ID })).toBe('')
    expect(auditSubject({ target: ID, details: {} })).toBe('')
  })

  it('keeps a readable target', () => {
    expect(auditSubject({ target: 'repo/alice' })).toBe('repo/alice')
  })

  it('handles an entry with nothing to show', () => {
    expect(auditSubject({})).toBe('')
  })
})

describe('auditSubjectView', () => {
  it('is null when the server resolved no subject', () => {
    expect(auditSubjectView({})).toBeNull()
    expect(auditSubjectView({ subject: null })).toBeNull()
  })

  it('names a package with its version and links to the version page', () => {
    expect(auditSubjectView({ subject: { kind: 'package', name: '@scope/left-pad', version: '1.3.0', repository: 'web npm' } })).toEqual({
      label: '@scope/left-pad 1.3.0',
      kind: 'Package',
      to: '/repos/web%20npm/packages/@scope/left-pad/versions/1.3.0',
    })
  })

  it('links repositories, projects, users and settings pages', () => {
    const to = (kind: string, name: string | null = 'x y') => auditSubjectView({ subject: { kind, name } as never })?.to
    expect(to('repository')).toBe('/repos/x%20y')
    expect(to('project')).toBe('/projects/x%20y')
    expect(to('user')).toBe('/settings/users?q=x%20y')
    expect(to('token')).toBe('/settings/tokens?type=all&q=x%20y')
    expect(to('permission_target')).toBe('/settings/permissions')
    expect(to('storage_tier')).toBe('/settings/tiers')
  })

  it('says a removed subject is gone and does not link it', () => {
    const view = auditSubjectView({ subject: { kind: 'repository', name: null } })
    expect(view).toEqual({ label: '(removed)', kind: 'Repository', to: null })
  })

  it('shows an unknown kind in words and does not link it', () => {
    expect(auditSubjectView({ subject: { kind: 'oci_manifest', name: 'a' } })).toEqual({ label: 'a', kind: 'oci manifest', to: null })
  })

  it('does not link a package without its repository', () => {
    expect(auditSubjectView({ subject: { kind: 'package', name: 'p' } })?.to).toBeNull()
  })
})

describe('isHttpNoise', () => {
  it('flags only per-request rows', () => {
    expect(isHttpNoise({ action: 'http.request' })).toBe(true)
    expect(isHttpNoise({ action: 'repository.create' })).toBe(false)
  })
})
