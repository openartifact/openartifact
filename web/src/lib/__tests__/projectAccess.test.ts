// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { ApiError } from '../../api/client'
import { atLeast, canModifyEntry, errorStatus, grantableRoles, visibleProjectTabs } from '../projectAccess'

describe('project role rules', () => {
  it('ranks roles viewer < developer < maintainer < owner', () => {
    expect(atLeast('owner', 'maintainer')).toBe(true)
    expect(atLeast('maintainer', 'maintainer')).toBe(true)
    expect(atLeast('developer', 'maintainer')).toBe(false)
    expect(atLeast(undefined, 'viewer')).toBe(false)
    expect(atLeast('nonsense', 'viewer')).toBe(false)
  })

  it('lets owners grant everything, maintainers only below maintainer, others nothing', () => {
    expect(grantableRoles('owner', 'member')).toEqual(['viewer', 'developer', 'maintainer', 'owner'])
    expect(grantableRoles('owner', 'group')).toEqual(['viewer', 'developer', 'maintainer'])
    expect(grantableRoles('maintainer', 'member')).toEqual(['viewer', 'developer'])
    expect(grantableRoles('maintainer', 'group')).toEqual(['viewer', 'developer'])
    expect(grantableRoles('developer', 'member')).toEqual([])
    expect(grantableRoles('viewer', 'group')).toEqual([])
    expect(grantableRoles(undefined, 'member')).toEqual([])
  })

  it('needs an owner to touch owner and maintainer entries', () => {
    expect(canModifyEntry('owner', 'owner')).toBe(true)
    expect(canModifyEntry('owner', 'maintainer')).toBe(true)
    expect(canModifyEntry('maintainer', 'maintainer')).toBe(false)
    expect(canModifyEntry('maintainer', 'owner')).toBe(false)
    expect(canModifyEntry('maintainer', 'developer')).toBe(true)
    expect(canModifyEntry('developer', 'viewer')).toBe(false)
  })

  it('shows the project sections only to roles that may read them', () => {
    expect(visibleProjectTabs('viewer')).toEqual(['overview', 'repositories', 'members', 'tokens', 'settings'])
    expect(visibleProjectTabs('owner')).toEqual(['overview', 'repositories', 'members', 'tokens', 'settings'])
    expect(visibleProjectTabs(undefined)).toEqual(['overview'])
  })

  it('reads the status of an ApiError only', () => {
    expect(errorStatus(new ApiError('x', 409))).toBe(409)
    expect(errorStatus(new Error('x'))).toBeUndefined()
  })
})
