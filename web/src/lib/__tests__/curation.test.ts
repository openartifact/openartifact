// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import type { CurationRepository, CurationRule } from '../../api/curation'
import { curationModeOf, emptyRuleForm, modeText, ruleFormFrom, ruleFormValid, ruleInput, versionPatternText } from '../curation'

const rule: CurationRule = {
  id: 'r1',
  action: 'allow',
  package_pattern: '@acme/*',
  version_pattern: '2.*',
  reason: 'vetted',
  created_by: 'ada',
  created_at: '2026-10-01T00:00:00Z',
}

describe('curationModeOf', () => {
  it('reads the mode of the repository and treats a missing one as off', () => {
    expect(curationModeOf({ curation_mode: 'allowlist' } as CurationRepository)).toBe('allowlist')
    expect(curationModeOf({} as CurationRepository)).toBe('off')
  })
})

describe('rule form', () => {
  it('starts with the action its mode is about', () => {
    expect(emptyRuleForm('allowlist').action).toBe('allow')
    expect(emptyRuleForm('denylist').action).toBe('block')
    expect(emptyRuleForm('off').action).toBe('block')
  })

  it('round-trips a rule through the form into a request body', () => {
    expect(ruleInput(ruleFormFrom(rule))).toEqual({ action: 'allow', package_pattern: '@acme/*', version_pattern: '2.*', reason: 'vetted' })
  })

  it('trims the fields and still sends the cleared ones, so an edit can empty a version pattern', () => {
    const form = { ...ruleFormFrom(rule), packagePattern: '  left-pad ', versionPattern: '   ', reason: '' }
    expect(ruleInput(form)).toEqual({ action: 'allow', package_pattern: 'left-pad', version_pattern: '', reason: '' })
  })

  it('needs a package pattern', () => {
    expect(ruleFormValid(emptyRuleForm('denylist'))).toBe(false)
    expect(ruleFormValid({ ...emptyRuleForm('denylist'), packagePattern: '   ' })).toBe(false)
    expect(ruleFormValid({ ...emptyRuleForm('denylist'), packagePattern: 'lodash' })).toBe(true)
  })
})

describe('labels', () => {
  it('shows the words for an empty version pattern and the pattern otherwise', () => {
    expect(versionPatternText({ version_pattern: '' })).toBe('all versions')
    expect(versionPatternText({ version_pattern: '1.*' })).toBe('1.*')
  })

  it('names every mode with a one-line explanation', () => {
    expect(modeText('off').label).toBe('Off')
    expect(modeText('denylist').hint).toContain('blocked')
    expect(modeText('allowlist').hint).toContain('allowed')
  })
})
