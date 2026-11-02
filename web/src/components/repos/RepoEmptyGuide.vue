<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Empty state of a hosted repository: how to publish the first package with the
  format's own client and how to consume it, both with this repository's URL.
  The commands come from the shared client-setup templates (lib/installCommands).
-->
<template>
  <div class="px-4 py-10" data-testid="repo-empty-guide">
    <div class="mx-auto flex max-w-2xl flex-col gap-5">
      <div class="text-center">
        <InboxIcon class="mx-auto mb-3 h-8 w-8 text-muted" aria-hidden="true" />
        <h3 class="text-base font-semibold text-foreground">{{ t('rl.packages.empty') }}</h3>
        <p class="mt-1 text-sm text-muted">{{ t('repoDetail.empty.hint') }}</p>
        <AppButton v-if="canUpload" class="mt-3" variant="secondary" data-testid="empty-upload" @click="emit('upload')">
          <ArrowUpTrayIcon class="h-4 w-4 text-muted" aria-hidden="true" />
          {{ t('repoDetail.empty.upload') }}
        </AppButton>
      </div>

      <section v-if="publish.length > 0" data-testid="empty-publish">
        <h4 class="mb-2 text-sm font-semibold text-foreground">{{ t('repoDetail.empty.publish') }}</h4>
        <div class="space-y-2">
          <SnippetBlock v-for="s in publish" :key="s.client" :label="publish.length > 1 ? s.client : ''" :text="s.publish ?? ''" />
        </div>
        <p class="mt-2 text-xs text-muted" data-testid="empty-token-hint">
          {{ t('repoDetail.empty.tokenHint') }}
          <router-link to="/settings/tokens" class="text-primary hover:underline" data-testid="empty-token-link">{{ t('repoDetail.empty.tokenLink') }}</router-link>
        </p>
      </section>

      <section data-testid="empty-consume">
        <h4 class="mb-2 text-sm font-semibold text-foreground">{{ t('repoDetail.empty.consume') }}</h4>
        <div class="space-y-2">
          <SnippetBlock v-for="s in setups" :key="s.client" :label="setups.length > 1 ? s.client : ''" :text="s.configure" />
        </div>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ArrowUpTrayIcon, InboxIcon } from '@heroicons/vue/24/outline'
import AppButton from '../AppButton.vue'
import SnippetBlock from './SnippetBlock.vue'
import { clientSetup } from '../../lib/installCommands'
import { t } from '../../i18n'

const props = defineProps<{ format: string; repo: string; canUpload?: boolean }>()
const emit = defineEmits<{ upload: [] }>()

const setups = computed(() => clientSetup(props.format, window.location.origin, props.repo, 'hosted'))
const publish = computed(() => setups.value.filter((s) => s.publish))
</script>
