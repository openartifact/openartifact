// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { emptyVexFeedForm, validateVexFeedForm, vexFeedBody, VEX_FEED_KEYS_MAX_BYTES } from '../vexFeeds'

const valid = { ...emptyVexFeedForm(), name: 'acme', url: 'https://acme.example/.well-known/csaf/provider-metadata.json' }

describe('validateVexFeedForm', () => {
  it('accepts a named https feed with the default interval', () => {
    expect(validateVexFeedForm(valid)).toEqual({})
  })

  it('requires the name and the URL', () => {
    expect(validateVexFeedForm(emptyVexFeedForm())).toEqual({ name: 'required', url: 'required' })
    expect(validateVexFeedForm({ ...valid, name: '  ' }).name).toBe('required')
  })

  it('applies the server name rule', () => {
    expect(validateVexFeedForm({ ...valid, name: '-acme' }).name).toBe('invalid')
    expect(validateVexFeedForm({ ...valid, name: 'acme psirt' }).name).toBe('invalid')
    expect(validateVexFeedForm({ ...valid, name: 'a'.repeat(64) }).name).toBe('invalid')
    expect(validateVexFeedForm({ ...valid, name: 'acme_psirt.v2-1' }).name).toBeUndefined()
  })

  it('wants https, or http to a loopback host', () => {
    expect(validateVexFeedForm({ ...valid, url: 'http://acme.example/feed.json' }).url).toBe('invalid')
    expect(validateVexFeedForm({ ...valid, url: 'ftp://acme.example/feed.json' }).url).toBe('invalid')
    expect(validateVexFeedForm({ ...valid, url: 'not a url' }).url).toBe('invalid')
    expect(validateVexFeedForm({ ...valid, url: 'http://localhost:8080/csaf.json' }).url).toBeUndefined()
  })

  it('bounds the interval to whole hours between 1 and 720', () => {
    for (const intervalHours of [0, 721, 1.5, -1, ''] as const) {
      expect(validateVexFeedForm({ ...valid, intervalHours }).intervalHours).toBe('range')
    }
    expect(validateVexFeedForm({ ...valid, intervalHours: 1 })).toEqual({})
    expect(validateVexFeedForm({ ...valid, intervalHours: 720 })).toEqual({})
  })

  it('caps the pinned keys at 64 KiB', () => {
    expect(validateVexFeedForm({ ...valid, pgpKeys: 'k'.repeat(VEX_FEED_KEYS_MAX_BYTES + 1) }).pgpKeys).toBe('tooLarge')
    expect(validateVexFeedForm({ ...valid, pgpKeys: 'k'.repeat(VEX_FEED_KEYS_MAX_BYTES) }).pgpKeys).toBeUndefined()
  })
})

describe('vexFeedBody', () => {
  it('trims and sends every editable field', () => {
    expect(vexFeedBody({ ...valid, name: ' acme ', url: ` ${valid.url} `, pgpKeys: '\nKEY\n', requireSignature: false, intervalHours: 6 })).toEqual({
      name: 'acme',
      url: valid.url,
      enabled: true,
      require_signature: false,
      pgp_keys: 'KEY',
      interval_hours: 6,
    })
  })
})
