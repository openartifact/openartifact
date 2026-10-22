// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { defineStore } from 'pinia'
import { useStorage } from '@vueuse/core'
import { ref } from 'vue'

export const useUIStore = defineStore('ui', () => {
  const sidebarCollapsed = useStorage<boolean>('oa.sidebar.collapsed', false)
  const commandPaletteOpen = ref(false)
  // Off-canvas navigation drawer, used below the md breakpoint only.
  const mobileNavOpen = ref(false)

  function toggleSidebar(): void {
    sidebarCollapsed.value = !sidebarCollapsed.value
  }

  function toggleMobileNav(): void {
    mobileNavOpen.value = !mobileNavOpen.value
  }

  function closeMobileNav(): void {
    mobileNavOpen.value = false
  }

  function openCommandPalette(): void {
    commandPaletteOpen.value = true
  }

  function closeCommandPalette(): void {
    commandPaletteOpen.value = false
  }

  return {
    sidebarCollapsed,
    commandPaletteOpen,
    mobileNavOpen,
    toggleSidebar,
    toggleMobileNav,
    closeMobileNav,
    openCommandPalette,
    closeCommandPalette,
  }
})
