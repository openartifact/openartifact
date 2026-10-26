<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The primary navigation: the seven top-level areas, a collapsible rail on
  desktop and an off-canvas menu below md. The account menu, the theme and
  sign-out live in the header's user menu, so they stay reachable in the
  collapsed rail and on mobile.
-->
<template>
  <div
    id="app-sidebar"
    ref="sidebarEl"
    tabindex="-1"
    class="flex shrink-0 flex-col border-r border-border bg-surface outline-none
      max-md:fixed max-md:inset-y-0 max-md:left-0 max-md:z-40 max-md:w-72 max-md:max-w-[85vw] max-md:shadow-xl max-md:transition-transform max-md:duration-200 max-md:ease-out
      motion-reduce:transition-none
      md:relative md:transition-[width] md:duration-200 md:ease-out"
    :class="[
      collapsed ? 'md:w-16' : 'md:w-60',
      isMobile && !ui.mobileNavOpen ? '-translate-x-full' : 'translate-x-0',
    ]"
    :inert="isMobile && !ui.mobileNavOpen"
  >
    <!-- Brand mark: a long white-label name wraps instead of overflowing -->
    <div
      class="flex min-h-14 shrink-0 items-center gap-2.5 border-b border-border/60 py-2"
      :class="collapsed ? 'justify-center px-0' : 'px-3'"
    >
      <router-link
        to="/"
        class="flex min-h-11 min-w-0 flex-1 items-center rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
        :class="collapsed && 'flex-none'"
        :aria-label="brand.productName"
      >
        <BrandLogo :variant="collapsed ? 'mark' : 'horizontal'" />
      </router-link>
      <button
        v-if="isMobile"
        type="button"
        class="-mr-1 flex h-11 w-11 shrink-0 items-center justify-center rounded-md text-muted transition-colors hover:bg-surface-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
        :aria-label="t('sidebar.close')"
        @click="ui.closeMobileNav()"
      >
        <XMarkIcon class="h-5 w-5" aria-hidden="true" />
      </button>
    </div>

    <nav class="nice-scroll flex-1 overflow-y-auto px-2 py-3" :aria-label="t('shell.primaryNav')">
      <ul class="space-y-0.5">
        <li v-for="item in items" :key="item.id">
          <SidebarLink
            :id="item.id"
            :to="item.to"
            :label="item.label"
            :icon="navIcon(item.icon)"
            :collapsed="collapsed"
            :active="item.id === activeId"
            :locked="item.locked"
            :reason="item.reason"
            :note="item.note"
          />
        </li>
      </ul>
    </nav>

    <!-- Collapse / expand the rail (desktop) -->
    <div v-if="!isMobile" class="shrink-0 border-t border-border p-2">
      <button
        type="button"
        class="flex h-9 w-full items-center gap-3 rounded-md text-ui text-muted transition-colors hover:bg-surface-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
        :class="collapsed ? 'justify-center px-0' : 'px-2.5'"
        :aria-label="collapsed ? t('sidebar.expand') : t('sidebar.collapse')"
        :aria-expanded="!collapsed"
        aria-controls="app-sidebar"
        data-testid="sidebar-toggle"
        @click="ui.toggleSidebar()"
      >
        <ChevronRightIcon v-if="collapsed" class="h-[18px] w-[18px] shrink-0" aria-hidden="true" />
        <ChevronLeftIcon v-else class="h-[18px] w-[18px] shrink-0" aria-hidden="true" />
        <span v-if="!collapsed" class="truncate">{{ t('sidebar.collapse') }}</span>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useMediaQuery } from '@vueuse/core'
import { useRoute } from 'vue-router'
import { ChevronLeftIcon, ChevronRightIcon, XMarkIcon } from '@heroicons/vue/24/outline'
import SidebarLink from './SidebarLink.vue'
import BrandLogo from './BrandLogo.vue'
import { navIcon } from './navIcons'
import { useBrandingStore } from '../stores/branding'
import { useSessionStore } from '../stores/session'
import { useUIStore } from '../stores/ui'
import { activeAreaId, resolveNav } from '../lib/navigation'
import { t } from '../i18n/en'

const session = useSessionStore()
const ui = useUIStore()
const brand = useBrandingStore()
const route = useRoute()

const items = computed(() => resolveNav(session.isAdmin))
const activeId = computed(() => activeAreaId(route.path))

// Below md the sidebar is an off-canvas menu: always expanded, opened from the
// header, and the desktop rail preference does not apply.
const isMobile = useMediaQuery('(max-width: 767px)')
const collapsed = computed(() => ui.sidebarCollapsed && !isMobile.value)

const sidebarEl = ref<HTMLElement | null>(null)
watch(
  () => ui.mobileNavOpen,
  async (open) => {
    await nextTick()
    if (open) sidebarEl.value?.focus()
    // Closed from the menu itself (Esc, scrim, close button): focus goes back to the header toggle.
    else if (ui.mobileNavReturnFocus) document.getElementById('mobile-nav-toggle')?.focus()
  },
)
</script>
