// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Accent colours for storage backends, AI providers and SSO providers.
// The hex values below are third-party brand colours (Google, Microsoft,
// OpenAI, GitHub, GitLab, ...) that must not follow the app theme; every
// other entry uses a theme token via its CSS variable so it adapts to both
// themes.

const cssVar = (name: string): string => `rgb(var(--color-${name}))`

/** Fallback for unknown kinds/providers. */
const FALLBACK_COLOR = cssVar('muted')
/** Fallback for SSO providers without a known brand. */
const SSO_FALLBACK_COLOR = cssVar('primary')

/** Translucent tint of any CSS colour (hex or `rgb(var(--...))`). */
export function tint(color: string, percent: number): string {
  return `color-mix(in srgb, ${color} ${percent}%, transparent)`
}

const STORAGE_BACKEND_COLORS: Record<string, string> = {
  filesystem: cssVar('muted'),
  s3: cssVar('severity-medium'),
  gcs: '#4285f4',
  azure: '#0078d4',
  garage: cssVar('warning'),
  seaweedfs: cssVar('info'),
  oci: cssVar('danger'),
}

const AI_PROVIDER_COLORS: Record<string, string> = {
  openai: '#10a37f',
  azure: '#0078d4',
  ollama: cssVar('warning'),
  custom: cssVar('primary'),
}

/** Matched by substring against the lower-cased provider slug. */
const SSO_PROVIDER_COLORS: Record<string, string> = {
  google: '#4285f4',
  github: '#24292f',
  gitlab: '#fc6d26',
  microsoft: '#00a4ef',
  azure: '#0078d4',
  okta: '#007dc1',
  auth0: '#eb5424',
  keycloak: '#4d9a00',
  onelogin: '#1e3a5f',
  ping: '#e8401c',
  adfs: '#0078d4',
}

export function storageBackendColor(kind: string): string {
  return STORAGE_BACKEND_COLORS[kind] ?? FALLBACK_COLOR
}

export function aiProviderColor(key: string): string {
  return AI_PROVIDER_COLORS[key] ?? FALLBACK_COLOR
}

export function ssoProviderColor(slug: string): string {
  const key = slug.toLowerCase()
  for (const [k, v] of Object.entries(SSO_PROVIDER_COLORS)) {
    if (key.includes(k)) return v
  }
  return SSO_FALLBACK_COLOR
}
