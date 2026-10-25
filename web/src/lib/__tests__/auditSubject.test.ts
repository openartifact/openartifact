// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { auditSubject } from '../auditSubject'

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
