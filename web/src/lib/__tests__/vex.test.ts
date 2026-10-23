// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { VEX_STATEMENT_MAX_LENGTH, VEX_STATUSES, type VexRecord } from '../../api/vex'
import { validateVexForm, vexByVulnId, vexDecisionBody, vexFormState, vexStatusLabel, vexStatusVariant } from '../vex'

const record = (over: Partial<VexRecord> = {}): VexRecord => ({
  id: 's1',
  artifact_id: 'art-1',
  vuln_id: 'CVE-1',
  status: 'not_affected',
  justification: 'component_not_present',
  impact_statement: 'unused',
  action_statement: 'none',
  ...over,
})

describe('vex helpers', () => {
  it('colours every status and labels it', () => {
    expect(vexStatusVariant('affected')).toBe('danger')
    expect(vexStatusVariant('fixed')).toBe('success')
    expect(vexStatusVariant('not_affected')).toBe('success')
    expect(vexStatusVariant('under_investigation')).toBe('warning')
    for (const s of VEX_STATUSES) expect(vexStatusLabel(s)).not.toContain('sec.vex')
  })

  it('starts undecided without a record and prefilled with one', () => {
    expect(vexFormState(null)).toEqual({ status: 'under_investigation', justification: '', impact: '', action: '' })
    expect(vexFormState(record())).toEqual({
      status: 'not_affected',
      justification: 'component_not_present',
      impact: 'unused',
      action: 'none',
    })
  })

  it('requires a justification for not_affected only', () => {
    const form = { status: 'not_affected' as const, justification: '' as const, impact: '', action: '' }
    expect(validateVexForm(form)).toEqual({ justification: 'justificationRequired' })
    expect(validateVexForm({ ...form, justification: 'component_not_present' })).toEqual({})
    expect(validateVexForm({ ...form, status: 'affected' })).toEqual({})
  })

  it('rejects statements over the server limit', () => {
    const long = 'x'.repeat(VEX_STATEMENT_MAX_LENGTH + 1)
    const form = { status: 'affected' as const, justification: '' as const, impact: long, action: long }
    expect(validateVexForm(form)).toEqual({ impact: 'tooLong', action: 'tooLong' })
  })

  it('sends a justification only with not_affected and trims empty statements away', () => {
    const form = { status: 'affected' as const, justification: 'component_not_present' as const, impact: '  ', action: ' fix it ' }
    expect(vexDecisionBody('a', 'CVE-1', form)).toEqual({
      artifact_id: 'a',
      vuln_id: 'CVE-1',
      status: 'affected',
      action_statement: 'fix it',
    })
    expect(vexDecisionBody('a', 'CVE-1', { ...form, status: 'not_affected' }).justification).toBe('component_not_present')
  })

  it('indexes decisions by vulnerability id', () => {
    const map = vexByVulnId([record(), record({ id: 's2', vuln_id: 'CVE-2', status: 'fixed' })])
    expect(map.get('CVE-2')?.status).toBe('fixed')
    expect(map.size).toBe(2)
  })
})
