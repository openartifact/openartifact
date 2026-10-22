// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Client-side mirror of the project repository policy (docs/security/projects.md).
// The server enforces it for non-admin callers; these helpers only decide which
// choices the create forms offer. Pass `null` when no policy applies (global
// admins, or no project selected).

import type { Project } from '../api/projects'
import type { FormatId } from './formats'
import type { RepoType } from './repoTypes'

export type RepoPolicy = Pick<Project, 'allowed_formats' | 'allowed_repo_types' | 'repo_name_prefix' | 'allow_public_repos'>

/** Formats the project allows; an empty or missing allow-list means all of them. */
export function policyFormats(policy: RepoPolicy | null, formats: FormatId[]): FormatId[] {
  const allowed = policy?.allowed_formats
  return allowed && allowed.length > 0 ? formats.filter((f) => allowed.includes(f)) : formats
}

/** Whether the project allows repositories of `type`. */
export function policyAllowsType(policy: RepoPolicy | null, type: RepoType): boolean {
  const allowed = policy?.allowed_repo_types
  return !allowed || allowed.length === 0 || allowed.includes(type)
}

/** The name prefix the project requires ('' when none). */
export function policyPrefix(policy: RepoPolicy | null): string {
  return policy?.repo_name_prefix ?? ''
}

export function policyAllowsPublic(policy: RepoPolicy | null): boolean {
  return policy === null || policy.allow_public_repos === true
}
