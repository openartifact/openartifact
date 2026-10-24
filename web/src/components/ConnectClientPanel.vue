<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <section class="rounded-lg border border-border bg-surface">
    <div class="px-4 py-3 border-b border-border">
      <h2 class="text-sm font-semibold text-foreground">{{ t('repoDetail.connect.title') }}</h2>
      <p class="text-xs text-muted mt-0.5">
        {{ t('repoDetail.connect.hint') }}
        <a
          v-if="docsUrl"
          :href="docsUrl"
          target="_blank"
          rel="noopener"
          data-testid="connect-docs"
          class="whitespace-nowrap underline underline-offset-2 hover:text-foreground"
        >{{ t('repoDetail.connect.docs') }}</a>
      </p>
    </div>

    <div
      v-if="setups.length > 1"
      class="flex items-center gap-1 px-3 border-b border-border overflow-x-auto"
      role="tablist"
      :aria-label="t('repoDetail.connect.clients')"
    >
      <button
        v-for="(s, i) in setups"
        :key="s.client"
        role="tab"
        :aria-selected="active === i"
        class="relative h-9 px-3 text-sm font-medium shrink-0 cursor-pointer transition-colors"
        :class="active === i ? 'text-foreground' : 'text-muted hover:text-foreground'"
        @click="active = i"
      >
        {{ s.client }}
        <span v-if="active === i" class="absolute left-2 right-2 -bottom-px h-[2px] bg-primary rounded-full" />
      </button>
    </div>

    <div v-if="current" class="p-4 space-y-4">
      <div>
        <div class="flex items-center justify-between mb-1.5">
          <h3 class="text-xs font-medium text-muted">{{ t('repoDetail.connect.configure') }}</h3>
          <AppButton variant="secondary" size="xs"
            @click="copy(current.configure)"
          >
            {{ t('repoDetail.copy') }}
          </AppButton>
        </div>
        <pre class="rounded-md border border-border bg-surface-hover p-3 text-xs font-mono text-foreground whitespace-pre-wrap break-all">{{ current.configure }}</pre>
      </div>

      <div v-if="current.publish">
        <div class="flex items-center justify-between mb-1.5">
          <h3 class="text-xs font-medium text-muted">{{ t('repoDetail.connect.publish') }}</h3>
          <AppButton variant="secondary" size="xs"
            @click="copy(current.publish)"
          >
            {{ t('repoDetail.copy') }}
          </AppButton>
        </div>
        <pre class="rounded-md border border-border bg-surface-hover p-3 text-xs font-mono text-foreground whitespace-pre-wrap break-all">{{ current.publish }}</pre>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppButton from './AppButton.vue'
import { clientSetup } from '../lib/installCommands'
import { useDocsLink } from '../composables/useDocsLink'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const props = defineProps<{ format: string; repo: string }>()

// The client-setup page lists the configure and publish commands of every client.
const docsUrl = useDocsLink('repo-detail')

const toasts = useToastStore()
const active = ref(0)
const setups = computed(() => clientSetup(props.format, window.location.origin, props.repo))
const current = computed(() => setups.value[active.value])

watch(setups, () => {
  active.value = 0
})

function copy(text: string): void {
  navigator.clipboard
    .writeText(text)
    .then(() => toasts.success(t('repoDetail.copied')))
    .catch(() => toasts.error(t('repoDetail.urlCopyFailed')))
}
</script>
