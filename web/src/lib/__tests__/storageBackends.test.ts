// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import {
  BACKEND_TYPES,
  buildBackendTypes,
  locationSummary,
  statusBadgeClass,
  statusDotClass,
  statusLabel,
  submittableConfig,
  typeColor,
  typeIcon,
  visibleFields,
  type FieldDef,
  type StorageBackend,
} from '../storageBackends'
import { storageBackendColor } from '../brandColors'
import { t } from '../../i18n/en'

const KINDS = ['filesystem', 's3', 'gcs', 'azure', 'garage', 'seaweedfs', 'oci']

function field(kind: string, key: string): FieldDef {
  const f = BACKEND_TYPES[kind].fields.find((x) => x.key === key)
  if (!f) throw new Error(`${kind} has no field ${key}`)
  return f
}

function backend(kind: string, config: Record<string, unknown>): StorageBackend {
  return { id: 'x', name: 'n', kind, status: 'unknown', is_default: false, config }
}

describe('BACKEND_TYPES registry', () => {
  it('registers every backend kind the server supports', () => {
    expect(Object.keys(BACKEND_TYPES).sort()).toEqual([...KINDS].sort())
  })

  it('gives every kind a label, description and at least one field', () => {
    for (const kind of KINDS) {
      const meta = BACKEND_TYPES[kind]
      expect(meta.label).not.toBe('')
      expect(meta.desc).not.toBe('')
      expect(meta.fields.length).toBeGreaterThan(0)
    }
  })

  it('has unique field keys and non-empty labels within each kind', () => {
    for (const kind of KINDS) {
      const keys = BACKEND_TYPES[kind].fields.map((f) => f.key)
      expect(new Set(keys).size).toBe(keys.length)
      for (const f of BACKEND_TYPES[kind].fields) expect(f.label).not.toBe('')
    }
  })

  it('requires the connection essentials of each kind', () => {
    const required = (kind: string) =>
      BACKEND_TYPES[kind].fields.filter((f) => f.required).map((f) => f.key)
    expect(required('filesystem')).toEqual(['path'])
    expect(required('s3')).toEqual(['bucket', 'region', 'accessKey', 'secretKey'])
    expect(required('gcs')).toEqual(['project', 'bucket', 'creds'])
    expect(required('azure')).toEqual(['account', 'container', 'accountKey', 'connStr'])
    expect(required('garage')).toEqual(['endpoint', 'bucket', 'region', 'accessKey', 'secretKey'])
    expect(required('seaweedfs')).toEqual(['endpoint', 'bucket', 'region', 'accessKey', 'secretKey'])
    expect(required('oci')).toEqual(['endpoint', 'bucket'])
  })

  it('marks credentials as secret fields', () => {
    expect(field('s3', 'secretKey').type).toBe('secret')
    expect(field('garage', 'secretKey').type).toBe('secret')
    expect(field('seaweedfs', 'secretKey').type).toBe('secret')
    expect(field('azure', 'accountKey').type).toBe('secret')
    expect(field('azure', 'connStr').type).toBe('secret')
    expect(field('oci', 'password').type).toBe('secret')
  })

  it('keeps secret fields masked, monospaced and free of defaults', () => {
    for (const kind of KINDS) {
      for (const f of BACKEND_TYPES[kind].fields.filter((x) => x.type === 'secret')) {
        expect(f.mono).toBe(true)
        expect(f.default).toBeUndefined()
      }
    }
    expect(field('s3', 'secretKey').placeholder).toBe('••••••••')
    expect(field('azure', 'connStr').placeholder).toBe('DefaultEndpointsProtocol=https;…')
  })

  it('does not treat plain identifiers as secrets', () => {
    expect(field('s3', 'accessKey').type).toBe('text')
    expect(field('oci', 'username').type).toBe('text')
  })

  it('produces the exact field objects the factories stand for', () => {
    expect(field('s3', 'accessKey')).toEqual({
      key: 'accessKey',
      label: t('adm.storage.field.accessKey'),
      type: 'text',
      placeholder: 'AKIA…',
      mono: true,
      required: true,
    })
    expect(field('s3', 'pathStyle')).toEqual({
      key: 'pathStyle',
      label: field('s3', 'pathStyle').label,
      type: 'toggle',
      half: true,
      hint: field('s3', 'pathStyle').hint,
    })
    expect(field('seaweedfs', 'region')).toEqual({
      key: 'region',
      label: field('seaweedfs', 'region').label,
      type: 'text',
      placeholder: 'us-east-1',
      mono: true,
      required: true,
      half: true,
      default: 'us-east-1',
    })
    expect(field('garage', 'region').default).toBe('garage')
    expect(field('garage', 'endpoint').required).toBe(true)
    expect(field('s3', 'endpoint').required).toBeUndefined()
  })

  it('keeps toggle and segmented fields free of text-input attributes', () => {
    for (const kind of KINDS) {
      for (const f of BACKEND_TYPES[kind].fields.filter((x) => x.type === 'toggle')) {
        expect(f.placeholder).toBeUndefined()
        expect(f.mono).toBeUndefined()
      }
    }
    const auth = field('azure', 'authMode')
    expect(auth.type).toBe('segmented')
    expect(auth.options?.map((o) => o.v)).toEqual(['key', 'conn'])
    expect(auth.default).toBe('key')
  })

  it('rebuilds an identical registry on each call', () => {
    const again = buildBackendTypes()
    for (const kind of KINDS) {
      expect(again[kind].fields).toEqual(BACKEND_TYPES[kind].fields)
      expect(again[kind].label).toBe(BACKEND_TYPES[kind].label)
    }
  })
})

describe('visibleFields', () => {
  const keys = (kind: string, config: Record<string, unknown>) => visibleFields(kind, config).map((f) => f.key)

  it('returns nothing for unknown or empty kinds', () => {
    expect(visibleFields('', {})).toEqual([])
    expect(visibleFields('nope', {})).toEqual([])
  })

  it('returns every field of kinds without conditions', () => {
    expect(keys('oci', {})).toEqual(BACKEND_TYPES.oci.fields.map((f) => f.key))
  })

  it('falls back to the default auth mode for azure', () => {
    expect(keys('azure', {})).toEqual(['authMode', 'account', 'container', 'accountKey'])
  })

  it('switches azure fields with the chosen auth mode', () => {
    expect(keys('azure', { authMode: 'conn' })).toEqual(['authMode', 'container', 'connStr'])
    expect(keys('azure', { authMode: 'key' })).toEqual(['authMode', 'account', 'container', 'accountKey'])
  })
})

describe('submittableConfig', () => {
  it('drops has_ flags and empty values but keeps false and zero', () => {
    expect(
      submittableConfig({
        bucket: 'b',
        has_secretKey: true,
        secretKey: '',
        region: undefined,
        endpoint: null,
        pathStyle: false,
        retries: 0,
      }),
    ).toEqual({ bucket: 'b', pathStyle: false, retries: 0 })
  })
})

describe('locationSummary', () => {
  it('summarises each kind', () => {
    expect(locationSummary(backend('filesystem', { path: '/data' }))).toBe('/data')
    expect(locationSummary(backend('s3', { bucket: 'b', region: 'r' }))).toBe('b · r')
    expect(locationSummary(backend('gcs', { bucket: 'b', project: 'p' }))).toBe('b · p')
    expect(locationSummary(backend('azure', { container: 'c', account: 'a' }))).toBe('c · a')
    expect(locationSummary(backend('garage', { bucket: 'b', endpoint: 'e' }))).toBe('b · e')
    expect(locationSummary(backend('oci', { bucket: 'org/x', endpoint: 'reg' }))).toBe('org/x · reg')
  })

  it('shows placeholder words for unset values', () => {
    expect(locationSummary(backend('filesystem', {}))).toBe('')
    expect(locationSummary(backend('s3', {}))).toBe('bucket · region')
    expect(locationSummary(backend('azure', {}))).toBe('container · ')
    expect(locationSummary(backend('seaweedfs', {}))).toBe('bucket · endpoint')
    expect(locationSummary(backend('oci', {}))).toBe('repository · endpoint')
  })

  it('is empty for unknown kinds', () => {
    expect(locationSummary(backend('nope', { bucket: 'b' }))).toBe('')
  })
})

describe('presentation helpers', () => {
  it('maps status to label and classes with a muted fallback', () => {
    expect(statusLabel('healthy')).toBe('Healthy')
    expect(statusLabel('weird')).toBe('weird')
    expect(statusBadgeClass('unreachable')).toBe('bg-danger-soft text-danger')
    expect(statusBadgeClass('weird')).toBe('bg-surface-hover text-muted')
    expect(statusDotClass('healthy')).toBe('bg-success')
    expect(statusDotClass('weird')).toBe('bg-muted')
  })

  it('uses the shared brand colours and a distinct icon for filesystem', () => {
    expect(typeColor('s3')).toBe(storageBackendColor('s3'))
    expect(typeIcon('filesystem')).not.toBe(typeIcon('s3'))
    expect(typeIcon('s3')).toBe(typeIcon('gcs'))
  })
})
