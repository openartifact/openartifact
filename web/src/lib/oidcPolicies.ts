// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pure logic of the trust policy form: the condition rows (a claim and its patterns as one text),
// the CI presets, and the mapping between the form and the API body.

import type { OidcCondition, OidcPolicy, OidcPolicyInput } from '../api/serviceAccounts'

/** The server accepts 1-16 conditions of 1-8 patterns each. */
export const MAX_CONDITIONS = 16
export const MAX_PATTERNS = 8

/** Exchanged tokens live 60 to 3600 seconds; the form speaks minutes. */
export const MIN_TTL_MINUTES = 1
export const MAX_TTL_MINUTES = 60
export const DEFAULT_TTL_MINUTES = 15

export const NAME_PATTERN = /^[A-Za-z0-9._-]{1,64}$/

/** One row of the conditions editor: the patterns stay free text until the form is submitted. */
export interface ConditionRow {
  claim: string
  patterns: string
}

export type PresetId = 'github' | 'gitlab'

export interface Preset {
  id: PresetId
  issuer: string
  /** The claim that names the repository or project: a shared issuer is only accepted with a pattern on it. */
  claim: string
}

export const PRESETS: readonly Preset[] = [
  { id: 'github', issuer: 'https://token.actions.githubusercontent.com', claim: 'repository' },
  { id: 'gitlab', issuer: 'https://gitlab.com', claim: 'project_path' },
]

/** The preset an issuer URL belongs to, for the hint under the issuer field. */
export function presetOfIssuer(issuer: string): Preset | undefined {
  const url = issuer.trim().replace(/\/+$/, '')
  return PRESETS.find((p) => p.issuer === url)
}

/** Splits a pattern list typed one per line or separated by commas; blanks and repeats go. */
export function parsePatterns(text: string): string[] {
  const seen = new Set<string>()
  for (const part of text.split(/[\n,]/)) {
    const pattern = part.trim()
    if (pattern) seen.add(pattern)
  }
  return [...seen]
}

/** Patterns as the editor shows them: comma separated. */
export function formatPatterns(patterns: readonly string[]): string {
  return patterns.join(', ')
}

export const emptyRow = (): ConditionRow => ({ claim: '', patterns: '' })

export const rowsOf = (conditions: readonly OidcCondition[]): ConditionRow[] =>
  conditions.map((c) => ({ claim: c.claim, patterns: formatPatterns(c.patterns) }))

/** The row a preset starts with: its identity claim, pattern still to be filled in. */
export const presetRows = (preset: Preset): ConditionRow[] => [{ claim: preset.claim, patterns: '' }]

/** API conditions of the rows; a row with neither claim nor patterns is an unused blank and is dropped. */
export function conditionsOf(rows: readonly ConditionRow[]): OidcCondition[] {
  return rows
    .filter((r) => r.claim.trim() !== '' || r.patterns.trim() !== '')
    .map((r) => ({ claim: r.claim.trim(), patterns: parsePatterns(r.patterns) }))
}

export interface PolicyForm {
  name: string
  issuer: string
  audience: string
  serviceAccountId: string
  ttlMinutes: number
  enabled: boolean
  rows: ConditionRow[]
}

export const blankForm = (serviceAccountId = ''): PolicyForm => ({
  name: '',
  issuer: '',
  audience: '',
  serviceAccountId,
  ttlMinutes: DEFAULT_TTL_MINUTES,
  enabled: true,
  rows: [emptyRow()],
})

/** The form pre-filled for a preset: its issuer and identity claim row. */
export function presetForm(preset: Preset, serviceAccountId = ''): PolicyForm {
  return { ...blankForm(serviceAccountId), issuer: preset.issuer, rows: presetRows(preset) }
}

export function formOf(policy: OidcPolicy): PolicyForm {
  return {
    name: policy.name ?? '',
    issuer: policy.issuer ?? '',
    audience: policy.audience ?? '',
    serviceAccountId: policy.service_account_id ?? '',
    ttlMinutes: Math.round((policy.token_ttl_seconds ?? DEFAULT_TTL_MINUTES * 60) / 60),
    enabled: policy.enabled ?? true,
    rows: policy.conditions?.length ? rowsOf(policy.conditions) : [emptyRow()],
  }
}

export function inputOf(form: PolicyForm): OidcPolicyInput {
  return {
    name: form.name.trim(),
    issuer: form.issuer.trim(),
    audience: form.audience.trim(),
    conditions: conditionsOf(form.rows),
    service_account_id: form.serviceAccountId,
    token_ttl_seconds: Math.round(form.ttlMinutes) * 60,
    enabled: form.enabled,
  }
}

export type PolicyProblem =
  | 'name'
  | 'issuer'
  | 'audience'
  | 'account'
  | 'ttl'
  | 'noConditions'
  | 'claim'
  | 'patterns'
  | 'tooManyPatterns'

/**
 * The first thing wrong with the form that can be told without the server. Identity rules (a shared
 * issuer needs a pinned repository or project) stay with the server, whose message is shown as is.
 */
export function policyProblem(form: PolicyForm): PolicyProblem | null {
  const input = inputOf(form)
  if (!NAME_PATTERN.test(input.name)) return 'name'
  if (!/^https:\/\/\S+$/.test(input.issuer)) return 'issuer'
  if (!input.audience) return 'audience'
  if (!input.service_account_id) return 'account'
  if (!Number.isFinite(form.ttlMinutes) || form.ttlMinutes < MIN_TTL_MINUTES || form.ttlMinutes > MAX_TTL_MINUTES) return 'ttl'
  if (input.conditions.length === 0) return 'noConditions'
  for (const c of input.conditions) {
    if (!c.claim) return 'claim'
    if (c.patterns.length === 0) return 'patterns'
    if (c.patterns.length > MAX_PATTERNS) return 'tooManyPatterns'
  }
  return null
}
