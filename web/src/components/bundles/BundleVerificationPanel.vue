<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Verification tab of a bundle: signature, seal and archive facts, manifest downloads. -->
<template>
  <section class="rounded-lg border border-border bg-surface p-4 sm:p-5" :aria-label="t('bundles.tab.verification')">
    <dl class="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm" data-testid="verification">
      <dt class="text-muted">{{ t('bundles.verify.signature') }}</dt>
      <dd class="flex flex-wrap items-center gap-2 text-foreground">
        <template v-if="bundle.gpg_key_id">
          <span>{{ t('bundles.signed') }}</span>
          <span class="break-all font-mono text-xs text-muted">{{ bundle.gpg_key_id }}</span>
          <CopyButton :text="bundle.gpg_key_id" :label="t('bundles.verify.copyKey')" />
        </template>
        <span v-else>{{ bundle.state === 'draft' ? t('bundles.verify.signedAtSeal') : t('bundles.notSigned') }}</span>
      </dd>
      <template v-if="bundle.sealed_at">
        <dt class="text-muted">{{ t('bundles.verify.sealed') }}</dt>
        <dd class="text-foreground"><RelativeTime :value="bundle.sealed_at" /> · {{ bundle.sealed_by }}</dd>
      </template>
      <template v-if="bundle.archived_at">
        <dt class="text-muted">{{ t('bundles.verify.archived') }}</dt>
        <dd class="text-foreground"><RelativeTime :value="bundle.archived_at" /> · {{ bundle.archived_by }}</dd>
      </template>
      <dt class="text-muted">{{ t('bundles.verify.manifest') }}</dt>
      <dd class="flex flex-wrap gap-2">
        <template v-if="bundle.state !== 'draft'">
          <AppButton variant="secondary" size="sm" class="max-md:h-11" data-testid="download-manifest" @click="downloadManifest">
            <ArrowDownTrayIcon class="h-4 w-4" aria-hidden="true" />
            {{ t('bundles.action.manifest') }}
          </AppButton>
          <AppButton v-if="bundle.gpg_signature" variant="secondary" size="sm" class="max-md:h-11" data-testid="download-signature" @click="downloadSignature">
            <ArrowDownTrayIcon class="h-4 w-4" aria-hidden="true" />
            {{ t('bundles.action.signature') }}
          </AppButton>
        </template>
        <span v-else class="text-muted">{{ t('bundles.verify.manifestAtSeal') }}</span>
      </dd>
    </dl>
  </section>
</template>

<script setup lang="ts">
import { ArrowDownTrayIcon } from '@heroicons/vue/24/outline'
import AppButton from '../AppButton.vue'
import CopyButton from '../ui/CopyButton.vue'
import RelativeTime from '../ui/RelativeTime.vue'
import type { Bundle } from '../../api/bundles'
import { useBundleDownloads } from '../../composables/useBundleDownloads'
import { t } from '../../i18n'

const props = defineProps<{ bundle: Bundle; name: string; version: string }>()

const { downloadManifest, downloadSignature } = useBundleDownloads(
  () => props.name,
  () => props.version,
)
</script>
