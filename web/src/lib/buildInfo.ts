// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { formatDateTime } from './datetime'

/** Short-SHA length used by every build path (Makefile, Dockerfile, goreleaser). */
const SHORT_SHA_LENGTH = 7

const HEX_SHA = /^[0-9a-f]{7,40}$/i

/**
 * True when a build-metadata field carries a real value. Builds that were not
 * stamped with -ldflags report "unknown" (commit, build time) or "dev" (version),
 * which must not be presented as if they were data.
 */
export function isStamped(value: string | null | undefined): value is string {
  const v = value?.trim()
  return !!v && v.toLowerCase() !== 'unknown'
}

/** 7-character short SHA; non-SHA stamps (e.g. a tag name) pass through; null when unstamped. */
export function formatCommit(value: string | null | undefined): string | null {
  if (!isStamped(value)) return null
  const v = value.trim()
  return HEX_SHA.test(v) ? v.slice(0, SHORT_SHA_LENGTH) : v
}

/** Localised build time; the raw stamp when it is not a parseable date; null when unstamped. */
export function formatBuildTime(value: string | null | undefined): string | null {
  if (!isStamped(value)) return null
  const v = value.trim()
  return Number.isNaN(new Date(v).getTime()) ? v : formatDateTime(v)
}
