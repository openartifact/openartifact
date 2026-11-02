// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Builds tarballs for the upload tests.

export function tarEntry(name: string, body: string | Uint8Array, type = '0'): Uint8Array {
  const data = typeof body === 'string' ? new TextEncoder().encode(body) : body
  const header = new Uint8Array(512)
  header.set(new TextEncoder().encode(name))
  header.set(new TextEncoder().encode(`${data.length.toString(8).padStart(11, '0')}\0`), 124)
  header[156] = type.charCodeAt(0)
  const padded = new Uint8Array(Math.ceil(data.length / 512) * 512)
  padded.set(data)
  return Uint8Array.from([...header, ...padded])
}

export function tgz(...entries: Uint8Array[]): File {
  const tar = Uint8Array.from([...entries.flatMap((e) => [...e]), ...new Uint8Array(1024)])
  return new File([Bun.gzipSync(tar)], 'pkg.tgz')
}

