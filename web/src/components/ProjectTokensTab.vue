<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Project tokens: CI credentials of a service account of the project. They
  reach this project's repositories only (viewer: read, developer: read and
  write), never the management API. Maintainers and owners see and revoke all
  of them, other members the ones they minted.
-->
<template>
  <div class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <p class="text-sm text-muted max-w-2xl">{{ t('projects.tokens.hint') }}</p>
      <AppButton variant="primary" size="md" data-testid="token-new" @click="openCreate">
        <PlusIcon class="w-4 h-4" aria-hidden="true" />
        {{ t('projects.tokens.create') }}
      </AppButton>
    </div>

    <div v-if="loading" class="text-muted">{{ t('common.loading') }}</div>
    <div v-else-if="loadError" class="rounded-xl border border-danger/30 bg-surface p-6 text-center" role="alert">
      <p class="text-sm text-danger">{{ t('projects.tokens.loadFailed') }}</p>
      <AppButton variant="secondary" size="sm" class="mt-3" @click="load">{{ t('browser.retry') }}</AppButton>
    </div>
    <EmptyState v-else-if="tokens.length === 0" :title="t('projects.tokens.empty')" :description="t('projects.tokens.emptyHint')" />
    <section v-else class="rounded-xl border border-border bg-surface overflow-x-auto" :aria-label="t('projects.tab.tokens')">
      <table class="w-full">
        <thead>
          <tr class="text-caption font-semibold text-muted bg-surface-hover/30 border-b border-border">
            <th class="px-4 py-3 text-left">{{ t('projects.tokens.column.name') }}</th>
            <th class="px-4 py-3 text-left">{{ t('projects.tokens.column.role') }}</th>
            <th class="px-4 py-3 text-left">{{ t('projects.tokens.column.createdBy') }}</th>
            <th class="px-4 py-3 text-left">{{ t('projects.tokens.column.lastUsed') }}</th>
            <th class="px-4 py-3 text-left">{{ t('projects.tokens.column.expires') }}</th>
            <th class="px-4 py-3 text-right">{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="token in tokens" :key="token.id" class="border-b border-border" data-testid="token-row">
            <td class="px-4 py-3 font-mono font-medium">{{ token.name }}</td>
            <td class="px-4 py-3"><ProjectRoleBadge v-if="token.role" :role="token.role" /></td>
            <td class="px-4 py-3 text-sm text-muted">{{ token.created_by_username || '—' }}</td>
            <td class="px-4 py-3 text-sm text-muted">
              {{ token.last_used_at ? formatDateTime(token.last_used_at) : t('projects.tokens.never') }}
            </td>
            <td class="px-4 py-3 text-sm" :class="isExpired(token.expires_at) ? 'text-danger' : 'text-muted'">
              {{ token.expires_at ? formatDateTime(token.expires_at) : t('projects.tokens.never') }}
            </td>
            <td class="px-4 py-3 text-right">
              <button
                type="button"
                class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger-soft/40 transition-colors"
                :aria-label="`${t('projects.tokens.revoke')}: ${token.name}`"
                :title="t('projects.tokens.revoke')"
                data-testid="token-revoke"
                @click="revokeTarget = token"
              >
                <TrashIcon class="w-4 h-4" aria-hidden="true" />
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </section>

    <AdminDialog :open="createOpen" drawer :title="t('projects.tokens.create')" @close="createOpen = false">
      <form id="project-token-form" class="space-y-5" data-testid="token-form" @submit.prevent="submit">
        <div>
          <label for="project-token-name" class="text-caption font-semibold text-muted block mb-2">{{ t('projects.tokens.name') }}</label>
          <input
            id="project-token-name"
            v-model="form.name"
            type="text"
            required
            maxlength="100"
            :placeholder="t('projects.tokens.namePlaceholder')"
            class="w-full h-10 px-3 rounded-lg bg-surface-hover/40 border border-border-strong text-foreground placeholder-muted focus:outline-none focus:ring-2 focus:ring-primary"
          />
        </div>
        <div>
          <span class="text-caption font-semibold text-muted block mb-2">{{ t('projects.tokens.role') }}</span>
          <RoleSelect
            v-model="form.role"
            :roles="PROJECT_TOKEN_ROLES"
            :grantable="grantable"
            :label="t('projects.tokens.role')"
          />
          <p class="mt-1.5 text-xs text-muted">{{ t('projects.tokens.roleHint') }}</p>
        </div>
        <div>
          <label for="project-token-days" class="text-caption font-semibold text-muted block mb-2">{{ t('projects.tokens.expiresInDays') }}</label>
          <input
            id="project-token-days"
            v-model.number="form.days"
            type="number"
            min="1"
            :max="maxDays || undefined"
            :required="maxDays > 0"
            class="w-32 h-10 px-3 rounded-lg bg-surface-hover/40 border border-border-strong text-foreground focus:outline-none focus:ring-2 focus:ring-primary"
          />
          <p class="mt-1.5 text-xs text-muted">
            {{ maxDays > 0 ? `${t('projects.tokens.expiresCap')} ${maxDays} ${t('projects.settings.days')}` : t('projects.tokens.expiresOptional') }}
          </p>
        </div>
      </form>
      <template #footer>
        <AppButton variant="secondary" @click="createOpen = false">{{ t('common.cancel') }}</AppButton>
        <AppButton variant="primary" type="submit" form="project-token-form" :loading="creating">{{ t('common.create') }}</AppButton>
      </template>
    </AdminDialog>

    <TokenSecretDialog :title="t('projects.tokens.create')" :token="secret" @close="secret = null" />

    <ConfirmDialog
      :open="!!revokeTarget"
      :title="t('projects.tokens.confirmRevoke')"
      :description="revokeTarget?.name ?? ''"
      :confirm-label="t('projects.tokens.revoke')"
      :busy="revoking"
      @confirm="confirmRevoke"
      @close="revokeTarget = null"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { PlusIcon, TrashIcon } from '@heroicons/vue/24/outline'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import EmptyState from './EmptyState.vue'
import ProjectRoleBadge from './ProjectRoleBadge.vue'
import RoleSelect from './RoleSelect.vue'
import TokenSecretDialog from './TokenSecretDialog.vue'
import {
  PROJECT_TOKEN_ROLES,
  createProjectToken,
  listProjectTokens,
  revokeProjectToken,
  type Project,
  type ProjectToken,
  type ProjectTokenRole,
} from '../api/projects'
import { formatDateTime, isExpired } from '../lib/datetime'
import { atLeast } from '../lib/projectAccess'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const props = defineProps<{ project: Project }>()

const toasts = useToastStore()
const tokens = ref<ProjectToken[]>([])
const loading = ref(true)
const loadError = ref(false)
const createOpen = ref(false)
const creating = ref(false)
const secret = ref<string | null>(null)
const revokeTarget = ref<ProjectToken | null>(null)
const revoking = ref(false)
const form = ref<{ name: string; role: ProjectTokenRole; days: number | '' }>({ name: '', role: 'developer', days: '' })

const projectName = computed(() => props.project.name ?? '')
const maxDays = computed(() => props.project.token_max_ttl_days ?? 0)

// A token's role never exceeds its creator's: viewers can mint viewer tokens only.
const grantable = computed<readonly string[]>(() =>
  atLeast(props.project.my_role, 'developer') ? PROJECT_TOKEN_ROLES : ['viewer'],
)


async function load(): Promise<void> {
  loading.value = true
  loadError.value = false
  try {
    tokens.value = await listProjectTokens(projectName.value)
  } catch {
    loadError.value = true
  } finally {
    loading.value = false
  }
}

function openCreate(): void {
  form.value = {
    name: '',
    role: grantable.value.includes('developer') ? 'developer' : 'viewer',
    days: maxDays.value > 0 ? Math.min(maxDays.value, 30) : '',
  }
  createOpen.value = true
}

async function submit(): Promise<void> {
  if (creating.value) return
  creating.value = true
  try {
    const days = form.value.days === '' ? 0 : form.value.days
    const expires = days > 0 ? new Date(Date.now() + days * 86_400_000).toISOString() : undefined
    const created = await createProjectToken(projectName.value, {
      name: form.value.name.trim(),
      role: form.value.role,
      expires_at: expires,
    })
    createOpen.value = false
    secret.value = created.token ?? null
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('projects.tokens.createFailed'))
  } finally {
    creating.value = false
  }
}

async function confirmRevoke(): Promise<void> {
  const target = revokeTarget.value
  if (!target?.id) return
  revoking.value = true
  try {
    await revokeProjectToken(projectName.value, target.id)
    toasts.success(t('projects.tokens.revoked'))
    revokeTarget.value = null
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('projects.tokens.revokeFailed'))
  } finally {
    revoking.value = false
  }
}

watch(() => projectName.value, load)
onMounted(load)
</script>
