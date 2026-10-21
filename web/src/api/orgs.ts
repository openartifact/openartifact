// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request } from './client'
import type { Organization } from './generated/types.gen'

export type { Organization }

/** Name of the built-in system organization: the default, whose membership grants nothing. */
export const SYSTEM_ORG_NAME = 'system'

/** Organizations the caller belongs to (GET /api/v1/orgs lists the caller's own). */
export async function listOrganizations(): Promise<Organization[]> {
  const res = await request<{ organizations?: Organization[] }>('/api/v1/orgs')
  return res?.organizations ?? []
}
