// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { heldStatus, reasonText, sourceLabel } from '../availability'
import type { HeldBackVersion } from '../../api/curation'

const held = (reasons: HeldBackVersion['reasons']): HeldBackVersion => ({ version: '1.0.0', reasons })

describe('availability text', () => {
  it('strips the fixed curation prefix to the rule reason', () => {
    expect(reasonText({ source: 'curation', detail: 'blocked by repository curation: malware' })).toBe('malware')
    expect(reasonText({ source: 'curation', detail: 'blocked by repository curation' })).toBe('blocked by repository curation')
    expect(reasonText({ source: 'curation', detail: "not on the repository's allowlist" })).toBe("not on the repository's allowlist")
    expect(reasonText({ source: 'quarantine', detail: 'blocked by repository curation: x' })).toBe('blocked by repository curation: x')
  })
  it('says when a min_age version becomes available and why any other is blocked', () => {
    const until = new Date(Date.now() + 5 * 3_600_000 - 1000).toISOString()
    expect(heldStatus(held([{ source: 'min_age', detail: 'young', until }]))).toBe('Available in 5h')
    expect(heldStatus(held([{ source: 'curation', detail: 'blocked by repository curation: malware' }]))).toBe('Blocked: malware')
    expect(heldStatus(held([]))).toBe('')
  })
  it('labels every source', () => {
    for (const s of ['min_age', 'curation', 'quarantine', 'license_policy', 'repository_blocked'] as const) {
      expect(sourceLabel(s)).not.toContain('artifact.availability')
    }
  })
})
