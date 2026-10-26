// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { deriveChecklist, isFreshInstance, progress } from '../checklist'

describe('deriveChecklist', () => {
  it('marks steps from real counts for an admin', () => {
    const steps = deriveChecklist(true, { repositories: 0, packages: 0, tokens: 1, scannerReady: false, users: 1 })
    expect(steps.map((s) => [s.id, s.done])).toEqual([
      ['repository', false],
      ['publish', false],
      ['token', true],
      ['scanner', false],
      ['team', false],
    ])
    expect(progress(steps)).toEqual({ done: 1, total: 5 })
  })

  it('completes steps as the data appears', () => {
    const steps = deriveChecklist(true, { repositories: 2, packages: 5, tokens: 3, scannerReady: true, users: 4 })
    expect(progress(steps)).toEqual({ done: 5, total: 5 })
  })

  it('leaves out steps whose count the caller cannot read', () => {
    const steps = deriveChecklist(true, { repositories: 1 })
    expect(steps.map((s) => s.id)).toEqual(['repository'])
  })

  it('shows a developer only publish and token, never admin steps', () => {
    const steps = deriveChecklist(false, { repositories: 3, packages: 0, tokens: 0, scannerReady: true, users: 9 })
    expect(steps.map((s) => s.id)).toEqual(['publish', 'token'])
  })

  it('treats an instance without repositories or packages as fresh', () => {
    expect(isFreshInstance({ repositories: 0 })).toBe(true)
    expect(isFreshInstance({ repositories: 2, packages: 0 })).toBe(true)
    expect(isFreshInstance({ repositories: 2, packages: 7 })).toBe(false)
    expect(isFreshInstance({})).toBe(false)
  })
})
