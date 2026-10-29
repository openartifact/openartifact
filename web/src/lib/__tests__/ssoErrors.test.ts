// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { t } from '../../i18n'
import { ssoErrorMessage } from '../ssoErrors'

describe('ssoErrorMessage', () => {
  it('words each code the server sends from the catalog', () => {
    expect(ssoErrorMessage('user_not_provisioned')).toBe(t('login.ssoError.notProvisioned'))
    expect(ssoErrorMessage('account_disabled')).toBe(t('login.ssoError.disabled'))
    expect(ssoErrorMessage('state_mismatch')).toBe(t('login.ssoError.session'))
    expect(ssoErrorMessage('access_denied')).toBe(t('login.ssoError.accessDenied'))
  })

  it('gives a code it does not know the generic message and never echoes it', () => {
    const msg = ssoErrorMessage('<img src=x onerror=alert(1)>')
    expect(msg).toBe(t('login.ssoError.generic'))
    expect(msg).not.toContain('<img')
  })

  it('has no message for an absent or non-string value', () => {
    expect(ssoErrorMessage(undefined)).toBeUndefined()
    expect(ssoErrorMessage('')).toBeUndefined()
    expect(ssoErrorMessage(['a'])).toBeUndefined()
  })
})
