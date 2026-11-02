// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import type { DeclarativeRun, DeclarativeRunItem } from '../../api/declarative'
import { canApply, changeKind, deletions, joinManifests, listedItems, outcomeCounts, summaryCounts } from '../declarativePlan'

const item = (action: DeclarativeRunItem['action'], name: string, extra: Partial<DeclarativeRunItem> = {}): DeclarativeRunItem => ({ action, kind: 'Repository', name, ...extra })
const run = (items: DeclarativeRunItem[], extra: Partial<DeclarativeRun> = {}): DeclarativeRun => ({ summary: {}, changes: true, items, secrets: [], ...extra })

describe('summaryCounts', () => {
  it('orders deletions first and drops actions with no items', () => {
    const r = run([], { summary: { unchanged: 4, create: 2, delete: 1 } })
    expect(summaryCounts(r)).toEqual([
      { action: 'delete', count: 1 },
      { action: 'create', count: 2 },
      { action: 'unchanged', count: 4 },
    ])
  })
})

describe('listedItems', () => {
  const r = run([item('unchanged', 'q'), item('create', 'c'), item('delete', 'd'), item('update', 'u'), item('unchanged', 'adopted', { adopt: true }), item('unchanged', 'broken', { error: 'x' })])
  it('hides quiet items and lists deletions first', () => {
    expect(listedItems(r, false).map((i) => i.name)).toEqual(['d', 'c', 'u', 'adopted', 'broken'])
  })
  it('lists the quiet ones last on request', () => {
    expect(listedItems(r, true).map((i) => i.name)).toEqual(['d', 'c', 'u', 'q', 'adopted', 'broken'])
  })
})

describe('changeKind', () => {
  it('flags secrets first', () => {
    expect(changeKind({ path: 'p', secret: true, old: 1, new: 2 }, 'update')).toBe('secret')
  })
  it('reads a create or a new value as added', () => {
    expect(changeKind({ path: 'p', new: 1 }, 'create')).toBe('added')
    expect(changeKind({ path: 'p', new: 1 }, 'update')).toBe('added')
  })
  it('reads a dropped list element as removed', () => {
    expect(changeKind({ path: 'tags[1]', old: 'x' }, 'update')).toBe('removed')
  })
  it('reads anything else as replaced', () => {
    expect(changeKind({ path: 'p', old: 1, new: 2 }, 'update')).toBe('replaced')
    expect(changeKind({ path: 'p', old: 1 }, 'update')).toBe('replaced')
  })
})

describe('deletions', () => {
  it('lists only deleted items', () => {
    expect(deletions(run([item('delete', 'd'), item('create', 'c')])).map((i) => i.name)).toEqual(['d'])
  })
})

describe('canApply', () => {
  it('needs a plan that changes something', () => {
    expect(canApply(null)).toBe(false)
    expect(canApply(run([item('create', 'c')]))).toBe(true)
    expect(canApply(run([], { changes: false }))).toBe(false)
  })
  it('refuses a plan with a code, plan errors or an item error', () => {
    expect(canApply(run([], { code: 'plan_invalid' }))).toBe(false)
    expect(canApply(run([], { errors: ['immutable'] }))).toBe(false)
    expect(canApply(run([item('create', 'c', { error: 'bad' })]))).toBe(false)
  })
})

describe('outcomeCounts', () => {
  it('counts per result and ignores items without one', () => {
    const r = run([item('create', 'a', { result: 'applied' }), item('update', 'b', { result: 'failed' }), item('create', 'c', { result: 'applied' }), item('delete', 'd', { result: 'skipped' }), item('unchanged', 'e')])
    expect(outcomeCounts(r)).toEqual({ applied: 2, failed: 1, skipped: 1 })
  })
})

describe('joinManifests', () => {
  it('separates files with a --- line and ends with one newline', () => {
    expect(joinManifests(['a: 1\n\n', 'b: 2'])).toBe('a: 1\n---\nb: 2\n')
    expect(joinManifests(['a: 1\n'])).toBe('a: 1\n')
  })
})
