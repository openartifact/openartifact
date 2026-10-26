// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { installBlock } from '../artifactInstall'

const origin = 'https://oa.example'

describe('installBlock', () => {
  it('builds the tag pull and the registry URL from the origin', () => {
    const block = installBlock('npm', origin, 'web', '@scope/pkg', '1.2.3', undefined)
    expect(block.commands).toHaveLength(1)
    expect(block.commands[0]!.id).toBe('tag')
    expect(block.commands[0]!.code).toContain('@scope/pkg')
    expect(block.registryUrl).toContain(origin)
    expect(block.clients.length).toBeGreaterThan(0)
  })

  it('adds a digest pull for docker when the checksum is known', () => {
    const digest = 'a'.repeat(64)
    const block = installBlock('docker', origin, 'img', 'img/app', 'latest', digest)
    expect(block.commands.map((c) => c.id)).toEqual(['tag', 'digest'])
    expect(block.commands[1]!.code).toContain(digest)
    expect(installBlock('docker', origin, 'img', 'img/app', 'latest', undefined).commands).toHaveLength(1)
  })
})
