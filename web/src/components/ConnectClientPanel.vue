<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <section class="rounded-lg border border-border bg-surface">
    <div class="px-4 py-3 border-b border-border">
      <h2 v-if="!titled" class="text-sm font-semibold text-foreground">{{ t('repoDetail.connect.title') }}</h2>
      <p class="text-xs text-muted" :class="titled ? '' : 'mt-0.5'">
        {{ readOnly ? t('repoDetail.connect.hintReadOnly') : t('repoDetail.connect.hint') }}
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
          <h3 class="text-xs font-medium text-muted">{{ configureLabel }}</h3>
          <CopyButton :text="current.configure" :label="`${t('repoDetail.copy')}: ${configureLabel}`" />
        </div>
        <pre class="rounded-md border border-border bg-surface-hover p-3 text-xs font-mono text-foreground whitespace-pre-wrap break-all">{{ current.configure }}</pre>
      </div>

      <p v-if="readOnly" class="text-xs text-muted" data-testid="connect-read-only">{{ t('repoDetail.connect.readOnly') }}</p>
      <div v-else-if="current.publish">
        <div class="flex items-center justify-between mb-1.5">
          <h3 class="text-xs font-medium text-muted">{{ t('repoDetail.connect.publish') }}</h3>
          <CopyButton :text="current.publish" :label="`${t('repoDetail.copy')}: ${t('repoDetail.connect.publish')}`" />
        </div>
        <pre class="rounded-md border border-border bg-surface-hover p-3 text-xs font-mono text-foreground whitespace-pre-wrap break-all">{{ current.publish }}</pre>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import CopyButton from './ui/CopyButton.vue'
import { clientSetup } from '../lib/installCommands'
import { useDocsLink } from '../composables/useDocsLink'
import { t } from '../i18n'

const props = defineProps<{
  format: string
  repo: string
  /** hosted, proxy or group: a proxy of some formats is configured differently from a hosted repository. */
  repoType?: string
  /** The host already shows the "Connect a client" heading above the panel. */
  titled?: boolean
  /** A proxy or group repository cannot be published to: the panel only offers the commands that read from it. */
  readOnly?: boolean
}>()

// The client-setup page lists the configure and publish commands of every client.
const docsUrl = useDocsLink('repo-detail')

const active = ref(0)
const setups = computed(() => clientSetup(props.format, window.location.origin, props.repo, props.repoType))
const current = computed(() => setups.value[active.value])
// A raw repository is read with a plain download, not a client configuration.
const configureLabel = computed(() => (props.format === 'raw' ? t('repoDetail.connect.download') : t('repoDetail.connect.configure')))

watch(setups, () => {
  active.value = 0
})
</script>
