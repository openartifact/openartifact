<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <PageHeader :crumbs="[{ label: t('dashboard.title') }]" :title="isAdmin ? t('dashboard.title') : tp('dashboard.welcome', { name: username })" :description="isAdmin ? t('dashboard.description.admin') : t('dashboard.description.member')">
      <template #actions>
        <AppButton variant="secondary" :loading="refreshing" @click="refresh">
          <ArrowPathIcon v-if="!refreshing" class="h-4 w-4" aria-hidden="true" />
          {{ t('dashboard.refresh') }}
        </AppButton>
        <AppButton v-if="session.canCreateRepo" to="/repos?new=1">{{ t('repos.new') }}</AppButton>
      </template>
    </PageHeader>

    <HealthStrip v-if="isAdmin" :cells="d.health.value" class="mb-8 block" />

    <ChecklistCard v-if="d.showChecklist.value" :steps="d.checklist.value.steps" @dismiss="d.dismissChecklist" />

    <AttentionList :items="d.attention.value" :loading="d.attentionLoading.value" :failed="d.attentionFailed.value" />

    <div class="mt-8 grid grid-cols-1 gap-x-8 xl:grid-cols-[minmax(0,1fr)_380px]">
      <div class="min-w-0">
        <ProjectsList v-if="!isAdmin" :projects="session.projects" />
        <DashSection
          v-if="!isAdmin"
          :title="t('dashboard.recent.title')"
          heading-id="dash-recent"
          :state="blockState(d.recent.value)"
          :error="errorOf(d.recent.value)"
          :empty="(dataOf(d.recent.value) ?? []).length === 0"
          @retry="d.reload"
        >
          <template #empty>
            <StateBlock variant="empty" :title="t('dashboard.recent.empty')" :hint="t('dashboard.recent.emptyHint')" />
          </template>
          <RecentPackages :items="dataOf(d.recent.value) ?? []" />
        </DashSection>
        <ActivityFeed :items="activityItems" :state="blockState(d.activity.value)" :error="errorOf(d.activity.value)" :is-admin="isAdmin" @retry="d.reload" />
      </div>
      <div class="min-w-0 max-xl:mt-8">
        <ConnectClientBlock :repos="dataOf(d.repos.value)?.repositories ?? []" :state="blockState(d.repos.value)" :can-create="session.canCreateRepo" @retry="d.reload" />
      </div>
    </div>

    <TotalsRow :totals="d.totals.value" />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { ArrowPathIcon } from '@heroicons/vue/24/outline'
import AppButton from '../components/AppButton.vue'
import PageHeader from '../components/PageHeader.vue'
import ActivityFeed from '../components/dashboard/ActivityFeed.vue'
import AttentionList from '../components/dashboard/AttentionList.vue'
import ChecklistCard from '../components/dashboard/ChecklistCard.vue'
import ConnectClientBlock from '../components/dashboard/ConnectClientBlock.vue'
import DashSection from '../components/dashboard/DashSection.vue'
import HealthStrip from '../components/dashboard/HealthStrip.vue'
import ProjectsList from '../components/dashboard/ProjectsList.vue'
import TotalsRow from '../components/dashboard/TotalsRow.vue'
import RecentPackages from '../components/packages/RecentPackages.vue'
import StateBlock from '../components/ui/StateBlock.vue'
import { useDashboard } from '../composables/useDashboard'
import { t, tp } from '../i18n'
import { dataOf, type Block } from '../lib/block'
import { useSessionStore } from '../stores/session'
import { useToastStore } from '../stores/toast'

const session = useSessionStore()
const toasts = useToastStore()
const d = useDashboard()
const isAdmin = d.isAdmin
const username = computed(() => session.user?.username ?? '')
const refreshing = ref(false)

// A source the caller may not read is treated as empty, never as a hole.
const blockState = (b: Block<unknown>): 'loading' | 'ready' | 'error' => (b.state === 'forbidden' ? 'ready' : b.state)
const errorOf = (b: Block<unknown>): string => (b.state === 'error' ? b.message : '')
const activityItems = computed(() => dataOf(d.activity.value) ?? [])

async function refresh(): Promise<void> {
  refreshing.value = true
  try {
    await d.reload()
    toasts.success(t('dashboard.refreshed'))
  } finally {
    refreshing.value = false
  }
}
</script>
