<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Role picker for project members and groups. Roles the caller may not grant
  stay listed but disabled, so the escalation rules are visible rather than
  silently hiding options. The current value is never disabled.
-->
<template>
  <select
    :value="modelValue"
    :disabled="disabled"
    :aria-label="label"
    class="px-2.5 py-1.5 rounded-md border border-border-strong bg-bg text-foreground text-xs focus:ring-2 focus:ring-primary disabled:opacity-60 disabled:cursor-not-allowed"
    @change="emit('update:modelValue', ($event.target as HTMLSelectElement).value)"
  >
    <option
      v-for="role in roles"
      :key="role"
      :value="role"
      :disabled="role !== modelValue && !granted.has(role)"
      :title="role !== modelValue && !granted.has(role) ? t('projects.role.ownerOnly') : undefined"
    >{{ tDynamic(`projects.role.${role}`) }}</option>
  </select>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { t, tDynamic } from '../i18n'

const props = defineProps<{
  modelValue: string
  /** Every role the picker lists, in display order. */
  roles: readonly string[]
  /** Roles the caller may pick; the rest are disabled. Defaults to all. */
  grantable?: readonly string[]
  disabled?: boolean
  /** Accessible name. */
  label: string
}>()

const emit = defineEmits<{ (e: 'update:modelValue', value: string): void }>()

const granted = computed(() => new Set(props.grantable ?? props.roles))
</script>
