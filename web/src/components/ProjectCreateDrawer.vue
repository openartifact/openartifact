<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Quick create: name, display name and description. Policy and quota are
  administrator settings and live on the project's Settings tab.
-->
<template>
  <Drawer :open="open" :title="t('projects.create.title')" :eyebrow="t('nav.projects')" :busy="creating" @close="emit('close')">
    <form id="project-create-form" class="space-y-4" @submit.prevent="create">
      <FormField :label="t('projects.create.name')" id="new-project-name" :hint="t('projects.create.nameHint')">
        <input
          id="new-project-name"
          v-model="form.name"
          type="text"
          required
          autocomplete="off"
          placeholder="my-project"
          :class="[INPUT_CLASS, 'font-mono']"
        />
      </FormField>
      <FormField :label="t('projects.create.displayName')" id="new-project-display-name">
        <input id="new-project-display-name" v-model="form.display_name" type="text" :class="INPUT_CLASS" />
      </FormField>
      <FormField :label="t('projects.create.description')" id="new-project-description">
        <textarea id="new-project-description" v-model="form.description" rows="3" :class="INPUT_CLASS" />
      </FormField>
      <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    </form>
    <template #footer>
      <AppButton variant="secondary" size="sm" :disabled="creating" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton variant="primary" size="sm" type="submit" form="project-create-form" :loading="creating" :disabled="!form.name.trim()">
        {{ t('projects.create.action') }}
      </AppButton>
    </template>
  </Drawer>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import AppButton from './AppButton.vue'
import FormField from './FormField.vue'
import Drawer from './ui/Drawer.vue'
import { createProject } from '../api/projects'
import { INPUT_CLASS } from '../lib/formClasses'
import { errorStatus } from '../lib/projectAccess'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{
  close: []
  /** The project exists; the page opens it. */
  created: [name: string]
}>()

const toasts = useToastStore()
const creating = ref(false)
const error = ref('')
const form = ref({ name: '', display_name: '', description: '' })

// Every opening starts from an empty form.
watch(
  () => props.open,
  (open) => {
    if (!open) return
    form.value = { name: '', display_name: '', description: '' }
    error.value = ''
  },
)

async function create(): Promise<void> {
  const name = form.value.name.trim()
  if (!name || creating.value) return
  creating.value = true
  error.value = ''
  try {
    // Only the fields a non-admin may send: policy stays with global admins.
    await createProject({
      name,
      display_name: form.value.display_name.trim() || undefined,
      description: form.value.description.trim() || undefined,
    })
    toasts.success(t('projects.create.done'))
    emit('created', name)
  } catch (err) {
    const status = errorStatus(err)
    error.value =
      status === 409 ? t('projects.create.conflict')
      : status === 403 ? t('projects.create.forbidden')
      : t('projects.create.failed')
  } finally {
    creating.value = false
  }
}
</script>
