<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <EmptyState
    :title="t('shell.notFoundTitle')"
    :description="t('shell.notFoundBody')"
    :action-label="t('shell.backToDashboard')"
    action-to="/"
  />
</template>

<script setup lang="ts">
import EmptyState from '../components/EmptyState.vue'
import { t } from '../i18n/en'
</script>
