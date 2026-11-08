// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import {
  PRESETS,
  blankForm,
  conditionsOf,
  formOf,
  formatPatterns,
  inputOf,
  parsePatterns,
  policyProblem,
  presetForm,
  presetOfIssuer,
  type PolicyForm,
} from '../oidcPolicies'
import type { OidcPolicy } from '../../api/serviceAccounts'

const github = PRESETS[0]!

function valid(): PolicyForm {
  const form = presetForm(github, 'sa1')
  return { ...form, name: 'ci.shop', audience: 'https://oa.example', rows: [{ claim: 'repository', patterns: 'acme/shop' }] }
}

describe('parsePatterns / formatPatterns', () => {
  it('splits on commas and new lines, trims, drops blanks and repeats', () => {
    expect(parsePatterns(' acme/a ,acme/b\n\nacme/a,  ')).toEqual(['acme/a', 'acme/b'])
    expect(parsePatterns('')).toEqual([])
  })

  it('round-trips through the editor text', () => {
    expect(parsePatterns(formatPatterns(['x/*', 'y/?']))).toEqual(['x/*', 'y/?'])
  })
})

describe('presets', () => {
  it('match the issuer with or without a trailing slash', () => {
    expect(presetOfIssuer('https://token.actions.githubusercontent.com/')?.id).toBe('github')
    expect(presetOfIssuer(' https://gitlab.com ')?.id).toBe('gitlab')
    expect(presetOfIssuer('https://idp.example')).toBeUndefined()
  })

  it('start with the issuer and the identity claim, pattern empty', () => {
    const form = presetForm(github, 'sa1')
    expect(form.issuer).toBe(github.issuer)
    expect(form.serviceAccountId).toBe('sa1')
    expect(form.rows).toEqual([{ claim: 'repository', patterns: '' }])
    expect(presetForm(PRESETS[1]!).rows).toEqual([{ claim: 'project_path', patterns: '' }])
  })
})

describe('conditionsOf', () => {
  it('drops blank rows and keeps a half-filled one for validation', () => {
    expect(conditionsOf([{ claim: '', patterns: '' }, { claim: ' repository ', patterns: 'a, b' }, { claim: 'ref', patterns: '' }])).toEqual([
      { claim: 'repository', patterns: ['a', 'b'] },
      { claim: 'ref', patterns: [] },
    ])
  })
})

describe('form <-> API body', () => {
  it('serialises minutes as seconds and trims text', () => {
    const body = inputOf({ ...valid(), name: ' ci.shop ', ttlMinutes: 10 })
    expect(body).toEqual({
      name: 'ci.shop',
      issuer: github.issuer,
      audience: 'https://oa.example',
      conditions: [{ claim: 'repository', patterns: ['acme/shop'] }],
      service_account_id: 'sa1',
      token_ttl_seconds: 600,
      enabled: true,
      discovery: { mode: 'anonymous' },
      allow_private_issuer: false,
    })
  })

  it('sends the key set of the static mode only, and never the private-issuer switch with it', () => {
    const jwks = { keys: [{ kty: 'RSA', kid: 'k1', n: 'abc', e: 'AQAB' }] }
    const body = inputOf({ ...valid(), discoveryMode: 'static', jwks: JSON.stringify(jwks), allowPrivateIssuer: true })
    expect(body.discovery).toEqual({ mode: 'static', jwks })
    expect(body.allow_private_issuer).toBe(false)
    const kube = inputOf({ ...valid(), discoveryMode: 'serviceaccount', jwks: JSON.stringify(jwks), allowPrivateIssuer: true })
    expect(kube.discovery).toEqual({ mode: 'serviceaccount' })
    expect(kube.allow_private_issuer).toBe(true)
  })

  it('fills the form of an existing policy', () => {
    const policy = {
      name: 'p',
      issuer: 'https://gitlab.com',
      audience: 'a',
      service_account_id: 'sa2',
      token_ttl_seconds: 1800,
      enabled: false,
      conditions: [{ claim: 'project_path', patterns: ['g/p', 'g/q'] }],
    } as OidcPolicy
    expect(formOf(policy)).toEqual({
      name: 'p',
      issuer: 'https://gitlab.com',
      audience: 'a',
      serviceAccountId: 'sa2',
      ttlMinutes: 30,
      enabled: false,
      rows: [{ claim: 'project_path', patterns: 'g/p, g/q' }],
      discoveryMode: 'anonymous',
      jwks: '',
      allowPrivateIssuer: false,
    })
  })

  it('fills the discovery of a Kubernetes and of a static policy', () => {
    const kube = formOf({ name: 'k', discovery: { mode: 'serviceaccount' }, allow_private_issuer: true } as OidcPolicy)
    expect(kube).toMatchObject({ discoveryMode: 'serviceaccount', jwks: '', allowPrivateIssuer: true })
    const fixed = formOf({ name: 's', discovery: { mode: 'static', jwks: { keys: [] } } } as OidcPolicy)
    expect(fixed.discoveryMode).toBe('static')
    expect(JSON.parse(fixed.jwks)).toEqual({ keys: [] })
  })

  it('a blank form defaults to 15 minutes, enabled, one empty row', () => {
    const form = blankForm('sa1')
    expect(form).toMatchObject({ ttlMinutes: 15, enabled: true, serviceAccountId: 'sa1' })
    expect(form.rows).toHaveLength(1)
  })
})

describe('policyProblem', () => {
  it('accepts a complete form', () => {
    expect(policyProblem(valid())).toBeNull()
  })

  it('accepts dotted claim paths and a static key set', () => {
    const rows = [{ claim: 'kubernetes.io.namespace', patterns: 'team-a' }]
    expect(policyProblem({ ...valid(), rows })).toBeNull()
    expect(policyProblem({ ...valid(), rows, discoveryMode: 'static', jwks: '{"keys":[]}' })).toBeNull()
    expect(policyProblem({ ...valid(), rows, discoveryMode: 'serviceaccount', jwks: 'ignored outside static' })).toBeNull()
  })

  it.each([
    ['name', { name: 'bad name' }],
    ['issuer', { issuer: 'http://insecure.example' }],
    ['audience', { audience: ' ' }],
    ['account', { serviceAccountId: '' }],
    ['ttl', { ttlMinutes: 61 }],
    ['ttl', { ttlMinutes: 0 }],
    ['ttl', { ttlMinutes: Number.NaN }],
    ['noConditions', { rows: [{ claim: '', patterns: '' }] }],
    ['claim', { rows: [{ claim: '', patterns: 'x' }] }],
    ['patterns', { rows: [{ claim: 'repository', patterns: '' }] }],
    ['tooManyPatterns', { rows: [{ claim: 'repository', patterns: 'a,b,c,d,e,f,g,h,i' }] }],
    ['claim', { rows: [{ claim: 'kubernetes..namespace', patterns: 'x' }] }],
    ['claim', { rows: [{ claim: '.kubernetes.io', patterns: 'x' }] }],
    ['claim', { rows: [{ claim: 'kubernetes.io.', patterns: 'x' }] }],
    ['claim', { rows: [{ claim: 'has space', patterns: 'x' }] }],
    ['jwks', { discoveryMode: 'static', jwks: '' }],
    ['jwks', { discoveryMode: 'static', jwks: 'not json' }],
    ['jwks', { discoveryMode: 'static', jwks: '[1]' }],
  ] as const)('reports %s', (problem, change) => {
    expect(policyProblem({ ...valid(), ...change })).toBe(problem)
  })
})
