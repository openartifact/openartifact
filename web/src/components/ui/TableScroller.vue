<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The scroll container of every table. It drops the columns whose header carries
  `data-drop-order` when the table does not fit (see `useColumnFit`), and it tells the
  pinned actions column (`ACTIONS_CELL` / `ACTIONS_HEAD`) when to draw its border through
  `data-stuck`. `fitFrom` is the viewport width below which the table is not a table
  (stacked cards) and nothing is dropped.
-->
<template>
  <div ref="scroller" class="group/ts overflow-x-auto" :data-stuck="stuck || undefined">
    <slot />
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useColumnFit } from '../../composables/useColumnFit'
import { useScrollEdge } from '../../composables/useScrollEdge'

const props = withDefaults(defineProps<{ fitFrom?: number }>(), { fitFrom: 0 })

const scroller = ref<HTMLElement | null>(null)
const stuck = useScrollEdge(scroller)
useColumnFit(scroller, () => props.fitFrom <= 0 || window.matchMedia(`(min-width: ${props.fitFrom}px)`).matches)
</script>
