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
const preloadFont = {
  name: 'oa-preload-ui-font',
  apply: 'build' as const,
  transformIndexHtml(_html: string, ctx: { bundle?: Record<string, unknown> }) {
    const font = Object.keys(ctx.bundle ?? {}).find((f) => /^assets\/inter-latin-wght-normal-.*\.woff2$/.test(f))
    if (!font) return []
    return [{ tag: 'link', attrs: { rel: 'preload', as: 'font', type: 'font/woff2', crossorigin: '', href: `/${font}` }, injectTo: 'head' as const }]
  },
}

export default defineConfig({
  plugins: [vue(), tailwindcss(), pwa(), preloadFont, restoreGitkeep],
  build: {
    outDir: 'dist',
    manifest: true,
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
})
