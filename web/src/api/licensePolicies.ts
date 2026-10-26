// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'
import type { LicensePolicy } from './generated/types.gen'

export type { LicensePolicy }

/** What a policy does with a package whose license matches it. */
export const LICENSE_POLICY_ACTIONS = ['warn', 'block', 'allow'] as const
export type LicensePolicyAction = (typeof LICENSE_POLICY_ACTIONS)[number]

/** Fields of a new global policy. */
export interface LicensePolicyInput {
  name: string
  description?: string
  action: LicensePolicyAction
  allowed_licenses: string[]
  denied_licenses: string[]
  enabled: boolean
}

const BASE = '/api/v1/admin/license/policies'

/** Policies that apply to every repository (admin). The server lists and creates them; it has no edit or delete for them. */
export async function listGlobalLicensePolicies(): Promise<LicensePolicy[]> {
  const res = await request<{ policies?: LicensePolicy[] }>(BASE)
  return res?.policies ?? []
}

export function createGlobalLicensePolicy(body: LicensePolicyInput): Promise<LicensePolicy> {
  return request<LicensePolicy>(BASE, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify(body) })
}
