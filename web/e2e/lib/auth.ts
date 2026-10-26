// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { join } from 'node:path'

/** Roles the suites run as; `anon` carries no session. */
export type Role = 'admin' | 'dev' | 'anon'

/** Where global setup stores the session cookies of a role (git-ignored). */
export function authFile(role: string): string {
  return join(import.meta.dirname, '..', '.auth', `${role}.json`)
}
