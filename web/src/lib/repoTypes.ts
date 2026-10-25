// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Which formats can be created as proxy or group repositories. Mirrors the
// server policy in internal/repotype; the server stays authoritative and
// rejects anything else with validation_failed.

import { ALL_FORMATS, type FormatId } from './formats'

export type RepoType = 'hosted' | 'proxy' | 'group'

const PROXY_FORMATS: readonly FormatId[] = ['cargo', 'deb', 'docker', 'go', 'helm', 'maven', 'npm', 'nuget', 'pypi', 'raw', 'rpm']
const GROUP_FORMATS: readonly FormatId[] = ['cargo', 'deb', 'docker', 'go', 'helm', 'maven', 'npm', 'nuget', 'pypi', 'raw', 'rpm']

// Formats a hosted repository can federate. Mirrors FederationSupported in
// internal/repotype; the server stays authoritative.
const FEDERATION_FORMATS: readonly string[] = ['raw', 'maven', 'npm', 'docker', 'helm', 'pypi', 'cargo', 'go', 'nuget']

/** True when the format has a federation codec (regardless of repository type). */
export function isFederationFormat(format: string | null | undefined): boolean {
  return format != null && FEDERATION_FORMATS.includes(format)
}

/** True when a repository of this type and format can be federated: hosted and a capable format. */
export function federationAvailable(type: RepoType | null | undefined, format: string | null | undefined): boolean {
  return (type ?? 'hosted') === 'hosted' && isFederationFormat(format)
}

/** Docker, pypi, cargo, go and nuget proxies default to Docker Hub / pypi.org / crates.io / proxy.golang.org / nuget.org; every other proxy needs an upstream URL. */
export function upstreamUrlRequired(format: FormatId): boolean {
  return format !== 'docker' && format !== 'pypi' && format !== 'cargo' && format !== 'go' && format !== 'nuget'
}

/** Placeholder for the upstream URL field. */
export function upstreamUrlPlaceholder(format: FormatId): string {
  if (format === 'docker') return 'https://registry-1.docker.io'
  if (format === 'nuget') return 'https://api.nuget.org/v3/index.json'
  if (format === 'pypi') return 'https://pypi.org/simple'
  if (format === 'cargo') return 'https://index.crates.io'
  if (format === 'go') return 'https://proxy.golang.org'
  return 'https://registry.example.com'
}

/** Formats selectable for a repository type, in picker order. */
export function allowedFormats(type: RepoType): FormatId[] {
  switch (type) {
    case 'proxy':
      return ALL_FORMATS.filter((f) => PROXY_FORMATS.includes(f))
    case 'group':
      return ALL_FORMATS.filter((f) => GROUP_FORMATS.includes(f))
    default:
      return [...ALL_FORMATS]
  }
}

export function isFormatAllowed(type: RepoType, format: FormatId): boolean {
  return allowedFormats(type).includes(format)
}

/** Keeps `format` when the type supports it, else falls back to the first allowed one. */
export function coerceFormat(type: RepoType, format: FormatId): FormatId {
  return isFormatAllowed(type, format) ? format : allowedFormats(type)[0]
}

