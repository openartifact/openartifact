// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import type { FederationPeer } from '../../api/federation'
import {
  emptyPeerForm,
  fill,
  formatLag,
  peerCreateBody,
  peerFormFrom,
  peerFormValid,
  peerStateLabel,
  peerStateVariant,
  peerUpdateBody,
  previewError,
  resyncPending,
  showsFederation,
  upsertPeer,
  validatePeerForm,
} from '../federation'

const peer = (over: Partial<FederationPeer> = {}): FederationPeer => ({
  id: 'p1',
  name: 'site-b',
  base_url: 'https://b.example.org',
  enabled: true,
  state: 'ok',
  cursor_seq: 10,
  remote_head_seq: 10,
  backlog_units: 0,
  lag_seconds: 0,
  last_error: '',
  error_count: 0,
  applied_total: 5,
  conflicts_total: 0,
  deferred_units: 0,
  resync_requested: false,
  has_ca_pem: false,
  has_client_cert: false,
  created_at: '2026-10-01T00:00:00Z',
  ...over,
})

describe('peer state', () => {
  it('maps every state to a badge variant and a label', () => {
    expect(peerStateVariant('ok')).toBe('success')
    expect(peerStateVariant('lagging')).toBe('warning')
    expect(peerStateVariant('error')).toBe('danger')
    expect(peerStateVariant('pending')).toBe('neutral')
    expect(peerStateVariant('disabled')).toBe('neutral')
    expect(peerStateLabel('ok')).toBe('In sync')
    expect(peerStateLabel('lagging')).toBe('Lagging')
    expect(peerStateLabel('error')).toBe('Error')
  })
})

describe('formatLag', () => {
  const unit = (n: number, u: string) =>
    new Intl.NumberFormat('en', { style: 'unit', unit: u, unitDisplay: 'short' }).format(n)

  it('says caught up below one second and for missing or invalid values', () => {
    expect(formatLag(0)).toBe('Caught up')
    expect(formatLag(0.4)).toBe('Caught up')
    expect(formatLag(undefined)).toBe('Caught up')
    expect(formatLag(Number.NaN)).toBe('Caught up')
    expect(formatLag(-3)).toBe('Caught up')
  })
  it('renders a single unit', () => {
    expect(formatLag(45)).toBe(unit(45, 'second'))
    expect(formatLag(120)).toBe(unit(2, 'minute'))
    expect(formatLag(86_400)).toBe(unit(1, 'day'))
  })
  it('keeps the two most significant units', () => {
    expect(formatLag(3_900)).toBe(`${unit(1, 'hour')} ${unit(5, 'minute')}`)
    expect(formatLag(90_061)).toBe(`${unit(1, 'day')} ${unit(1, 'hour')}`)
  })
})

describe('previewError', () => {
  it('keeps short text untouched', () => {
    expect(previewError('connection refused')).toEqual({ text: 'connection refused', truncated: false })
    expect(previewError(undefined)).toEqual({ text: '', truncated: false })
  })
  it('cuts long text with an ellipsis and reports it', () => {
    const long = 'x'.repeat(200)
    const p = previewError(long, 20)
    expect(p.truncated).toBe(true)
    expect(p.text).toBe(`${'x'.repeat(20)}…`)
  })
  it('does not truncate text exactly at the limit', () => {
    expect(previewError('abcde', 5).truncated).toBe(false)
  })
})

describe('fill', () => {
  it('substitutes known placeholders and keeps unknown ones', () => {
    expect(fill('{a} of {b} {c}', { a: 1, b: 'two' })).toBe('1 of two {c}')
  })
})

describe('validatePeerForm', () => {
  const valid = { ...emptyPeerForm(), name: 'b', baseUrl: 'https://b.example.org', token: 's3cret' }

  it('accepts a complete form', () => {
    expect(validatePeerForm(valid, 'create')).toEqual({})
    expect(peerFormValid(valid, 'create')).toBe(true)
  })
  it('requires name, an http(s) URL and, when adding, the token', () => {
    const errors = validatePeerForm(emptyPeerForm(), 'create')
    expect(Object.keys(errors).sort()).toEqual(['baseUrl', 'name', 'token'])
  })
  it('rejects non-http URLs', () => {
    for (const bad of ['b.example.org', 'ftp://b.example.org', 'javascript:alert(1)', 'https://']) {
      expect(validatePeerForm({ ...valid, baseUrl: bad }, 'create').baseUrl).toBeDefined()
    }
    expect(validatePeerForm({ ...valid, baseUrl: 'http://10.0.0.5:8080' }, 'create').baseUrl).toBeUndefined()
  })
  it('does not require the token when editing', () => {
    expect(validatePeerForm({ ...valid, token: '' }, 'edit')).toEqual({})
  })
  it('requires a client certificate and its key together', () => {
    expect(validatePeerForm({ ...valid, clientCert: 'CERT' }, 'create').clientCert).toBeDefined()
    expect(validatePeerForm({ ...valid, clientKey: 'KEY' }, 'create').clientCert).toBeDefined()
    expect(validatePeerForm({ ...valid, clientCert: 'CERT', clientKey: 'KEY' }, 'create')).toEqual({})
  })
})

describe('peer request bodies', () => {
  it('trims the create body and omits empty TLS fields', () => {
    const form = { ...emptyPeerForm(), name: ' b ', baseUrl: ' https://b.example.org ', token: ' tok ' }
    expect(peerCreateBody(form)).toEqual({ name: 'b', base_url: 'https://b.example.org', token: 'tok' })
  })
  it('includes TLS fields that were filled in', () => {
    const form = { ...emptyPeerForm(), name: 'b', baseUrl: 'https://b', token: 't', caPem: 'CA', clientCert: 'C', clientKey: 'K' }
    expect(peerCreateBody(form)).toEqual({
      name: 'b',
      base_url: 'https://b',
      token: 't',
      ca_pem: 'CA',
      client_cert: 'C',
      client_key: 'K',
    })
  })
  it('seeds the edit form without secrets', () => {
    expect(peerFormFrom(peer({ enabled: false }))).toEqual({
      name: 'site-b',
      baseUrl: 'https://b.example.org',
      token: '',
      caPem: '',
      clientCert: '',
      clientKey: '',
      enabled: false,
    })
  })
  it('sends nothing for an untouched edit form', () => {
    expect(peerUpdateBody(peerFormFrom(peer()), peer())).toEqual({})
  })
  it('sends only the changed fields and newly typed secrets', () => {
    const form = { ...peerFormFrom(peer()), baseUrl: 'https://c.example.org', enabled: false, token: 'new' }
    expect(peerUpdateBody(form, peer())).toEqual({ base_url: 'https://c.example.org', enabled: false, token: 'new' })
  })
})

describe('resyncPending', () => {
  it('is pending while a request runs or the server still holds the request', () => {
    expect(resyncPending({ resync_requested: false }, false)).toBe(false)
    expect(resyncPending({ resync_requested: false }, true)).toBe(true)
    expect(resyncPending({ resync_requested: true }, false)).toBe(true)
  })
})

describe('upsertPeer', () => {
  it('replaces by id without mutating the input', () => {
    const before = [peer({ id: 'a' }), peer({ id: 'b', name: 'old' })]
    const after = upsertPeer(before, peer({ id: 'b', name: 'new' }))
    expect(after.map((p) => p.name)).toEqual(['site-b', 'new'])
    expect(before[1]!.name).toBe('old')
  })
  it('appends an unknown peer', () => {
    expect(upsertPeer([peer({ id: 'a' })], peer({ id: 'z' })).map((p) => p.id)).toEqual(['a', 'z'])
  })
})

describe('showsFederation', () => {
  it('offers the tab for capable formats and for federated repositories', () => {
    expect(showsFederation({ format: 'npm' })).toBe(true)
    expect(showsFederation({ format: 'deb' })).toBe(false)
    expect(showsFederation({ format: 'deb', federated: true })).toBe(true)
  })
})
