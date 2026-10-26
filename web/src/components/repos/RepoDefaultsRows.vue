<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  "Defaults applied": the collapsed rows of the create drawer and the repository settings,
  each with a live summary. `mode="edit"` locks what cannot change after creation (type,
  format) and drops the quota (it has its own editor in the settings).
-->
<template>
  <div class="overflow-hidden rounded-lg border border-border bg-surface" data-testid="defaults-rows">
    <FormRow id="type" :label="t('rl.row.type')" :summary="typeSummary(form)">
      <p v-if="mode === 'edit'" class="text-sm text-muted">{{ t('wizard.immutable') }}</p>
      <div v-else class="grid gap-2 sm:grid-cols-3" role="radiogroup" :aria-label="t('rl.row.type')">
        <button
          v-for="ty in TYPES"
          :key="ty"
          type="button"
          role="radio"
          :aria-checked="form.type === ty"
          :disabled="!policyAllowsType(policy, ty)"
          class="min-h-11 rounded-lg border px-3 py-2 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary disabled:opacity-50"
          :class="form.type === ty ? 'border-primary bg-primary-soft' : 'border-border-strong bg-surface hover:bg-surface-hover'"
          @click="form.type = ty"
        >
          <span class="block text-sm font-medium text-foreground">{{ t(`repos.new.type.${ty}` as 'repos.new.type.hosted') }}</span>
          <span class="block text-xs text-muted">
            {{ policyAllowsType(policy, ty) ? t(`repos.new.type.${ty}Desc` as 'repos.new.type.hostedDesc') : t('repos.project.typeBlocked') }}
          </span>
        </button>
      </div>
    </FormRow>

    <FormRow id="vis" :label="t('rl.row.visibility')" :summary="visibilitySummary(form)">
      <label class="flex min-h-11 items-center gap-3 text-sm text-foreground">
        <input v-model="form.isPublic" type="checkbox" :disabled="!publicAllowed" class="h-4 w-4 rounded accent-[rgb(var(--color-primary))]" />
        {{ t('repos.new.public') }}
      </label>
      <p class="text-xs text-muted">{{ publicAllowed ? t('wizard.publicHint') : t('repos.project.publicBlocked') }}</p>
    </FormRow>

    <FormRow id="project" :label="t('rl.row.project')" :summary="projectSummary(form)">
      <ProjectSelect v-if="mode === 'create' || isAdmin" id="rl-project" v-model="form.project" @select="emit('project', $event)" />
      <p v-else class="text-sm text-muted">{{ t('rl.project.moveHint') }}</p>
    </FormRow>

    <FormRow v-if="form.type === 'proxy'" id="upstream" :label="t('rl.row.upstream')" :summary="upstreamSummary(form)">
      <FormField :label="t('repos.new.upstreamUrl')" id="rl-upstream-url" :hint="upstreamHint">
        <input id="rl-upstream-url" v-model="form.upstreamUrl" type="url" :placeholder="upstreamUrlPlaceholder(form.format)" :class="INPUT_CLASS" />
      </FormField>
      <div class="grid gap-3 sm:grid-cols-2">
        <FormField :label="t('repos.new.upstreamUsername')" id="rl-upstream-user">
          <input id="rl-upstream-user" v-model="form.upstreamUsername" type="text" autocomplete="off" :class="INPUT_CLASS" />
        </FormField>
        <FormField :label="t('repos.new.upstreamPassword')" id="rl-upstream-pass" :hint="mode === 'edit' ? t('rl.upstream.passwordKeep') : undefined">
          <input id="rl-upstream-pass" v-model="form.upstreamPassword" type="password" autocomplete="new-password" :class="INPUT_CLASS" />
        </FormField>
      </div>
    </FormRow>

    <FormRow v-if="form.type === 'group'" id="members" :label="t('rl.row.members')" :summary="membersSummary(form)">
      <RepoMembersEditor v-model="form.members" :candidates="candidates" />
    </FormRow>

    <FormRow id="retention" :label="mode === 'create' ? t('rl.row.retentionQuota') : t('rl.row.retention')" :summary="retentionSummary(form, mode === 'create')">
      <FormField :label="t('repos.settings.trashRetention')" id="rl-retention" :hint="t('repos.settings.trashRetentionHint')" :error="retentionError(form.retentionDays)">
        <input
          id="rl-retention"
          v-model="form.retentionDays"
          type="number"
          min="0"
          step="1"
          :placeholder="t('repos.settings.trashRetentionPlaceholder')"
          :class="INPUT_CLASS"
        />
      </FormField>
      <FormField v-if="mode === 'create'" :label="t('quota.maxSize')" id="rl-quota" :hint="t('rl.quota.hint')">
        <div class="flex gap-2">
          <input id="rl-quota" v-model="form.quotaValue" type="number" min="0" step="any" :placeholder="t('quota.unlimited')" :class="INPUT_CLASS" />
          <select v-model="form.quotaUnit" :aria-label="t('rl.quota.unit')" :class="[INPUT_CLASS, 'w-24']">
            <option v-for="u in SIZE_UNITS" :key="u.label" :value="u.label">{{ u.label }}</option>
          </select>
        </div>
      </FormField>
    </FormRow>

    <FormRow id="policies" :label="t('rl.row.policies')" :summary="policiesSummary(form)">
      <label v-if="signable" class="flex min-h-11 items-start gap-3 text-sm text-foreground">
        <input v-model="form.signPackages" type="checkbox" class="mt-0.5 h-4 w-4 rounded accent-[rgb(var(--color-primary))]" />
        <span>{{ t('repos.settings.signPackages') }}<span class="block text-xs text-muted">{{ t('repos.settings.signPackagesHint') }}</span></span>
      </label>
      <label class="flex min-h-11 items-start gap-3 text-sm text-foreground">
        <input v-model="form.immutableReleases" type="checkbox" class="mt-0.5 h-4 w-4 rounded accent-[rgb(var(--color-primary))]" />
        <span>{{ t('repos.settings.immutableReleases') }}<span class="block text-xs text-muted">{{ t('repos.settings.immutableReleasesHint') }}</span></span>
      </label>
      <label v-if="form.type === 'group'" class="flex min-h-11 items-start gap-3 text-sm text-foreground">
        <input v-model="form.mergeProxyVersions" type="checkbox" :disabled="!isAdmin" class="mt-0.5 h-4 w-4 rounded accent-[rgb(var(--color-primary))]" />
        <span>
          {{ t('repos.settings.mergeProxyVersions') }}
          <span class="block text-xs text-muted">{{ t('repos.settings.mergeProxyVersionsHint') }}</span>
          <span v-if="!isAdmin" class="block text-xs text-muted">{{ t('repos.settings.mergeProxyVersionsAdminOnly') }}</span>
        </span>
      </label>
      <label v-if="mode === 'create' && canFederate" class="flex min-h-11 items-start gap-3 text-sm text-foreground">
        <input v-model="form.federated" type="checkbox" class="mt-0.5 h-4 w-4 rounded accent-[rgb(var(--color-primary))]" />
        <span>{{ t('federation.create.label') }}<span class="block text-xs text-muted">{{ t('federation.create.hint') }}</span></span>
      </label>
      <FormField v-if="mode === 'edit'" :label="t('repos.settings.presign')" id="rl-presign" :hint="t('repos.settings.presign.hint')">
        <select id="rl-presign" v-model="form.presign" :class="INPUT_CLASS">
          <option value="inherit">{{ t('repos.settings.presign.inherit') }}</option>
          <option value="always">{{ t('repos.settings.presign.always') }}</option>
          <option value="never">{{ t('repos.settings.presign.never') }}</option>
        </select>
      </FormField>
    </FormRow>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import FormField from '../FormField.vue'
import ProjectSelect from '../ProjectSelect.vue'
import FormRow from './FormRow.vue'
import RepoMembersEditor from './RepoMembersEditor.vue'
import type { Project } from '../../api/projects'
import { INPUT_CLASS } from '../../lib/formClasses'
import { SIZE_UNITS } from '../../lib/quota'
import { policyAllowsPublic, policyAllowsType, type RepoPolicy } from '../../lib/projectPolicy'
import {
  membersSummary,
  policiesSummary,
  projectSummary,
  retentionError,
  retentionSummary,
  typeSummary,
  upstreamSummary,
  visibilitySummary,
  type RepoForm,
} from '../../lib/repoForm'
import { federationAvailable, upstreamUrlPlaceholder, upstreamUrlRequired, type RepoType } from '../../lib/repoTypes'
import { t } from '../../i18n/en'

const TYPES: RepoType[] = ['hosted', 'proxy', 'group']

const props = defineProps<{
  mode: 'create' | 'edit'
  isAdmin: boolean
  /** Project policy that narrows the choices of a non-admin; null when none applies. */
  policy: RepoPolicy | null
  /** Names a group may take as members (same format, not a group). */
  candidates: string[]
}>()
const form = defineModel<RepoForm>({ required: true })
const emit = defineEmits<{ project: [project: Project | null] }>()

const publicAllowed = computed(() => policyAllowsPublic(props.policy))
const signable = computed(() => form.value.type === 'hosted' && form.value.format === 'rpm')
const canFederate = computed(() => federationAvailable(form.value.type, form.value.format))
const upstreamHint = computed(() => (upstreamUrlRequired(form.value.format) ? t('rl.upstream.required') : t('rl.upstream.optional')))
</script>
