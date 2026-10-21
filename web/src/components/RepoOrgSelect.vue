<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <label :for="id" class="block text-sm font-medium text-foreground mb-2">{{ t('repos.org.label') }}</label>
    <select
      :id="id"
      v-model="model"
      class="w-full px-3 py-2 border border-border rounded-md text-sm font-mono bg-bg text-foreground focus:outline-none focus:ring-2 focus:ring-primary"
    >
      <option value="">{{ t('repos.org.default') }}</option>
      <option v-for="name in names" :key="name" :value="name">{{ name }}</option>
    </select>
    <p class="text-xs mt-1 text-muted">{{ t('repos.org.hint') }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { listOrganizations, SYSTEM_ORG_NAME } from '../api/orgs'
import type { Organization } from '../api/orgs'
import { t } from '../i18n/en'

defineProps<{ id: string }>()

// '' selects the built-in system organization (the default).
const model = defineModel<string>({ default: '' })
const orgs = ref<Organization[]>([])

// The repository's current organization stays selectable even when the caller
// is not a member of it (the list only holds the caller's own organizations).
const names = computed(() => {
  const list = orgs.value.map((o) => o.name ?? '').filter(Boolean)
  return model.value && !list.includes(model.value) ? [model.value, ...list] : list
})

onMounted(async () => {
  try {
    orgs.value = (await listOrganizations()).filter((o) => o.name !== SYSTEM_ORG_NAME)
  } catch {
    orgs.value = []
  }
})
</script>
