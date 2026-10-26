// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { duplicateName, propertyChanges, propertyRows } from '../artifactProperties'

describe('artifact properties form', () => {
  it('lists stored properties in key order', () => {
    expect(propertyRows({ b: '2', a: '1' })).toEqual([{ key: 'a', value: '1' }, { key: 'b', value: '2' }])
  })

  it('finds a name used twice after trimming and ignores empty names', () => {
    expect(duplicateName([{ key: 'a', value: '1' }, { key: ' a ', value: '2' }])).toBe('a')
    expect(duplicateName([{ key: '', value: '1' }, { key: '', value: '2' }])).toBeUndefined()
  })

  it('sends only what changed and removes dropped keys', () => {
    const changes = propertyChanges({ keep: '1', edit: 'old', drop: 'x' }, [
      { key: 'keep', value: '1' },
      { key: 'edit', value: 'new' },
      { key: ' added ', value: 'v' },
      { key: '', value: 'ignored' },
    ])
    expect(changes).toEqual({ set: { edit: 'new', added: 'v' }, remove: ['drop'] })
  })
})
