// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { buildNpmPublish, npmTarballName, readTarballManifest } from '../npmPublish'
import { UploadInputError } from '../uploadInput'
import { tarEntry, tgz } from './tarFixture'

const manifest = { name: '@acme/widget', version: '1.2.3', license: 'MIT' }

describe('readTarballManifest', () => {
  it('reads package/package.json, skipping the entries before it', async () => {
    const file = tgz(tarEntry('package/', '', '5'), tarEntry('package/index.js', 'x'.repeat(700)), tarEntry('package/package.json', JSON.stringify(manifest)))
    expect(await readTarballManifest(file)).toEqual(manifest)
  })

  it('accepts any top directory, as npm packs from a custom one', async () => {
    expect(await readTarballManifest(tgz(tarEntry('widget-src/package.json', JSON.stringify(manifest))))).toEqual(manifest)
  })

  it('ignores a package.json nested deeper than the top directory', async () => {
    const file = tgz(tarEntry('package/node_modules/x/package.json', JSON.stringify({ name: 'x', version: '1.0.0' })))
    await expect(readTarballManifest(file)).rejects.toBeInstanceOf(UploadInputError)
  })

  it.each([
    ['a file that is not gzip', new File(['not a tarball'], 'a.tgz')],
    ['a tarball without package.json', tgz(tarEntry('package/index.js', 'x'))],
    ['a package.json that is not JSON', tgz(tarEntry('package/package.json', '{nope'))],
    ['a package.json without a version', tgz(tarEntry('package/package.json', JSON.stringify({ name: 'a' })))],
  ])('rejects %s', async (_label, file) => {
    await expect(readTarballManifest(file)).rejects.toMatchObject({ key: 'upload.error.npmManifest' })
  })
})

describe('buildNpmPublish', () => {
  it('builds the document npm publish sends: manifest, dist-tag and the base64 tarball attachment', async () => {
    const file = tgz(tarEntry('package/package.json', JSON.stringify(manifest)))
    const { name, body } = await buildNpmPublish(file, 'next')
    expect(name).toBe('@acme/widget')
    expect(body.type).toBe('application/json')
    const doc = JSON.parse(await body.text())
    expect(doc.name).toBe('@acme/widget')
    expect(doc['dist-tags']).toEqual({ next: '1.2.3' })
    expect(doc.versions['1.2.3']).toMatchObject({ name: '@acme/widget', version: '1.2.3', license: 'MIT' })
    const att = doc._attachments['widget-1.2.3.tgz']
    expect(att.content_type).toBe('application/octet-stream')
    expect(att.length).toBe(file.size)
    expect(new Uint8Array(Buffer.from(att.data, 'base64'))).toEqual(new Uint8Array(await file.arrayBuffer()))
  })

  it('encodes a tarball spanning several base64 chunks back to the same bytes', async () => {
    // Incompressible content, so the gzip file is longer than two 768 KiB chunks and not a multiple of 3.
    let x = 2463534242
    const noise = Uint8Array.from({ length: 1_700_000 }, () => {
      x ^= x << 13
      x ^= x >>> 17
      x ^= x << 5
      return x & 0xff
    })
    const file = tgz(tarEntry('package/package.json', JSON.stringify(manifest)), tarEntry('package/data.bin', noise))
    expect(file.size).toBeGreaterThan(2 * 3 * 256 * 1024)
    const doc = JSON.parse(await (await buildNpmPublish(file, 'latest')).body.text())
    const att = doc._attachments['widget-1.2.3.tgz']
    expect(att.length).toBe(file.size)
    expect(Buffer.from(att.data, 'base64').equals(Buffer.from(await file.arrayBuffer()))).toBe(true)
  })
})

describe('npmTarballName', () => {
  it('drops the scope', () => {
    expect(npmTarballName('@acme/widget', '1.0.0')).toBe('widget-1.0.0.tgz')
    expect(npmTarballName('left-pad', '1.3.0')).toBe('left-pad-1.3.0.tgz')
  })
})
