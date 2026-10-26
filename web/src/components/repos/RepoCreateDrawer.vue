<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The one way to create a repository: a name, a format and the "Defaults applied" rows
  (the same fields component as the repository Settings tab). Everything else keeps its default.
-->
<template>
  <Drawer :open="open" :title="t('rl.create.title')" :description="t('rl.create.description')" :busy="submitting" @close="emit('close')">
    <form id="repo-create-form" class="space-y-5" novalidate @submit.prevent="submit">
      <FormField :label="t('wizard.name')" id="rl-name" :error="nameErr" :hint="t('repos.new.nameHint')">
        <input
          id="rl-name"
          v-model="form.name"
          type="text"
          autocomplete="off"
          spellcheck="false"
          required
          data-autofocus
          :aria-invalid="nameErr !== ''"
          :aria-describedby="nameErr ? 'rl-name-error' : undefined"
          :class="[INPUT_CLASS, 'font-mono']"
        />
      </FormField>

      <fieldset class="min-w-0 space-y-2">
        <legend class="text-sm font-medium text-foreground">{{ t('rl.create.format') }}</legend>
        <FormatTiles v-model="form.format" :formats="formats" :label="t('rl.create.format')" />
      </fieldset>

      <div>
        <h3 class="text-sm font-semibold text-foreground">{{ t('rl.create.defaults') }}</h3>
        <p class="mb-2 text-xs text-muted">{{ t('rl.create.defaultsHint') }}</p>
        <RepoDefaultsRows v-model="form" mode="create" :is-admin="session.isAdmin" :policy="policy" :candidates="candidates" @project="selectedProject = $event" />
      </div>

      <p v-if="failure" role="alert" class="text-sm text-danger">{{ failure }}</p>
    </form>

    <template #footer>
      <AppButton variant="secondary" :disabled="submitting" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton type="submit" form="repo-create-form" :disabled="!valid" :loading="submitting">
        {{ submitting ? t('repos.new.creating') : t('repos.create') }}
      </AppButton>
    </template>
  </Drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppButton from '../AppButton.vue'
import FormField from '../FormField.vue'
import Drawer from '../ui/Drawer.vue'
import FormatTiles from './FormatTiles.vue'
import RepoDefaultsRows from './RepoDefaultsRows.vue'
import { createRepository, listAllRepositories } from '../../api/repositories'
import { createProjectRepository } from '../../api/projects'
import { setRepoQuota } from '../../api/quota'
import type { Project } from '../../api/projects'
import { INPUT_CLASS } from '../../lib/formClasses'
import { policyAllowsPublic, policyAllowsType, policyFormats, policyPrefix } from '../../lib/projectPolicy'
import { buildCreateBody, emptyRepoForm, formValid, nameError, quotaBytesOf } from '../../lib/repoForm'
import { allowedFormats, coerceFormat, upstreamUrlRequired } from '../../lib/repoTypes'
import { useSessionStore } from '../../stores/session'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'

const props = defineProps<{
  open: boolean
  /** Project to preselect (`?project=` of the list). */
  project?: string
}>()
const emit = defineEmits<{ close: []; created: [name: string] }>()

const session = useSessionStore()
const toasts = useToastStore()

const form = ref(emptyRepoForm({ project: props.project ?? '' }))
const submitting = ref(false)
const failure = ref('')
const existing = ref<{ name: string; format: string; repo_type?: string }[]>([])
const selectedProject = ref<Project | null>(null)

// The project policy narrows what a non-admin can choose; admins bypass it.
const policy = computed(() => (session.isAdmin ? null : selectedProject.value))
const prefix = computed(() => policyPrefix(policy.value))
const formats = computed(() => policyFormats(policy.value, allowedFormats(form.value.type)))
const nameErr = computed(() => nameError(form.value.name, prefix.value))
const candidates = computed(() =>
  existing.value.filter((r) => r.format === form.value.format && r.repo_type !== 'group').map((r) => r.name),
)
const valid = computed(() => formValid(form.value, prefix.value, upstreamUrlRequired(form.value.format)))

// A fresh form every time the drawer opens.
watch(
  () => props.open,
  async (open) => {
    if (!open) return
    form.value = emptyRepoForm({ project: props.project ?? '' })
    failure.value = ''
    try {
      existing.value = await listAllRepositories()
    } catch {
      existing.value = []
    }
  },
  { immediate: true },
)

watch(
  () => form.value.format,
  () => {
    form.value.members = []
  },
)

// Proxy and group repositories only exist for some formats.
watch(
  () => form.value.type,
  (next) => {
    form.value.format = coerceFormat(next, form.value.format)
  },
)

// A project change can rule out the chosen format, type or visibility.
watch(policy, () => {
  if (!policyAllowsType(policy.value, form.value.type)) {
    form.value.type = (['hosted', 'proxy', 'group'] as const).find((ty) => policyAllowsType(policy.value, ty)) ?? 'hosted'
  }
  if (!formats.value.includes(form.value.format) && formats.value.length > 0) form.value.format = formats.value[0]
  if (!policyAllowsPublic(policy.value)) form.value.isPublic = false
})

async function submit(): Promise<void> {
  if (submitting.value || !valid.value) return
  submitting.value = true
  failure.value = ''
  const body = buildCreateBody(form.value, { isAdmin: session.isAdmin })
  try {
    const create = form.value.project && !session.isAdmin
      ? (b: typeof body) => createProjectRepository(form.value.project, b)
      : createRepository
    const created = await create(body)
    const name = created.name ?? form.value.name.trim()
    const quota = quotaBytesOf(form.value)
    if (quota > 0) {
      // The repository exists at this point: a quota that cannot be set is reported, not rolled back.
      try {
        await setRepoQuota(name, quota)
      } catch (err) {
        toasts.error(t('quota.saveFailed'), err instanceof Error ? err.message : undefined)
      }
    }
    toasts.success(t('repos.new.created'))
    emit('created', name)
  } catch (err) {
    failure.value = err instanceof Error ? err.message : t('repos.new.failed')
  } finally {
    submitting.value = false
  }
}
</script>
