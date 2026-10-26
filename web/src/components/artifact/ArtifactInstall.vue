<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  How to fetch the shown version with the native client of its format. The
  commands come from lib/installCommands (the same snippets the server's
  client_setup tool returns); Docker also offers a digest-pinned pull.
-->
<template>
  <section class="rounded-lg border border-border bg-surface" :aria-labelledby="`${uid}-title`" data-testid="artifact-install">
    <div class="flex flex-wrap items-center justify-between gap-2 border-b border-border px-4 py-3">
      <h2 :id="`${uid}-title`" class="text-sm font-semibold text-foreground">{{ t('artifact.install.title') }}</h2>
      <div v-if="block.commands.length > 1" role="group" :aria-label="t('artifact.install.pullBy')" class="inline-flex rounded-lg border border-border p-0.5">
        <button
          v-for="c in block.commands"
          :key="c.id"
          type="button"
          :aria-pressed="c.id === current.id"
          class="h-7 rounded-md px-3 text-xs font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:h-11"
          :class="c.id === current.id ? 'bg-primary-soft text-primary' : 'text-muted hover:text-foreground'"
          @click="mode = c.id"
        >{{ t(`artifact.install.mode.${c.id}`) }}</button>
      </div>
    </div>

    <div class="space-y-3 px-4 py-3">
      <p v-if="quarantined" role="status" class="flex items-start gap-2 text-sm text-warning" data-testid="install-quarantined">
        <LockClosedIcon class="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
        {{ t('artifact.install.quarantined') }}
      </p>
      <p v-if="critical > 0" role="alert" class="flex items-start gap-2 text-sm text-danger" data-testid="install-critical">
        <XCircleIcon class="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
        <span>
          {{ tpn('artifact.install.critical', critical) }}
          <router-link :to="{ query: { tab: 'security' } }" class="underline underline-offset-2">{{ t('artifact.install.openSecurity') }}</router-link>
        </span>
      </p>

      <div class="flex items-start gap-1 rounded-md border border-border bg-surface-hover pl-3">
        <code class="min-w-0 flex-1 overflow-x-auto whitespace-pre py-2 font-mono text-xs text-foreground" data-testid="install-command">{{ current.code }}</code>
        <CopyButton :text="current.code" :label="t('artifact.install.copy')" />
      </div>

      <p class="flex flex-wrap items-center gap-x-2 text-xs text-muted">
        <span>{{ t('artifact.install.registry') }}</span>
        <span class="break-all font-mono text-foreground" data-testid="registry-url">{{ block.registryUrl }}</span>
        <CopyButton :text="block.registryUrl" :label="t('artifact.install.copyRegistry')" />
      </p>

      <details v-if="block.clients.length > 0" class="border-t border-border pt-3">
        <summary class="cursor-pointer text-sm font-medium text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:min-h-11">
          {{ t('artifact.install.configure') }}
        </summary>
        <div class="mt-3 space-y-4">
          <div v-for="client in block.clients" :key="client.client">
            <h3 class="mb-1 text-xs font-medium text-muted">{{ client.client }}</h3>
            <div class="flex items-start gap-1 rounded-md border border-border bg-surface-hover pl-3">
              <pre class="min-w-0 flex-1 overflow-x-auto py-2 font-mono text-xs text-foreground">{{ client.configure }}</pre>
              <CopyButton :text="client.configure" :label="tp('artifact.install.copyConfigure', { client: client.client })" />
            </div>
            <div v-if="client.publish" class="mt-2 flex items-start gap-1 rounded-md border border-border bg-surface-hover pl-3">
              <pre class="min-w-0 flex-1 overflow-x-auto py-2 font-mono text-xs text-foreground">{{ client.publish }}</pre>
              <CopyButton :text="client.publish" :label="tp('artifact.install.copyPublish', { client: client.client })" />
            </div>
          </div>
          <p class="text-xs text-muted">
            {{ t('artifact.install.tokenHint') }}
            <router-link :to="{ path: '/settings/tokens', query: { type: 'personal' } }" class="underline underline-offset-2">{{ t('artifact.install.createToken') }}</router-link>
          </p>
        </div>
      </details>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import { LockClosedIcon, XCircleIcon } from '@heroicons/vue/20/solid'
import CopyButton from '../ui/CopyButton.vue'
import { installBlock } from '../../lib/artifactInstall'
import { t, tp, tpn } from '../../i18n/en'

const props = defineProps<{
  format: string
  repo: string
  pkg: string
  version: string
  /** Checksum of the primary file; for Docker the manifest digest of the digest-pinned pull. */
  sha256?: string
  quarantined: boolean
  critical: number
}>()

const uid = useId()
const block = computed(() => installBlock(props.format, window.location.origin, props.repo, props.pkg, props.version, props.sha256))
const mode = ref<'tag' | 'digest'>('tag')
const current = computed(() => block.value.commands.find((c) => c.id === mode.value) ?? block.value.commands[0]!)
</script>
