// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import CleanupLivePreview from '../CleanupLivePreview.vue'
import type { LivePreviewResult } from '../../composables/useCleanupLivePreview'

const items = (n: number) => Array.from({ length: n }, (_, i) => ({ id: `i${i}`, name: `pkg-${i}`, version: `1.0.${i}` }))
const result = (over: Partial<LivePreviewResult>): LivePreviewResult => ({
  rows: 0,
  bytes: 0,
  items: [],
  truncated: false,
  ...over,
})

const mountPreview = (props: { status: 'idle' | 'loading' | 'ready' | 'error'; result: LivePreviewResult | null; error?: string }) =>
  mount(CleanupLivePreview, { props: { error: '', ...props } })

describe('CleanupLivePreview', () => {
  it('always says nothing is deleted', () => {
    const w = mountPreview({ status: 'idle', result: null })
    expect(w.get('[data-testid="cleanup-preview-safe"]').text()).toContain('nothing is deleted')
    expect(w.find('[data-testid="cleanup-preview-idle"]').exists()).toBe(true)
  })

  it('shows the row count, the size and a sample', () => {
    const w = mountPreview({ status: 'ready', result: result({ rows: 3, bytes: 3 * 1024 ** 2, items: items(3) }) })
    expect(w.get('[data-testid="cleanup-preview-rows"]').text()).toBe('3')
    expect(w.get('[data-testid="cleanup-preview-bytes"]').text()).toBe('3.0 MB')
    expect(w.findAll('[data-testid="cleanup-preview-item"]')).toHaveLength(3)
    expect(w.find('[data-testid="cleanup-preview-more"]').exists()).toBe(false)
  })

  it('limits the sample and says how many more rows there are', () => {
    const w = mountPreview({ status: 'ready', result: result({ rows: 40, bytes: 1, items: items(40) }) })
    expect(w.findAll('[data-testid="cleanup-preview-item"]').length).toBeLessThan(40)
    expect(w.get('[data-testid="cleanup-preview-more"]').text()).toMatch(/and 3\d more/)
  })

  it('flags a truncated list', () => {
    const w = mountPreview({ status: 'ready', result: result({ rows: 900, items: items(500), truncated: true }) })
    expect(w.get('[data-testid="cleanup-preview-more"]').text()).toContain('truncated')
    expect(w.get('[data-testid="cleanup-preview-rows"]').text()).toBe('900')
  })

  it('says so when nothing would be removed', () => {
    const w = mountPreview({ status: 'ready', result: result({ rows: 0 }) })
    expect(w.get('[data-testid="cleanup-preview-none"]').text()).toContain('remove nothing')
    expect(w.find('[data-testid="cleanup-preview-rows"]').exists()).toBe(false)
  })

  it('keeps the last result visible but busy while it refreshes', () => {
    const w = mountPreview({ status: 'loading', result: result({ rows: 2, items: items(2) }) })
    expect(w.get('[data-testid="cleanup-preview-rows"]').text()).toBe('2')
    expect(w.find('[aria-busy="true"]').exists()).toBe(true)
  })

  it('shows the shared error state with the server message', () => {
    const w = mountPreview({ status: 'error', result: null, error: 'name_pattern is invalid' })
    const err = w.get('[data-testid="cleanup-preview-error"]')
    expect(err.attributes('data-state')).toBe('error')
    expect(err.attributes('role')).toBe('alert')
    expect(err.text()).toContain('name_pattern is invalid')
  })

  it('names the repository of each sampled row and how many repositories a project default covers', () => {
    const w = mountPreview({
      status: 'ready',
      result: result({ rows: 2, repositories: 2, items: [{ id: '1', name: 'a', version: '1', repository: 'raw-a' }, { id: '2', name: 'b', version: '2', repository: 'raw-b' }] }),
    })
    expect(w.get('[data-testid="cleanup-preview-repos"]').text()).toBe('Across 2 repositories.')
    expect(w.findAll('[data-testid="cleanup-preview-item-repo"]').map((e) => e.text())).toEqual(['raw-a', 'raw-b'])
  })

  it('shows no repository line for a single repository', () => {
    const w = mountPreview({ status: 'ready', result: result({ rows: 1, items: items(1) }) })
    expect(w.find('[data-testid="cleanup-preview-repos"]').exists()).toBe(false)
  })
})
