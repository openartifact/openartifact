<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <Drawer :open="open" :title="t('tok.create.title')" :description="t('tok.create.desc')" :busy="busy" @close="emit('close')">
    <form id="token-create-form" class="flex flex-col gap-5" novalidate @submit.prevent="submit">
      <fieldset class="flex flex-col gap-2" data-testid="token-type-picker">
        <legend class="mb-1 text-sm font-medium text-foreground">{{ t('tok.create.type') }}</legend>
        <label
          v-for="o in types"
          :key="o"
          class="flex min-h-11 cursor-pointer items-start gap-3 rounded-lg border p-3"
          :class="type === o ? 'border-primary bg-primary/5' : 'border-border hover:bg-surface-hover'"
        >
          <input v-model="type" type="radio" name="token-type" :value="o" class="mt-1" :data-testid="`type-${o}`" />
          <span>
            <span class="block text-sm font-medium text-foreground">{{ tDynamic(`tok.type.${o}`) }}</span>
            <span class="block text-xs text-muted">{{ tDynamic(`tok.create.typeHint.${o}`) }}</span>
          </span>
        </label>
      </fieldset>

      <FormField :label="t('tok.create.name')" id="token-name" :error="nameError" :hint="t('tok.create.nameHint')">
        <input
          id="token-name"
          v-model="name"
          type="text"
          autocomplete="off"
          :class="INPUT_CLASS"
          :aria-invalid="nameError ? 'true' : undefined"
          :aria-describedby="nameError ? 'token-name-error' : undefined"
        />
      </FormField>

      <template v-if="type === 'personal'">
        <FormField :label="t('tok.create.kind')" id="token-kind" :hint="tDynamic(`tok.kind.${kind}.hint`)">
          <select id="token-kind" v-model="kind" :class="INPUT_CLASS">
            <option value="access">{{ t('tok.kind.access') }}</option>
            <option value="deploy">{{ t('tok.kind.deploy') }}</option>
          </select>
        </FormField>
        <fieldset v-if="targets.permissions.value.length > 0" class="flex flex-col gap-1" data-testid="token-scopes">
          <legend class="text-sm font-medium text-foreground">{{ t('tok.create.scopes') }}</legend>
          <p class="text-xs text-muted">{{ t('tok.create.scopesHint') }}</p>
          <label
            v-for="p in targets.permissions.value"
            :key="p.id"
            class="flex min-h-9 items-center gap-2 text-sm text-foreground max-md:min-h-11"
          >
            <input v-model="scopes" type="checkbox" :value="p.id" />
            <span class="font-mono">{{ p.name }}</span>
          </label>
        </fieldset>
      </template>

      <template v-else-if="type === 'project'">
        <FormField :label="t('tok.create.project')" id="token-project" :error="noProject ? t('tok.create.noProject') : undefined">
          <select id="token-project" v-model="project" :class="INPUT_CLASS">
            <option v-for="p in targets.projects.value" :key="p.name" :value="p.name">{{ p.name }}</option>
          </select>
        </FormField>
        <FormField :label="t('tok.create.role')" id="token-role" :hint="tDynamic(`tok.role.${role}.hint`)">
          <select id="token-role" v-model="role" :class="INPUT_CLASS">
            <option v-for="r in roles" :key="r" :value="r">{{ tDynamic(`tok.role.${r}`) }}</option>
          </select>
        </FormField>
      </template>

      <template v-else>
        <FormField :label="t('tok.create.repository')" id="token-repo" :error="noRepo ? t('tok.create.noRepo') : undefined">
          <select id="token-repo" v-model="repository" :class="INPUT_CLASS">
            <option v-for="r in targets.repositories.value" :key="r" :value="r">{{ r }}</option>
          </select>
        </FormField>
        <p class="text-xs text-muted">{{ t('tok.create.federationNote') }}</p>
      </template>

      <FormField v-if="choices.length > 0" :label="t('tok.create.expires')" id="token-expiry">
        <select id="token-expiry" v-model.number="days" :class="INPUT_CLASS">
          <option v-for="d in choices" :key="d" :value="d">{{ d === 0 ? t('tok.expiry.never') : tp('tok.expiry.days', { count: String(d) }) }}</option>
        </select>
        <p v-if="days === 0" class="text-xs text-warning" data-testid="no-expiry-warning">{{ t('tok.create.noExpiryWarning') }}</p>
        <p v-else-if="cap > 0" class="text-xs text-muted">{{ tp('tok.create.capHint', { count: String(cap) }) }}</p>
      </FormField>

      <p v-if="targets.error.value" role="alert" class="text-sm text-danger">{{ targets.error.value }}</p>
      <p v-if="error" role="alert" class="text-sm text-danger" data-testid="create-error">{{ error }}</p>
    </form>
    <template #footer>
      <AppButton variant="ghost" :disabled="busy" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton type="submit" form="token-create-form" :loading="busy" :disabled="!canSubmit">{{ t('tok.create.submit') }}</AppButton>
    </template>
  </Drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppButton from '../AppButton.vue'
import FormField from '../FormField.vue'
import Drawer from '../ui/Drawer.vue'
import { createMyToken, type TokenType } from '../../api/tokens'
import { createProjectToken } from '../../api/projects'
import { createFederationToken } from '../../api/federation'
import { useTokenTargets } from '../../composables/useTokenTargets'
import { INPUT_CLASS } from '../../lib/formClasses'
import {
  defaultExpiry,
  expiresAtFor,
  expiryChoices,
  grantableTokenRoles,
  nameProblem,
  offeredTypes,
  presetType,
} from '../../lib/tokens'
import { t, tDynamic, tp } from '../../i18n/en'
import { useSessionStore } from '../../stores/session'

/** The one-time result of a creation: the secret is shown once by the page. */
export interface CreatedSecret {
  secret: string
  name: string
  type: TokenType
}

const props = defineProps<{
  open: boolean
  /** Type to start on (`?type=` deep link); ignored when not offered. */
  initialType?: string
  /** Project to start on (`?project=` deep link). */
  initialProject?: string
}>()
const emit = defineEmits<{ close: []; created: [CreatedSecret] }>()

const session = useSessionStore()
const openRef = computed(() => props.open)
const targets = useTokenTargets(openRef, session.isAdmin)

const types = computed(() =>
  offeredTypes({ isAdmin: session.isAdmin, hasProjects: !targets.loaded.value || targets.projects.value.length > 0 }),
)
const type = ref<TokenType>('personal')
const name = ref('')
const touched = ref(false)
const kind = ref<'access' | 'deploy'>('access')
const scopes = ref<string[]>([])
const project = ref('')
const role = ref<'viewer' | 'developer'>('viewer')
const repository = ref('')
const days = ref(defaultExpiry(expiryChoices('personal', 0)))
const busy = ref(false)
const error = ref('')

const selectedProject = computed(() => targets.projects.value.find((p) => p.name === project.value))
const cap = computed(() => (type.value === 'project' ? (selectedProject.value?.token_max_ttl_days ?? 0) : 0))
const choices = computed(() => expiryChoices(type.value, cap.value))
const roles = computed(() => grantableTokenRoles(selectedProject.value?.my_role))
const noProject = computed(() => targets.loaded.value && targets.projects.value.length === 0)
const noRepo = computed(() => targets.loaded.value && targets.repositories.value.length === 0)

const problem = computed(() => nameProblem(type.value, name.value))
const nameError = computed(() => (touched.value && problem.value ? tDynamic(`tok.create.nameError.${problem.value}`) : undefined))
const canSubmit = computed(() => {
  if (busy.value || problem.value) return false
  if (type.value === 'project') return project.value !== ''
  if (type.value === 'federation') return repository.value !== ''
  return true
})

// Fresh form each time the drawer opens, on the deep-linked type and project.
watch(
  () => props.open,
  (isOpen) => {
    if (!isOpen) return
    type.value = presetType(props.initialType, types.value)
    name.value = ''
    touched.value = false
    kind.value = 'access'
    scopes.value = []
    project.value = props.initialProject ?? ''
    repository.value = ''
    error.value = ''
  },
  { immediate: true },
)

// Defaults that need the loaded lists; a deep-linked project the caller cannot mint for is dropped.
watch(
  [() => targets.projects.value, () => targets.repositories.value, type],
  () => {
    const names = targets.projects.value.map((p) => p.name)
    if (!names.includes(project.value)) project.value = names[0] ?? ''
    if (!targets.repositories.value.includes(repository.value)) repository.value = targets.repositories.value[0] ?? ''
    // The deep-linked type only stands once the projects are known.
    if (!types.value.includes(type.value)) type.value = presetType(undefined, types.value)
  },
)

// The lifetime and role stay inside what the type and project allow.
watch(
  [choices, roles],
  () => {
    if (!choices.value.includes(days.value)) days.value = defaultExpiry(choices.value)
    if (!roles.value.includes(role.value)) role.value = roles.value[roles.value.length - 1] ?? 'viewer'
  },
  { immediate: true },
)

async function submit(): Promise<void> {
  touched.value = true
  if (!canSubmit.value) return
  busy.value = true
  error.value = ''
  const trimmed = name.value.trim()
  try {
    let secret: string
    if (type.value === 'personal') {
      const res = await createMyToken({
        name: trimmed,
        kind: kind.value,
        scopes: scopes.value.length > 0 ? scopes.value : undefined,
        expires_at: expiresAtFor(days.value),
      })
      secret = res.token ?? ''
    } else if (type.value === 'project') {
      const res = await createProjectToken(project.value, { name: trimmed, role: role.value, expires_at: expiresAtFor(days.value) })
      secret = res.token ?? ''
    } else {
      const res = await createFederationToken(repository.value, trimmed)
      secret = res.token ?? ''
    }
    emit('created', { secret, name: trimmed, type: type.value })
  } catch (err) {
    error.value = err instanceof Error ? err.message : t('tok.create.failed')
  } finally {
    busy.value = false
  }
}
</script>
