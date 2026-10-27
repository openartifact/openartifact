// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Which formats can be created as proxy or group repositories. The lists come
// from the server policy in internal/repotype (formats.generated.ts, generated
// by cmd/genformats); the server stays authoritative and rejects anything else
// with validation_failed.

import { ALL_FORMATS, type FormatId } from './formats'
import { DEFAULT_UPSTREAMS, FEDERATION_FORMATS, GROUP_FORMATS, PROXY_FORMATS } from './formats.generated'

export type RepoType = 'hosted' | 'proxy' | 'group'

/** True when the format has a federation codec (regardless of repository type). */
export function isFederationFormat(format: string | null | undefined): boolean {
  return format != null && (FEDERATION_FORMATS as readonly string[]).includes(format)
}

/** True when a repository of this type and format can be federated: hosted and a capable format. */
export function federationAvailable(type: RepoType | null | undefined, format: string | null | undefined): boolean {
  return (type ?? 'hosted') === 'hosted' && isFederationFormat(format)
}

/** Proxy upstream a format falls back to on the server (the generated default upstreams); every other proxy needs an upstream URL. */
export function upstreamUrlRequired(format: FormatId): boolean {
  return DEFAULT_UPSTREAMS[format] === undefined
}

// Example upstreams for formats without a server default, shown as the
// placeholder of the required upstream URL field.
const EXAMPLE_UPSTREAMS: Readonly<Partial<Record<FormatId, string>>> = {
  apk: 'https://dl-cdn.alpinelinux.org/alpine',
  sbt: 'https://repo.scala-sbt.org/scalasbt/sbt-plugin-releases',
  conda: 'https://conda.anaconda.org/conda-forge',
}

/** Placeholder for the upstream URL field: the server default when there is one, else an example. */
export function upstreamUrlPlaceholder(format: FormatId): string {
  return DEFAULT_UPSTREAMS[format] ?? EXAMPLE_UPSTREAMS[format] ?? 'https://registry.example.com'
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

