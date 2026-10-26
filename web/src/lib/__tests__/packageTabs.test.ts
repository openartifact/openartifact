// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { resolveTab } from '../../composables/useUrlTab'
import { PACKAGE_TABS, packageTabDefs } from '../packageTabs'

describe('packageTabDefs', () => {
  const tabs = packageTabDefs({ canSeeActivity: true, canSeeSbom: true, versionCount: 12, fileCount: 3, securityFlag: '2 critical' })

  it('lists the seven tabs with overview first', () => {
    expect(tabs.map((tab) => tab.id)).toEqual([...PACKAGE_TABS])
    expect(tabs[0]!.id).toBe('overview')
  })

  it('carries the counts and the security flag on their tabs only', () => {
    expect(tabs.find((tab) => tab.id === 'versions')!.count).toBe(12)
    expect(tabs.find((tab) => tab.id === 'files')!.count).toBe(3)
    expect(tabs.find((tab) => tab.id === 'security')!.flag).toBe('2 critical')
    expect(tabs.find((tab) => tab.id === 'sbom')!.count).toBeUndefined()
  })

  it('hides activity from people who cannot read it', () => {
    expect(packageTabDefs({ canSeeActivity: false, canSeeSbom: true }).find((tab) => tab.id === 'activity')!.hidden).toBe(true)
    expect(tabs.find((tab) => tab.id === 'activity')!.hidden).toBe(false)
  })

  it('hides the SBOM tab from everyone but admins, whom the server serves it to', () => {
    expect(packageTabDefs({ canSeeActivity: true, canSeeSbom: false }).find((tab) => tab.id === 'sbom')!.hidden).toBe(true)
    expect(tabs.find((tab) => tab.id === 'sbom')!.hidden).toBe(false)
  })
})

describe('tab resolution', () => {
  const tabs = packageTabDefs({ canSeeActivity: false, canSeeSbom: true })

  it('opens the tab the URL names', () => {
    expect(resolveTab(tabs, 'files')).toBe('files')
    expect(resolveTab(tabs, 'sbom')).toBe('sbom')
  })

  it('resolves unknown, legacy and hidden tabs to overview', () => {
    expect(resolveTab(tabs, undefined)).toBe('overview')
    expect(resolveTab(tabs, 'properties')).toBe('overview')
    expect(resolveTab(tabs, 'activity')).toBe('overview')
    expect(resolveTab(tabs, ['nope', 'files'])).toBe('overview')
  })
})
