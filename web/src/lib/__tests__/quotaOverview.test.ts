// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import type { QuotaOverviewItem } from '../../api/quota'
import { compareQuotas, quotaCounts, quotaKey, quotaState } from '../quotaOverview'

const item = (over: Partial<QuotaOverviewItem>): QuotaOverviewItem => ({
  scope: 'repository',
  name: 'r',
  used_bytes: 0,
  usage_percent: 0,
  exceeded: false,
  ...over,
})

describe('quotaState', () => {
  it('is over when the server says exceeded or usage reaches 100 %', () => {
    expect(quotaState(item({ exceeded: true, usage_percent: 90 }))).toBe('over')
    expect(quotaState(item({ usage_percent: 100 }))).toBe('over')
  })

  it('is near from 80 % by default and from the entry threshold when it sets one', () => {
    expect(quotaState(item({ usage_percent: 79 }))).toBe('ok')
    expect(quotaState(item({ usage_percent: 80 }))).toBe('near')
    expect(quotaState(item({ usage_percent: 60, warn_threshold_percent: 50 }))).toBe('near')
    expect(quotaState(item({ usage_percent: 85, warn_threshold_percent: 90 }))).toBe('ok')
  })
})

describe('quotaCounts and ordering', () => {
  it('counts each state', () => {
    const rows = [item({ usage_percent: 100 }), item({ usage_percent: 85 }), item({ usage_percent: 10 }), item({ usage_percent: 90 })]
    expect(quotaCounts(rows)).toEqual({ over: 1, near: 2, ok: 1 })
  })

  it('sorts by usage, then by used bytes, and by name', () => {
    const a = item({ name: 'a', usage_percent: 50, used_bytes: 5 })
    const b = item({ name: 'b', usage_percent: 50, used_bytes: 9 })
    expect(compareQuotas(a, b, 'usage')).toBeLessThan(0)
    expect(compareQuotas(a, b, 'name')).toBeLessThan(0)
    expect(compareQuotas(b, a, 'used')).toBeGreaterThan(0)
  })

  it('keys a repository and a project of the same name apart', () => {
    expect(quotaKey({ scope: 'repository', name: 'x' })).not.toBe(quotaKey({ scope: 'project', name: 'x' }))
  })
})
