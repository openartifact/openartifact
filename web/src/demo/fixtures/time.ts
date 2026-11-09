// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

const HOUR = 3_600_000

/** RFC 3339 time `hours` before the page loaded (negative: in the future), so the demo always looks recent. */
export function ago(hours: number): string {
  return new Date(Date.now() - hours * HOUR).toISOString()
}

const KINDS = ['user', 'group', 'project', 'token', 'repo', 'file', 'finding', 'audit'] as const

/** A stable UUID per kind and number: `uid('repo', 3)`. */
export function uid(kind: (typeof KINDS)[number], n: number): string {
  const k = (KINDS.indexOf(kind) + 1).toString(16).padStart(4, '0')
  return `00000000-${k}-4000-8000-${n.toString(16).padStart(12, '0')}`
}

/** A deterministic sha256-shaped hex digest of `seed`. */
export function digest(seed: string): string {
  let h = 0x811c9dc5
  let out = ''
  for (let round = 0; out.length < 64; round++) {
    for (const ch of `${seed}#${round}`) h = Math.imul(h ^ ch.charCodeAt(0), 0x01000193) >>> 0
    out += h.toString(16).padStart(8, '0')
  }
  return out.slice(0, 64)
}
