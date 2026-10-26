<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Settings > Access tokens: personal, project and federation tokens in one list. -->
<template>
  <div>
    <SectionActions>
      <AppButton size="sm" class="max-md:h-11" data-testid="token-new" @click="creating = true">
        <PlusIcon class="h-4 w-4" aria-hidden="true" />
        {{ t('tok.create.open') }}
      </AppButton>
    </SectionActions>

    <FilterBar
      :search="list.text.q"
      :placeholder="t('tok.filter.search')"
      :search-label="t('tok.filter.search')"
      :active="list.filtered.value"
      @update:search="list.setFilter('q', $event)"
      @clear="list.clear"
    >
      <SegmentedControl
        :options="typeOptions"
        :model-value="list.filters.value.type"
        :label="t('tok.filter.type')"
        data-testid="type-filter"
        @update:model-value="list.setFilter('type', $event as TokenTypeFilter)"
      />
      <template #after>
        <button
          v-for="d in EXPIRING_DAYS"
          :key="d"
          type="button"
          class="inline-flex h-9 items-center rounded-full border px-3 text-sm max-md:h-11"
          :class="list.filters.value.expiring === d ? 'border-warning bg-warning-soft text-warning' : 'border-border-strong text-foreground hover:bg-surface-hover'"
          :aria-pressed="list.filters.value.expiring === d"
          :data-testid="`filter-expiring-${d}`"
          @click="list.setFilter('expiring', list.filters.value.expiring === d ? '' : d)"
        >{{ tp('tok.filter.expiring', { days: d }) }}</button>
      </template>
    </FilterBar>

    <p
      v-if="notice"
      class="mb-4 flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-border bg-surface-hover px-4 py-2 text-sm text-foreground"
      role="status"
      data-testid="token-notice"
    >
      {{ notice }}
      <button type="button" class="text-primary hover:underline max-md:min-h-11" @click="list.clear">{{ t('tok.filter.showAll') }}</button>
    </p>

    <TokensTable
      :query="list"
      :can-revoke="canRevoke"
      :docs-url="docsUrl"
      @create="creating = true"
      @revoke="revoking = $event"
    />

    <TokenCreateDrawer
      :open="creating"
      :initial-type="list.filters.value.type === 'all' ? undefined : list.filters.value.type"
      :initial-project="list.filters.value.project || undefined"
      @close="closeCreate"
      @created="onCreated"
    />
    <TokenSecretDialog :title="revealTitle" :token="secret" :hint="revealHint" @close="secret = null" />
    <ConfirmDialog
      :open="revoking !== null"
      :title="t('tok.revoke.title')"
      :description="revoking ? revokeDescription(revoking) : ''"
      :confirm-label="t('tok.revoke.action')"
      :busy="revokeBusy"
      @confirm="confirmRevoke"
      @close="revoking = null"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { PlusIcon } from '@heroicons/vue/20/solid'
import { useRoute, useRouter } from 'vue-router'
import AppButton from '../../../components/AppButton.vue'
import ConfirmDialog from '../../../components/ConfirmDialog.vue'
import SectionActions from '../../../components/SectionActions.vue'
import TokenSecretDialog from '../../../components/TokenSecretDialog.vue'
import TokenCreateDrawer, { type CreatedSecret } from '../../../components/tokens/TokenCreateDrawer.vue'
import TokensTable from '../../../components/tokens/TokensTable.vue'
import FilterBar from '../../../components/ui/FilterBar.vue'
import SegmentedControl from '../../../components/ui/SegmentedControl.vue'
import { useDocsLink } from '../../../composables/useDocsLink'
import { useTokenList } from '../../../composables/useTokenList'
import type { UnifiedToken } from '../../../api/tokens'
import { TOKEN_TYPE_FILTERS, type TokenTypeFilter } from '../../../lib/tokens'
import { t, tDynamic, tp } from '../../../i18n'
import { formatLocale } from '../../../lib/intl'

const EXPIRING_DAYS = ['7', '30'] as const
const typeOptions = TOKEN_TYPE_FILTERS.map((o) => ({ value: o, label: tDynamic(`tok.type.${o}`) }))

const route = useRoute()
const router = useRouter()
const { list, who, canRevoke, revoking, revokeBusy, confirmRevoke } = useTokenList()
const docsUrl = useDocsLink('settings-tokens')

// `?new=1` (with `type` and `project`) opens the drawer from a link.
const creating = ref(route.query.new === '1')
const secret = ref<string | null>(null)
const revealTitle = ref('')
const revealHint = ref('')

function closeCreate(): void {
  creating.value = false
  if (route.query.new !== undefined) void router.replace({ query: { ...route.query, new: undefined } })
}

function onCreated(created: CreatedSecret): void {
  closeCreate()
  revealTitle.value = tp('tok.reveal.title', { name: created.name })
  revealHint.value = tDynamic(`tok.reveal.hint.${created.type}`)
  secret.value = created.secret
  void list.reload()
}

const notice = computed(() => {
  const f = list.filters.value
  if (f.expiring) return tp('tok.notice.expiring', { days: f.expiring })
  if (f.project) return tp('tok.notice.project', { project: f.project })
  return ''
})

function revokeDescription(token: UnifiedToken): string {
  const owner = token.owner === who.value.username ? t('tok.revoke.you') : token.owner
  return tp('tok.revoke.desc', { name: token.name, type: tDynamic(`tok.type.${token.type}`).toLocaleLowerCase(formatLocale()), owner })
}
</script>
