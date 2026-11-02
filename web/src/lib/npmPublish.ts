// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// `npm publish` is one PUT of a JSON document that carries the tarball base64-encoded. The browser builds it
// from a packed `.tgz` (the output of `npm pack`): package.json is read out of the tarball and the document
// is assembled as a Blob, so the file is never held base64-encoded as one string.

import { UploadInputError } from './uploadInput'

const TAR_BLOCK = 512
const MAX_MANIFEST_BYTES = 1 << 20
// A multiple of 3, so every chunk but the last encodes without padding and the chunks concatenate.
const BASE64_CHUNK = 3 * 256 * 1024

/** Reads exact byte counts from a stream, keeping the unread remainder of the last chunk. */
class ByteReader {
  private chunks: Uint8Array[] = []
  private buffered = 0
  private done = false
  private readonly reader: ReadableStreamDefaultReader<Uint8Array>

  constructor(stream: ReadableStream<Uint8Array>) {
    this.reader = stream.getReader()
  }

  private async fill(n: number): Promise<void> {
    while (this.buffered < n && !this.done) {
      const { value, done } = await this.reader.read()
      if (done) this.done = true
      else if (value.length > 0) {
        this.chunks.push(value)
        this.buffered += value.length
      }
    }
  }

  /** Up to n bytes; fewer only at the end of the stream. */
  async read(n: number): Promise<Uint8Array> {
    await this.fill(n)
    const out = new Uint8Array(Math.min(n, this.buffered))
    let at = 0
    while (at < out.length) {
      const head = this.chunks[0]
      const take = Math.min(head.length, out.length - at)
      out.set(head.subarray(0, take), at)
      at += take
      if (take === head.length) this.chunks.shift()
      else this.chunks[0] = head.subarray(take)
    }
    this.buffered -= out.length
    return out
  }

  async skip(n: number): Promise<void> {
    let left = n
    while (left > 0) {
      const got = await this.read(Math.min(left, 1 << 20))
      if (got.length === 0) return
      left -= got.length
    }
  }

  close(): void {
    void this.reader.cancel().catch(() => {})
  }
}

function cString(block: Uint8Array, from: number, to: number): string {
  const end = block.indexOf(0, from)
  return new TextDecoder().decode(block.subarray(from, end >= 0 && end < to ? end : to))
}

/** The package.json of an npm tarball (`package/package.json`, whatever the top directory is called). */
export async function readTarballManifest(file: Blob): Promise<Record<string, unknown>> {
  const reader = new ByteReader(file.stream().pipeThrough(new DecompressionStream('gzip')))
  try {
    for (;;) {
      const header = await reader.read(TAR_BLOCK)
      if (header.length < TAR_BLOCK || header[0] === 0) break
      const name = (cString(header, 345, 500) ? cString(header, 345, 500) + '/' : '') + cString(header, 0, 100)
      const size = Number.parseInt(cString(header, 124, 136).trim() || '0', 8)
      const type = String.fromCharCode(header[156])
      if (!Number.isFinite(size)) break
      if ((type === '0' || type === '\0') && /^[^/]+\/package\.json$/.test(name)) {
        if (size > MAX_MANIFEST_BYTES) break
        const body = await reader.read(size)
        return parseManifest(body)
      }
      await reader.skip(Math.ceil(size / TAR_BLOCK) * TAR_BLOCK)
    }
  } catch (err) {
    if (err instanceof UploadInputError) throw err
    // not a gzip stream or a broken archive
  } finally {
    reader.close()
  }
  throw new UploadInputError('upload.error.npmManifest')
}

function parseManifest(bytes: Uint8Array): Record<string, unknown> {
  let manifest: unknown
  try {
    manifest = JSON.parse(new TextDecoder().decode(bytes))
  } catch {
    throw new UploadInputError('upload.error.npmManifest')
  }
  if (typeof manifest !== 'object' || manifest === null) throw new UploadInputError('upload.error.npmManifest')
  const m = manifest as Record<string, unknown>
  if (typeof m.name !== 'string' || m.name === '' || typeof m.version !== 'string' || m.version === '') {
    throw new UploadInputError('upload.error.npmManifest')
  }
  return m
}

/** File name the registry stores a tarball under: the unscoped name and the version. */
export function npmTarballName(name: string, version: string): string {
  return `${name.startsWith('@') ? name.slice(name.indexOf('/') + 1) : name}-${version}.tgz`
}

function toBase64(bytes: Uint8Array): string {
  let text = ''
  for (let i = 0; i < bytes.length; i += 0x8000) text += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
  return btoa(text)
}

/** The publish document (what `npm publish` PUTs to `/<package>`) for a packed tarball, tagged `tag`. */
export async function buildNpmPublish(file: Blob, tag: string): Promise<{ name: string; body: Blob }> {
  const manifest = await readTarballManifest(file)
  const name = manifest.name as string
  const version = manifest.version as string
  const head = JSON.stringify({
    _id: name,
    name,
    'dist-tags': { [tag]: version },
    versions: { [version]: { ...manifest, _id: `${name}@${version}` } },
  })
  const attachment = `"_attachments":{${JSON.stringify(npmTarballName(name, version))}:{"content_type":"application/octet-stream","data":"`
  const parts: BlobPart[] = [`${head.slice(0, -1)},${attachment}`]
  for (let at = 0; at < file.size; at += BASE64_CHUNK) {
    parts.push(toBase64(new Uint8Array(await file.slice(at, at + BASE64_CHUNK).arrayBuffer())))
  }
  parts.push(`","length":${file.size}}}}`)
  return { name, body: new Blob(parts, { type: 'application/json' }) }
}
