// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import type { AuditEntry } from '../../api/audit'
import { auditSubject, auditSubjectView } from '../auditSubject'
import { fc, hostileString, hostileWord } from './propertySetup'

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const text = fc.oneof(hostileString, fc.string({ unit: 'grapheme' }), fc.constantFrom('/api/v1/repositories/{name}', '/api/v1/x', '/repos/a'))
const uuid = fc.uuid()

// `details` is free-form JSON from the server: any value may sit under `message` and `path`.
const anyJson = fc.jsonValue({ maxDepth: 2 })
const details = fc.oneof(
  fc.constant(null),
  fc.dictionary(fc.constantFrom('message', 'path', 'method', 'status', 'other'), fc.oneof(text, anyJson, fc.record({ path: fc.oneof(text, anyJson) })), { maxKeys: 5 }),
)

const entry: fc.Arbitrary<AuditEntry> = fc.record(
  {
    target: fc.oneof(text, uuid, fc.constant(null)),
    details,
  },
  { requiredKeys: [] },
) as fc.Arbitrary<AuditEntry>

/** Every string the entry could legitimately put in a summary. */
function candidates(e: AuditEntry): string[] {
  const out: string[] = []
  const message = e.details?.message
  if (typeof message === 'string') out.push(message)
  const nested = (message as { path?: unknown } | null | undefined)?.path
  if (typeof nested === 'string') out.push(nested)
  if (typeof e.details?.path === 'string') out.push(e.details.path)
  if (typeof e.target === 'string') out.push(e.target)
  return out
}

describe('auditSubject (property)', () => {
  it('returns a string for any entry, taken from the entry itself or empty', () => {
    fc.assert(fc.property(entry, (e) => {
      const subject = auditSubject(e)
      expect(typeof subject).toBe('string')
      if (subject !== '') expect(candidates(e)).toContain(subject)
    }))
  })

  it('never prints a placeholder the entry did not contain', () => {
    fc.assert(fc.property(entry, (e) => {
      const input = JSON.stringify(e)
      const subject = auditSubject(e)
      for (const word of ['undefined', '[object Object]', 'NaN']) {
        if (!input.includes(word)) expect(subject).not.toContain(word)
      }
    }))
  })

  it('prefers a non-empty description over everything else', () => {
    fc.assert(fc.property(entry, hostileWord, (e, message) => {
      expect(auditSubject({ ...e, details: { ...e.details, message } })).toBe(message)
    }))
  })

  it('never shows an API route pattern taken from the request path', () => {
    fc.assert(fc.property(fc.string({ unit: 'grapheme', maxLength: 12 }), fc.boolean(), (rest, nested) => {
      const path = `/api/${rest}`
      const details = nested ? { message: { path } } : { path }
      expect(auditSubject({ details })).toBe('')
    }))
  })

  it('never shows a bare UUID id as the subject, but keeps any other target', () => {
    fc.assert(fc.property(uuid, text, (id, target) => {
      fc.pre(!UUID.test(target))
      expect(auditSubject({ target: id })).toBe('')
      expect(auditSubject({ target: id, details: {} })).toBe('')
      expect(auditSubject({ target })).toBe(target)
    }))
  })
})

const KINDS = ['repository', 'package', 'project', 'user', 'group', 'token', 'bundle', 'permission_target', 'gpg_key', 'sso_provider', 'setting', 'storage_tier', 'storage_policy']
const subject = fc.record(
  {
    kind: fc.oneof(fc.constantFrom(...KINDS), hostileWord),
    name: fc.oneof(hostileString, fc.constant(null)),
    version: hostileString,
    repository: hostileString,
    project: hostileString,
  },
  { requiredKeys: ['kind', 'name'] },
)

describe('auditSubjectView (property)', () => {
  it('is null exactly when the server resolved no subject', () => {
    fc.assert(fc.property(entry, (e) => {
      expect(auditSubjectView({ ...e, subject: null })).toBeNull()
      expect(auditSubjectView({ ...e, subject: undefined as never })).toBeNull()
    }))
  })

  it('names every resolved subject, never with a placeholder the subject did not contain', () => {
    fc.assert(fc.property(subject, (s) => {
      const view = auditSubjectView({ subject: s } as AuditEntry)!
      expect(view.label.length).toBeGreaterThan(0)
      expect(view.kind.length).toBeGreaterThan(0)
      const input = JSON.stringify(s)
      for (const word of ['undefined', '[object Object]']) {
        if (!input.includes(word)) {
          expect(view.label).not.toContain(word)
          expect(view.kind).not.toContain(word)
        }
      }
      if (s.name) expect(view.label.startsWith(s.name)).toBe(true)
    }))
  })

  it('only ever links to a page inside the app, never off-origin and never with raw whitespace', () => {
    fc.assert(fc.property(subject, (s) => {
      const to = auditSubjectView({ subject: s } as AuditEntry)!.to
      if (to === null) return
      expect(to.startsWith('/')).toBe(true)
      expect(to.startsWith('//')).toBe(false)
      expect(/[\s\\]/.test(to)).toBe(false)
      expect(new URL(to, 'https://app.test').origin).toBe('https://app.test')
    }))
  })

  it('does not link a subject that has no name (it is gone)', () => {
    fc.assert(fc.property(subject, (s) => {
      expect(auditSubjectView({ subject: { ...s, name: null } } as AuditEntry)!.to).toBeNull()
    }))
  })

  it('links repositories and projects by their encoded name, recoverably', () => {
    fc.assert(fc.property(fc.constantFrom('repository', 'project'), hostileWord, (kind, name) => {
      const to = auditSubjectView({ subject: { kind, name } } as AuditEntry)!.to!
      expect(to.startsWith(kind === 'repository' ? '/repos/' : '/projects/')).toBe(true)
      expect(decodeURIComponent(to.slice(to.lastIndexOf('/') + 1))).toBe(name)
    }))
  })
})
