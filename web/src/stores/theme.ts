// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { defineStore } from 'pinia'
import { computed, watchEffect } from 'vue'
import { usePreferredDark, useStorage } from '@vueuse/core'

type ThemeMode = 'auto' | 'light' | 'dark'
export type ResolvedTheme = 'light' | 'dark'

const STORAGE_KEY = 'oa.theme'

export const useThemeStore = defineStore('theme', () => {
  const mode = useStorage<ThemeMode>(STORAGE_KEY, 'auto', undefined, {
    serializer: {
      read: (raw) => {
        try {
          const v = JSON.parse(raw)
          return v === 'light' || v === 'dark' || v === 'auto' ? v : 'auto'
        } catch {
          // Legacy plain-string storage
          return raw === 'light' || raw === 'dark' || raw === 'auto' ? raw : 'auto'
        }
      },
      write: (v) => JSON.stringify(v),
    },
  })

  const preferredDark = usePreferredDark()

  const resolved = computed<ResolvedTheme>(() => {
    if (mode.value === 'auto') return preferredDark.value ? 'dark' : 'light'
    return mode.value
  })

  function setMode(next: ThemeMode): void {
    mode.value = next
  }

  function init(): void {
    if (typeof document === 'undefined') return
    watchEffect(() => {
      const root = document.documentElement
      root.classList.toggle('dark', resolved.value === 'dark')
    })
  }

  return { mode, resolved, preferredDark, setMode, init }
})
