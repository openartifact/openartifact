<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Everything a repository's maintainers configure, in one place: the same fields as the create drawer,
  the storage quota, cleanup policies, federation and deletion.
-->
<template>
  <div class="space-y-6">
    <form class="space-y-4 rounded-lg border border-border bg-surface p-5" novalidate :aria-label="t('rl.settings.general')" @submit.prevent="save">
      <h2 class="text-sm font-semibold text-foreground">{{ t('rl.settings.general') }}</h2>
      <FormField :label="t('repos.settings.description')" id="rl-description">
        <textarea id="rl-description" v-model="form.description" rows="2" :class="INPUT_CLASS" />
      </FormField>
      <RepoDefaultsRows v-model="form" mode="edit" :is-admin="session.isAdmin" :policy="null" :candidates="candidates" />
      <p v-if="saveError" role="alert" class="text-sm text-danger">{{ saveError }}</p>
      <div class="flex justify-end">
        <AppButton type="submit" :disabled="!dirty || !valid" :loading="saving">{{ saving ? t('common.saving') : t('common.save') }}</AppButton>
      </div>
    </form>

    <section class="space-y-4 rounded-lg border border-border bg-surface p-5" :aria-label="t('repos.settings.quota')">
      <h2 class="text-sm font-semibold text-foreground">{{ t('repos.settings.quota') }}</h2>
      <p v-if="usage" class="text-sm text-muted" data-testid="usage-line">
        {{ tp('rl.settings.usage', { packages: String(usage.package_count), versions: String(usage.version_count), size: formatBytes(usage.size_bytes) }) }}
      </p>
      <QuotaBar v-if="usage" :used="usage.quota.used_bytes" :limit="usage.quota.limit_bytes" />
      <p v-else-if="usageFailed" class="text-xs text-muted">{{ t('rl.settings.usageUnavailable') }}</p>
      <AppButton variant="secondary" size="sm" :aria-expanded="quotaOpen" aria-controls="rl-quota-editor" data-testid="quota-toggle" @click="quotaOpen = !quotaOpen">
        {{ quotaOpen ? t('rl.settings.hideQuota') : t('rl.settings.editQuota') }}
      </AppButton>
      <div v-if="quotaOpen" id="rl-quota-editor"><RepoQuotaTab :repo-name="repo.name" @changed="loadUsage" /></div>
    </section>

    <ApkKeyPanel v-if="repo.format === 'apk' && repo.repo_type !== 'proxy'" :repo="repo.name" />
    <NixKeyPanel v-if="repo.format === 'nix' && repo.repo_type === 'hosted'" :repo="repo.name" />

    <section class="space-y-4 rounded-lg border border-border bg-surface p-5" :aria-label="t('repoDetail.tab.cleanup')">
      <h2 class="text-sm font-semibold text-foreground">{{ t('repoDetail.tab.cleanup') }}</h2>
      <RepoCleanupTab :repo-name="repo.name" />
    </section>

    <section v-if="session.isAdmin && showsFederation(repo)" class="space-y-4 rounded-lg border border-border bg-surface p-5" :aria-label="t('federation.tab')">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div class="min-w-0">
          <h2 class="text-sm font-semibold text-foreground">{{ t('federation.tab') }}</h2>
          <p v-if="!federationShown" class="mt-1 max-w-xl text-sm text-muted" data-testid="federation-off">{{ t('rl.federation.off') }}</p>
        </div>
        <AppButton v-if="!federationShown" variant="secondary" size="sm" data-testid="federation-setup" @click="federationOpen = true">{{ t('rl.federation.setup') }}</AppButton>
      </div>
      <RepoFederationTab v-if="federationShown" :repo="repo" @toggled="onFederationToggled" />
    </section>

    <section class="space-y-3 rounded-lg border border-danger/40 bg-surface p-5" :aria-label="t('rl.settings.danger')">
      <h2 class="text-sm font-semibold text-danger">{{ t('rl.settings.danger') }}</h2>
      <p class="max-w-xl text-sm text-muted">{{ t('rl.settings.deleteHint') }}</p>
      <AppButton variant="danger-outline" data-testid="delete-repo" @click="deleteOpen = true">{{ t('rl.settings.delete') }}</AppButton>
    </section>

    <RepoDeleteDialog :open="deleteOpen" :name="repo.name" :busy="deleting" @close="deleteOpen = false" @confirm="remove" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import AppButton from '../AppButton.vue'
import FormField from '../FormField.vue'
import RepoDeleteDialog from '../RepoDeleteDialog.vue'
import ApkKeyPanel from './ApkKeyPanel.vue'
import NixKeyPanel from './NixKeyPanel.vue'
import QuotaBar from './QuotaBar.vue'
import RepoCleanupTab from './RepoCleanupTab.vue'
import RepoDefaultsRows from './RepoDefaultsRows.vue'
import RepoFederationTab from './RepoFederationTab.vue'
import RepoQuotaTab from './RepoQuotaTab.vue'
import {
  deleteRepository,
  getRepositoryUsage,
  listAllRepositories,
  updateRepoMembers,
  updateRepository,
  type Repository,
  type RepositoryUsage,
} from '../../api/repositories'
import { showsFederation } from '../../lib/federation'
import { INPUT_CLASS } from '../../lib/formClasses'
import { formatBytes } from '../../lib/quota'
import { buildUpdateBody, formValid, repoFormFrom } from '../../lib/repoForm'
import { useSessionStore } from '../../stores/session'
import { useToastStore } from '../../stores/toast'
import { t, tp } from '../../i18n'

const props = defineProps<{ repo: Repository }>()
const emit = defineEmits<{ saved: [repo: Repository] }>()

const session = useSessionStore()
const toasts = useToastStore()
const router = useRouter()

const form = ref(repoFormFrom(props.repo))
const initial = ref(JSON.stringify(form.value))
const dirty = computed(() => JSON.stringify(form.value) !== initial.value)
// The name is fixed after creation, so the create-time name rules do not apply: an empty prefix and the stored name always pass.
// Unlike at creation, no proxy format falls back to a default upstream here: the server refuses an empty upstream_url on update.
const valid = computed(() => formValid(form.value, '', form.value.type === 'proxy'))
const saving = ref(false)
const saveError = ref('')

function reset(repo: Repository): void {
  form.value = repoFormFrom(repo)
  initial.value = JSON.stringify(form.value)
}
watch(() => props.repo, reset)

const others = ref<Repository[]>([])
const candidates = computed(() =>
  others.value.filter((r) => r.format === props.repo.format && r.repo_type !== 'group' && r.name !== props.repo.name).map((r) => r.name),
)

const usage = ref<RepositoryUsage | null>(null)
const usageFailed = ref(false)
const quotaOpen = ref(false)

async function loadUsage(): Promise<void> {
  try {
    usage.value = await getRepositoryUsage(props.repo.name)
    usageFailed.value = false
  } catch {
    usage.value = null
    usageFailed.value = true
  }
}

onMounted(async () => {
  void loadUsage()
  if (props.repo.repo_type === 'group') {
    try {
      others.value = await listAllRepositories()
    } catch {
      others.value = []
    }
  }
})

async function save(): Promise<void> {
  if (saving.value || !dirty.value || !valid.value) return
  saving.value = true
  saveError.value = ''
  try {
    let updated = await updateRepository(props.repo.name, buildUpdateBody(form.value, props.repo, { isAdmin: session.isAdmin }))
    const before = props.repo.member_repos ?? []
    if (props.repo.repo_type === 'group' && form.value.members.join('\n') !== before.join('\n')) {
      await updateRepoMembers(props.repo.name, form.value.members)
      updated = { ...updated, member_repos: [...form.value.members] }
    }
    toasts.success(t('rl.settings.saved'))
    emit('saved', updated)
  } catch (err) {
    saveError.value = err instanceof Error ? err.message : t('repos.settings.saveFailed')
  } finally {
    saving.value = false
  }
}

// Federation: a repository without it shows one row that says so and offers the set-up.
const federationOpen = ref(false)
const federationShown = computed(() => federationOpen.value || props.repo.federated === true)

function onFederationToggled(enabled: boolean): void {
  emit('saved', { ...props.repo, federated: enabled })
}

const deleteOpen = ref(false)
const deleting = ref(false)

async function remove(): Promise<void> {
  deleting.value = true
  try {
    await deleteRepository(props.repo.name)
    deleteOpen.value = false
    await router.push('/repos')
  } catch (err) {
    toasts.error(t('repoDetail.deleteRepoFailed'), err instanceof Error ? err.message : undefined)
  } finally {
    deleting.value = false
  }
}
</script>
