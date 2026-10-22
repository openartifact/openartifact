// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

/**
 * Returns `next` when it is a same-origin relative path and '/' otherwise.
 * Absolute and scheme-relative URLs ('//host', '/\host', 'https://host'),
 * backslashes and control characters are rejected, mirroring the server's
 * safeRedirectPath, so a `?next=` value can never send the user off-site.
 */
export function safeNextPath(next: unknown): string {
  if (typeof next !== 'string') return '/'
  if (!next.startsWith('/') || next.startsWith('//') || next.startsWith('/\\')) return '/'
  for (const ch of next) {
    const c = ch.codePointAt(0) ?? 0
    if (ch === '\\' || c < 0x20 || c === 0x7f) return '/'
  }
  return next
}
