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

// The initial bundle is the entry chunk and its static imports (from the Vite manifest); every
// other chunk is lazy and is held to a budget of its own below. Falls back to counting all assets
// as initial if the manifest is absent.
const allCode = files.filter((f) => f.endsWith('.js') || f.endsWith('.css'))
let initial = allCode
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

const LIMITS = {
  initialKB: 208,
  // Files fetched before the first render (the entry chunk's static imports): each is a request.
  initialFiles: 70,
  // A lazy chunk (route page, tab, dialog): code that ships in the initial bundle's wake, so one
  // that grows past this should be split (a tab that is not open on arrival loads on its own).
  lazyKB: 30,
  // One language's catalog (src/i18n/catalogs): grows with the number of translated keys.
  localeKB: 80,
  // The optional frontend telemetry wrapper.
  otelKB: 36,
  // The brand icons of every format (one data file): the one lazy chunk that is large by nature.
  iconsKB: 40,
}

// Limit is about 1.3x the current initial bundle (159 KB); raise it only with a reason.
const failures = []
if (totalGzip > LIMITS.initialKB * 1024) failures.push(`initial bundle ${kb} KB exceeds ${LIMITS.initialKB} KB`)

const initialJS = initial.filter((f) => f.endsWith('.js')).length
console.log(`Initial files: ${initialJS} scripts`)
if (initialJS > LIMITS.initialFiles) failures.push(`${initialJS} initial scripts exceed ${LIMITS.initialFiles}`)

const initialSet = new Set(initial)
const lazy = allCode.filter((f) => f.endsWith('.js') && !initialSet.has(f))
let largest = { file: '', size: 0 }
for (const file of lazy) {
  const size = gzipSync(readAsset(file)).length
  const budget = /^locale-/.test(file) ? LIMITS.localeKB : /^otel-/.test(file) ? LIMITS.otelKB : /^simple-icons-/.test(file) ? LIMITS.iconsKB : LIMITS.lazyKB
  if (budget === LIMITS.lazyKB && size > largest.size) largest = { file, size }
  if (size > budget * 1024) failures.push(`lazy chunk ${file} is ${(size / 1024).toFixed(1)} KB gzipped, over its ${budget} KB budget`)
}
if (largest.file) console.log(`Largest lazy chunk: ${largest.file} ${(largest.size / 1024).toFixed(1)} KB gzipped (budget ${LIMITS.lazyKB} KB)`)

if (failures.length > 0) {
  for (const f of failures) console.error(`Bundle budget: ${f}`)
  process.exit(1)
}
