#!/usr/bin/env node

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Replaces every compressible file under dist/ with its gzip (`name.gz`),
// keeping the original only when gzip does not make it smaller.
//
// dist/ is embedded into the server binary, which serves a gzip variant to
// clients that accept it and the plain bytes to the rest. Embedding the plain
// files too would store every asset twice at startup cost only, so the binary
// carries just the gzip and expands it once when it loads (web/assets.go).
// The list of extensions mirrors compressible() in web/assets.go; a file
// missing here is simply embedded as is.

import { readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { dirname, extname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { gzipSync, constants } from 'node:zlib'

const dist = join(dirname(fileURLToPath(import.meta.url)), '..', 'dist')

const COMPRESSIBLE = new Set([
  '.html', '.js', '.mjs', '.css', '.json', '.webmanifest', '.svg', '.txt', '.xml', '.map', '.ico',
])

function walk(dir) {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name)
    return statSync(p).isDirectory() ? walk(p) : [p]
  })
}

let before = 0
let after = 0

for (const file of walk(dist)) {
    const body = readFileSync(file)
  before += body.length
  after += body.length

  if (!COMPRESSIBLE.has(extname(file))) continue

  const gz = gzipSync(body, { level: constants.Z_BEST_COMPRESSION })
  if (gz.length >= body.length) continue

  writeFileSync(`${file}.gz`, gz)
  rmSync(file)
  after += gz.length - body.length
}

console.log(`dist: ${(before / 1024).toFixed(0)} KB -> ${(after / 1024).toFixed(0)} KB embedded`)
