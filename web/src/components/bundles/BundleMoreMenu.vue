<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- The secondary actions of a bundle (downloads, archive, delete) behind one menu button, so the page keeps a single primary action. -->
<template>
  <Menu as="div" class="relative">
    <MenuButton
      class="inline-flex h-9 items-center gap-1.5 rounded-lg border border-border px-3 text-sm font-medium text-foreground hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:h-11"
      data-testid="bundle-more"
    >
      <EllipsisHorizontalIcon class="h-4 w-4" aria-hidden="true" />
      {{ t('bundles.more') }}
    </MenuButton>
    <transition
      enter-active-class="motion-safe:transition motion-safe:duration-100 motion-safe:ease-out"
      enter-from-class="motion-safe:opacity-0 motion-safe:scale-95"
      enter-to-class="motion-safe:opacity-100 motion-safe:scale-100"
      leave-active-class="motion-safe:transition motion-safe:duration-75 motion-safe:ease-in"
      leave-from-class="motion-safe:opacity-100 motion-safe:scale-100"
      leave-to-class="motion-safe:opacity-0 motion-safe:scale-95"
    >
      <MenuItems class="absolute right-0 z-40 mt-2 w-56 origin-top-right rounded-md border border-border bg-surface py-1 shadow-lg focus:outline-none">
        <MenuItem v-for="item in items" :key="item.id" v-slot="{ active }">
          <button
            type="button"
            class="flex min-h-9 w-full items-center px-3 py-2 text-left text-sm max-md:min-h-11"
            :class="[item.danger ? 'text-danger' : 'text-foreground', active ? (item.danger ? 'bg-danger-soft' : 'bg-surface-hover') : '']"
            :data-testid="`bundle-action-${item.id}`"
            @click="emit('select', item.id)"
          >
            {{ item.label }}
          </button>
        </MenuItem>
      </MenuItems>
    </transition>
  </Menu>
</template>

<script setup lang="ts">
import { Menu, MenuButton, MenuItem, MenuItems } from '@headlessui/vue'
import { EllipsisHorizontalIcon } from '@heroicons/vue/24/outline'
import { t } from '../../i18n/en'

export interface MoreMenuItem {
  id: string
  label: string
  danger?: boolean
}

defineProps<{ items: MoreMenuItem[] }>()
const emit = defineEmits<{ select: [id: string] }>()
</script>
