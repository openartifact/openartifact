// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import {
  siAuth0,
  siGithub,
  siGitlab,
  siGoogle,
  siKeycloak,
  siOkta,
  siOllama,
} from 'simple-icons'

interface BrandIcon {
  path: string
  viewBox: string
}

const viewBox = '0 0 24 24'

const ssoIcons: readonly (readonly [string, BrandIcon])[] = [
  ['google', { path: siGoogle.path, viewBox }],
  ['github', { path: siGithub.path, viewBox }],
  ['gitlab', { path: siGitlab.path, viewBox }],
  ['okta', { path: siOkta.path, viewBox }],
  ['auth0', { path: siAuth0.path, viewBox }],
  ['keycloak', { path: siKeycloak.path, viewBox }],
]

const ollamaIcon: BrandIcon = { path: siOllama.path, viewBox }

export function ssoBrandIcon(slug: string): BrandIcon | undefined {
  const normalized = slug.toLowerCase()
  for (const [brand, icon] of ssoIcons) {
    if (normalized.includes(brand)) return icon
  }
}

export function aiBrandIcon(provider: string): BrandIcon | undefined {
  return provider.toLowerCase() === 'ollama' ? ollamaIcon : undefined
}
