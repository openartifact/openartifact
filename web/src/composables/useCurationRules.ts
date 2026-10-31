// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, ref, toValue, watch, type MaybeRefOrGetter } from 'vue'
import {
  createCurationRule,
  deleteCurationRule,
  listCurationRules,
  setCurationMode,
  updateCurationRule,
  type CurationMode,
  type CurationRepository,
  type CurationRule,
} from '../api/curation'
import { emptyRuleForm, modeText, ruleFormFrom, ruleFormValid, ruleInput, type RuleForm } from '../lib/curation'
import { useToastStore } from '../stores/toast'
import { t, tp } from '../i18n'

/**
 * The curation panel of a proxy repository: its mode, its rules, and the add/edit drawer and delete confirmation.
 * `onMode` receives the repository the server answered a mode change with, so the page that owns it can adopt it.
 */
export function useCurationRules(repoName: MaybeRefOrGetter<string>, initialMode: MaybeRefOrGetter<CurationMode>, onMode: (repo: CurationRepository) => void) {
  const toasts = useToastStore()

  const mode = ref<CurationMode>(toValue(initialMode))
  const savingMode = ref(false)
  watch(
    () => toValue(initialMode),
    (next) => {
      mode.value = next
    },
  )

  const rules = ref<CurationRule[]>([])
  const loading = ref(true)
  const loadError = ref('')
  let generation = 0

  async function load(): Promise<void> {
    const mine = ++generation
    loadError.value = ''
    try {
      const res = await listCurationRules(toValue(repoName))
      if (mine === generation) rules.value = res
    } catch (err) {
      if (mine === generation) loadError.value = err instanceof Error ? err.message : t('curation.loadFailed')
    } finally {
      if (mine === generation) loading.value = false
    }
  }

  async function changeMode(next: CurationMode): Promise<void> {
    if (savingMode.value || next === mode.value) return
    const previous = mode.value
    mode.value = next
    savingMode.value = true
    try {
      const updated = await setCurationMode(toValue(repoName), next)
      mode.value = updated.curation_mode ?? next
      onMode(updated)
      toasts.success(tp('curation.modeSaved', { mode: modeText(mode.value).label }))
    } catch (err) {
      mode.value = previous
      toasts.error(t('curation.modeFailed'), err instanceof Error ? err.message : undefined)
    } finally {
      savingMode.value = false
    }
  }

  // ─── Add / edit drawer ──────────────────────────────────────────────────────────
  const drawerOpen = ref(false)
  const editing = ref<CurationRule | null>(null)
  const form = ref<RuleForm>(emptyRuleForm(mode.value))
  const formValid = computed(() => ruleFormValid(form.value))
  const saving = ref(false)
  const formError = ref('')

  function openAdd(): void {
    editing.value = null
    form.value = emptyRuleForm(mode.value)
    formError.value = ''
    drawerOpen.value = true
  }

  function openEdit(rule: CurationRule): void {
    editing.value = rule
    form.value = ruleFormFrom(rule)
    formError.value = ''
    drawerOpen.value = true
  }

  function closeDrawer(): void {
    drawerOpen.value = false
  }

  async function save(): Promise<void> {
    if (saving.value || !formValid.value) return
    saving.value = true
    formError.value = ''
    const body = ruleInput(form.value)
    const repo = toValue(repoName)
    try {
      if (editing.value) await updateCurationRule(repo, editing.value.id, body)
      else await createCurationRule(repo, body)
      toasts.success(t(editing.value ? 'curation.updated' : 'curation.created'))
      closeDrawer()
      await load()
    } catch (err) {
      // A pattern the server rejects stays in the drawer, with the server's reason, so it can be corrected.
      formError.value = err instanceof Error ? err.message : t('curation.saveFailed')
    } finally {
      saving.value = false
    }
  }

  // ─── Delete ─────────────────────────────────────────────────────────────────────
  const deleteTarget = ref<CurationRule | null>(null)
  const deleting = ref(false)

  async function confirmDelete(): Promise<void> {
    const target = deleteTarget.value
    if (!target || deleting.value) return
    deleting.value = true
    try {
      await deleteCurationRule(toValue(repoName), target.id)
      toasts.success(t('curation.deleted'))
      deleteTarget.value = null
      await load()
    } catch (err) {
      toasts.error(t('curation.deleteFailed'), err instanceof Error ? err.message : undefined)
    } finally {
      deleting.value = false
    }
  }

  /** An allowlist with no allow rule makes every version unavailable: worth saying before it surprises somebody. */
  const allowlistEmpty = computed(() => mode.value === 'allowlist' && !rules.value.some((r) => r.action === 'allow'))

  return {
    mode,
    savingMode,
    rules,
    loading,
    loadError,
    allowlistEmpty,
    load,
    changeMode,
    drawerOpen,
    editing,
    form,
    formValid,
    saving,
    formError,
    openAdd,
    openEdit,
    closeDrawer,
    save,
    deleteTarget,
    deleting,
    confirmDelete,
  }
}
