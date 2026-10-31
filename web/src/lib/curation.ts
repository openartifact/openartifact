// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { CurationAction, CurationMode, CurationRepository, CurationRule, CurationRuleInput } from '../api/curation'
import { t } from '../i18n'

export const CURATION_MODES: readonly CurationMode[] = ['off', 'denylist', 'allowlist']
export const CURATION_ACTIONS: readonly CurationAction[] = ['block', 'allow']

/** The mode a repository response carries; a repository that predates curation (or a response without it) is off. */
export function curationModeOf(repo: CurationRepository): CurationMode {
  return repo.curation_mode ?? 'off'
}

/** Name and one-line explanation of a mode (the catalog changes with the language, so this is read at render time). */
export function modeText(mode: CurationMode): { label: string; hint: string } {
  switch (mode) {
    case 'denylist':
      return { label: t('curation.mode.denylist'), hint: t('curation.mode.denylistHint') }
    case 'allowlist':
      return { label: t('curation.mode.allowlist'), hint: t('curation.mode.allowlistHint') }
    default:
      return { label: t('curation.mode.off'), hint: t('curation.mode.offHint') }
  }
}

export function actionText(action: CurationAction): { label: string; hint: string } {
  return action === 'allow'
    ? { label: t('curation.action.allow'), hint: t('curation.action.allowHint') }
    : { label: t('curation.action.block'), hint: t('curation.action.blockHint') }
}

/** The form of the add/edit drawer. */
export interface RuleForm {
  action: CurationAction
  packagePattern: string
  versionPattern: string
  reason: string
}

/** A new rule starts with the action its mode is about: allow in an allowlist, block otherwise. */
export function emptyRuleForm(mode: CurationMode): RuleForm {
  return { action: mode === 'allowlist' ? 'allow' : 'block', packagePattern: '', versionPattern: '', reason: '' }
}

export function ruleFormFrom(rule: CurationRule): RuleForm {
  return { action: rule.action, packagePattern: rule.package_pattern, versionPattern: rule.version_pattern, reason: rule.reason }
}

export function ruleFormValid(form: RuleForm): boolean {
  return form.packagePattern.trim() !== ''
}

/** The request body: every field is sent, so clearing the version pattern or the reason of a rule reaches the server. */
export function ruleInput(form: RuleForm): CurationRuleInput {
  return {
    action: form.action,
    package_pattern: form.packagePattern.trim(),
    version_pattern: form.versionPattern.trim(),
    reason: form.reason.trim(),
  }
}

/** The version column of a rule: its pattern, or the words for "no pattern". */
export function versionPatternText(rule: Pick<CurationRule, 'version_pattern'>): string {
  return rule.version_pattern === '' ? t('curation.allVersions') : rule.version_pattern
}
