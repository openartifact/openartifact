// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { resolve } from 'path'
import { writeFileSync } from 'fs'
import { pwa } from './vite.pwa'

// Vite empties dist/ before every build, which would delete the tracked
// .gitkeep that keeps `//go:embed all:dist` compiling on a fresh clone.
const restoreGitkeep = {
  name: 'restore-dist-gitkeep',
  closeBundle() {
    writeFileSync(resolve(__dirname, 'dist/.gitkeep'), '')
  },
}

// The UI font is referenced from the stylesheet, so the browser only learns about it after the CSS
// arrived and the first paint needs it: preload it from the document instead (the file name carries
// a build-time hash, so the link is added here). Only the latin subset of the sans font: it is what
// the first screen renders.
function preloadFont(base: string) {
  return {
    name: 'oa-preload-ui-font',
    apply: 'build' as const,
    transformIndexHtml(_html: string, ctx: { bundle?: Record<string, unknown> }) {
      const font = Object.keys(ctx.bundle ?? {}).find((f) => /^assets\/inter-latin-wght-normal-.*\.woff2$/.test(f))
      if (!font) return []
      return [{ tag: 'link', attrs: { rel: 'preload', as: 'font', type: 'font/woff2', crossorigin: '', href: `${base}${font}` }, injectTo: 'head' as const }]
    },
  }
}

// The static demo (`vite build --mode demo`, src/demo) has no server behind it: the web app manifest
// is a server route, so its link would only 404.
const dropManifestLink = {
  name: 'oa-demo-drop-manifest',
  transformIndexHtml: (html: string) => html.replace(/\s*<link rel="manifest"[^>]*>/, ''),
}

export default defineConfig(({ mode }) => {
  // The demo is static files served under a path of its own (OA_DEMO_BASE, e.g. /demo/), without
  // the service worker, the gzip-only embed layout or the dist/.gitkeep of the server build.
  const demo = mode === 'demo'
  const base = demo ? (process.env.OA_DEMO_BASE ?? '/') : '/'
  return {
    base,
    plugins: demo ? [vue(), tailwindcss(), preloadFont(base), dropManifestLink] : [vue(), tailwindcss(), pwa(), preloadFont(base), restoreGitkeep],
    build: {
      outDir: demo ? 'dist-demo' : 'dist',
      manifest: !demo,
      rolldownOptions: {
        output: {
          chunkFileNames: 'assets/[name]-[hash].js',
          entryFileNames: 'assets/[name]-[hash].js',
          assetFileNames: 'assets/[name]-[hash][extname]',
          // Each translated language is one dynamic module (src/i18n/catalogs/locale-<tag>.ts), so it is
          // one chunk named locale-<tag> with no per-file stub chunks; English stays in the entry chunk.
        },
      },
    },
    resolve: {
      alias: {
        '@': resolve(__dirname, 'src'),
      },
    },
    server: {
      proxy: {
        '/api': 'http://localhost:8080',
      },
    },
  }
})
