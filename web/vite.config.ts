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

export default defineConfig({
  plugins: [vue(), tailwindcss(), pwa(), restoreGitkeep],
  build: {
    outDir: 'dist',
    manifest: true,
    rollupOptions: {
      output: {
        chunkFileNames: 'assets/[name]-[hash].js',
        entryFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash][extname]',
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
