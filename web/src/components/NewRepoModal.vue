<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <AdminDialog :open="open" :title="t('repos.new')" max-width="max-w-xl" @close="close">
    <form id="new-repo-form" class="space-y-4" @submit.prevent="handleCreate">
                <div>
                  <label for="new-repo-name" class="text-caption font-semibold text-muted">{{ t('repos.column.name') }}</label>
                  <input
                    id="new-repo-name"
                    v-model="name"
                    placeholder="e.g. docker-prod"
                    autocomplete="off"
                    class="mt-1.5 w-full h-10 px-3 rounded-md bg-surface-hover/40 border border-border-strong focus:bg-surface focus:border-primary/50 focus:ring-2 focus:ring-primary/20 text-sm font-mono transition-colors"
                  />
                  <p class="mt-1 text-caption" :class="name && !nameValid ? 'text-danger' : 'text-muted'">{{ t('repos.new.nameHint') }}</p>
                </div>
  
                <fieldset>
                  <legend class="text-caption font-semibold text-muted">{{ t('repos.column.format') }}</legend>
                  <div class="mt-1.5 grid grid-cols-4 sm:grid-cols-6 gap-1.5">
                    <button
                      v-for="f in formats"
                      :key="f"
                      type="button"
                      :aria-pressed="format === f"
                      :class="['flex items-center justify-center py-2 rounded-md border transition-all',
                        format === f ? 'border-primary bg-primary-soft' : 'border-border bg-surface hover:bg-surface-hover']"
                      @click="format = f"
                    >
                      <FormatBadge :format="f" size="sm" />
                    </button>
                  </div>
                  <p v-if="type === 'proxy'" class="mt-1.5 text-xs text-muted">{{ t('repos.new.proxyFormatsHint') }}</p>
                  <p v-else-if="type === 'group'" class="mt-1.5 text-xs text-muted">{{ t('repos.new.groupFormatsHint') }}</p>
                </fieldset>
  
                <fieldset>
                  <legend class="text-caption font-semibold text-muted">{{ t('repos.column.type') }}</legend>
                  <div class="mt-1.5 grid grid-cols-3 gap-2">
                    <button
                      v-for="ty in types"
                      :key="ty.value"
                      type="button"
                      :aria-pressed="type === ty.value"
                      :disabled="!policyAllowsType(policy, ty.value)"
                      :title="policyAllowsType(policy, ty.value) ? undefined : t('repos.project.typeBlocked')"
                      :class="['p-3 disabled:opacity-50 rounded-md border text-left transition-all',
                        type === ty.value ? 'border-primary bg-primary-soft' : 'border-border bg-surface hover:bg-surface-hover']"
                      @click="type = ty.value"
                    >
                      <div :class="['text-sm font-semibold', type === ty.value ? 'text-primary' : '']">{{ ty.label }}</div>
                      <div class="mt-0.5 text-caption text-muted leading-snug">{{ ty.desc }}</div>
                    </button>
                  </div>
                </fieldset>
  
                <!-- Proxy: upstream -->
                <div v-if="type === 'proxy'" class="space-y-3">
                  <div>
                    <label for="new-repo-upstream" class="text-caption font-semibold text-muted">{{ t('repos.new.upstreamUrl') }}</label>
                    <input
                      id="new-repo-upstream"
                      v-model="upstreamUrl"
                      type="url"
                      :placeholder="upstreamUrlPlaceholder(format)"
                      class="mt-1.5 w-full h-10 px-3 rounded-md bg-surface-hover/40 border border-border-strong focus:bg-surface focus:border-primary/50 focus:ring-2 focus:ring-primary/20 text-sm font-mono transition-colors"
                    />
                  </div>
                  <div class="grid grid-cols-2 gap-2">
                    <input
                      v-model="upstreamUsername"
                      :placeholder="t('repos.new.upstreamUsername')"
                      :aria-label="t('repos.new.upstreamUsername')"
                      autocomplete="off"
                      class="h-9 px-3 rounded-md bg-surface-hover/40 border border-border-strong text-sm focus:border-primary/50"
                    />
                    <input
                      v-model="upstreamPassword"
                      type="password"
                      :placeholder="t('repos.new.upstreamPassword')"
                      :aria-label="t('repos.new.upstreamPassword')"
                      autocomplete="new-password"
                      class="h-9 px-3 rounded-md bg-surface-hover/40 border border-border-strong text-sm focus:border-primary/50"
                    />
                  </div>
                </div>
  
                <!-- Group: members -->
                <fieldset v-if="type === 'group'">
                  <legend class="text-caption font-semibold text-muted">{{ t('repos.new.members') }}</legend>
                  <p v-if="!candidateMembers.length" class="mt-1.5 text-xs text-muted">{{ t('repos.new.noMembers') }}</p>
                  <div v-else class="mt-1.5 max-h-32 overflow-y-auto rounded-md border border-border-strong divide-y divide-border">
                    <label v-for="r in candidateMembers" :key="r.id" class="flex items-center gap-2 px-3 py-1.5 text-sm cursor-pointer hover:bg-surface-hover">
                      <input v-model="members" type="checkbox" :value="r.name" class="accent-[rgb(var(--color-primary))]" />
                      <span class="truncate">{{ r.name }}</span>
                    </label>
                  </div>
                </fieldset>
  
                <ProjectSelect id="new-repo-project" v-model="project" @select="selectedProject = $event" />
                <p v-if="prefix" class="-mt-2 text-xs text-muted">{{ t('repos.project.prefix') }} <span class="font-mono text-foreground">{{ prefix }}</span></p>
  
                <label class="flex items-center gap-2 pt-1 text-sm">
                  <input v-model="isPublic" type="checkbox" :disabled="!publicAllowed" class="accent-[rgb(var(--color-primary))]" />
                  <span>{{ t('repos.new.public') }}</span>
                  <span v-if="!publicAllowed" class="text-xs text-muted">{{ t('repos.project.publicBlocked') }}</span>
                </label>

                <div v-if="canFederate">
                  <label class="flex items-center gap-2 pt-1 text-sm">
                    <input v-model="federated" type="checkbox" class="accent-[rgb(var(--color-primary))]" />
                    <span>{{ t('federation.create.label') }}</span>
                  </label>
                  <p class="mt-1 text-xs text-muted">{{ t('federation.create.hint') }}</p>
                </div>
              </form>
    <template #footer>
      <AppButton variant="ghost" @click="close">{{ t('common.cancel') }}</AppButton>
      <AppButton type="submit" form="new-repo-form" :disabled="!canSubmit" :loading="creating">
        <CheckIcon class="w-4 h-4" aria-hidden="true" />
        {{ creating ? t('repos.new.creating') : t('repos.create') }}
      </AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useRouter } from 'vue-router'
import { createRepository, listAllRepositories } from '../api/repositories'
import { createProjectRepository } from '../api/projects'
import type { Project } from '../api/projects'
import { policyAllowsPublic, policyAllowsType, policyFormats, policyPrefix } from '../lib/projectPolicy'
import { useSessionStore } from '../stores/session'
import type { Repository } from '../api/repositories'
import { useToastStore } from '../stores/toast'
import { CheckIcon } from '@heroicons/vue/24/outline'
import type { FormatId } from '../lib/formats'
import { allowedFormats, coerceFormat, federationAvailable, upstreamUrlPlaceholder, upstreamUrlRequired } from '../lib/repoTypes'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import FormatBadge from './FormatBadge.vue'
import ProjectSelect from './ProjectSelect.vue'
import { t } from '../i18n/en'

const open = defineModel<boolean>({ default: false })
const emit = defineEmits<{ created: [name: string] }>()

const router = useRouter()
const toast = useToastStore()
const session = useSessionStore()

const name = ref('')
const format = ref<FormatId>('docker')
const type = ref<'hosted' | 'proxy' | 'group'>('hosted')
const isPublic = ref(false)
const federated = ref(false)
const project = ref('')
const upstreamUrl = ref('')
const upstreamUsername = ref('')
const upstreamPassword = ref('')
const members = ref<string[]>([])
const existing = ref<Repository[]>([])
const creating = ref(false)

// The project policy narrows the choices for non-admins; admins bypass it.
const selectedProject = ref<Project | null>(null)
const policy = computed(() => (session.isAdmin ? null : selectedProject.value))
const prefix = computed(() => policyPrefix(policy.value))
const publicAllowed = computed(() => policyAllowsPublic(policy.value))
const formats = computed(() => policyFormats(policy.value, allowedFormats(type.value)))

// Federation applies to hosted repositories of a capable format only.
const canFederate = computed(() => federationAvailable(type.value, format.value))

const types = [
  { value: 'hosted', label: t('repos.new.type.hosted'), desc: t('repos.new.type.hostedDesc') },
  { value: 'proxy', label: t('repos.new.type.proxy'), desc: t('repos.new.type.proxyDesc') },
  { value: 'group', label: t('repos.new.type.group'), desc: t('repos.new.type.groupDesc') },
] as const

// Mirrors the backend's repository-name rule.
const nameValid = computed(() => /^[a-zA-Z0-9_-]{1,100}$/.test(name.value.trim()) && name.value.trim().startsWith(prefix.value))
const canSubmit = computed(() => {
  if (!nameValid.value) return false
  if (type.value === 'proxy') return !upstreamUrlRequired(format.value) || upstreamUrl.value.trim() !== ''
  return true
})

// A group may only contain repositories of its own format.
const candidateMembers = computed(() =>
  existing.value.filter((r) => r.format === format.value && r.repo_type !== 'group'),
)

watch(format, () => {
  members.value = []
})

// Proxy and group repositories only exist for some formats; drop a selection
// the new type cannot serve.
watch(type, (next) => {
  format.value = coerceFormat(next, format.value)
})

// A project change can rule out the chosen format or visibility.
watch(policy, () => {
  if (!policyAllowsType(policy.value, type.value)) {
    type.value = types.find((ty) => policyAllowsType(policy.value, ty.value))?.value ?? 'hosted'
  }
  if (!formats.value.includes(format.value) && formats.value.length > 0) format.value = formats.value[0]
  if (!publicAllowed.value) isPublic.value = false
})

watch(open, async (isOpen) => {
  if (!isOpen) return
  name.value = ''
  type.value = 'hosted'
  isPublic.value = false
  federated.value = false
  project.value = ''
  upstreamUrl.value = ''
  upstreamUsername.value = ''
  upstreamPassword.value = ''
  members.value = []
  try {
    existing.value = await listAllRepositories()
  } catch {
    existing.value = []
  }
})

function close(): void {
  if (!creating.value) open.value = false
}

async function handleCreate(): Promise<void> {
  if (!canSubmit.value || creating.value) return
  creating.value = true
  const repoName = name.value.trim()
  try {
    const create = project.value && !session.isAdmin
      ? (body: Parameters<typeof createRepository>[0]) => createProjectRepository(project.value, body)
      : createRepository
    await create({
      name: repoName,
      format: format.value,
      public: isPublic.value,
      description: '',
      repo_type: type.value,
      ...(canFederate.value && federated.value ? { federated: true } : {}),
      ...(project.value && session.isAdmin ? { project: project.value } : {}),
      ...(type.value === 'proxy'
        ? {
            upstream_url: upstreamUrl.value.trim() || undefined,
            upstream_username: upstreamUsername.value || undefined,
            upstream_password: upstreamPassword.value || undefined,
          }
        : {}),
      ...(type.value === 'group' ? { member_repos: members.value } : {}),
    })
    toast.success(t('repos.new.created'), repoName)
    open.value = false
    emit('created', repoName)
    await router.push(`/repos/${encodeURIComponent(repoName)}`)
  } catch (err: unknown) {
    toast.error(t('repos.new.failed'), err instanceof Error ? err.message : undefined)
  } finally {
    creating.value = false
  }
}
</script>
