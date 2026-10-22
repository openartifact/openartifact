// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import openapi from '../../../../internal/api/openapi.json'
import { ALL_FORMATS, FORMAT_META } from '../formats'
import { allowedFormats, type RepoType } from '../repoTypes'

// The server accepts exactly these ids as a repository's format. The pickers
// send the id they show, so every selectable id must be one of them: the Go
// picker used to send "gomod" and the create request failed with 400.
const serverFormats: string[] = openapi.components.schemas.Repository.properties.format.enum

describe('repository format ids', () => {
  it('are all accepted by the API for every repository type', () => {
    for (const type of ['hosted', 'proxy', 'group'] as RepoType[]) {
      for (const f of allowedFormats(type)) expect(serverFormats).toContain(f)
    }
  })

  it('send "go" for Go modules', () => {
    expect(ALL_FORMATS).toContain('go')
    expect(ALL_FORMATS as string[]).not.toContain('gomod')
    expect(Object.keys(FORMAT_META)).not.toContain('gomod')
    expect(allowedFormats('hosted')).toContain('go')
    expect(allowedFormats('proxy')).toContain('go')
    expect(allowedFormats('group')).toContain('go')
  })

  it('cover every server format with display metadata', () => {
    for (const f of serverFormats) expect(Object.keys(FORMAT_META)).toContain(f)
  })
})
