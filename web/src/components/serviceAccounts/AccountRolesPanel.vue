<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Global roles of a service account: the permission targets it is assigned to. -->
<template>
  <AccessPanel
    :title="t('sa.roles.title')"
    :description="t('sa.roles.desc')"
    :rows="rows"
    :options="options"
    :add-label="t('sa.roles.add')"
    :remove-label="t('sa.roles.remove')"
    :empty-text="t('sa.roles.empty')"
    :no-options-text="targets.data.value.available.length === 0 ? t('sa.roles.noTargets') : undefined"
    :loading="targets.loading.value"
    :error="targets.error.value"
    @add="assign"
    @remove="unassign"
  />
</template>

<script setup lang="ts">
import { computed } from 'vue'
import {
  assignPermissionTarget,
  listPermissionAssignments,
  listPermissions,
  unassignPermissionTarget,
  type PermissionItem,
} from '../../api/admin'
import AccessPanel from './AccessPanel.vue'
import { useAccountAccess } from '../../composables/useAccountAccess'
import { useLoadable } from '../../composables/useLoadable'
import { t, tp } from '../../i18n'

const props = defineProps<{ accountId: string }>()

interface Split {
  assigned: PermissionItem[]
  available: PermissionItem[]
}

// There is no per-principal endpoint: every target's assignments are read and the account's picked out.
async function loadTargets(): Promise<Split> {
  const { items } = await listPermissions()
  const assignments = await Promise.all(items.map((p) => listPermissionAssignments(String(p.id))))
  const split: Split = { assigned: [], available: [] }
  items.forEach((p, i) => {
    const held = assignments[i]!.some((a) => a.principal_type === 'user' && a.principal_id === props.accountId)
    ;(held ? split.assigned : split.available).push(p)
  })
  return split
}

const targets = useLoadable(loadTargets, { assigned: [], available: [] } as Split)
const change = useAccountAccess(targets.reload)

const rows = computed(() =>
  targets.data.value.assigned.map((p) => ({
    id: String(p.id),
    label: p.name ?? String(p.id),
    hint: `${p.repository_filter ?? ''}  ${(p.actions ?? []).join(', ')}`.trim(),
  })),
)
const options = computed(() => targets.data.value.available.map((p) => ({ value: String(p.id), label: p.name ?? String(p.id) })))

const principal = () => ({ principal_type: 'user' as const, principal_id: props.accountId })
const nameOf = (id: string): string => [...targets.data.value.assigned, ...targets.data.value.available].find((p) => String(p.id) === id)?.name ?? id

const assign = (id: string): Promise<void> => change(() => assignPermissionTarget(id, principal()), tp('sa.roles.added', { name: nameOf(id) }))
const unassign = (id: string): Promise<void> => change(() => unassignPermissionTarget(id, principal()), tp('sa.roles.removed', { name: nameOf(id) }))
</script>
