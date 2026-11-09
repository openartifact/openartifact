<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Where a VEX decision comes from: an administrator, a vendor CSAF feed (linked to its document) or a federated peer. -->
<template>
  <span class="inline-flex flex-wrap items-center gap-1 text-xs" :data-vex-source="kind" data-testid="vex-source">
    <template v-if="kind === 'local'">
      <span class="text-muted">{{ t('security.vex.source.local') }}</span>
    </template>
    <template v-else>
      <span class="text-muted">{{ kind === 'csaf' ? t('security.vex.source.csaf') : t('security.vex.source.peer') }}</span>
      <a
        v-if="href"
        :href="href"
        target="_blank"
        rel="noopener noreferrer"
        class="inline-flex items-center gap-0.5 text-primary hover:underline break-all"
        data-testid="vex-source-link"
      >
        {{ name }}
        <ArrowTopRightOnSquareIcon class="w-3 h-3 shrink-0" aria-hidden="true" />
        <span class="sr-only">{{ t('security.vex.source.newTab') }}</span>
      </a>
      <span v-else class="text-foreground break-all" :title="record.source_ref || undefined">{{ name }}</span>
    </template>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ArrowTopRightOnSquareIcon } from '@heroicons/vue/20/solid'
import type { VexRecord } from '../api/vex'
import { vexSourceHref } from '../lib/vex'
import { t } from '../i18n'

const props = defineProps<{ record: Pick<VexRecord, 'source_kind' | 'source_name' | 'source_ref'> }>()

const kind = computed(() => props.record.source_kind ?? 'local')
const name = computed(() => props.record.source_name || props.record.source_ref || '—')
/** A CSAF decision links to the vendor document it was read from. */
const href = computed(() => (kind.value === 'csaf' ? vexSourceHref(props.record.source_ref) : null))
</script>
