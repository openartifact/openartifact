<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <aside
    id="app-sidebar"
    ref="asideEl"
    tabindex="-1"
    class="bg-surface border-r border-border flex flex-col shrink-0 outline-none
      max-md:fixed max-md:inset-y-0 max-md:left-0 max-md:z-40 max-md:shadow-xl max-md:transition-transform max-md:duration-200 max-md:ease-out
      md:relative md:transition-[width] md:duration-200 md:ease-out"
    :class="[
      collapsed ? 'w-16' : 'w-60',
      isMobile && !ui.mobileNavOpen ? '-translate-x-full' : 'translate-x-0',
    ]"
    :inert="isMobile && !ui.mobileNavOpen"
    :aria-label="t('nav.repositories')"
  >
    <!-- Brand mark -->
    <div
      class="flex items-center gap-2.5 h-14 border-b border-border/60 shrink-0"
      :class="collapsed ? 'justify-center px-0' : 'px-3'"
    >
      <div class="w-8 h-8 rounded-md bg-primary flex items-center justify-center shrink-0">
        <AppLogo class="w-5 h-5 text-primary-contrast" />
      </div>
      <div v-if="!collapsed" class="leading-none min-w-0 flex-1">
        <div class="text-lead font-semibold tracking-tight">OpenArtifact</div>
        <div class="mt-1 text-caption text-muted font-mono">{{ versionLabel }}</div>
      </div>
      <button
        v-if="isMobile"
        type="button"
        class="shrink-0 -mr-1 p-2 rounded-md text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
        :aria-label="t('sidebar.close')"
        @click="ui.closeMobileNav()"
      >
        <XMarkIcon class="w-5 h-5" aria-hidden="true" />
      </button>
    </div>

    <!-- Collapse / expand toggle -->
    <button
      v-if="!isMobile"
      type="button"
      class="absolute -right-3 top-[3.5rem] w-6 h-6 rounded-full bg-surface border border-border shadow-sm flex items-center justify-center hover:bg-surface-hover transition-colors z-10"
      :aria-label="collapsed ? t('sidebar.expand') : t('sidebar.collapse')"
      @click="ui.toggleSidebar()"
    >
      <ChevronRightIcon v-if="collapsed" class="w-3.5 h-3.5 text-muted" aria-hidden="true" />
      <ChevronLeftIcon v-else class="w-3.5 h-3.5 text-muted" aria-hidden="true" />
    </button>

    <!-- Scrollable navigation -->
    <nav class="flex-1 overflow-y-auto nice-scroll px-2 py-3" :aria-label="t('shell.primaryNav')">

      <!-- BROWSE -->
      <p v-if="!collapsed" class="px-2 mt-2 mb-1 text-xs text-muted">
        {{ t('sidebar.browse') }}
      </p>
      <div v-else class="my-3 mx-auto w-6 h-px bg-border" />

      <SidebarLink to="/" :icon="HomeIcon" :label="t('nav.dashboard')" :collapsed="collapsed" />
      <SidebarLink to="/repos" :icon="CubeIcon" :label="t('nav.repositories')" :collapsed="collapsed" />
      <SidebarLink to="/projects" :icon="FolderIcon" :label="t('nav.projects')" :collapsed="collapsed" />
      <SidebarLink to="/search" :icon="MagnifyingGlassIcon" :label="t('nav.search')" :collapsed="collapsed" />

      <!-- SECURITY (admins only: the scan APIs need the manage permission) -->
      <template v-if="session.isAdmin">
        <p v-if="!collapsed" class="px-2 mt-4 mb-1 text-xs text-muted">
          {{ t('nav.security') }}
        </p>
        <div v-else class="my-3 mx-auto w-6 h-px bg-border" />

        <SidebarLink to="/security/vulns" :icon="ShieldExclamationIcon" :label="t('nav.vulns')" :collapsed="collapsed" />
        <SidebarLink to="/security/licenses" :icon="DocumentTextIcon" :label="t('nav.licenses')" :collapsed="collapsed" />
        <SidebarLink to="/security/secrets" :icon="EyeSlashIcon" :label="t('nav.secrets')" :collapsed="collapsed" />
        <SidebarLink to="/security/ai-analysis" :icon="SparklesIcon" :label="t('shell.aiAnalysis')" :collapsed="collapsed" />
      </template>

      <!-- ADMINISTRATION (admins only, collapsible) -->
      <template v-if="session.isAdmin">
        <button
          v-if="!collapsed"
          type="button"
          class="w-full flex items-center justify-between px-2 mt-4 mb-1 text-xs text-muted hover:text-foreground transition-colors"
          :aria-expanded="adminOpen"
          aria-controls="sidebar-admin-links"
          @click="adminOpen = !adminOpen"
        >
          <span>{{ t('sidebar.admin') }}</span>
          <ChevronDownIcon class="w-3.5 h-3.5 transition-transform" :class="adminOpen ? '' : '-rotate-90'" aria-hidden="true" />
        </button>
        <div v-else class="my-3 mx-auto w-6 h-px bg-border" />

        <div id="sidebar-admin-links" v-show="adminOpen || collapsed">
          <SidebarLink to="/admin/users" :icon="UsersIcon" :label="t('nav.users')" :collapsed="collapsed" />
          <SidebarLink to="/admin/groups" :icon="UserGroupIcon" :label="t('nav.groups')" :collapsed="collapsed" />
          <SidebarLink to="/admin/projects" :icon="BuildingOfficeIcon" :label="t('shell.projects')" :collapsed="collapsed" />
          <SidebarLink to="/admin/permissions" :icon="ShieldCheckIcon" :label="t('nav.permissions')" :collapsed="collapsed" />
          <SidebarLink to="/admin/sso" :icon="KeyIcon" :label="t('nav.sso')" :collapsed="collapsed" />
          <SidebarLink to="/admin/gpg" :icon="LockClosedIcon" :label="t('nav.gpg')" :collapsed="collapsed" />
          <SidebarLink to="/admin/tokens" :icon="TicketIcon" :label="t('nav.tokens')" :collapsed="collapsed" />
          <SidebarLink to="/admin/audit" :icon="ClipboardDocumentListIcon" :label="t('nav.audit')" :collapsed="collapsed" />
          <SidebarLink to="/admin/trash" :icon="TrashIcon" :label="t('nav.trash')" :collapsed="collapsed" />
          <SidebarLink to="/admin/storage" exact :icon="ServerStackIcon" :label="t('nav.storage')" :collapsed="collapsed" />
          <SidebarLink to="/admin/storage/tiers" :icon="Square3Stack3DIcon" :label="t('nav.storageTiers')" :collapsed="collapsed" />
          <SidebarLink to="/admin/backup" :icon="ArchiveBoxIcon" :label="t('nav.backup')" :collapsed="collapsed" />
          <SidebarLink to="/admin/webhooks" :icon="BellAlertIcon" :label="t('nav.webhooks')" :collapsed="collapsed" />
          <SidebarLink to="/admin/system" :icon="Cog6ToothIcon" :label="t('nav.system')" :collapsed="collapsed" />
          <SidebarLink to="/admin/ai" :icon="SparklesIcon" :label="t('shell.aiSettings')" :collapsed="collapsed" />
        </div>
      </template>

      <!-- SETTINGS -->
      <p v-if="!collapsed" class="px-2 mt-4 mb-1 text-xs text-muted">
        {{ t('sidebar.settings') }}
      </p>
      <div v-else class="my-3 mx-auto w-6 h-px bg-border" />

      <SidebarLink to="/settings/profile" :icon="UserCircleIcon" :label="t('nav.profile')" :collapsed="collapsed" />

    </nav>

    <!-- User footer -->
    <div
      class="border-t border-border px-3 py-3 flex items-center gap-2"
      :class="collapsed && 'justify-center'"
    >
      <!-- Avatar -->
      <div class="w-8 h-8 rounded-full bg-primary/20 flex items-center justify-center shrink-0">
        <span class="text-primary text-sm font-medium" aria-hidden="true">
          {{ session.user?.username?.[0]?.toUpperCase() ?? '?' }}
        </span>
      </div>

      <!-- Username + logout button (hidden when collapsed) -->
      <template v-if="!collapsed">
        <span class="flex-1 text-sm font-medium text-foreground truncate">
          {{ session.user?.username }}
        </span>
        <button
          v-if="canInstall"
          type="button"
          class="shrink-0 text-muted hover:text-foreground transition-colors"
          :aria-label="t('pwa.install')"
          :title="t('pwa.install')"
          @click="install"
        >
          <ArrowDownTrayIcon class="w-4 h-4" aria-hidden="true" />
        </button>
        <button
          type="button"
          class="shrink-0 text-muted hover:text-foreground transition-colors"
          :aria-label="t('auth.signOut')"
          @click="handleLogout"
        >
          <ArrowRightOnRectangleIcon class="w-4 h-4" aria-hidden="true" />
        </button>
      </template>
    </div>
  </aside>
</template>

<script setup lang="ts">
import { useSessionStore } from '../stores/session'
import { useUIStore } from '../stores/ui'
import { computed, nextTick, ref, watch } from 'vue'
import { useMediaQuery } from '@vueuse/core'
import { useRoute } from 'vue-router'
import { useSignOut } from '../composables/useSignOut'
import { useInstallPrompt } from '../composables/useInstallPrompt'
import {
  HomeIcon,
  CubeIcon,
  FolderIcon,
  MagnifyingGlassIcon,
  ChevronDownIcon,
  UsersIcon,
  UserGroupIcon,
  BuildingOfficeIcon,
  ShieldCheckIcon,
  KeyIcon,
  LockClosedIcon,
  TicketIcon,
  ClipboardDocumentListIcon,
  ServerStackIcon,
  Square3Stack3DIcon,
  ArchiveBoxIcon,
  Cog6ToothIcon,
  UserCircleIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  ArrowDownTrayIcon,
  ArrowRightOnRectangleIcon,
  BellAlertIcon,
  ShieldExclamationIcon,
  DocumentTextIcon,
  EyeSlashIcon,
  SparklesIcon,
  TrashIcon,
  XMarkIcon,
} from '@heroicons/vue/24/outline'
import SidebarLink from './SidebarLink.vue'
import { t } from '../i18n/en'
import { useAppVersion, formatVersion } from '../composables/useAppVersion'
import AppLogo from './AppLogo.vue'

const session = useSessionStore()
const ui = useUIStore()
const version = useAppVersion()
const route = useRoute()

// Below md the sidebar is an off-canvas drawer: always expanded, toggled from
// the header, and the desktop rail preference does not apply.
const isMobile = useMediaQuery('(max-width: 767px)')
const collapsed = computed(() => ui.sidebarCollapsed && !isMobile.value)
const asideEl = ref<HTMLElement | null>(null)
watch(
  () => ui.mobileNavOpen,
  async (open) => {
    if (!open) return
    await nextTick()
    asideEl.value?.focus()
  },
)

const versionLabel = computed(() => (version.value ? formatVersion(version.value) : t('shell.versionUnknown')))

const ADMIN_OPEN_KEY = 'openartifact.sidebar.adminOpen'
function readAdminOpen(): boolean {
  try {
    return localStorage.getItem(ADMIN_OPEN_KEY) === '1'
  } catch {
    return false
  }
}
const adminOpen = ref(route.path.startsWith('/admin') || readAdminOpen())
watch(adminOpen, (open) => {
  try {
    localStorage.setItem(ADMIN_OPEN_KEY, open ? '1' : '0')
  } catch {
    // storage unavailable — state simply isn't remembered
  }
})
watch(() => route.path, (path) => {
  if (path.startsWith('/admin')) adminOpen.value = true
})

const handleLogout = useSignOut()
const { canInstall, install } = useInstallPrompt()
</script>
