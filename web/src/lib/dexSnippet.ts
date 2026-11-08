// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { DEX_CONNECTORS, DEX_TYPE_NAMES, type DexConnectorType } from './ssoPresets'

// A Dex config snippet for the "Dex" preset. It is generated English YAML (not
// translated) with placeholders where the admin must fill in a value. Secrets
// are never printed: the static client reads its secret from `secretEnv` and
// every connector secret is a `$ENV_VAR`, which Dex reads from its environment.

/** The environment variable of the Dex process that holds the client secret entered in OpenArtifact. */
export const DEX_CLIENT_SECRET_ENV = 'OPENARTIFACT_DEX_CLIENT_SECRET'

export interface DexSnippetInput {
  connectorType: DexConnectorType
  /** The connector ID of the form; empty shows a placeholder (or the type's usual ID). */
  connectorId: string
  clientId: string
  displayName: string
  /** The OpenArtifact redirect URI to register as the static client's redirectURI. */
  callbackUrl: string
  /** The issuer of the form, which is Dex's own URL; empty shows a placeholder. */
  dexIssuer: string
}

/** A YAML scalar: plain when that is unambiguous, else a double-quoted (JSON) string. */
function scalar(value: string): string {
  const plain = /^[A-Za-z_][A-Za-z0-9_./:@-]*$/.test(value) && !/^(true|false|null|yes|no|on|off)$/i.test(value)

  return plain ? value : JSON.stringify(value)
}

/** The connector-specific settings (indented for `config:`), taken from the Dex connector docs. */
function connectorConfig(type: Exclude<DexConnectorType, 'local'>, redirectURI: string): string[] {
  const redirect = `redirectURI: ${scalar(redirectURI)}`
  switch (type) {
    case 'ldap':
      return [
        'host: ldap.example.com:636',
        'bindDN: cn=serviceaccount,dc=example,dc=com',
        'bindPW: $LDAP_BIND_PW',
        'userSearch:',
        '  baseDN: ou=people,dc=example,dc=com',
        '  filter: "(objectClass=person)"',
        '  username: uid',
        '  idAttr: uid',
        '  emailAttr: mail',
        '  nameAttr: cn',
        'groupSearch:',
        '  baseDN: ou=groups,dc=example,dc=com',
        '  filter: "(objectClass=groupOfNames)"',
        '  userMatchers:',
        '    - userAttr: DN',
        '      groupAttr: member',
        '  nameAttr: cn',
      ]
    case 'github':
      return [
        'clientID: $GITHUB_CLIENT_ID',
        'clientSecret: $GITHUB_CLIENT_SECRET',
        redirect,
        'orgs:',
        '  - name: my-organization',
      ]
    case 'gitlab':
      return [
        'baseURL: https://gitlab.com',
        'clientID: $GITLAB_APPLICATION_ID',
        'clientSecret: $GITLAB_CLIENT_SECRET',
        redirect,
        'groups:',
        '  - my-group',
      ]
    case 'google':
      return [
        'clientID: $GOOGLE_CLIENT_ID',
        'clientSecret: $GOOGLE_CLIENT_SECRET',
        redirect,
        'hostedDomains:',
        '  - example.com',
      ]
    case 'microsoft':
      return [
        'clientID: $MICROSOFT_APPLICATION_ID',
        'clientSecret: $MICROSOFT_CLIENT_SECRET',
        redirect,
        'tenant: organizations',
      ]
    case 'oidc':
      return [
        'issuer: https://idp.example.com',
        'clientID: $OIDC_CLIENT_ID',
        'clientSecret: $OIDC_CLIENT_SECRET',
        redirect,
        'scopes:',
        '  - profile',
        '  - email',
        '  - groups',
        'claimMapping:',
        '  preferred_username: preferred_username',
        '  email: email',
        '  groups: groups',
      ]
    case 'other':
      return [
        '# The settings depend on the connector type: https://dexidp.io/docs/connectors/',
        redirect,
      ]
  }
}

/** The redirect URI of a Dex upstream connector: Dex's own issuer URL + "/callback". */
export function dexConnectorRedirectUri(dexIssuer: string): string {
  const issuer = dexIssuer.trim().replace(/\/+$/, '')

  return `${issuer || '<dex-issuer>'}/callback`
}

/** The Dex config to copy: a static client for OpenArtifact plus a skeleton of the chosen connector. */
export function dexSnippet(input: DexSnippetInput): string {
  const type = input.connectorType
  const lines = [
    '# Dex config.yaml: the OpenArtifact client',
    'staticClients:',
    `  - id: ${scalar(input.clientId.trim() || '<client-id>')}`,
    `    name: ${scalar(input.displayName.trim() || 'OpenArtifact')}`,
    '    redirectURIs:',
    `      - ${scalar(input.callbackUrl)}`,
    '    # Name of the environment variable of the Dex process that holds the client secret.',
    `    secretEnv: ${DEX_CLIENT_SECRET_ENV}`,
    '',
  ]
  if (type === 'local') {
    lines.push(
      '# The built-in connector (connector ID: local).',
      'enablePasswordDB: true',
      '# Users of the built-in connector. The hash is a bcrypt hash, for example:',
      '#   echo password | htpasswd -BinC 10 admin | cut -d: -f2',
      '# staticPasswords:',
      '#   - email: "admin@example.com"',
      '#     hash: "<bcrypt-hash>"',
      '#     username: admin',
      '#     userID: "<uuid>"',
    )
    return lines.join('\n') + '\n'
  }
  const id = input.connectorId.trim() || DEX_CONNECTORS[type].id || '<connector-id>'
  const name = type === 'other' ? '<connector-name>' : DEX_TYPE_NAMES[type]
  lines.push(
    '# Values starting with $ are read from the environment of the Dex process.',
    'connectors:',
    `  - type: ${scalar(type === 'other' ? '<connector-type>' : type)}`,
    `    id: ${scalar(id)}`,
    `    name: ${scalar(name)}`,
    '    config:',
    ...connectorConfig(type, dexConnectorRedirectUri(input.dexIssuer)).map((line) => `      ${line}`),
  )

  return lines.join('\n') + '\n'
}
