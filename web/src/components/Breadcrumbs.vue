<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <nav v-if="segments.length" :aria-label="t('shell.breadcrumbs')" class="text-sm text-muted px-6 py-2 border-b border-border bg-surface">
    <ol class="flex items-center gap-1 flex-wrap">
      <li v-for="(seg, idx) in segments" :key="idx" class="flex items-center gap-1">
        <ChevronRightIcon v-if="idx > 0" class="w-3 h-3 text-muted" aria-hidden="true" />
        <router-link
          v-if="seg.to && idx < segments.length - 1"
          :to="seg.to"
          class="hover:text-foreground"
        >
          {{ seg.label }}
        </router-link>
        <span v-else class="text-foreground font-medium">{{ seg.label }}</span>
      </li>
    </ol>
  </nav>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { ChevronRightIcon } from '@heroicons/vue/20/solid'
import { t } from '../i18n/en'

interface Segment {
  label: string
  to?: string
}

const route = useRoute()

const labelMap: Record<string, string> = {
  dashboard: t('nav.dashboard'),
  repos: t('nav.repositories'),
  'repo-detail': '',
  'package-detail': '',
  search: t('nav.search'),
  'admin-users': t('nav.users'),
  'admin-groups': t('nav.groups'),
  'admin-permissions': t('nav.permissions'),
  'admin-sso': t('nav.sso'),
  'admin-system': t('nav.system'),
  'admin-gpg': t('nav.gpg'),
  'admin-tokens': t('nav.tokens'),
  'admin-audit': t('nav.audit'),
  'admin-storage': t('nav.storage'),
  'admin-webhooks': t('nav.webhooks'),
  'security-vulns': t('nav.vulns'),
  'security-licenses': t('nav.licenses'),
  'security-secrets': t('nav.secrets'),
  'settings-profile': t('nav.profile'),
  'settings-password': t('settings.password'),
  'settings-tokens': t('settings.tokens'),
  'settings-theme': t('settings.theme'),
  'settings-notifications': t('settings.notifications'),
}

const segments = computed<Segment[]>(() => {
  const out: Segment[] = []
  if (route.meta.layout === 'blank') return []
  out.push({ label: t('breadcrumbs.home'), to: '/' })

  for (const r of route.matched) {
    if (!r.name || r.name === 'dashboard') continue
    const name = String(r.name)
    let label = labelMap[name] ?? name
    // Resolve dynamic segments (repo name, package name, etc.) from params
    if (name === 'repo-detail') {
      label = String(route.params.name ?? '')
    } else if (name === 'package-detail') {
      label = String(route.params.pkg ?? '')
    } else if (name === 'repo-edit') {
      label = `${String(route.params.name ?? '')} · ${t('common.edit')}`
    } else if (name === 'repo-new') {
      label = t('repos.create')
    } else if (name.startsWith('admin-') && !labelMap[name]) {
      label = name.replace('admin-', '')
    }
    out.push({ label, to: r.path === route.path ? undefined : r.path })
  }
  return out
})
</script>
