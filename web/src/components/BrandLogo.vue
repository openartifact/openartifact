<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The instance logo. Which artwork shows:
  - a custom logo from the branding settings (the light/dark pair switches with
    the `.dark` class, so there is no flash);
  - else, while the product still carries the default name, the built-in
    OpenArtifact lockup;
  - else (renamed, no logo) the built-in mark next to the product name as text,
    so a renamed instance never shows the OpenArtifact wordmark as its own name.
  The `mark` variant of a custom setup uses the favicon when one is set, else the
  custom logo. Sizes never go below the brand minimums (lib/branding.ts); the
  lockup SVGs carry their own clear space. Attribution is not part of the logo.
-->
<template>
  <span
    class="inline-flex max-w-full shrink-0 items-center"
    :class="rootClass"
    :data-brand-logo="source"
    data-testid="brand-logo"
  >
    <template v-if="custom">
      <img v-if="custom.light === custom.dark" :src="custom.light" :alt="name" :style="imgStyle" class="object-contain" />
      <template v-else>
        <img :src="custom.light" :alt="name" :style="imgStyle" class="object-contain dark:hidden" />
        <img :src="custom.dark" :alt="name" :style="imgStyle" class="hidden object-contain dark:block" />
      </template>
    </template>

    <template v-else-if="showLockup">
      <img :src="lockup.light" :alt="name" :style="imgStyle" class="h-auto dark:hidden" />
      <img :src="lockup.dark" :alt="name" :style="imgStyle" class="hidden h-auto dark:block" />
    </template>

    <template v-else>
      <span class="inline-flex shrink-0" :style="{ width: markPx + 'px', height: markPx + 'px' }">
        <img :src="markUrl" alt="" aria-hidden="true" class="h-full w-full dark:hidden" />
        <img :src="markDarkUrl" alt="" aria-hidden="true" class="hidden h-full w-full dark:block" />
      </span>
      <span v-if="variant !== 'mark'" class="min-w-0 truncate text-lead font-semibold tracking-tight text-foreground">{{ name }}</span>
      <span v-else class="sr-only">{{ name }}</span>
    </template>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { storeToRefs } from 'pinia'
import { useBrandingStore } from '../stores/branding'
import { isDefaultName, logoSize, type LogoVariant } from '../lib/branding'
import horizontalUrl from '../assets/brand/lockup-horizontal.svg'
import horizontalDarkUrl from '../assets/brand/lockup-horizontal-dark.svg'
import stackedUrl from '../assets/brand/lockup-stacked.svg'
import stackedDarkUrl from '../assets/brand/lockup-stacked-dark.svg'
import markUrl from '../assets/brand/mark.svg'
import markDarkUrl from '../assets/brand/mark-dark.svg'

const props = withDefaults(
  defineProps<{
    variant?: LogoVariant
    /** Width in px (height for the mark); clamped to the brand minimum of the variant. */
    size?: number
  }>(),
  { variant: 'horizontal', size: undefined },
)

/** Height of a custom horizontal logo: fits the 56px sidebar header with padding. */
const HORIZONTAL_LOGO_HEIGHT_PX = 36

const store = useBrandingStore()
const { productName: name } = storeToRefs(store)

const px = computed(() => logoSize(props.variant, props.size))
const markPx = computed(() => logoSize('mark', props.variant === 'mark' ? props.size : Math.round(px.value / 4.5)))

/** The custom artwork for this variant, light and dark; null selects the built-in one. */
const custom = computed(() => {
  const light = store.logoFor('light')
  const dark = store.logoFor('dark')
  if (!light && !dark) return null
  if (props.variant === 'mark' && store.faviconUrl) return { light: store.faviconUrl, dark: store.faviconUrl }
  return { light: light || dark, dark: dark || light }
})

const showLockup = computed(() => !custom.value && props.variant !== 'mark' && isDefaultName(name.value))

const lockup = computed(() =>
  props.variant === 'stacked'
    ? { light: stackedUrl, dark: stackedDarkUrl }
    : { light: horizontalUrl, dark: horizontalDarkUrl },
)

const source = computed(() => (custom.value ? 'custom' : showLockup.value ? 'lockup' : 'mark-name'))

// Mark next to the name: beside it for the horizontal variant, above it for the stacked one.
const rootClass = computed(() => {
  if (source.value !== 'mark-name') return ''
  return props.variant === 'stacked' ? 'flex-col gap-2 text-center' : props.variant === 'horizontal' ? 'gap-2.5' : ''
})

// A custom logo has an unknown aspect ratio: the horizontal one is fitted to a
// fixed height (the sidebar header is only so tall) and keeps its own width, the
// stacked one to a width with a height cap. Built-in lockups have known ratios.
const imgStyle = computed(() => {
  if (props.variant === 'mark') return { width: `${px.value}px`, height: `${px.value}px` }
  if (custom.value && props.variant === 'horizontal') return { height: `${HORIZONTAL_LOGO_HEIGHT_PX}px`, width: 'auto', maxWidth: '100%' }
  return { width: `${px.value}px`, maxWidth: '100%', maxHeight: `${Math.round(px.value / (props.variant === 'stacked' ? 1.2 : 2))}px` }
})
</script>
