// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

/**
 * Returns `next` when it is a same-origin app path (`/x`, not `//host` or `/\host`,
 * and not the login page itself); otherwise `fallback`. Guards post-login redirects
 * against open-redirect targets.
 */
export function safeNextPath(next: unknown, fallback = '/'): string {
  if (typeof next !== 'string') return fallback
  if (!/^\/(?![/\\])/.test(next)) return fallback
  if (next === '/login' || next.startsWith('/login?') || next.startsWith('/login#')) return fallback
  return next
}
