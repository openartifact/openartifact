// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { tDynamic } from '../i18n'

type Variant = 'success' | 'warning' | 'neutral' | 'danger'

const STATUSES = ['pending', 'processing', 'completed', 'failed']
const LICENSES = ['SAFE', 'REVIEW_REQUIRED', 'UNKNOWN']

export function aiStatusLabel(status: string): string {
  return STATUSES.includes(status) ? tDynamic(`security.ai.status.${status}`) : status
}

export function aiStatusVariant(status: string): Variant {
  return status === 'completed' ? 'success' : status === 'failed' ? 'danger' : 'warning'
}

export function aiLicenseLabel(status: string): string {
  return LICENSES.includes(status) ? tDynamic(`security.ai.license.${status}`) : status
}

export function aiLicenseVariant(status: string): Variant {
  return status === 'SAFE' ? 'success' : status === 'REVIEW_REQUIRED' ? 'warning' : 'neutral'
}
