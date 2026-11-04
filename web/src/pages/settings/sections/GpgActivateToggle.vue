<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <label class="flex items-center gap-2 text-sm text-foreground">
    <input v-model="activate" type="checkbox" class="rounded border-border-strong text-primary focus:ring-primary/50" />
    {{ t('adm.gpg.makeActive') }}
  </label>
</template>

<script setup lang="ts">
import { t } from '../../../i18n'

const activate = defineModel<boolean>({ required: true })
</script>
