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

/** `locale-pt-BR` for src/i18n/locales/pt-BR/admin.json (the group's `name` callback receives the module id). */
function localeChunk(id: string): string {
  const m = /i18n[\\/]locales[\\/]([^\\/]+)[\\/]/.exec(id)
  return `locale-${m?.[1] ?? 'unknown'}`
}

export default defineConfig({
  plugins: [vue(), tailwindcss(), pwa(), restoreGitkeep],
  build: {
    outDir: 'dist',
    manifest: true,
    rolldownOptions: {
      output: {
        chunkFileNames: 'assets/[name]-[hash].js',
        entryFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash][extname]',
        // One chunk per translated language (all its catalog files), fetched only for a user of that language.
        // English is statically imported and stays in the entry chunk.
        codeSplitting: {
          groups: [
            { name: (id: string) => localeChunk(id), test: /src[\\/]i18n[\\/]locales[\\/](?!en[\\/])[^\\/]+[\\/]/ },
          ],
        },
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
