<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <aside
    class="bg-surface border-r border-border flex flex-col shrink-0 relative transition-[width] duration-200 ease-out"
    :class="ui.sidebarCollapsed ? 'w-16' : 'w-60'"
    :aria-label="t('nav.repositories')"
  >
    <!-- Brand mark -->
    <div
      class="flex items-center gap-2.5 h-14 border-b border-border/60 shrink-0"
      :class="ui.sidebarCollapsed ? 'justify-center px-0' : 'px-3'"
    >
      <div class="w-8 h-8 rounded-md bg-primary flex items-center justify-center shrink-0">
        <svg viewBox="0 0 24 24" class="w-5 h-5 text-white" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M3 12v8a1 1 0 001 1h16a1 1 0 001-1v-8"/>
          <rect x="2" y="9" width="20" height="3" rx="0.5"/>
          <path d="M7 9V5h10v4"/>
          <path d="M7 5l5-2.5L17 5"/>
        </svg>
      </div>
      <div v-if="!ui.sidebarCollapsed" class="leading-none min-w-0">
        <div class="text-[15px] font-semibold tracking-tight">OpenArtifact</div>
        <div class="mt-1 text-[11px] text-muted font-mono">{{ versionLabel }}</div>
      </div>
    </div>

    <!-- Collapse / expand toggle -->
    <button
      type="button"
      class="absolute -right-3 top-[3.5rem] w-6 h-6 rounded-full bg-surface border border-border shadow-sm flex items-center justify-center hover:bg-surface-hover transition-colors z-10"
      :aria-label="ui.sidebarCollapsed ? t('sidebar.expand') : t('sidebar.collapse')"
      @click="ui.toggleSidebar()"
    >
      <ChevronRightIcon v-if="ui.sidebarCollapsed" class="w-3.5 h-3.5 text-muted" aria-hidden="true" />
      <ChevronLeftIcon v-else class="w-3.5 h-3.5 text-muted" aria-hidden="true" />
    </button>

    <!-- Scrollable navigation -->
    <nav class="flex-1 overflow-y-auto nice-scroll px-2 py-3" :aria-label="t('shell.primaryNav')">

      <!-- BROWSE -->
      <p v-if="!ui.sidebarCollapsed" class="px-2 mt-2 mb-1 text-xs text-muted">
        {{ t('sidebar.browse') }}
      </p>
      <div v-else class="my-3 mx-auto w-6 h-px bg-border" />

      <SidebarLink to="/" :icon="HomeIcon" :label="t('nav.dashboard')" :collapsed="ui.sidebarCollapsed" />
      <SidebarLink to="/repos" :icon="CubeIcon" :label="t('nav.repositories')" :collapsed="ui.sidebarCollapsed" />
      <SidebarLink to="/search" :icon="MagnifyingGlassIcon" :label="t('nav.search')" :collapsed="ui.sidebarCollapsed" />

      <!-- SECURITY -->
      <p v-if="!ui.sidebarCollapsed" class="px-2 mt-4 mb-1 text-xs text-muted">
        {{ t('nav.security') }}
      </p>
      <div v-else class="my-3 mx-auto w-6 h-px bg-border" />

      <SidebarLink to="/security/vulns" :icon="ShieldExclamationIcon" :label="t('nav.vulns')" :collapsed="ui.sidebarCollapsed" />
      <SidebarLink to="/security/licenses" :icon="DocumentTextIcon" :label="t('nav.licenses')" :collapsed="ui.sidebarCollapsed" />
      <SidebarLink to="/security/secrets" :icon="EyeSlashIcon" :label="t('nav.secrets')" :collapsed="ui.sidebarCollapsed" />
      <SidebarLink to="/security/ai-analysis" :icon="SparklesIcon" :label="t('shell.aiAnalysis')" :collapsed="ui.sidebarCollapsed" />

      <!-- ADMINISTRATION (admins only, collapsible) -->
      <template v-if="session.isAdmin">
        <button
          v-if="!ui.sidebarCollapsed"
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

        <div id="sidebar-admin-links" v-show="adminOpen || ui.sidebarCollapsed">
          <SidebarLink to="/admin/users" :icon="UsersIcon" :label="t('nav.users')" :collapsed="ui.sidebarCollapsed" />
          <SidebarLink to="/admin/groups" :icon="UserGroupIcon" :label="t('nav.groups')" :collapsed="ui.sidebarCollapsed" />
          <SidebarLink to="/admin/orgs" :icon="BuildingOfficeIcon" :label="t('shell.organizations')" :collapsed="ui.sidebarCollapsed" />
          <SidebarLink to="/admin/permissions" :icon="ShieldCheckIcon" :label="t('nav.permissions')" :collapsed="ui.sidebarCollapsed" />
          <SidebarLink to="/admin/sso" :icon="KeyIcon" :label="t('nav.sso')" :collapsed="ui.sidebarCollapsed" />
          <SidebarLink to="/admin/gpg" :icon="LockClosedIcon" :label="t('nav.gpg')" :collapsed="ui.sidebarCollapsed" />
          <SidebarLink to="/admin/tokens" :icon="TicketIcon" :label="t('nav.tokens')" :collapsed="ui.sidebarCollapsed" />
          <SidebarLink to="/admin/audit" :icon="ClipboardDocumentListIcon" :label="t('nav.audit')" :collapsed="ui.sidebarCollapsed" />
          <SidebarLink to="/admin/storage" :icon="ServerStackIcon" :label="t('nav.storage')" :collapsed="ui.sidebarCollapsed" />
          <SidebarLink to="/admin/backup" :icon="ArchiveBoxIcon" :label="t('nav.backup')" :collapsed="ui.sidebarCollapsed" />
          <SidebarLink to="/admin/webhooks" :icon="BellAlertIcon" :label="t('nav.webhooks')" :collapsed="ui.sidebarCollapsed" />
          <SidebarLink to="/admin/system" :icon="Cog6ToothIcon" :label="t('nav.system')" :collapsed="ui.sidebarCollapsed" />
          <SidebarLink to="/admin/ai" :icon="SparklesIcon" :label="t('shell.aiSettings')" :collapsed="ui.sidebarCollapsed" />
        </div>
      </template>

      <!-- SETTINGS -->
      <p v-if="!ui.sidebarCollapsed" class="px-2 mt-4 mb-1 text-xs text-muted">
        {{ t('sidebar.settings') }}
      </p>
      <div v-else class="my-3 mx-auto w-6 h-px bg-border" />

      <SidebarLink to="/settings/profile" :icon="UserCircleIcon" :label="t('nav.profile')" :collapsed="ui.sidebarCollapsed" />

    </nav>

    <!-- User footer -->
    <div
      class="border-t border-border px-3 py-3 flex items-center gap-2"
      :class="ui.sidebarCollapsed && 'justify-center'"
    >
      <!-- Avatar -->
      <div class="w-8 h-8 rounded-full bg-primary/20 flex items-center justify-center shrink-0">
        <span class="text-primary text-sm font-medium" aria-hidden="true">
          {{ session.user?.username?.[0]?.toUpperCase() ?? '?' }}
        </span>
      </div>

      <!-- Username + logout button (hidden when collapsed) -->
      <template v-if="!ui.sidebarCollapsed">
        <span class="flex-1 text-sm font-medium text-foreground truncate">
          {{ session.user?.username }}
        </span>
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
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { logout as apiLogout } from '../api/auth'
import {
  HomeIcon,
  CubeIcon,
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
  ArchiveBoxIcon,
  Cog6ToothIcon,
  UserCircleIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  ArrowRightOnRectangleIcon,
  BellAlertIcon,
  ShieldExclamationIcon,
  DocumentTextIcon,
  EyeSlashIcon,
  SparklesIcon,
} from '@heroicons/vue/24/outline'
import SidebarLink from './SidebarLink.vue'
import { t } from '../i18n/en'
import { useAppVersion, formatVersion } from '../composables/useAppVersion'

const session = useSessionStore()
const ui = useUIStore()
const router = useRouter()
const version = useAppVersion()
const route = useRoute()

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

async function handleLogout(): Promise<void> {
  try {
    await apiLogout()
  } catch {
    // already logged out — ignore
  }
  session.clearSession()
  await router.push('/login')
}
</script>
