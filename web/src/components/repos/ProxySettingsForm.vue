<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Edit form of a proxy repository's upstream and cache settings. Sends only what changed (PATCH);
  the upstream password is write-only: keep, replace or remove the stored one.
-->
<template>
  <form class="space-y-5 rounded-lg border border-border bg-surface p-4" novalidate :aria-label="t('repoDetail.proxy.editTitle')" @submit.prevent="save">
    <fieldset class="space-y-3">
      <legend class="text-sm font-semibold text-foreground">{{ t('repoDetail.proxy.title') }}</legend>
      <FormField :label="t('repos.new.upstreamUrl')" id="ps-upstream-url" :error="errorOf('upstream_url')">
        <input id="ps-upstream-url" v-model="form.upstreamUrl" type="url" :class="MONO_INPUT_CLASS" :aria-invalid="!!errorOf('upstream_url')" :aria-describedby="describedBy('upstream_url', 'ps-upstream-url')" />
      </FormField>
      <div class="grid gap-3 sm:grid-cols-2">
        <FormField :label="t('repoDetail.proxy.username')" id="ps-upstream-user" :error="errorOf('upstream_username')">
          <input id="ps-upstream-user" v-model="form.upstreamUsername" type="text" autocomplete="off" :class="INPUT_CLASS" :aria-invalid="!!errorOf('upstream_username')" />
        </FormField>
        <FormField :label="t('repos.new.upstreamPassword')" id="ps-upstream-pass" :error="serverErrors.upstream_password" :hint="passwordHint">
          <SegmentedControl v-if="repo.upstream_password_set" v-model="form.passwordMode" class="self-start" :options="passwordOptions" :label="t('repoDetail.proxy.password.mode')" data-testid="password-mode" />
          <input
            v-if="!repo.upstream_password_set || form.passwordMode === 'replace'"
            id="ps-upstream-pass"
            v-model="form.upstreamPassword"
            type="password"
            autocomplete="new-password"
            :class="[INPUT_CLASS, repo.upstream_password_set ? 'mt-2' : '']"
            :aria-invalid="!!serverErrors.upstream_password"
          />
        </FormField>
      </div>
      <p v-if="droppingPassword" role="status" class="text-xs text-warning" data-testid="password-drop">{{ t('repoDetail.proxy.password.dropped') }}</p>
    </fieldset>

    <fieldset class="space-y-3">
      <legend class="text-sm font-semibold text-foreground">{{ t('repoDetail.proxy.caching') }}</legend>
      <div class="grid gap-3 sm:grid-cols-2">
        <FormField :label="t('repoDetail.proxy.cacheTtl')" id="ps-cache-ttl" :error="errorOf('cache_ttl_seconds')" :hint="secondsHint('cacheTtl')">
          <input id="ps-cache-ttl" v-model="form.cacheTtl" type="text" inputmode="numeric" :placeholder="String(PROXY_LIMITS.cacheTtl.def)" :class="INPUT_CLASS" :aria-invalid="!!errorOf('cache_ttl_seconds')" />
        </FormField>
        <FormField :label="t('repoDetail.proxy.negativeCacheTtl')" id="ps-negative-ttl" :error="errorOf('negative_cache_ttl_seconds')" :hint="secondsHint('negativeCacheTtl')">
          <input id="ps-negative-ttl" v-model="form.negativeCacheTtl" type="text" inputmode="numeric" :placeholder="String(PROXY_LIMITS.negativeCacheTtl.def)" :class="INPUT_CLASS" :aria-invalid="!!errorOf('negative_cache_ttl_seconds')" />
        </FormField>
        <FormField :label="t('repoDetail.proxy.maxStale')" id="ps-max-stale" :error="errorOf('max_stale_seconds')" :hint="staleHint">
          <input id="ps-max-stale" v-model="form.maxStale" type="text" inputmode="numeric" :placeholder="String(PROXY_LIMITS.maxStale.def)" :class="INPUT_CLASS" :aria-invalid="!!errorOf('max_stale_seconds')" />
        </FormField>
        <FormField :label="t('repoDetail.proxy.minAge')" id="ps-min-age" :error="errorOf('min_age_hours')" :hint="minAgeHint">
          <input id="ps-min-age" v-model="form.minAgeHours" type="text" inputmode="numeric" placeholder="0" :class="INPUT_CLASS" :aria-invalid="!!errorOf('min_age_hours')" />
        </FormField>
      </div>
    </fieldset>

    <div class="flex items-start gap-3">
      <AppToggle v-model="form.blocked" labelledby="ps-blocked-label" :disabled="!canBlock" data-testid="blocked-toggle" />
      <div class="min-w-0 text-sm">
        <p id="ps-blocked-label" class="font-medium text-foreground">{{ t('repoDetail.proxy.blocked') }}</p>
        <p class="text-xs text-muted">{{ canBlock ? t('repoDetail.proxy.blockedHint') : t('repoDetail.proxy.blockedAdminOnly') }}</p>
        <p v-if="errorOf('blocked')" role="alert" class="text-xs text-danger">{{ errorOf('blocked') }}</p>
      </div>
    </div>

    <p v-if="formError" role="alert" class="text-sm text-danger" data-testid="proxy-form-error">{{ formError }}</p>
    <div class="flex justify-end gap-2">
      <AppButton variant="secondary" :disabled="saving" data-testid="proxy-cancel" @click="emit('cancel')">{{ t('common.cancel') }}</AppButton>
      <AppButton type="submit" :disabled="!changed || hasErrors" :loading="saving">{{ saving ? t('common.saving') : t('common.save') }}</AppButton>
    </div>
  </form>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import AppButton from '../AppButton.vue'
import AppToggle from '../AppToggle.vue'
import FormField from '../FormField.vue'
import SegmentedControl from '../ui/SegmentedControl.vue'
import { updateRepository, type Repository } from '../../api/repositories'
import { formatDuration } from '../../lib/duration'
import { INPUT_CLASS, MONO_INPUT_CLASS } from '../../lib/formClasses'
import {
  buildProxyPatch,
  dropsStoredPassword,
  effectiveSeconds,
  fieldOfServerError,
  proxyFormFrom,
  PROXY_LIMITS,
  validateProxyForm,
  type PasswordMode,
  type ProxyErrors,
  type ProxyField,
} from '../../lib/proxySettings'
import { t, tp } from '../../i18n'

const props = defineProps<{
  repo: Repository
  /** Only a global administrator may block a repository; a project maintainer may edit the rest. */
  canBlock: boolean
}>()
const emit = defineEmits<{ saved: [repo: Repository]; cancel: [] }>()

const form = reactive(proxyFormFrom(props.repo))
const saving = ref(false)
const formError = ref('')
// What the server said about a field; cleared as soon as the form changes.
const serverErrors = ref<ProxyErrors>({})

const clientErrors = computed(() => validateProxyForm(form))
const hasErrors = computed(() => Object.keys(clientErrors.value).length > 0)
const patch = computed(() => buildProxyPatch(form, props.repo, props.canBlock))
const changed = computed(() => Object.keys(patch.value).length > 0)
const droppingPassword = computed(() => dropsStoredPassword(form, props.repo))

function errorOf(field: ProxyField): string {
  return serverErrors.value[field] ?? clientErrors.value[field] ?? ''
}

function describedBy(field: ProxyField, id: string): string | undefined {
  return errorOf(field) ? `${id}-error` : undefined
}

watch(form, () => {
  serverErrors.value = {}
  formError.value = ''
})

// A password typed with nothing stored is a new one.
watch(
  () => form.upstreamPassword,
  (value) => {
    if (!props.repo.upstream_password_set) form.passwordMode = value === '' ? 'keep' : 'replace'
  },
)

const passwordOptions = computed(() => [
  { value: 'keep', label: t('repoDetail.proxy.password.keep') },
  { value: 'replace', label: t('repoDetail.proxy.password.replace') },
  { value: 'clear', label: t('repoDetail.proxy.password.clear') },
])

const passwordHint = computed(() => {
  if (!props.repo.upstream_password_set) return t('repoDetail.proxy.password.none')
  const mode: PasswordMode = form.passwordMode
  return t(`repoDetail.proxy.password.hint.${mode}` as 'repoDetail.proxy.password.hint.keep')
})

/** "= 1 hour" for a typed value, "Default: 1 hour" for an empty one. */
function secondsHint(field: 'cacheTtl' | 'negativeCacheTtl'): string {
  const raw = field === 'cacheTtl' ? form.cacheTtl : form.negativeCacheTtl
  const seconds = effectiveSeconds(raw, field)
  if (seconds === null) return ''
  return raw.trim() === '' || Number(raw) === 0
    ? tp('repoDetail.proxy.defaultValue', { duration: formatDuration(seconds) })
    : tp('repoDetail.proxy.equals', { duration: formatDuration(seconds) })
}

const staleHint = computed(() => {
  const seconds = effectiveSeconds(form.maxStale, 'maxStale')
  if (seconds === null) return ''
  if (form.maxStale.trim() === '') return tp('repoDetail.proxy.defaultValue', { duration: formatDuration(seconds) })
  return seconds === 0 ? t('repoDetail.proxy.maxStaleNever') : tp('repoDetail.proxy.equals', { duration: formatDuration(seconds) })
})

const minAgeHint = computed(() => {
  const hours = Number(form.minAgeHours.trim() || 0)
  if (!Number.isFinite(hours) || hours > PROXY_LIMITS.minAgeHours.max) return ''
  return hours === 0 ? t('repoDetail.proxy.minAgeOff') : tp('repoDetail.proxy.equals', { duration: formatDuration(hours * 3600, { maxUnits: 2 }) })
})

async function save(): Promise<void> {
  if (saving.value || !changed.value || hasErrors.value) return
  saving.value = true
  formError.value = ''
  serverErrors.value = {}
  try {
    emit('saved', await updateRepository(props.repo.name, patch.value))
  } catch (err) {
    const message = err instanceof Error ? err.message : t('repos.settings.saveFailed')
    const field = fieldOfServerError(message)
    if (field) serverErrors.value = { [field]: message }
    else formError.value = message
  } finally {
    saving.value = false
  }
}
</script>
