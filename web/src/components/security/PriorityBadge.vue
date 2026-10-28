<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Remediation priority: `kev` and `epss` come from exploitation signals and get their
  own strong colours; the severity priorities are the severity badge.
-->
<template>
  <span
    v-if="priority === 'kev'"
    class="inline-flex items-center gap-1 whitespace-nowrap rounded bg-danger px-1.5 py-0.5 text-xs font-semibold text-danger-contrast"
    :title="t('security.exploit.kev.title')"
    data-priority="kev"
  >
    <FireIcon class="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
    {{ t('security.exploit.priority.kev') }}
  </span>
  <span
    v-else-if="priority === 'epss'"
    class="inline-flex items-center gap-1 whitespace-nowrap rounded bg-primary-soft px-1.5 py-0.5 text-xs font-medium text-primary"
    :title="t('security.exploit.epss.title')"
    data-priority="epss"
  >
    {{ t('security.exploit.priority.epss') }}
  </span>
  <SeverityBadge v-else :severity="priority" :data-priority="priority" />
</template>

<script setup lang="ts">
import { FireIcon } from '@heroicons/vue/20/solid'
import type { ExploitPriority } from '../../api/security'
import SeverityBadge from '../ui/SeverityBadge.vue'
import { t } from '../../i18n'

defineProps<{ priority: ExploitPriority }>()
</script>
