<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- A row under the header of the demo build only: says the data is sample data and points at the real install. -->
<template>
  <div class="shrink-0 border-b border-border bg-primary-soft px-4 py-2 text-sm md:px-8" data-testid="demo-banner">
    <div class="mx-auto flex max-w-[1400px] flex-wrap items-center gap-x-3 gap-y-1">
      <span class="text-foreground">{{ t('shell.demo.banner') }}</span>
      <a :href="INSTALL_URL" target="_blank" rel="noopener" class="font-medium text-primary underline-offset-2 hover:underline">{{ t('shell.demo.install') }}</a>
    </div>
  </div>
</template>

<script setup lang="ts">
import { t } from '../i18n'

const INSTALL_URL = 'https://openartifact.github.io/latest/getting-started/installation'
</script>
