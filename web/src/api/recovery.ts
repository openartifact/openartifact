// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request } from './client'
import type { RecoveryStatus } from './generated/types.gen'

export type { RecoveryStatus }

/** Admin only: the disaster-recovery journal (sidecar backlog, lag, reconcile progress and last error). */
export function getRecoveryStatus(): Promise<RecoveryStatus> {
  return request<RecoveryStatus>('/api/v1/admin/recovery/status')
}
