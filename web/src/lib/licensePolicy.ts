// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { LicensePolicy, LicensePolicyAction } from '../api/licensePolicies'

/** Longest policy name the server accepts. */
export const LICENSE_POLICY_NAME_MAX = 255

/** License identifiers typed as a list: separated by commas, semicolons, spaces or new lines; duplicates dropped, order kept. */
export function parseLicenseList(text: string): string[] {
  const seen = new Set<string>()
  for (const part of text.split(/[\s,;]+/)) {
    if (part !== '') seen.add(part)
  }
  return [...seen]
}

export type LicensePolicyProblem = 'required' | 'tooLong' | null

export function licensePolicyNameProblem(name: string): LicensePolicyProblem {
  const n = name.trim()
  if (n === '') return 'required'
  return n.length > LICENSE_POLICY_NAME_MAX ? 'tooLong' : null
}

/** A policy's action as one of the three the UI knows; the server defaults an empty one to warn. */
export function policyAction(p: Pick<LicensePolicy, 'action'>): LicensePolicyAction {
  return p.action === 'block' || p.action === 'allow' ? p.action : 'warn'
}
