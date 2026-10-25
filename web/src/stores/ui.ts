// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { defineStore } from 'pinia'
import { useStorage } from '@vueuse/core'
import { ref } from 'vue'

export const useUIStore = defineStore('ui', () => {
  // The desktop rail preference is remembered across visits.
  const sidebarCollapsed = useStorage<boolean>('oa.sidebar.collapsed', false)
  const commandPaletteOpen = ref(false)
  // The palette's code is a separate chunk: it is requested the first time the
  // palette opens and stays mounted from then on.
  const commandPaletteLoaded = ref(false)
  // Off-canvas navigation drawer, used below the md breakpoint only.
  const mobileNavOpen = ref(false)
  // Whether closing the drawer hands focus back to its toggle (not after a navigation).
  const mobileNavReturnFocus = ref(false)

  function toggleSidebar(): void {
    sidebarCollapsed.value = !sidebarCollapsed.value
  }

  function toggleMobileNav(): void {
    if (mobileNavOpen.value) closeMobileNav()
    else openMobileNav()
  }

  function openMobileNav(): void {
    mobileNavOpen.value = true
  }

  /** Closes the drawer; `returnFocus: false` after a navigation, where the new page takes focus. */
  function closeMobileNav(returnFocus = true): void {
    mobileNavReturnFocus.value = mobileNavOpen.value && returnFocus
    mobileNavOpen.value = false
  }

  function openCommandPalette(): void {
    commandPaletteLoaded.value = true
    commandPaletteOpen.value = true
  }

  function closeCommandPalette(): void {
    commandPaletteOpen.value = false
  }

  return {
    sidebarCollapsed,
    commandPaletteOpen,
    commandPaletteLoaded,
    mobileNavOpen,
    mobileNavReturnFocus,
    toggleSidebar,
    toggleMobileNav,
    openMobileNav,
    closeMobileNav,
    openCommandPalette,
    closeCommandPalette,
  }
})
