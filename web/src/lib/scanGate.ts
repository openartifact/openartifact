// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { GateCriteria, SecurityFinding } from '../api/security'
import { comparePriority } from './exploitPriority'

/** The exploitation criteria of the gate form: a KEV switch and an EPSS threshold typed as a percentage ('' = no threshold). */
export interface GateForm {
  kev: boolean
  epssPercent: string
}

/** The criteria the API takes, or null while the form names no condition (a gate without conditions passes everything). */
export function gateCriteria(form: GateForm): GateCriteria | null {
  const percent = form.epssPercent.trim() === '' ? Number.NaN : Number(form.epssPercent)
  const epss = Number.isFinite(percent) && percent > 0 && percent <= 100 ? percent / 100 : undefined
  if (!form.kev && epss === undefined) return null
  return { kev: form.kev || undefined, epss_min: epss }
}

/** True when the EPSS field holds text that is not a percentage in (0, 100]. */
export function epssInvalid(form: GateForm): boolean {
  const s = form.epssPercent.trim()
  if (s === '') return false
  const n = Number(s)
  return !Number.isFinite(n) || n <= 0 || n > 100
}

/** The scans that meet any condition of `criteria`: the gate fails on any one, so the lists of each condition are united. */
export function unionFindings(lists: readonly (readonly SecurityFinding[])[]): SecurityFinding[] {
  const byId = new Map<string, SecurityFinding>()
  for (const f of lists.flat()) if (!byId.has(f.id)) byId.set(f.id, f)
  return [...byId.values()].sort((a, b) => comparePriority(a.priority, b.priority))
}

/** How a gate reason reads: `kev`, `epss>=0.1` or `priority>=high`. */
export function reasonParts(reason: string): { kind: 'kev' | 'epss' | 'priority' | 'other'; value: string } {
  if (reason === 'kev') return { kind: 'kev', value: '' }
  const m = /^(epss|priority)>=(.+)$/.exec(reason)
  return m ? { kind: m[1] as 'epss' | 'priority', value: m[2]! } : { kind: 'other', value: reason }
}
