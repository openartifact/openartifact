<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <Menu as="div" class="relative">
    <MenuButton
      class="inline-flex items-center justify-center w-9 h-9 rounded-md text-foreground hover:bg-surface-hover focus:outline-none focus-visible:ring-2 focus-visible:ring-primary"
      :aria-label="switchLabel"
      :title="switchLabel"
    >
      <MoonIcon v-if="theme.resolved === 'light'" class="w-5 h-5" aria-hidden="true" />
      <SunIcon v-else class="w-5 h-5" aria-hidden="true" />
    </MenuButton>
    <transition
      enter-active-class="motion-safe:transition motion-safe:duration-100 motion-safe:ease-out"
      enter-from-class="motion-safe:transform motion-safe:opacity-0 motion-safe:scale-95"
      enter-to-class="motion-safe:transform motion-safe:opacity-100 motion-safe:scale-100"
      leave-active-class="motion-safe:transition motion-safe:duration-75 motion-safe:ease-in"
      leave-from-class="motion-safe:transform motion-safe:opacity-100 motion-safe:scale-100"
      leave-to-class="motion-safe:transform motion-safe:opacity-0 motion-safe:scale-95"
    >
      <MenuItems
        class="absolute right-0 mt-2 w-40 origin-top-right rounded-md bg-surface border border-border shadow-lg ring-1 ring-foreground/5 focus:outline-none z-40"
      >
        <div class="py-1">
          <MenuItem v-slot="{ active }">
            <button
              type="button"
              class="flex w-full items-center gap-2 px-3 py-1.5 text-sm"
              :class="[
                active ? 'bg-surface-hover' : '',
                theme.mode === 'light' ? 'text-primary font-medium' : 'text-foreground',
              ]"
              @click="theme.setMode('light')"
            >
              <SunIcon class="w-4 h-4" aria-hidden="true" />
              {{ t('theme.light') }}
            </button>
          </MenuItem>
          <MenuItem v-slot="{ active }">
            <button
              type="button"
              class="flex w-full items-center gap-2 px-3 py-1.5 text-sm"
              :class="[
                active ? 'bg-surface-hover' : '',
                theme.mode === 'dark' ? 'text-primary font-medium' : 'text-foreground',
              ]"
              @click="theme.setMode('dark')"
            >
              <MoonIcon class="w-4 h-4" aria-hidden="true" />
              {{ t('theme.dark') }}
            </button>
          </MenuItem>
          <MenuItem v-slot="{ active }">
            <button
              type="button"
              class="flex w-full items-center gap-2 px-3 py-1.5 text-sm"
              :class="[
                active ? 'bg-surface-hover' : '',
                theme.mode === 'auto' ? 'text-primary font-medium' : 'text-foreground',
              ]"
              @click="theme.setMode('auto')"
            >
              <ComputerDesktopIcon class="w-4 h-4" aria-hidden="true" />
              {{ t('theme.auto') }}
            </button>
          </MenuItem>
        </div>
      </MenuItems>
    </transition>
  </Menu>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Menu, MenuButton, MenuItems, MenuItem } from '@headlessui/vue'
import { SunIcon, MoonIcon, ComputerDesktopIcon } from '@heroicons/vue/24/outline'
import { useThemeStore } from '../stores/theme'
import { t } from '../i18n/en'

const theme = useThemeStore()
const switchLabel = computed(() =>
  theme.resolved === 'light' ? t('palette.command.theme.dark') : t('palette.command.theme.light'),
)
</script>
