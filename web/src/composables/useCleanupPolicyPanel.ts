// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, ref } from 'vue'
import { EyeIcon, PencilSquareIcon, PlayIcon, TrashIcon } from '@heroicons/vue/20/solid'
import type { RowAction } from '../components/ui/RowActions.vue'
import type { CleanupPreview } from '../api/cleanup'
import {
  cleanupFormFrom,
  cleanupFormValid,
  emptyCleanupForm,
  type CleanupPolicyForm,
  type CleanupPolicyView,
} from '../lib/cleanupPolicy'
import { t } from '../i18n'

/**
 * The UI state both cleanup policy lists (repository and project scope) share:
 * the create/edit drawer, the delete confirmation, the dry-run preview and the
 * row action buttons. Loading and the API calls stay with the list.
 */
export function useCleanupPolicyPanel() {
  const drawerOpen = ref(false)
  const editing = ref<CleanupPolicyView | null>(null)
  const form = ref<CleanupPolicyForm>(emptyCleanupForm())
  const formValid = computed(() => cleanupFormValid(form.value))
  const saving = ref(false)
  const togglingId = ref<string | null>(null)
  const deleteTarget = ref<CleanupPolicyView | null>(null)
  const deleting = ref(false)
  const previewingId = ref<string | null>(null)
  const preview = ref<{ name: string; repositories: CleanupPreview[] } | null>(null)

  function openDrawer(policy?: CleanupPolicyView): void {
    editing.value = policy ?? null
    form.value = policy ? cleanupFormFrom(policy) : emptyCleanupForm()
    drawerOpen.value = true
  }

  function closeDrawer(): void {
    drawerOpen.value = false
    editing.value = null
  }

  /** Dry-run, edit and delete; plus "run now" when `runningId` is given (repository scope). */
  function rowActions(id: string, runningId?: string | null): RowAction[] {
    const run: RowAction[] =
      runningId === undefined ? [] : [{ key: 'run', label: t('cleanup.run'), icon: PlayIcon, loading: runningId === id }]
    return [
      ...run,
      { key: 'dry-run', label: t('cleanup.dryRun'), icon: EyeIcon, loading: previewingId.value === id },
      { key: 'edit', label: t('cleanup.editTitle'), icon: PencilSquareIcon },
      { key: 'delete', label: t('cleanup.deleteTitle'), icon: TrashIcon, tone: 'danger' },
    ]
  }

  return {
    drawerOpen,
    editing,
    form,
    formValid,
    saving,
    togglingId,
    deleteTarget,
    deleting,
    previewingId,
    preview,
    openDrawer,
    closeDrawer,
    rowActions,
  }
}
