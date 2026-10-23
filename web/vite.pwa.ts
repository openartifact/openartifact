// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { createHash } from 'node:crypto'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative, resolve, sep } from 'node:path'
import { build, type Plugin } from 'vite'

// Static app shell served next to the hashed assets (all from web/public).
const SHELL_FILES = ['offline.html', 'favicon.svg', 'manifest.webmanifest']

function walk(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name)
    return statSync(p).isDirectory() ? walk(p) : [p]
  })
}

/**
 * Builds the service worker (src/pwa/sw.ts) into dist/sw.js once the app
 * bundle is written, injecting the precache list: every file under assets/
 * (content-hashed by Vite) plus the shell files and icons. The cache version
 * is a digest of the listed files, so any change yields a new worker.
 */
export function pwa(): Plugin {
  let outDir = ''
  let root = ''

  return {
    name: 'oa-pwa-service-worker',
    apply: 'build',
    configResolved(config) {
      root = config.root
      outDir = resolve(config.root, config.build.outDir)
    },
    async closeBundle() {
      const files = [
        ...walk(join(outDir, 'assets')),
        ...SHELL_FILES.map((f) => join(outDir, f)),
        ...walk(join(outDir, 'icons')),
      ]
        .filter((f) => !f.endsWith('.map'))
        .map((f) => ({ file: f, url: '/' + relative(outDir, f).split(sep).join('/') }))
        .sort((a, b) => a.url.localeCompare(b.url))

      const digest = createHash('sha256')
      for (const { file, url } of files) digest.update(url).update(readFileSync(file))

      const precache = { version: digest.digest('hex').slice(0, 16), urls: files.map((f) => f.url) }

      await build({
        root,
        configFile: false,
        publicDir: false,
        logLevel: 'warn',
        define: {
          __PRECACHE_VERSION__: JSON.stringify(precache.version),
          __PRECACHE_URLS__: JSON.stringify(precache.urls),
        },
        build: {
          outDir,
          emptyOutDir: false,
          manifest: false,
          target: 'es2020',
          lib: { entry: resolve(root, 'src/pwa/sw.ts'), formats: ['iife'], name: 'oaServiceWorker', fileName: () => 'sw.js' },
        },
      })
    },
  }
}
