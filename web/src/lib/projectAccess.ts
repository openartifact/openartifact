// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Client-side mirror of the project role rules enforced by the server
// (docs/security/rbac.md). The server is the gate; these only decide which
// controls to offer.

import { ApiError } from '../api/client'
import {
  PROJECT_GROUP_ROLES,
  PROJECT_MEMBER_ROLES,
  type ProjectGroupRole,
  type ProjectMemberRole,
} from '../api/projects'

const RANK: Record<ProjectMemberRole, number> = { viewer: 1, developer: 2, maintainer: 3, owner: 4 }

function rank(role: string | undefined): number {
  return role && role in RANK ? RANK[role as ProjectMemberRole] : 0
}

/** True when `role` is at least `min`; an unknown or missing role is below everything. */
export function atLeast(role: string | undefined, min: ProjectMemberRole): boolean {
  return rank(role) >= RANK[min]
}

/** Member roles lowest first, the order role pickers list them in. */
export const MEMBER_ROLES_ASC: readonly ProjectMemberRole[] = [...PROJECT_MEMBER_ROLES].reverse()
/** Group roles lowest first. */
export const GROUP_ROLES_ASC: readonly ProjectGroupRole[] = [...PROJECT_GROUP_ROLES].reverse()

/**
 * Roles the caller may grant to a member or a group. Maintainers stay below
 * maintainer; owners grant anything; everyone else grants nothing.
 */
export function grantableRoles(myRole: string | undefined, kind: 'member' | 'group'): readonly string[] {
  const all: readonly string[] = kind === 'group' ? GROUP_ROLES_ASC : MEMBER_ROLES_ASC
  if (!atLeast(myRole, 'maintainer')) return []
  if (myRole === 'owner') return all
  return all.filter((r) => rank(r) < RANK.maintainer)
}

/**
 * Whether the caller may change or remove an entry that currently holds
 * `current`: owner/maintainer entries need an owner.
 */
export function canModifyEntry(myRole: string | undefined, current: string | undefined): boolean {
  if (!atLeast(myRole, 'maintainer')) return false
  return myRole === 'owner' || rank(current) < RANK.maintainer
}

export type ProjectTab = 'overview' | 'repositories' | 'members' | 'tokens' | 'settings'

/** Tabs a caller with `myRole` sees. Members, tokens and settings need at least viewer. */
export function visibleProjectTabs(myRole: string | undefined): ProjectTab[] {
  const tabs: ProjectTab[] = ['overview']
  if (atLeast(myRole, 'viewer')) tabs.push('repositories', 'members', 'tokens', 'settings')
  return tabs
}

/** HTTP status of an API failure, undefined for anything else. */
export function errorStatus(err: unknown): number | undefined {
  return err instanceof ApiError ? err.status : undefined
}
