// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { DEX_CLIENT_SECRET_ENV, dexConnectorRedirectUri, dexSnippet, type DexSnippetInput } from '../dexSnippet'
import { DEX_CONNECTOR_TYPES } from '../ssoPresets'

const input = (over: Partial<DexSnippetInput> = {}): DexSnippetInput => ({
  connectorType: 'github',
  connectorId: 'github',
  clientId: 'openartifact',
  displayName: 'Acme SSO',
  callbackUrl: 'https://artifacts.example.org/api/v1/auth/sso/oidc/acme/callback',
  dexIssuer: 'https://dex.example.org',
  ...over,
})

describe('dexSnippet', () => {
  it('registers OpenArtifact as a static client whose secret is read from the environment', () => {
    const yaml = dexSnippet(input())
    expect(yaml).toContain('staticClients:\n  - id: openartifact\n    name: "Acme SSO"\n')
    expect(yaml).toContain('      - https://artifacts.example.org/api/v1/auth/sso/oidc/acme/callback\n')
    expect(yaml).toContain(`secretEnv: ${DEX_CLIENT_SECRET_ENV}`)
    expect(yaml).not.toMatch(/^\s*secret:/m)
  })

  it('never prints a secret: every secret setting is an environment reference', () => {
    for (const type of DEX_CONNECTOR_TYPES) {
      const yaml = dexSnippet(input({ connectorType: type, connectorId: '' }))
      for (const line of yaml.split('\n').filter((l) => /(secret|bindPW|password)\s*:/i.test(l) && !l.trimStart().startsWith('#'))) {
        expect(line).toMatch(/:\s*(\$[A-Z_]+|OPENARTIFACT_DEX_CLIENT_SECRET)$/)
      }
    }
  })

  it('uses placeholders for a client id and a display name that are not typed yet', () => {
    const yaml = dexSnippet(input({ clientId: '', displayName: ' ' }))
    expect(yaml).toContain('  - id: "<client-id>"')
    expect(yaml).toContain('    name: OpenArtifact')
  })

  it('quotes values that would change meaning as YAML', () => {
    const yaml = dexSnippet(input({ clientId: '123', displayName: 'Acme: "prod" #1' }))
    expect(yaml).toContain('  - id: "123"')
    expect(yaml).toContain('    name: "Acme: \\"prod\\" #1"')
  })

  it('writes the connector id of the form, and the redirectURI from the Dex issuer', () => {
    const yaml = dexSnippet(input({ connectorId: 'github-main', dexIssuer: 'https://auth.example.org/dex/' }))
    expect(yaml).toContain('connectors:\n  - type: github\n    id: github-main\n')
    expect(yaml).toContain('redirectURI: https://auth.example.org/dex/callback')
    expect(dexConnectorRedirectUri('https://auth.example.org/dex///')).toBe('https://auth.example.org/dex/callback')
  })

  it('uses a placeholder redirectURI until the Dex issuer is typed', () => {
    expect(dexSnippet(input({ dexIssuer: '' }))).toContain('redirectURI: "<dex-issuer>/callback"')
  })

  it('falls back to the usual connector id of the type when the form has none', () => {
    expect(dexSnippet(input({ connectorType: 'gitlab', connectorId: ' ' }))).toContain('    id: gitlab\n')
  })

  it('has the fields of each connector from the Dex docs', () => {
    const fields: Record<string, string[]> = {
      ldap: ['type: ldap', 'host: ', 'bindDN: ', 'bindPW: $LDAP_BIND_PW', 'userSearch:', 'groupSearch:'],
      github: ['type: github', 'clientID: $GITHUB_CLIENT_ID', 'clientSecret: $GITHUB_CLIENT_SECRET', 'redirectURI: ', 'orgs:'],
      gitlab: ['type: gitlab', 'baseURL: https://gitlab.com', 'clientID: ', 'clientSecret: ', 'redirectURI: ', 'groups:'],
      google: ['type: google', 'clientID: ', 'clientSecret: ', 'redirectURI: ', 'hostedDomains:'],
      microsoft: ['type: microsoft', 'clientID: ', 'clientSecret: ', 'redirectURI: ', 'tenant: '],
      oidc: ['type: oidc', 'issuer: ', 'clientID: ', 'clientSecret: ', 'redirectURI: ', 'scopes:', 'claimMapping:'],
    }
    for (const [type, expected] of Object.entries(fields)) {
      const yaml = dexSnippet(input({ connectorType: type as never, connectorId: '' }))
      for (const text of expected) expect(yaml).toContain(text)
      expect(yaml).toContain(`    id: ${type}\n`)
    }
  })

  it('the built-in local connector enables the password database and notes staticPasswords', () => {
    const yaml = dexSnippet(input({ connectorType: 'local' }))
    expect(yaml).toContain('enablePasswordDB: true')
    expect(yaml).toContain('staticPasswords:')
    expect(yaml).not.toContain('connectors:')
  })

  it('another connector type gets a generic skeleton that points to the Dex docs', () => {
    const yaml = dexSnippet(input({ connectorType: 'other', connectorId: 'corp-saml' }))
    expect(yaml).toContain('  - type: "<connector-type>"\n    id: corp-saml\n')
    expect(yaml).toContain('https://dexidp.io/docs/connectors/')
    expect(dexSnippet(input({ connectorType: 'other', connectorId: '' }))).toContain('id: "<connector-id>"')
  })
})
