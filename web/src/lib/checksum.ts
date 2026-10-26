// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later
export type ChecksumAlgorithm = 'md5' | 'sha1' | 'sha256'

const ALGORITHM_BY_LENGTH: Record<number, ChecksumAlgorithm> = { 32: 'md5', 40: 'sha1', 64: 'sha256' }
const PREFIX = /^(?:sha-?256|sha-?1|md5):/i

/** Trimmed, lower-cased hex digest; an `sha256:`-style prefix is dropped. */
export function normalizeChecksum(text: string): string {
  return text.trim().replace(PREFIX, '').toLowerCase()
}

/** The algorithm of a full-length hex digest (32 = md5, 40 = sha1, 64 = sha256), else null. */
export function checksumAlgorithm(text: string): ChecksumAlgorithm | null {
  const hex = normalizeChecksum(text)
  return /^[0-9a-f]+$/.test(hex) ? (ALGORITHM_BY_LENGTH[hex.length] ?? null) : null
}

export function looksLikeChecksum(text: string): boolean {
  return checksumAlgorithm(text) !== null
}

export type SearchMode = 'name' | 'checksum'

/** An explicit `?mode=` wins; otherwise a digest-shaped query is a checksum lookup. */
export function effectiveMode(explicit: '' | SearchMode, query: string): SearchMode {
  if (explicit) return explicit
  return looksLikeChecksum(query) ? 'checksum' : 'name'
}
