<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <svg v-if="glyph.kind === 'brand'" :viewBox="glyph.viewBox" :class="SIZES[size]" fill="currentColor" focusable="false" aria-hidden="true">
    <path :d="glyph.path" />
  </svg>
  <component :is="glyph.component" v-else :class="SIZES[size]" aria-hidden="true" />
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ssoIcon } from '../lib/brandIcons'

// The one place an SSO icon key becomes markup: the login page, the provider card and the icon picker.
// It paints in currentColor, so the colour comes from the surrounding text colour and follows the theme.
const props = withDefaults(defineProps<{ icon: string; size?: 'sm' | 'md' | 'lg' }>(), { size: 'md' })

const SIZES = { sm: 'w-4 h-4', md: 'w-5 h-5', lg: 'w-6 h-6' } as const

const glyph = computed(() => ssoIcon(props.icon))
</script>
