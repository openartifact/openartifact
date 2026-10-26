<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Language picker: the browser default or one of the translated languages, each
  by its own name (never translated). The choice applies at once and is saved in
  this browser only. Used in the user menu and on the login page.
-->
<template>
  <div>
    <label :for="id" :class="labelClass">{{ t('shell.menu.language') }}</label>
    <select
      :id="id"
      :value="choice"
      class="w-full rounded-md border border-border bg-surface px-2 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary min-h-11 md:min-h-8"
      data-testid="language-select"
      @change="onChange"
    >
      <option value="auto">{{ t('shell.menu.languageAuto') }}</option>
      <option v-for="l in languages" :key="l.tag" :value="l.tag" :lang="l.tag">{{ l.name }}</option>
    </select>
  </div>
</template>

<script setup lang="ts">
import { nextTick } from 'vue'
import { useLanguage, type LanguageChoice } from '../composables/useLanguage'
import { t } from '../i18n'

const props = withDefaults(
  defineProps<{
    id?: string
    labelClass?: string
    /** Element that takes focus after the switch (the app re-renders in the new language, which drops the focus). */
    returnFocusTo?: string
  }>(),
  { id: 'language-select', labelClass: 'mb-1.5 block text-xs text-muted', returnFocusTo: '' },
)

const { choice, languages, choose } = useLanguage()

async function onChange(event: Event): Promise<void> {
  const select = event.target as HTMLSelectElement
  const ok = await choose(select.value as LanguageChoice)
  // A failed load keeps the old language: show the old choice again.
  if (!ok) select.value = choice.value
  await nextTick()
  document.getElementById(props.returnFocusTo || props.id)?.focus()
}
</script>
