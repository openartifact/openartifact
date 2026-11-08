// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { ApiError } from '../client'
import { declarativeRefusal } from '../declarative'

describe('declarativeRefusal', () => {
  it('gives a refusal without a plan an empty item list', () => {
    const err = new ApiError('no manifest documents found', 422, 'manifest_invalid', { error: 'no manifest documents found', code: 'manifest_invalid' })
    expect(declarativeRefusal(err)).toMatchObject({ code: 'manifest_invalid', items: [] })
  })

  it('keeps the items a refusal carries', () => {
    const items = [{ action: 'create', kind: 'Webhook', name: 'x' }]
    expect(declarativeRefusal(new ApiError('x', 422, 'plan', { code: 'plan', items }))?.items).toEqual(items)
  })

  it('is null for any other failure', () => {
    expect(declarativeRefusal(new ApiError('x', 500, undefined, {}))).toBeNull()
    expect(declarativeRefusal(new Error('x'))).toBeNull()
  })
})
