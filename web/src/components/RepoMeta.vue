<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- The format, type, visibility and federation badges beside a repository name (`PageHeader` `meta` slot). -->
<template>
  <FormatBadge :format="repo.format" />
  <span class="text-xs text-muted">{{ t(`repos.new.type.${repo.repo_type ?? 'hosted'}` as 'repos.new.type.hosted') }}</span>
  <span class="text-xs text-muted" aria-hidden="true">·</span>
  <span class="inline-flex items-center gap-1 text-xs text-muted">
    <EyeIcon v-if="repo.public" class="h-3.5 w-3.5" aria-hidden="true" />
    <EyeSlashIcon v-else class="h-3.5 w-3.5" aria-hidden="true" />
    {{ repo.public ? t('repos.public') : t('repos.private') }}
  </span>
  <span
    v-if="repo.federated"
    class="inline-flex items-center gap-1 rounded-md bg-surface-hover px-2 py-0.5 text-xs font-medium text-muted"
    :title="t('federation.badgeTitle')"
    data-testid="federated-badge"
  >
    <ArrowsRightLeftIcon class="h-3 w-3" aria-hidden="true" />
    {{ t('federation.badge') }}
  </span>
</template>

<script setup lang="ts">
import { ArrowsRightLeftIcon, EyeIcon, EyeSlashIcon } from '@heroicons/vue/24/outline'
import FormatBadge from './FormatBadge.vue'
import type { Repository } from '../api/repositories'
import { t } from '../i18n'

defineProps<{ repo: Repository }>()
</script>
