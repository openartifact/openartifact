// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import type { Bundle, BundlePromotion, ItemCandidate, Pipeline } from '../../api/bundles'
import {
  bundleRoute,
  bundleStatus,
  candidateToItem,
  isBundleable,
  lifecycleSteps,
  promotableStages,
  validateBundleLabel,
  type LifecycleStep,
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
const bundle = (over: Partial<Bundle> = {}): Bundle =>
  ({
    name: 'shop',
    version: '1',
    state: 'sealed',
    current_stage: null,
    pipeline_id: 'p',
    gpg_key_id: 'K',
    created_at: '2026-10-01T10:00:00Z',
    created_by: 'ann',
    sealed_at: '2026-10-02T10:00:00Z',
    sealed_by: 'bob',
    ...over,
  }) as Bundle
const promotion = (to: string, over: Partial<BundlePromotion> = {}): BundlePromotion =>
  ({ status: 'success', to_stage: to, from_stage: null, promoted_by: 'cy', started_at: '2026-10-03T10:00:00Z', ...over }) as BundlePromotion

describe('promotable stages', () => {
  it('only offers stages after the current one, and only for sealed bundles', () => {
    expect(promotableStages(pipeline, bundle()).map((s) => s.name)).toEqual(['dev', 'qa', 'prod'])
    expect(promotableStages(pipeline, bundle({ current_stage: 'qa' })).map((s) => s.name)).toEqual(['prod'])
    expect(promotableStages(pipeline, bundle({ current_stage: 'prod' }))).toEqual([])
    expect(promotableStages(pipeline, bundle({ state: 'draft' }))).toEqual([])
    expect(promotableStages(undefined, bundle())).toEqual([])
  })
})

describe('bundle status: one primary action per state', () => {
  it('draft: seal, blocked only while the items are known to be empty', () => {
    const draft = bundle({ state: 'draft', gpg_key_id: undefined })
    expect(bundleStatus(draft, pipeline, 2)).toMatchObject({ primary: 'seal', blocked: false, notices: [] })
    expect(bundleStatus(draft, pipeline, 0)).toMatchObject({ primary: 'seal', blocked: true, notices: ['noItems'] })
    expect(bundleStatus(draft, pipeline, null)).toMatchObject({ primary: 'seal', blocked: false })
  })

  it('sealed: promote to the next stage', () => {
    const s = bundleStatus(bundle({ current_stage: 'dev' }), pipeline, 3)
    expect(s.primary).toBe('promote')
    expect(s.nextStage?.name).toBe('qa')
    expect(s.notices).toEqual([])
  })

  it('sealed without a signature says so', () => {
    expect(bundleStatus(bundle({ gpg_key_id: undefined }), pipeline, 3).notices).toEqual(['unsigned'])
  })

  it('sealed with nothing to promote to: archive, with the reason', () => {
    expect(bundleStatus(bundle({ current_stage: 'prod' }), pipeline, 3)).toMatchObject({ primary: 'archive', notices: ['finalStage'] })
    expect(bundleStatus(bundle({ pipeline_id: null }), undefined, 3)).toMatchObject({ primary: 'archive', notices: ['noPipeline'] })
    expect(bundleStatus(bundle(), undefined, 3)).toMatchObject({ primary: 'archive', notices: ['pipelineUnavailable'] })
  })

  it('archived: no action', () => {
    expect(bundleStatus(bundle({ state: 'archived' }), pipeline, 3)).toMatchObject({ primary: null, notices: ['archived'] })
  })
})

describe('lifecycle steps', () => {
  const by = (steps: LifecycleStep[]) => steps.map((s) => `${s.id}:${s.status}`)

  it('a draft is done at creation and waits for the seal', () => {
    const steps = lifecycleSteps(bundle({ state: 'draft', sealed_at: null, sealed_by: null, gpg_key_id: undefined }), pipeline, [])
    expect(by(steps)).toEqual(['draft:done', 'sealed:current', 'signed:todo', 'stage:dev:todo', 'stage:qa:todo', 'stage:prod:todo'])
    expect(steps[0]).toMatchObject({ by: 'ann', at: '2026-10-01T10:00:00Z' })
  })

  it('a sealed signed bundle waits for its first stage', () => {
    expect(by(lifecycleSteps(bundle(), pipeline, []))).toEqual(['draft:done', 'sealed:done', 'signed:done', 'stage:dev:current', 'stage:qa:todo', 'stage:prod:todo'])
  })

  it('reached stages are done with their latest successful promotion', () => {
    const steps = lifecycleSteps(bundle({ current_stage: 'qa' }), pipeline, [
      promotion('qa', { promoted_by: 'old', started_at: '2026-10-03T10:00:00Z' }),
      promotion('qa', { promoted_by: 'new', started_at: '2026-10-05T10:00:00Z' }),
      promotion('qa', { promoted_by: 'failed', status: 'failed', started_at: '2026-10-09T10:00:00Z' }),
    ])
    expect(by(steps).slice(3)).toEqual(['stage:dev:done', 'stage:qa:done', 'stage:prod:current'])
    expect(steps.find((s) => s.id === 'stage:qa')).toMatchObject({ by: 'new', at: '2026-10-05T10:00:00Z' })
  })

  it('an unsigned sealed bundle skips the signature step instead of waiting for it', () => {
    const steps = lifecycleSteps(bundle({ gpg_key_id: undefined }), pipeline, [])
    expect(by(steps).slice(0, 4)).toEqual(['draft:done', 'sealed:done', 'signed:skipped', 'stage:dev:current'])
  })

  it('an archived bundle has no current step', () => {
    expect(lifecycleSteps(bundle({ state: 'archived', current_stage: 'dev' }), pipeline, []).some((s) => s.status === 'current')).toBe(false)
  })

  it('without a pipeline the trail ends at the signature', () => {
    expect(by(lifecycleSteps(bundle({ pipeline_id: null }), undefined, []))).toEqual(['draft:done', 'sealed:done', 'signed:done'])
  })
})

describe('item candidates', () => {
  const hit: ItemCandidate = { repository: 'maven-dev', format: 'maven', name: 'shop', version: '1', path: 'com/acme/shop-1.jar', sha256: 'A'.repeat(64), size: 2048 }
  const repos = new Map([['maven-dev', 'r1']])

  it('needs the digest and path an item pins', () => {
    expect(isBundleable(hit)).toBe(true)
    expect(isBundleable({ ...hit, sha256: '' })).toBe(false)
    expect(isBundleable({ ...hit, sha256: 'xyz' })).toBe(false)
    expect(isBundleable({ ...hit, path: '' })).toBe(false)
  })

  it('becomes an item of the hit repository, with a lower-case digest', () => {
    expect(candidateToItem(hit, repos)).toEqual({
      source_repo_id: 'r1',
      sha256: 'a'.repeat(64),
      artifact_path: 'com/acme/shop-1.jar',
      format: 'maven',
      size_bytes: 2048,
    })
    expect(candidateToItem({ ...hit, size: Number.NaN }, repos)).not.toHaveProperty('size_bytes')
    expect(candidateToItem({ ...hit, repository: 'other' }, repos)).toBeNull()
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
