// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Form model of the admin project create/edit drawer and its mapping to the API policy.

import { PROJECT_REPO_TYPES, type Project, type ProjectRepoType } from '../api/projects'

export interface ProjectForm {
  name: string
  display_name: string
  description: string
  all_formats: boolean
  allowed_formats: string[]
  allowed_repo_types: ProjectRepoType[]
  repo_name_prefix: string
  allow_public_repos: boolean
  max_repositories: number
  token_max_ttl_days: number
}

export function emptyProjectForm(): ProjectForm {
  return {
    name: '',
    display_name: '',
    description: '',
    all_formats: true,
    allowed_formats: [],
    allowed_repo_types: [...PROJECT_REPO_TYPES],
    repo_name_prefix: '',
    allow_public_repos: true,
    max_repositories: 0,
    token_max_ttl_days: 0,
  }
}

export function projectToForm(project: Project): ProjectForm {
  return {
    name: project.name ?? '',
    display_name: project.display_name ?? '',
    description: project.description ?? '',
    all_formats: project.allowed_formats == null,
    allowed_formats: [...(project.allowed_formats ?? [])],
    allowed_repo_types: [...(project.allowed_repo_types ?? PROJECT_REPO_TYPES)],
    repo_name_prefix: project.repo_name_prefix ?? '',
    allow_public_repos: project.allow_public_repos ?? true,
    max_repositories: project.max_repositories ?? 0,
    token_max_ttl_days: project.token_max_ttl_days ?? 0,
  }
}

/** Number inputs yield '' when cleared; the API wants a non-negative integer. */
export function nonNegativeInt(value: unknown): number {
  const n = Math.floor(Number(value))
  return Number.isFinite(n) && n > 0 ? n : 0
}

/** The policy half of the form as the API expects it. */
export function formPolicy(form: ProjectForm) {
  return {
    // An empty list means every format (stored as null on the server).
    allowed_formats: form.all_formats ? [] : form.allowed_formats,
    allowed_repo_types: form.allowed_repo_types,
    repo_name_prefix: form.repo_name_prefix.trim(),
    allow_public_repos: form.allow_public_repos,
    max_repositories: nonNegativeInt(form.max_repositories),
    token_max_ttl_days: nonNegativeInt(form.token_max_ttl_days),
  }
}
