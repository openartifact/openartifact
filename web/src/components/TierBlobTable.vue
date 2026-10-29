<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Table of blobs a tier move touches (dry-run sample, mover queue): the digest,
  size and route columns, then whatever columns the caller adds through the
  `head` and `cells` slots.
-->
<template>
  <TableScroller>
    <table class="w-full text-sm" v-bind="$attrs">
      <thead>
        <tr class="text-left text-caption font-semibold text-muted">
          <th scope="col" class="py-1.5 pr-3">{{ t('tiering.dryRun.sha') }}</th>
          <th scope="col" class="py-1.5 px-3 text-right">{{ t('tiering.dryRun.size') }}</th>
          <th scope="col" class="py-1.5 px-3">{{ t('tiering.dryRun.route') }}</th>
          <slot name="head" />
        </tr>
      </thead>
      <tbody class="divide-y divide-border">
        <tr v-for="row in rows" :key="row.sha256" class="group/row" :data-testid="rowTestid">
          <td class="py-1.5 pr-3"><ShortSha :sha="row.sha256 ?? ''" /></td>
          <td class="py-1.5 px-3 text-right tabular-nums">{{ formatBytes(row.size_bytes ?? 0) }}</td>
          <td class="py-1.5 px-3 font-mono text-xs">{{ row.from }} → {{ row.to }}</td>
          <slot name="cells" :row="row" />
        </tr>
      </tbody>
    </table>
  </TableScroller>
</template>

<script lang="ts">
export interface BlobRow {
  sha256?: string
  size_bytes?: number
  from?: string
  to?: string
}
</script>

<script setup lang="ts" generic="T extends BlobRow">
import ShortSha from './ShortSha.vue'
import TableScroller from './ui/TableScroller.vue'
import { t } from '../i18n'
import { formatBytes } from '../lib/quota'

defineOptions({ inheritAttrs: false })

defineProps<{ rows: T[]; rowTestid?: string }>()

defineSlots<{ head?: () => unknown; cells?: (props: { row: T }) => unknown }>()
</script>
