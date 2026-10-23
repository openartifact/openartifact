// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { VEX_STATEMENT_MAX_LENGTH, type VexDecision, type VexJustification, type VexRecord, type VexStatus } from '../api/vex'
import { tDynamic } from '../i18n/en'

export type VexBadgeVariant = 'success' | 'warning' | 'neutral' | 'danger'

/** Badge colour of a decision: exploitable is danger, resolved is success, undecided is warning. */
export function vexStatusVariant(status: VexStatus): VexBadgeVariant {
  switch (status) {
    case 'affected':
      return 'danger'
    case 'not_affected':
    case 'fixed':
      return 'success'
    case 'under_investigation':
      return 'warning'
  }
}

export function vexStatusLabel(status: string): string {
  return tDynamic(`sec.vex.status.${status}`)
}

export function vexJustificationLabel(justification: string): string {
  return tDynamic(`sec.vex.justification.${justification}`)
}

/** Decisions of a list, keyed by vulnerability id (one artifact's decisions have unique vuln ids). */
export function vexByVulnId(records: readonly VexRecord[]): Map<string, VexRecord> {
  return new Map(records.map((r) => [r.vuln_id, r]))
}

export interface VexFormState {
  status: VexStatus
  justification: VexJustification | ''
  impact: string
  action: string
}

export type VexFormErrors = Partial<Record<'justification' | 'impact' | 'action', 'justificationRequired' | 'tooLong'>>

/** The form state a decision starts from: the recorded one, or an undecided finding. */
export function vexFormState(existing: VexRecord | null): VexFormState {
  return {
    status: existing?.status ?? 'under_investigation',
    justification: (existing?.justification as VexJustification | undefined) ?? '',
    impact: existing?.impact_statement ?? '',
    action: existing?.action_statement ?? '',
  }
}

/** The server's rules: a justification is required for not_affected and rejected otherwise; statements are length-capped. */
export function validateVexForm(form: VexFormState): VexFormErrors {
  const errors: VexFormErrors = {}
  if (form.status === 'not_affected' && !form.justification) errors.justification = 'justificationRequired'
  if (form.impact.length > VEX_STATEMENT_MAX_LENGTH) errors.impact = 'tooLong'
  if (form.action.length > VEX_STATEMENT_MAX_LENGTH) errors.action = 'tooLong'
  return errors
}

/** The PUT body of a valid form; a justification is sent only with not_affected. */
export function vexDecisionBody(artifactId: string, vulnId: string, form: VexFormState): VexDecision {
  const body: VexDecision = { artifact_id: artifactId, vuln_id: vulnId, status: form.status }
  if (form.status === 'not_affected' && form.justification) body.justification = form.justification
  const impact = form.impact.trim()
  const action = form.action.trim()
  if (impact) body.impact_statement = impact
  if (action) body.action_statement = action
  return body
}
