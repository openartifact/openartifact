// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import type { Bundle, BundleCheck, BundlePromotion, ItemCandidate, Pipeline } from '../../api/bundles'
import {
  bundleRoute,
  bundleStatus,
  candidateToItem,
  isBundleable,
  checkExpired,
  checkVariant,
  lifecycleSteps,
  parseRequiredChecks,
  promotableStages,
  requiredCheckStates,
  safeDetailsUrl,
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

describe('required checks', () => {
  it('reads names separated by commas and spaces, trimmed', () => {
    expect(parseRequiredChecks('')).toEqual({ names: [], error: null })
    expect(parseRequiredChecks(' conforma,scan  sbom ,')).toEqual({ names: ['conforma', 'scan', 'sbom'], error: null })
  })

  it('refuses duplicates, long names, control characters and more than 20', () => {
    expect(parseRequiredChecks('a, b, a').error).toContain('listed twice')
    expect(parseRequiredChecks('x'.repeat(64)).error).toBeNull()
    expect(parseRequiredChecks('x'.repeat(65)).error).toContain('longer than 64')
    expect(parseRequiredChecks('a\u0000b').error).toContain('control characters')
    const many = Array.from({ length: 21 }, (_, i) => `c${i}`)
    expect(parseRequiredChecks(many.slice(0, 20).join(',')).error).toBeNull()
    expect(parseRequiredChecks(many.join(',')).error).toContain('at most 20')
  })

  const check = (over: Partial<BundleCheck>): BundleCheck => ({
    name: 'scan',
    status: 'success',
    summary: '',
    details_url: '',
    reported_by: 'ci',
    updated_at: '2026-10-03T10:00:00Z',
    expires_at: null,
    ...over,
  })
  const now = Date.parse('2026-10-10T00:00:00Z')

  it('classifies each required check: reported status, missing or expired', () => {
    const states = requiredCheckStates(
      ['conforma', 'scan', 'sbom', 'lint', 'old'],
      [
        check({ name: 'conforma' }),
        check({ name: 'scan', status: 'failure' }),
        check({ name: 'lint', status: 'pending' }),
        check({ name: 'old', expires_at: '2026-10-09T00:00:00Z' }),
        check({ name: 'unrelated' }),
      ],
      now,
    )
    expect(states).toEqual([
      { name: 'conforma', state: 'success' },
      { name: 'scan', state: 'failure' },
      { name: 'sbom', state: 'missing' },
      { name: 'lint', state: 'pending' },
      { name: 'old', state: 'expired' },
    ])
  })

  it('expires only at or after the expiry time', () => {
    expect(checkExpired(check({ expires_at: null }), now)).toBe(false)
    expect(checkExpired(check({ expires_at: '2026-10-11T00:00:00Z' }), now)).toBe(false)
    expect(checkExpired(check({ expires_at: '2026-10-10T00:00:00Z' }), now)).toBe(true)
  })

  it('colours success green, failures red, the rest amber', () => {
    expect(checkVariant('success')).toBe('success')
    expect(checkVariant('failure')).toBe('danger')
    expect(checkVariant('error')).toBe('danger')
    expect(checkVariant('pending')).toBe('warning')
  })

  it('links only http and https details', () => {
    expect(safeDetailsUrl('https://ci.example/run/1')).toBe('https://ci.example/run/1')
    expect(safeDetailsUrl('http://ci.example/x')).toBe('http://ci.example/x')
    for (const bad of ['', 'javascript:alert(1)', 'data:text/html,x', '/relative', 'not a url', undefined]) expect(safeDetailsUrl(bad)).toBeNull()
  })
})
