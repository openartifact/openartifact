<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <DashSection
    :title="t('dashboard.activity.title')"
    heading-id="dash-activity"
    :state="state"
    :error="error"
    :empty="items.length === 0"
    @retry="emit('retry')"
  >
    <template #actions>
      <router-link v-if="isAdmin" to="/settings/audit" class="text-xs text-primary hover:underline max-md:flex max-md:min-h-11 max-md:items-center">{{ t('dashboard.activity.viewAudit') }}</router-link>
    </template>
    <template #empty>
      <StateBlock
        variant="empty"
        :title="t('dashboard.activity.empty')"
        :hint="isAdmin ? t('dashboard.activity.emptyHint.admin') : t('dashboard.activity.emptyHint.member')"
      />
    </template>
    <ul class="divide-y divide-border rounded-lg border border-border bg-surface" role="list" data-testid="activity-feed">
      <li v-for="item in items" :key="item.id" class="flex items-baseline justify-between gap-3 px-4 py-2.5 text-sm">
        <span class="min-w-0">
          <span class="font-medium text-foreground">{{ item.actor || t('dashboard.activity.someone') }}</span>
          {{ ' ' }}<span class="text-muted">{{ item.verb }}</span>
          <template v-if="item.subject">
            {{ ' ' }}
            <router-link v-if="item.subject.to" :to="item.subject.to" class="text-foreground hover:underline">{{ item.subject.label }}</router-link>
            <span v-else class="text-foreground">{{ item.subject.label }}</span>
          </template>
          <template v-if="item.where">
            {{ ' ' }}<span class="text-muted">{{ t('dashboard.activity.in') }}</span>{{ ' ' }}
            <router-link v-if="item.where.to" :to="item.where.to" class="text-foreground hover:underline">{{ item.where.label }}</router-link>
            <span v-else class="text-foreground">{{ item.where.label }}</span>
          </template>
        </span>
        <time class="shrink-0 text-xs tabular-nums text-muted" :datetime="item.at" :title="formatDateTime(item.at)">{{ timeAgo(item.at) }}</time>
      </li>
    </ul>
  </DashSection>
</template>

<script setup lang="ts">
import StateBlock from '../ui/StateBlock.vue'
import DashSection from './DashSection.vue'
import { t } from '../../i18n/en'
import { formatDateTime, timeAgo } from '../../lib/datetime'
import type { ActivityItem } from '../../lib/activity'

defineProps<{ items: ActivityItem[]; state: 'loading' | 'ready' | 'error'; error?: string; isAdmin: boolean }>()
const emit = defineEmits<{ retry: [] }>()
</script>
