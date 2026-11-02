// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { createHash } from 'node:crypto'
import { gzipSync } from 'node:zlib'
import { adminApi, uniqueName } from './api'
import { expect } from 'bun:test'

/** One package version the stand-in upstream publishes. */
export interface UpstreamPackage {
  name: string
  version: string
  /** When the upstream says the version was published (ISO 8601). */
  published: string
}

/** A stand-in npm registry on the loopback interface: no network, nothing real. */
export interface FakeUpstream {
  /** Base URL a proxy repository points at. */
  url: string
  stop(): Promise<void>
}

/** A gzipped tar with one `package/<file>` entry per file: what an npm tarball is. */
function tarball(files: Record<string, string>): Buffer {
  const blocks: Buffer[] = []
  for (const [name, content] of Object.entries(files)) {
    const body = Buffer.from(content)
    const header = Buffer.alloc(512)
    header.write(`package/${name}`, 0)
    header.write('0000644\0', 100)
    header.write('0000000\0', 108)
    header.write('0000000\0', 116)
    header.write(`${body.length.toString(8).padStart(11, '0')}\0`, 124)
    header.write('00000000000\0', 136)
    header.write('        ', 148)
    header.write('0', 156)
    header.write('ustar\0', 257)
    header.write('00', 263)
    let sum = 0
    for (const byte of header) sum += byte
    header.write(`${sum.toString(8).padStart(6, '0')}\0 `, 148)
    blocks.push(header, body, Buffer.alloc((512 - (body.length % 512)) % 512))
  }
  blocks.push(Buffer.alloc(1024))
  return gzipSync(Buffer.concat(blocks))
}

/**
 * Serves the packuments and tarballs of `packages` the way registry.npmjs.org does, on an
 * ephemeral loopback port, so a proxy repository has something to cache without any network.
 * The server under test must allow private upstreams (OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true,
 * set for the e2e job in .github/workflows/e2e.yml).
 */
export function startFakeNpmUpstream(packages: UpstreamPackage[]): FakeUpstream {
  const files = new Map<string, Buffer>()
  const packuments = new Map<string, unknown>()
  const server = Bun.serve({
    hostname: '127.0.0.1',
    port: 0,
    fetch(req) {
      const path = decodeURIComponent(new URL(req.url).pathname)
      const packument = packuments.get(path.slice(1))
      if (packument) return Response.json(packument)
      const file = files.get(path)
      return file ? new Response(new Uint8Array(file)) : new Response('not found', { status: 404 })
    },
  })
  const base = `http://127.0.0.1:${server.port}`

  for (const name of new Set(packages.map((p) => p.name))) {
    const versions: Record<string, unknown> = {}
    const time: Record<string, string> = {}
    const mine = packages.filter((p) => p.name === name)
    for (const p of mine) {
      const data = tarball({ 'package.json': JSON.stringify({ name, version: p.version }), 'index.js': `module.exports = ${JSON.stringify(`${name}@${p.version}`)}\n` })
      const path = `/${name}/-/${name}-${p.version}.tgz`
      files.set(path, data)
      time[p.version] = p.published
      versions[p.version] = {
        name,
        version: p.version,
        dist: {
          tarball: `${base}${path}`,
          integrity: `sha512-${createHash('sha512').update(data).digest('base64')}`,
          shasum: createHash('sha1').update(data).digest('hex'),
        },
      }
    }
    packuments.set(name, { name, 'dist-tags': { latest: mine.at(-1)!.version }, time, versions })
  }

  return { url: base, stop: () => server.stop(true) }
}

/**
 * Creates a proxy repository for `upstream` and has the proxy cache every package once (as a
 * client's first request does), so the repository has metadata to hold back or release.
 */
export async function createCachedNpmProxy(upstream: FakeUpstream, packageNames: string[], prefix = 'e2e-proxy'): Promise<string> {
  const api = adminApi()
  const name = uniqueName(prefix)
  const created = await api.post('/api/v1/repositories', { data: { name, format: 'npm', repo_type: 'proxy', upstream_url: upstream.url } })
  expect(created.ok, `${created.text()} (the e2e server needs OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true for the loopback upstream)`).toBe(true)
  for (const pkg of packageNames) {
    const res = await api.get(`/repos/${name}/${pkg}`)
    expect(res.ok, res.text()).toBe(true)
  }
  return name
}
