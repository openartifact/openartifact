#!/usr/bin/env node

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { readFileSync, readdirSync } from 'fs'
import { join, dirname } from 'path'
import { fileURLToPath } from 'url'
import { gunzipSync, gzipSync } from 'zlib'

const __dirname = dirname(fileURLToPath(import.meta.url))
const assetsDir = join(__dirname, '..', 'dist', 'assets')

// dist/ is precompressed by scripts/precompress.js: foo.js is stored as foo.js.gz.
let files
try {
  files = readdirSync(assetsDir).map((f) => f.replace(/\.gz$/, ''))
} catch {
  console.error('dist/assets not found — run npm run build first')
  process.exit(1)
}

// Only count the entry chunk and its static imports (from the Vite manifest);
// lazy chunks are excluded. Falls back to all assets if the manifest is absent.
let initial = files.filter((f) => f.endsWith('.js') || f.endsWith('.css'))
try {
  const manifestDir = join(assetsDir, '..', '.vite')
  const manifestName = readdirSync(manifestDir).includes('manifest.json.gz') ? 'manifest.json.gz' : 'manifest.json'
  const raw = readFileSync(join(manifestDir, manifestName))
  const manifest = JSON.parse((manifestName.endsWith('.gz') ? gunzipSync(raw) : raw).toString('utf8'))
  const seen = new Set()
  const walk = (key) => {
    const c = manifest[key]
    if (!c || seen.has(key)) return
    seen.add(key)
    ;(c.imports || []).forEach(walk)
  }
  Object.keys(manifest).filter((k) => manifest[k].isEntry).forEach(walk)
  const names = new Set()
  for (const k of seen) {
    const c = manifest[k]
    names.add(c.file.replace(/^assets\//, ''))
    ;(c.css || []).forEach((x) => names.add(x.replace(/^assets\//, '')))
  }
  initial = initial.filter((f) => names.has(f))
} catch {
  console.warn('dist/.vite/manifest.json not found — counting all assets')
}

function readAsset(file) {
  try {
    return gunzipSync(readFileSync(join(assetsDir, `${file}.gz`)))
  } catch {
    return readFileSync(join(assetsDir, file))
  }
}

let totalGzip = 0
for (const file of initial) {
  totalGzip += gzipSync(readAsset(file)).length
}

const kb = (totalGzip / 1024).toFixed(1)
console.log(`Bundle size: ${kb} KB gzipped`)

// Limit is about 1.3x the current initial bundle (159 KB); raise it only with a reason.
const LIMIT_KB = 208
if (totalGzip > LIMIT_KB * 1024) {
  console.error(`Bundle size ${kb} KB exceeds limit of ${LIMIT_KB} KB`)
  process.exit(1)
}
