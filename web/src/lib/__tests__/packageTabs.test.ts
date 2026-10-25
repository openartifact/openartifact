// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { packageTabQuery, resolvePackageTab } from '../packageTabs'

describe('resolvePackageTab', () => {
  it('opens the tab the URL names', () => {
    expect(resolvePackageTab('versions')).toBe('versions')
    expect(resolvePackageTab('properties')).toBe('properties')
    expect(resolvePackageTab('security')).toBe('security')
  })

  it('falls back to overview for a missing or unknown value, judging only the first of a repeated one', () => {
    expect(resolvePackageTab(undefined)).toBe('overview')
    expect(resolvePackageTab('nope')).toBe('overview')
    expect(resolvePackageTab(['nope', 'versions'])).toBe('overview')
    expect(resolvePackageTab(['versions', 'security'])).toBe('versions')
  })
})

describe('packageTabQuery', () => {
  it('puts a non-default tab in the query and keeps other keys', () => {
    expect(packageTabQuery({ v: '1.0' }, 'security')).toEqual({ v: '1.0', tab: 'security' })
  })

  it('drops the tab key for the default tab', () => {
    expect(packageTabQuery({ tab: 'versions', v: '1.0' }, 'overview')).toEqual({ v: '1.0' })
  })
})
