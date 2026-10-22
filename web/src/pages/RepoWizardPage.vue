<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <PageHeader :title="isEdit ? t('wizard.editTitle') : t('wizard.title')" />
    <div class="grid grid-cols-1 md:grid-cols-[200px_1fr] gap-6">
      <!-- Left Stepper -->
      <div class="flex md:block gap-1 md:space-y-1 overflow-x-auto md:overflow-visible">
        <div
          v-for="(s, idx) in steps"
          :key="idx"
          class="flex shrink-0 items-center gap-3 py-2.5 px-3 rounded-lg transition-colors"
          :class="
            idx === step
              ? 'bg-primary-soft/40 text-primary'
              : idx < step
              ? 'text-foreground'
              : 'text-muted'
          "
        >
          <div
            class="w-7 h-7 rounded-full flex items-center justify-center text-xs font-bold border-2 flex-shrink-0"
            :class="
              idx < step
                ? 'border-primary bg-primary text-white'
                : idx === step
                ? 'border-primary bg-primary text-white'
                : 'border-border bg-surface'
            "
          >
            <CheckIcon v-if="idx < step" class="w-4 h-4" aria-hidden="true" />
            <span v-else>{{ idx + 1 }}</span>
          </div>
          <span class="text-sm font-medium">{{ s.label }}</span>
        </div>
      </div>

      <!-- Right Content -->
      <div class="bg-surface rounded-xl border border-border p-6 min-h-[420px] flex flex-col">
        <!-- Step Title & Subtitle -->
        <div>
          <h2 class="text-lg font-semibold">{{ steps[step].label }}</h2>
          <p class="text-sm text-muted mt-1">{{ stepSubtitles[step] }}</p>
          <p v-if="loadError" class="mt-2 text-sm text-danger" role="alert">{{ loadError }}</p>
        </div>

        <!-- Content Area -->
        <div class="flex-1 mt-5">
          <!-- Step 0: Identity -->
          <template v-if="step === 0">
            <div class="space-y-4">
              <!-- Name Input -->
              <div>
                <label for="repo-name" class="block text-sm font-medium text-foreground mb-2">
                  {{ t('wizard.name') }}
                </label>
                <input
                  id="repo-name"
                  v-model="form.name"
                  :disabled="isEdit"
                  type="text"
                  placeholder="my-repo"
                  class="w-full px-3 py-2 border border-border-strong rounded-md text-sm font-mono bg-bg text-foreground disabled:opacity-60 focus:ring-2 focus:ring-primary"
                />
                <p class="text-xs mt-1" :class="form.name && !nameValid ? 'text-danger' : 'text-muted'">{{ t('repos.new.nameHint') }}</p>
              </div>

              <ProjectSelect v-if="!isEdit || session.isAdmin" id="repo-project" v-model="form.project" @select="selectedProject = $event" />
              <p v-if="prefix && !isEdit" class="-mt-2 text-xs text-muted" data-testid="name-prefix">
                {{ t('repos.project.prefix') }} <span class="font-mono text-foreground">{{ prefix }}</span>
              </p>

              <!-- Visibility Toggle -->
              <div>
                <label class="block text-sm font-medium text-foreground mb-3">{{ t('repos.column.visibility') }}</label>
                <div class="grid grid-cols-2 gap-3">
                  <button
                    type="button"
                    class="p-3 rounded-lg border-2 transition-all text-center"
                    :class="
                      !form.public
                        ? 'border-primary bg-primary-soft/40'
                        : 'border-border bg-surface hover:border-border/60'
                    "
                    @click="form.public = false"
                  >
                    <div class="text-sm font-medium">{{ t('repos.private') }}</div>
                    <div class="text-xs text-muted mt-1">{{ t('wizard.privateHint') }}</div>
                  </button>
                  <button
                    type="button"
                    class="p-3 rounded-lg border-2 transition-all text-center"
                    :class="
                      form.public
                        ? 'border-primary bg-primary-soft/40'
                        : 'border-border bg-surface hover:border-border/60'
                    "
                    :disabled="!publicAllowed"
                    :title="publicAllowed ? undefined : t('repos.project.publicBlocked')"
                    @click="form.public = true"
                  >
                    <div class="text-sm font-medium">{{ t('repos.public') }}</div>
                    <div class="text-xs text-muted mt-1">{{ t('wizard.publicHint') }}</div>
                  </button>
                </div>
              </div>
            </div>
          </template>

          <!-- Step 1: Format -->
          <template v-else-if="step === 1">
            <p v-if="isEdit" class="mb-3 text-xs text-muted">{{ t('wizard.immutable') }}</p>
            <p v-if="form.type === 'proxy'" class="mb-3 text-xs text-muted">{{ t('repos.new.proxyFormatsHint') }}</p>
            <p v-else-if="form.type === 'group'" class="mb-3 text-xs text-muted">{{ t('repos.new.groupFormatsHint') }}</p>
            <div class="grid grid-cols-3 gap-3">
              <button
                v-for="fmt in formats"
                :key="fmt"
                type="button"
                :disabled="isEdit"
                :aria-pressed="form.format === fmt"
                class="p-3 rounded-lg border-2 transition-all text-left flex items-center gap-3 disabled:opacity-60"
                :class="
                  form.format === fmt
                    ? 'border-primary bg-primary-soft/40 ring-1 ring-primary/30'
                    : 'border-border bg-surface hover:border-border/60'
                "
                @click="form.format = fmt"
              >
                <FormatBadge :format="fmt" />
              </button>
            </div>
          </template>

          <!-- Step 2: Type -->
          <template v-else-if="step === 2">
            <p v-if="isEdit" class="mb-3 text-xs text-muted">{{ t('wizard.immutable') }}</p>
            <div class="grid grid-cols-1 gap-3">
              <button
                v-for="ty in types"
                :key="ty.value"
                type="button"
                :disabled="isEdit || !policyAllowsType(policy, ty.value)"
                :title="policyAllowsType(policy, ty.value) ? undefined : t('repos.project.typeBlocked')"
                :aria-pressed="form.type === ty.value"
                class="p-4 rounded-lg border-2 transition-all text-left disabled:opacity-60"
                :class="
                  form.type === ty.value
                    ? 'border-primary bg-primary-soft/40'
                    : 'border-border bg-surface hover:border-border/60'
                "
                @click="form.type = ty.value"
              >
                <div class="flex items-start gap-3">
                  <component :is="ty.icon" class="w-5 h-5 text-primary flex-shrink-0 mt-0.5" aria-hidden="true" />
                  <div>
                    <div class="text-sm font-medium">{{ ty.title }}</div>
                    <div class="text-xs text-muted mt-1">{{ ty.description }}</div>
                  </div>
                </div>
              </button>
            </div>

            <p v-if="!isEdit && form.type === 'proxy'" class="mt-3 text-xs text-muted">{{ t('repos.new.proxyFormatsHint') }}</p>
            <p v-else-if="!isEdit && form.type === 'group'" class="mt-3 text-xs text-muted">{{ t('repos.new.groupFormatsHint') }}</p>

            <div v-if="form.type === 'proxy'" class="mt-4 space-y-3">
              <div>
                <label for="repo-upstream" class="block text-sm font-medium text-foreground mb-2">{{ t('repos.new.upstreamUrl') }}</label>
                <input
                  id="repo-upstream"
                  v-model="form.upstreamUrl"
                  type="url"
                  :placeholder="upstreamUrlPlaceholder(form.format)"
                  class="w-full px-3 py-2 border border-border-strong rounded-md text-sm font-mono bg-bg text-foreground focus:ring-2 focus:ring-primary"
                />
              </div>
              <div v-if="!isEdit" class="grid grid-cols-2 gap-2">
                <input v-model="form.upstreamUsername" :placeholder="t('repos.new.upstreamUsername')" :aria-label="t('repos.new.upstreamUsername')" autocomplete="off" class="px-3 py-2 border border-border-strong rounded-md text-sm bg-bg text-foreground" />
                <input v-model="form.upstreamPassword" type="password" :placeholder="t('repos.new.upstreamPassword')" :aria-label="t('repos.new.upstreamPassword')" autocomplete="new-password" class="px-3 py-2 border border-border-strong rounded-md text-sm bg-bg text-foreground" />
              </div>
            </div>

            <fieldset v-if="form.type === 'group' && !isEdit" class="mt-4">
              <legend class="block text-sm font-medium text-foreground mb-2">{{ t('repos.new.members') }}</legend>
              <p v-if="!candidateMembers.length" class="text-xs text-muted">{{ t('repos.new.noMembers') }}</p>
              <div v-else class="max-h-40 overflow-y-auto rounded-md border border-border-strong divide-y divide-border">
                <label v-for="r in candidateMembers" :key="r.id" class="flex items-center gap-2 px-3 py-1.5 text-sm cursor-pointer hover:bg-surface-hover">
                  <input v-model="form.members" type="checkbox" :value="r.name" class="accent-[rgb(var(--color-primary))]" />
                  <span class="truncate">{{ r.name }}</span>
                </label>
              </div>
            </fieldset>
          </template>

          <!-- Step 3: Policies -->
          <template v-else-if="step === 3">
            <div class="space-y-4">
              <!-- Description -->
              <div>
                <label for="repo-desc" class="block text-sm font-medium text-foreground mb-2">
                  {{ t('wizard.description') }}
                </label>
                <textarea
                  id="repo-desc"
                  v-model="form.description"
                  :placeholder="t('wizard.descriptionHint')"
                  rows="4"
                  class="w-full px-3 py-2 border border-border-strong rounded-md text-sm bg-bg text-foreground focus:ring-2 focus:ring-primary"
                />
              </div>

              <!-- Public Toggle -->
              <div class="flex items-center gap-3">
                <input
                  id="repo-public"
                  v-model="form.public"
                  type="checkbox"
                  class="rounded"
                />
                <label for="repo-public" class="text-sm font-medium text-foreground">
                  {{ t('wizard.makePublic') }}
                </label>
              </div>

              <!-- Trash retention -->
              <div>
                <label for="repo-trash-retention" class="block text-sm font-medium text-foreground mb-2">
                  {{ t('repos.settings.trashRetention') }}
                </label>
                <input
                  id="repo-trash-retention"
                  v-model="form.trashRetention"
                  type="number"
                  min="0"
                  step="1"
                  :placeholder="t('repos.settings.trashRetentionPlaceholder')"
                  class="w-full px-3 py-2 border border-border-strong rounded-md text-sm bg-bg text-foreground focus:ring-2 focus:ring-primary"
                />
                <p v-if="retentionInvalid" role="alert" class="mt-1 text-xs text-danger">{{ t('repos.settings.trashRetentionInvalid') }}</p>
                <p v-else class="mt-1 text-xs text-muted">{{ t('repos.settings.trashRetentionHint') }}</p>
              </div>

              <!-- Immutable releases -->
              <div>
                <div class="flex items-center gap-3">
                  <input id="repo-immutable" v-model="form.immutableReleases" type="checkbox" class="rounded accent-[rgb(var(--color-primary))]" />
                  <label for="repo-immutable" class="text-sm font-medium text-foreground">
                    {{ t('repos.settings.immutableReleases') }}
                  </label>
                </div>
                <p class="mt-1 text-xs text-muted">{{ t('repos.settings.immutableReleasesHint') }}</p>
              </div>
            </div>
          </template>

          <!-- Step 4: Review -->
          <template v-else-if="step === 4">
            <dl class="space-y-3 text-sm">
              <div class="flex justify-between py-2 border-b border-border/50">
                <dt class="text-muted">{{ t('repos.column.name') }}</dt>
                <dd class="text-foreground font-medium font-mono">{{ form.name }}</dd>
              </div>
              <div class="flex justify-between py-2 border-b border-border/50">
                <dt class="text-muted">{{ t('repos.column.format') }}</dt>
                <dd><FormatBadge :format="form.format" /></dd>
              </div>
              <div class="flex justify-between py-2 border-b border-border/50">
                <dt class="text-muted">{{ t('repos.column.type') }}</dt>
                <dd class="text-foreground">{{ types.find((ty) => ty.value === form.type)?.title }}</dd>
              </div>
              <div class="flex justify-between py-2 border-b border-border/50">
                <dt class="text-muted">{{ t('repos.project.label') }}</dt>
                <dd class="text-foreground font-mono">{{ form.project || SYSTEM_PROJECT_NAME }}</dd>
              </div>
              <div class="flex justify-between py-2 border-b border-border/50">
                <dt class="text-muted">{{ t('repos.column.visibility') }}</dt>
                <dd class="text-foreground">{{ form.public ? t('repos.public') : t('repos.private') }}</dd>
              </div>
              <div v-if="form.type === 'proxy'" class="flex justify-between py-2 border-b border-border/50">
                <dt class="text-muted">{{ t('repos.new.upstreamUrl') }}</dt>
                <dd class="text-foreground font-mono text-right max-w-xs truncate">{{ form.upstreamUrl }}</dd>
              </div>
              <div v-if="form.type === 'group' && form.members.length" class="flex justify-between py-2 border-b border-border/50">
                <dt class="text-muted">{{ t('repos.new.members') }}</dt>
                <dd class="text-foreground text-right max-w-xs">{{ form.members.join(', ') }}</dd>
              </div>
              <div v-if="form.description" class="flex justify-between py-2">
                <dt class="text-muted">{{ t('wizard.description') }}</dt>
                <dd class="text-foreground text-right max-w-xs">{{ form.description }}</dd>
              </div>
            </dl>
          </template>
        </div>

        <!-- Navigation -->
        <div class="flex items-center justify-between mt-5 pt-4 border-t border-border">
          <AppButton v-if="step > 0" variant="secondary" size="sm" @click="step--">
            {{ t('wizard.back') }}
          </AppButton>
          <div v-else />
          <AppButton v-if="step < steps.length - 1" size="sm" :disabled="!stepValid" @click="step++">
            {{ t('wizard.next') }}
          </AppButton>
          <AppButton v-else size="sm" :disabled="!allValid" :loading="submitting" @click="submit">
            {{ submitting ? t('repos.new.creating') : isEdit ? t('wizard.update') : t('wizard.create') }}
          </AppButton>
        </div>
      </div>

    </div>
  </div>
</template>

<script setup lang="ts">
import PageHeader from '../components/PageHeader.vue'
import AppButton from '../components/AppButton.vue'
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  TagIcon,
  CubeIcon,
  ServerIcon,
  ShieldCheckIcon,
  CheckCircleIcon,
  CheckIcon,
} from '@heroicons/vue/24/outline'
import { createRepository, getRepository, updateRepository, listRepositories } from '../api/repositories'
import { parseRetentionInput } from '../lib/trash'
import type { Repository } from '../api/repositories'
import FormatBadge from '../components/FormatBadge.vue'
import ProjectSelect from '../components/ProjectSelect.vue'
import { createProjectRepository, SYSTEM_PROJECT_NAME } from '../api/projects'
import type { Project } from '../api/projects'
import { policyAllowsPublic, policyAllowsType, policyFormats, policyPrefix } from '../lib/projectPolicy'
import { useSessionStore } from '../stores/session'
import { t } from '../i18n/en'
import { useToastStore } from '../stores/toast'
import type { FormatId } from '../lib/formats'
import { allowedFormats, coerceFormat, upstreamUrlPlaceholder, upstreamUrlRequired } from '../lib/repoTypes'

const route = useRoute()
const router = useRouter()
const toasts = useToastStore()
const session = useSessionStore()

const types: { value: 'hosted' | 'proxy' | 'group'; icon: unknown; title: string; description: string }[] = [
  {
    value: 'hosted',
    icon: ServerIcon,
    title: t('repos.new.type.hosted'),
    description: t('repos.new.type.hostedDesc'),
  },
  {
    value: 'proxy',
    icon: ServerIcon,
    title: t('repos.new.type.proxy'),
    description: t('repos.new.type.proxyDesc'),
  },
  {
    value: 'group',
    icon: ServerIcon,
    title: t('repos.new.type.group'),
    description: t('repos.new.type.groupDesc'),
  },
]

const isEdit = computed(() => !!route.params.name)

const step = ref(0)
const submitting = ref(false)

const form = ref({
  name: '',
  format: 'docker' as FormatId,
  type: 'hosted' as 'hosted' | 'proxy' | 'group',
  description: '',
  public: false,
  project: '',
  upstreamUrl: '',
  upstreamUsername: '',
  upstreamPassword: '',
  members: [] as string[],
  trashRetention: '',
  immutableReleases: false,
})
// Project the repository had when loaded (edit mode); '' is the system project.
const loadedProject = ref('')
const loadError = ref('')
const existing = ref<Repository[]>([])

// The project policy narrows the choices for non-admins; admins bypass it.
const selectedProject = ref<Project | null>(null)
const policy = computed(() => (session.isAdmin || isEdit.value ? null : selectedProject.value))
const prefix = computed(() => policyPrefix(policy.value))
const publicAllowed = computed(() => policyAllowsPublic(policy.value))
const formats = computed(() => policyFormats(policy.value, allowedFormats(form.value.type)))

const nameValid = computed(
  () => /^[a-zA-Z0-9_-]{1,100}$/.test(form.value.name.trim()) && (isEdit.value || form.value.name.trim().startsWith(prefix.value)),
)
const candidateMembers = computed(() =>
  existing.value.filter((r) => r.format === form.value.format && r.repo_type !== 'group' && r.name !== form.value.name),
)
const retentionInvalid = computed(() => parseRetentionInput(form.value.trashRetention) === undefined)
const upstreamOk = computed(
  () => form.value.type !== 'proxy' || !upstreamUrlRequired(form.value.format) || form.value.upstreamUrl.trim() !== '',
)
const stepValid = computed(() => {
  if (step.value === 3 && retentionInvalid.value) return false
  if (step.value === 0) return nameValid.value
  if (step.value === 2 && form.value.type === 'proxy') return upstreamOk.value
  return true
})
const allValid = computed(() => nameValid.value && !retentionInvalid.value && upstreamOk.value)

const steps = [
  { label: t('wizard.step.identity'), icon: TagIcon },
  { label: t('wizard.step.format'), icon: CubeIcon },
  { label: t('wizard.step.type'), icon: ServerIcon },
  { label: t('wizard.step.policies'), icon: ShieldCheckIcon },
  { label: t('wizard.step.review'), icon: CheckCircleIcon },
]

const stepSubtitles = [
  t('wizard.sub.identity'),
  t('wizard.sub.format'),
  t('wizard.sub.type'),
  t('wizard.sub.policies'),
  t('wizard.sub.review'),
]

onMounted(async () => {
  if (isEdit.value) {
    try {
      const repo = await getRepository(route.params.name as string)
      form.value = {
        ...form.value,
        name: repo.name,
        format: repo.format,
        type: repo.repo_type || 'hosted',
        description: repo.description || '',
        public: repo.public || false,
        project: repo.project && repo.project !== SYSTEM_PROJECT_NAME ? repo.project : '',
        upstreamUrl: repo.upstream_url || '',
        trashRetention: repo.trash_retention_days == null ? '' : String(repo.trash_retention_days),
        immutableReleases: repo.immutable_releases ?? false,
      }
      loadedProject.value = form.value.project
    } catch (err) {
      loadError.value = err instanceof Error ? err.message : t('repoDetail.loadFailed')
    }
  } else {
    if (typeof route.query.project === 'string') form.value.project = route.query.project
    try {
      existing.value = (await listRepositories({ limit: 100 })).repositories || []
    } catch {
      existing.value = []
    }
  }
})

watch(
  () => form.value.format,
  () => {
    form.value.members = []
  },
)

// Proxy and group repositories only exist for some formats; drop a selection
// the new type cannot serve. Existing repositories keep their stored format.
watch(
  () => form.value.type,
  (next) => {
    if (!isEdit.value) form.value.format = coerceFormat(next, form.value.format)
  },
)

// A project change can rule out the chosen format, type or visibility.
watch(policy, () => {
  if (isEdit.value) return
  if (!policyAllowsType(policy.value, form.value.type)) {
    form.value.type = types.find((ty) => policyAllowsType(policy.value, ty.value))?.value ?? 'hosted'
  }
  if (!formats.value.includes(form.value.format) && formats.value.length > 0) form.value.format = formats.value[0]
  if (!publicAllowed.value) form.value.public = false
})

async function submit(): Promise<void> {
  if (submitting.value || !allValid.value) return
  submitting.value = true
  const name = form.value.name.trim()
  try {
    if (isEdit.value) {
      await updateRepository(name, {
        description: form.value.description,
        public: form.value.public,
        trash_retention_days: parseRetentionInput(form.value.trashRetention) ?? null,
        immutable_releases: form.value.immutableReleases,
        ...(form.value.project !== loadedProject.value ? { project: form.value.project || SYSTEM_PROJECT_NAME } : {}),
        ...(form.value.type === 'proxy' ? { upstream_url: form.value.upstreamUrl.trim() } : {}),
      })
    } else {
      const create = form.value.project && !session.isAdmin
        ? (body: Parameters<typeof createRepository>[0]) => createProjectRepository(form.value.project, body)
        : createRepository
      await create({
        name,
        format: form.value.format,
        public: form.value.public,
        description: form.value.description,
        trash_retention_days: parseRetentionInput(form.value.trashRetention) ?? null,
        immutable_releases: form.value.immutableReleases,
        repo_type: form.value.type,
        ...(form.value.project && session.isAdmin ? { project: form.value.project } : {}),
        ...(form.value.type === 'proxy'
          ? {
              upstream_url: form.value.upstreamUrl.trim() || undefined,
              upstream_username: form.value.upstreamUsername || undefined,
              upstream_password: form.value.upstreamPassword || undefined,
            }
          : {}),
        ...(form.value.type === 'group' ? { member_repos: form.value.members } : {}),
      })
    }
    toasts.success(isEdit.value ? t('wizard.updated') : t('repos.new.created'))
    await router.push(`/repos/${encodeURIComponent(name)}`)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('wizard.failed'))
  } finally {
    submitting.value = false
  }
}
</script>
