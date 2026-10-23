// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import type { Bundle, Pipeline } from '../../api/bundles'
import {
  bundleRoute,
  itemFormToInput,
  promotableStages,
  stageSteps,
  validateBundleLabel,
  validateItemForm,
} from '../bundles'

const pipeline: Pipeline = {
  id: 'p',
  name: 'std',
  description: '',
  stages: [{ name: 'dev' }, { name: 'qa', repo_id: 'r-qa' }, { name: 'prod' }],
  created_by: 'a',
  created_at: '',
  updated_at: '',
}
const sealed = (current: string | null): Bundle =>
  ({ name: 'shop', version: '1', state: 'sealed', current_stage: current, pipeline_id: 'p' }) as Bundle

describe('stage trail', () => {
  it('marks stages before the current one done and the rest pending', () => {
    expect(stageSteps(pipeline, 'qa').map((s) => s.status)).toEqual(['done', 'current', 'pending'])
    expect(stageSteps(pipeline, null).map((s) => s.status)).toEqual(['pending', 'pending', 'pending'])
    expect(stageSteps(undefined, 'qa')).toEqual([])
  })

  it('only offers stages after the current one, and only for sealed bundles', () => {
    expect(promotableStages(pipeline, sealed(null)).map((s) => s.name)).toEqual(['dev', 'qa', 'prod'])
    expect(promotableStages(pipeline, sealed('qa')).map((s) => s.name)).toEqual(['prod'])
    expect(promotableStages(pipeline, sealed('prod'))).toEqual([])
    expect(promotableStages(pipeline, { ...sealed(null), state: 'draft' })).toEqual([])
    expect(promotableStages(undefined, sealed(null))).toEqual([])
  })
})

describe('item form', () => {
  const ok = { source_repo_id: 'r', sha256: 'A'.repeat(64), artifact_path: ' a/b.jar ', format: '', size_bytes: '' }

  it('accepts a valid item and normalises it', () => {
    expect(validateItemForm(ok)).toEqual({})
    expect(itemFormToInput({ ...ok, size_bytes: '42', format: 'maven' })).toEqual({
      source_repo_id: 'r',
      sha256: 'a'.repeat(64),
      artifact_path: 'a/b.jar',
      format: 'maven',
      size_bytes: 42,
    })
    expect(itemFormToInput(ok)).not.toHaveProperty('size_bytes')
  })

  it('reports each invalid field', () => {
    const errors = validateItemForm({ source_repo_id: '', sha256: 'xyz', artifact_path: '', format: '', size_bytes: '-1' })
    expect(Object.keys(errors).sort()).toEqual(['artifact_path', 'sha256', 'size_bytes', 'source_repo_id'])
    expect(validateItemForm({ ...ok, artifact_path: 'é'.repeat(600) }).artifact_path).toBeDefined()
  })
})

describe('labels', () => {
  it('mirrors the server name rules', () => {
    expect(validateBundleLabel('shop')).toBe(true)
    expect(validateBundleLabel('2.4.0-rc.1')).toBe(true)
    for (const bad of ['', 'a/b', 'a\\b', 'a\nb', 'x'.repeat(256)]) expect(validateBundleLabel(bad)).toBe(false)
  })

  it('escapes the detail route', () => {
    expect(bundleRoute({ name: 'my shop', version: '1+2' })).toBe('/bundles/my%20shop/1%2B2')
  })
})
