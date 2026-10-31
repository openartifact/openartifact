// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { AvailabilityReason, HeldBackVersion } from '../api/curation'
import { timeUntil } from './datetime'
import { t, tp } from '../i18n'

const CURATION_PREFIX = 'blocked by repository curation: '

/** The words after the server's fixed curation prefix: the rule's own reason, else the whole detail. */
export function reasonText(reason: AvailabilityReason): string {
  return reason.source === 'curation' && reason.detail.startsWith(CURATION_PREFIX) ? reason.detail.slice(CURATION_PREFIX.length) : reason.detail
}

/** "Available in 3h" for a version held by the minimum age, "Blocked: <reason>" for any other hold. */
export function heldStatus(held: HeldBackVersion): string {
  const reason = held.reasons[0]
  if (!reason) return ''
  if (reason.source === 'min_age' && reason.until) return tp('artifact.versions.availableAt', { when: timeUntil(reason.until) })
  return tp('artifact.versions.blocked', { reason: reasonText(reason) })
}

export function sourceLabel(source: AvailabilityReason['source']): string {
  return t(`artifact.availability.source.${source}` as const)
}
