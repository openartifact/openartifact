<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Group membership of a service account. -->
<template>
  <AccessPanel
    :title="t('sa.groups.title')"
    :description="t('sa.groups.desc')"
    :rows="rows"
    :options="options"
    :add-label="t('sa.groups.add')"
    :remove-label="t('sa.groups.remove')"
    :empty-text="t('sa.groups.empty')"
    :loading="groups.loading.value"
    :error="groups.error.value"
    @add="join"
    @remove="leave"
  />
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { addGroupMember, listGroups, listUserGroups, removeGroupMember, type GroupItem } from '../../api/admin'
import AccessPanel from './AccessPanel.vue'
import { useAccountAccess } from '../../composables/useAccountAccess'
import { useLoadable } from '../../composables/useLoadable'
import { t, tp } from '../../i18n'

const props = defineProps<{ accountId: string }>()

interface Split {
  joined: GroupItem[]
  available: GroupItem[]
}

async function loadGroups(): Promise<Split> {
  const [joined, all] = await Promise.all([listUserGroups(props.accountId), listGroups()])
  const ids = new Set(joined.map((g) => String(g.id)))
  return { joined, available: all.items.filter((g) => !ids.has(String(g.id))) }
}

const groups = useLoadable(loadGroups, { joined: [], available: [] } as Split)
const change = useAccountAccess(groups.reload)

const rows = computed(() =>
  groups.data.value.joined.map((g) => ({ id: String(g.id), label: g.name ?? String(g.id), hint: g.description ?? undefined })),
)
const options = computed(() => groups.data.value.available.map((g) => ({ value: String(g.id), label: g.name ?? String(g.id) })))

const nameOf = (id: string): string => [...groups.data.value.joined, ...groups.data.value.available].find((g) => String(g.id) === id)?.name ?? id

const join = (id: string): Promise<void> => change(() => addGroupMember(id, props.accountId), tp('sa.groups.added', { name: nameOf(id) }))
const leave = (id: string): Promise<void> => change(() => removeGroupMember(id, props.accountId), tp('sa.groups.removed', { name: nameOf(id) }))
</script>
