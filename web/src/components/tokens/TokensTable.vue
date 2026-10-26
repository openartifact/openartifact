<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <ListTable
    :columns="columns"
    :caption="t('tok.table.caption')"
    :query="query"
    :empty-title="t('tok.empty.title')"
    :empty-hint="t('tok.empty.hint')"
  >
    <template #empty-action>
      <div class="flex flex-col items-center gap-2">
        <AppButton data-testid="token-empty-create" @click="emit('create')">{{ t('tok.create.open') }}</AppButton>
        <DocsLink v-if="docsUrl" :url="docsUrl">{{ t('tok.docs') }}</DocsLink>
      </div>
    </template>

    <template #cell-name="{ row }">
      <div class="min-w-0">
        <span class="break-all font-mono text-sm text-foreground">{{ (row as UnifiedToken).name }}</span>
        <span v-if="(row as UnifiedToken).owner" class="block text-xs text-muted">
          {{ tp('tok.owner', { owner: (row as UnifiedToken).owner }) }}
        </span>
      </div>
    </template>

    <template #cell-type="{ row }">
      <StatusBadge :label="tDynamic(`tok.type.${(row as UnifiedToken).type}`)" />
    </template>

    <template #cell-scope="{ row }">
      <template v-for="s in [scopeOf(row as UnifiedToken)]" :key="s.kind">
        <template v-if="s.kind === 'personal'">
          <span class="text-sm text-foreground">{{ t('tok.scope.personal') }}</span>
          <span class="block text-xs text-muted">
            {{ s.scopes > 0 ? tp('tok.scope.limited', { count: String(s.scopes) }) : t('tok.scope.full') }}
          </span>
        </template>
        <template v-else-if="s.kind === 'project'">
          <router-link v-if="s.project" :to="`/projects/${encodeURIComponent(s.project)}`" class="text-sm text-primary hover:underline">{{ s.project }}</router-link>
          <span v-if="s.role" class="block text-xs text-muted">{{ tDynamic(`tok.role.${s.role}`) }}</span>
        </template>
        <template v-else>
          <router-link v-if="s.repository" :to="`/repos/${encodeURIComponent(s.repository)}?tab=settings`" class="text-sm text-primary hover:underline">{{ s.repository }}</router-link>
          <span v-if="s.project" class="block text-xs text-muted">{{ tp('tok.scope.inProject', { project: s.project }) }}</span>
        </template>
      </template>
    </template>

    <template #cell-lastUsed="{ row }">
      <time
        v-if="(row as UnifiedToken).last_used_at"
        :datetime="(row as UnifiedToken).last_used_at"
        :title="formatDateTime((row as UnifiedToken).last_used_at)"
        class="text-sm text-foreground"
      >{{ tp('tok.ago', { when: timeAgo((row as UnifiedToken).last_used_at, { maxDays: 30 }) }) }}</time>
      <span v-else class="text-sm text-muted">{{ t('tok.lastUsed.never') }}</span>
    </template>

    <template #cell-expires="{ row }">
      <template v-for="e in [expiryOf(row as UnifiedToken)]" :key="e.kind">
        <time
          v-if="(row as UnifiedToken).expires_at"
          :datetime="(row as UnifiedToken).expires_at"
          :title="formatDateTime((row as UnifiedToken).expires_at)"
          class="inline-flex items-center gap-1.5 text-sm"
          :class="e.kind === 'expired' ? 'font-medium text-danger' : e.kind === 'soon' ? 'font-medium text-warning' : 'text-foreground'"
          :data-expiry="e.kind"
        >
          <ExclamationTriangleIcon v-if="e.kind !== 'ok'" class="h-4 w-4 shrink-0" aria-hidden="true" />
          {{ expiryText(e) }}
        </time>
        <span v-else class="text-sm text-muted" data-expiry="never">{{ expiryText(e) }}</span>
      </template>
    </template>

    <template #cell-actions="{ row }">
      <AppButton
        v-if="canRevoke(row as UnifiedToken)"
        variant="danger-outline"
        size="sm"
        class="max-md:min-h-11"
        :data-testid="`revoke-${(row as UnifiedToken).id}`"
        @click="emit('revoke', row as UnifiedToken)"
      >{{ t('tok.revoke.action') }}</AppButton>
    </template>
  </ListTable>
</template>

<script setup lang="ts">
import { ExclamationTriangleIcon } from '@heroicons/vue/20/solid'
import AppButton from '../AppButton.vue'
import DocsLink from '../DocsLink.vue'
import StatusBadge from '../StatusBadge.vue'
import ListTable, { type ListColumn } from '../ui/ListTable.vue'
import type { UnifiedToken } from '../../api/tokens'
import type { ListQuery } from '../../composables/useListQuery'
import type { Schema } from '../../composables/useUrlState'
import { formatDateTime, timeAgo } from '../../lib/datetime'
import { expiryOf, expiryText, scopeOf } from '../../lib/tokens'
import { t, tDynamic, tp } from '../../i18n/en'

defineProps<{
  query: ListQuery<Schema, UnifiedToken>
  canRevoke: (token: UnifiedToken) => boolean
  docsUrl: string | null
}>()
const emit = defineEmits<{ create: []; revoke: [token: UnifiedToken] }>()

const columns: ListColumn[] = [
  { key: 'name', label: t('tok.col.name') },
  { key: 'type', label: t('tok.col.type') },
  { key: 'scope', label: t('tok.col.scope') },
  { key: 'lastUsed', label: t('tok.col.lastUsed') },
  { key: 'expires', label: t('tok.col.expires') },
  { key: 'actions', label: t('tok.col.actions'), actions: true },
]
</script>
