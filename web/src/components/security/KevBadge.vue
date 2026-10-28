<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  "In the CISA Known Exploited Vulnerabilities catalog". The tooltip says when the
  vulnerability was added, the remediation deadline CISA set and whether it is known to
  be used in ransomware campaigns; `count` is for a finding that sums several of them.
-->
<template>
  <span
    class="inline-flex items-center gap-1 whitespace-nowrap rounded bg-danger px-1.5 py-0.5 text-xs font-semibold text-danger-contrast"
    :title="title"
    data-testid="kev-badge"
  >
    <FireIcon class="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
    {{ count !== undefined && count > 1 ? tp('security.exploit.kev.badgeCount', { count: String(count) }) : 'KEV' }}
    <span v-if="ransomware" class="sr-only">{{ t('security.exploit.kev.ransomware') }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { FireIcon } from '@heroicons/vue/20/solid'
import { formatDate } from '../../lib/datetime'
import { t, tp, tpn } from '../../i18n'

const props = defineProps<{
  /** How many vulnerabilities of a finding are in the catalog; omit for one vulnerability. */
  count?: number
  /** Day CISA added the CVE (YYYY-MM-DD). */
  dateAdded?: string
  /** Remediation deadline CISA set (YYYY-MM-DD). */
  dueDate?: string
  /** CISA knows the CVE to be used in ransomware campaigns. */
  ransomware?: boolean
}>()

const title = computed(() => {
  const lines = [props.count !== undefined && props.count > 0 ? tpn('security.exploit.kev.count', props.count) : t('security.exploit.kev.title')]
  if (props.dateAdded) lines.push(tp('security.exploit.kev.added', { date: formatDate(props.dateAdded) }))
  if (props.dueDate) lines.push(tp('security.exploit.kev.due', { date: formatDate(props.dueDate) }))
  if (props.ransomware) lines.push(t('security.exploit.kev.ransomware'))
  return lines.join('\n')
})
</script>
