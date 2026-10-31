// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { actionVisual, entryDetail, groupByDay, toneClasses, visibleCategories } from '../repoActivity'
import { formatDate } from '../datetime'

const at = (y: number, m: number, d: number, h = 12): { timestamp: string } => ({ timestamp: new Date(y, m - 1, d, h).toISOString() })

describe('groupByDay', () => {
  const now = new Date(2026, 4, 10, 15)

  it('names today and yesterday and dates the rest, keeping the order', () => {
    const groups = groupByDay([at(2026, 5, 10, 14), at(2026, 5, 10, 9), at(2026, 5, 9), at(2026, 5, 1)], now)
    expect(groups.map((g) => [g.label, g.items.length])).toEqual([
      ['Today', 2],
      ['Yesterday', 1],
      [formatDate(new Date(2026, 4, 1, 12)), 1],
    ])
  })

  it('splits at local midnight', () => {
    const groups = groupByDay([{ timestamp: new Date(2026, 4, 10, 0, 1).toISOString() }, { timestamp: new Date(2026, 4, 9, 23, 59).toISOString() }], now)
    expect(groups.map((g) => g.label)).toEqual(['Today', 'Yesterday'])
  })

  it('extends the last group when a later page continues the same day', () => {
    const first = [at(2026, 5, 10, 14)]
    const more = [...first, at(2026, 5, 10, 8), at(2026, 5, 9)]
    expect(groupByDay(more, now).map((g) => g.items.length)).toEqual([2, 1])
  })

  it('keeps an entry with an unreadable timestamp with its neighbour and drops nothing', () => {
    const groups = groupByDay([at(2026, 5, 10), { timestamp: 'garbage' }], now)
    expect(groups).toHaveLength(1)
    expect(groups[0]!.items).toHaveLength(2)
    expect(groupByDay([], now)).toEqual([])
  })
})

describe('categories and visuals', () => {
  it('offers the operational categories only to admins', () => {
    expect(visibleCategories(false)).toEqual(['uploads', 'deletions', 'trash', 'promotions', 'quarantine'])
    expect(visibleCategories(true)).toEqual(['uploads', 'deletions', 'trash', 'promotions', 'quarantine', 'cleanup', 'settings', 'federation'])
  })

  it('gives every action a design-token tone, and unknown ones a neutral icon', () => {
    expect(actionVisual('artifact.deleted').tone).toBe('danger')
    expect(actionVisual('federation.peer_created').tone).toBe('neutral')
    expect(actionVisual('something.new').tone).toBe('neutral')
    for (const tone of ['success', 'danger', 'warning', 'info', 'neutral'] as const) {
      expect(toneClasses(tone)).not.toMatch(/#|\[/)
    }
  })

  it('hides a message that only restates the package line', () => {
    const pkg = { kind: 'package' }
    expect(entryDetail({ action: 'artifact.uploaded', message: 'uploaded: lib 1.0', subject: pkg })).toBe('')
    expect(entryDetail({ action: 'cleanup.run_completed', message: 'removed 5 versions', subject: null })).toBe('removed 5 versions')
    expect(entryDetail({ action: 'artifact.quarantined', message: 'quarantined after license review', subject: pkg })).toBe('quarantined after license review')
    expect(entryDetail({ action: 'artifact.uploaded', subject: pkg })).toBe('')
  })
})
