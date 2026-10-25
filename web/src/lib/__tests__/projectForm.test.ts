// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { emptyProjectForm, formPolicy, nonNegativeInt, projectToForm } from '../projectForm'

describe('project form', () => {
  it('starts with every format and repo type allowed', () => {
    const form = emptyProjectForm()
    expect(form.all_formats).toBe(true)
    expect(form.allowed_repo_types).toEqual(['hosted', 'proxy', 'group'])
    expect(form.allow_public_repos).toBe(true)
  })

  it('maps a project without a format allow-list to "all formats"', () => {
    expect(projectToForm({ name: 'a' }).all_formats).toBe(true)
    const form = projectToForm({ name: 'a', allowed_formats: ['npm'], allowed_repo_types: ['hosted'], max_repositories: 3 })
    expect(form.all_formats).toBe(false)
    expect(form.allowed_formats).toEqual(['npm'])
    expect(form.allowed_repo_types).toEqual(['hosted'])
    expect(form.max_repositories).toBe(3)
  })

  it('coerces cleared or invalid numbers to 0', () => {
    expect(nonNegativeInt('')).toBe(0)
    expect(nonNegativeInt(-4)).toBe(0)
    expect(nonNegativeInt('abc')).toBe(0)
    expect(nonNegativeInt(7.9)).toBe(7)
  })

  it('sends an empty format list for "all formats" and trims the prefix', () => {
    const form = { ...emptyProjectForm(), allowed_formats: ['npm'], repo_name_prefix: ' acme- ', max_repositories: 2 }
    expect(formPolicy(form).allowed_formats).toEqual([])
    expect(formPolicy(form).repo_name_prefix).toBe('acme-')
    expect(formPolicy({ ...form, all_formats: false }).allowed_formats).toEqual(['npm'])
    expect(formPolicy(form).max_repositories).toBe(2)
  })
})
