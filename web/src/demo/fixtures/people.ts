// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Group, Project, ProjectMember, UnifiedToken, User } from '../../api/generated/types.gen'
import { ago, uid } from './time'

export const USERS: User[] = [
  { username: 'admin', full_name: 'Demo Admin', email: 'admin@acme.example', last_login_at: ago(0.1), created_at: ago(24 * 400) },
  { username: 'alice.chen', full_name: 'Alice Chen', email: 'alice.chen@acme.example', last_login_at: ago(3), created_at: ago(24 * 380) },
  { username: 'bob.martin', full_name: 'Bob Martin', email: 'bob.martin@acme.example', last_login_at: ago(26), created_at: ago(24 * 350) },
  { username: 'carla.rossi', full_name: 'Carla Rossi', email: 'carla.rossi@acme.example', last_login_at: ago(5), created_at: ago(24 * 300) },
  { username: 'erik.nilsson', full_name: 'Erik Nilsson', email: 'erik.nilsson@acme.example', last_login_at: ago(50), created_at: ago(24 * 210) },
  { username: 'fatima.khan', full_name: 'Fatima Khan', email: 'fatima.khan@acme.example', last_login_at: ago(8), created_at: ago(24 * 120) },
  { username: 'kenji.sato', full_name: 'Kenji Sato', email: 'kenji.sato@acme.example', last_login_at: null, created_at: ago(30), must_change_password: true },
  { username: 'olivia.brown', full_name: 'Olivia Brown', email: 'olivia.brown@acme.example', last_login_at: ago(24 * 95), created_at: ago(24 * 330), disabled: true },
].map((u, i) => ({ id: uid('user', i + 1), disabled: false, must_change_password: false, updated_at: ago(24), ...u }))

export interface DemoGroup extends Group {
  members: string[]
}

export const GROUPS: DemoGroup[] = [
  { name: 'platform', description: 'Platform engineering: CI, base images, shared libraries', members: ['alice.chen', 'erik.nilsson'] },
  { name: 'payments', description: 'Payments backend team', members: ['bob.martin', 'fatima.khan', 'kenji.sato'] },
  { name: 'web', description: 'Web front ends and design system', members: ['carla.rossi', 'alice.chen'] },
  { name: 'data', description: 'Data science and ML serving', members: ['fatima.khan'] },
  { name: 'security', description: 'Security reviews and vulnerability triage', members: ['erik.nilsson'] },
].map((g, i) => ({ id: uid('group', i + 1), created_at: ago(24 * (300 - i * 20)), updated_at: ago(24 * 10), ...g }))

type Role = NonNullable<ProjectMember['role']>

export interface DemoProject extends Project {
  name: string
  members: { username: string; role: Role }[]
  groups: { name: string; role: Exclude<Role, 'owner'> }[]
}

export const PROJECTS: DemoProject[] = ([
  {
    name: 'system',
    display_name: 'System',
    description: 'Default project of repositories created without one',
    is_system: true,
    members: [],
    groups: [],
  },
  {
    name: 'payments',
    display_name: 'Payments',
    description: 'Payment processing services and their libraries',
    members: [
      { username: 'admin', role: 'owner' },
      { username: 'bob.martin', role: 'maintainer' },
      { username: 'fatima.khan', role: 'developer' },
    ],
    groups: [{ name: 'payments', role: 'developer' }],
  },
  {
    name: 'web-platform',
    display_name: 'Web platform',
    description: 'Design system, front ends and their deployment charts',
    members: [
      { username: 'admin', role: 'owner' },
      { username: 'carla.rossi', role: 'maintainer' },
      { username: 'alice.chen', role: 'developer' },
    ],
    groups: [{ name: 'web', role: 'developer' }],
  },
  {
    name: 'data-science',
    display_name: 'Data science',
    description: 'Feature pipelines and model serving',
    members: [{ username: 'fatima.khan', role: 'owner' }],
    groups: [{ name: 'data', role: 'maintainer' }],
  },
] satisfies Pick<DemoProject, 'name' | 'display_name' | 'description' | 'is_system' | 'members' | 'groups'>[]).map((p, i) => ({
  id: uid('project', i + 1),
  slug: p.name,
  allowed_repo_types: ['hosted', 'proxy', 'group'],
  allow_public_repos: false,
  created_at: ago(24 * (360 - i * 40)),
  updated_at: ago(24 * 3),
  ...p,
}))

export const TOKENS: UnifiedToken[] = ([
  { name: 'laptop', type: 'personal', owner: 'admin', scopes: ['read', 'write'], created_at: ago(24 * 40), last_used_at: ago(2), expires_at: ago(-24 * 50) },
  { name: 'renovate', type: 'personal', owner: 'alice.chen', scopes: ['read'], created_at: ago(24 * 200), last_used_at: ago(1), expires_at: ago(-24 * 5) },
  { name: 'github-actions', type: 'project', owner: 'bob.martin', project: 'payments', role: 'developer', created_at: ago(24 * 90), last_used_at: ago(0.5), expires_at: ago(-24 * 275) },
  { name: 'argo-cd', type: 'project', owner: 'carla.rossi', project: 'web-platform', role: 'viewer', created_at: ago(24 * 120), last_used_at: ago(0.2) },
  { name: 'old-jenkins', type: 'personal', owner: 'erik.nilsson', scopes: ['read', 'write'], created_at: ago(24 * 400), last_used_at: ago(24 * 60), expires_at: ago(24 * 2) },
] satisfies Omit<UnifiedToken, 'id' | 'expired'>[]).map((tok, i) => ({ id: uid('token', i + 1), expired: tok.expires_at !== undefined && Date.parse(tok.expires_at) < Date.now(), ...tok }))
