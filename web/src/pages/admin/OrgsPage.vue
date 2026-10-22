<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <PageHeader :title="t('adm.orgs.title')" :description="t('adm.orgs.description')">
      <template #actions>
        <button
          type="button"
          class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium bg-primary text-primary-contrast hover:opacity-90 transition-opacity"
          @click="openCreate"
        >
          <PlusIcon class="w-4 h-4" aria-hidden="true" />
          {{ t('adm.orgs.new') }}
        </button>
      </template>
    </PageHeader>

    <!-- What organization roles grant -->
    <div class="bg-surface rounded-xl border border-border p-4 text-xs text-muted space-y-1.5" data-testid="org-effect">
      <p class="font-semibold text-foreground">{{ t('adm.orgs.effect.title') }}</p>
      <p>{{ t('adm.orgs.effect.owner') }}</p>
      <p>{{ t('adm.orgs.effect.member') }}</p>
      <p>{{ t('adm.orgs.effect.limits') }}</p>
    </div>

    <!-- Stat Tiles -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <div class="w-9 h-9 rounded-lg flex items-center justify-center shrink-0 bg-primary/10">
          <BuildingOfficeIcon class="w-5 h-5 text-primary" aria-hidden="true" />
        </div>
        <div class="min-w-0">
          <p class="text-[10px] font-mono font-semibold text-muted truncate">{{ t('adm.orgs.stat.orgs') }}</p>
          <p class="text-xl font-bold tabular-nums text-foreground leading-tight">{{ items.length }}</p>
        </div>
      </div>
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <div class="w-9 h-9 rounded-lg flex items-center justify-center shrink-0 bg-success/10">
          <UsersIcon class="w-5 h-5 text-success" aria-hidden="true" />
        </div>
        <div class="min-w-0">
          <p class="text-[10px] font-mono font-semibold text-muted truncate">{{ t('adm.orgs.stat.memberships') }}</p>
          <p class="text-xl font-bold tabular-nums text-foreground leading-tight">{{ totalMembers }}</p>
        </div>
      </div>
    </div>

    <!-- Loading skeleton -->
    <div v-if="loading" class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-5 py-4 border-b border-border">
        <div class="h-4 w-40 rounded skel" />
      </div>
      <div v-for="i in 4" :key="i" class="flex items-center gap-4 px-5 py-4 border-b border-border last:border-0">
        <div class="h-3 w-36 rounded skel" />
        <div class="h-5 w-12 rounded-full skel" />
        <div class="h-3 w-28 rounded skel" />
      </div>
    </div>

    <div v-else-if="loadError" class="bg-surface rounded-xl border border-border px-5 py-8 text-center text-danger" role="alert">
      {{ loadError }}
    </div>

    <!-- Table -->
    <div v-else class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-5 py-4 flex items-center gap-2 border-b border-border">
        <h2 class="text-sm font-semibold text-foreground">{{ t('adm.orgs.title') }}</h2>
        <span class="px-1.5 py-0.5 rounded-md bg-surface-hover text-[11px] font-mono font-medium text-muted">
          {{ items.length }}
        </span>
      </div>

      <div v-if="items.length === 0" class="py-16 text-center">
        <div class="w-12 h-12 rounded-full bg-surface-hover mx-auto flex items-center justify-center mb-3">
          <BuildingOfficeIcon class="w-6 h-6 text-muted" aria-hidden="true" />
        </div>
        <p class="text-sm font-medium text-foreground">{{ t('adm.orgs.empty') }}</p>
        <p class="text-xs text-muted mt-1 mb-4">{{ t('adm.orgs.emptyHint') }}</p>
        <button
          type="button"
          class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium bg-primary text-primary-contrast hover:opacity-90 transition-opacity"
          @click="openCreate"
        >
          <PlusIcon class="w-4 h-4" aria-hidden="true" />
          {{ t('adm.orgs.new') }}
        </button>
      </div>

      <table v-else class="w-full">
        <thead>
          <tr class="bg-surface-hover/30">
            <th scope="col" class="text-left text-[11px] font-semibold text-muted px-5 py-2.5">{{ t('adm.orgs.column.name') }}</th>
            <th scope="col" class="text-left text-[11px] font-semibold text-muted px-3 py-2.5">{{ t('adm.orgs.column.members') }}</th>
            <th scope="col" class="text-left text-[11px] font-semibold text-muted px-3 py-2.5">{{ t('adm.orgs.column.memberRole') }}</th>
            <th scope="col" class="text-left text-[11px] font-semibold text-muted px-3 py-2.5">{{ t('adm.orgs.column.created') }}</th>
            <th scope="col" class="text-right text-[11px] font-semibold text-muted px-5 py-2.5">{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          <tr v-for="org in items" :key="org.id" class="hover:bg-surface-hover/30 transition-colors">
            <td class="px-5 py-3.5">
              <div class="flex items-center gap-2.5">
                <div class="w-8 h-8 rounded-md bg-primary/10 flex items-center justify-center shrink-0">
                  <BuildingOfficeIcon class="w-4 h-4 text-primary" aria-hidden="true" />
                </div>
                <div class="min-w-0">
                  <p class="font-mono text-[13px] font-medium text-foreground truncate">{{ org.name }}</p>
                  <p v-if="org.display_name && org.display_name !== org.name" class="text-[11px] text-muted truncate">
                    {{ org.display_name }}
                  </p>
                </div>
              </div>
            </td>
            <td class="px-3 py-3.5">
              <button
                type="button"
                :aria-label="`${t('adm.orgs.manageMembers')}: ${org.name}`"
                class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-xs font-mono bg-surface-hover text-foreground hover:bg-surface-hover/80 transition-colors"
                @click="openMembers(org)"
              >
                <UsersIcon class="w-3 h-3 text-muted" aria-hidden="true" />
                {{ memberCounts[org.id] ?? '—' }}
              </button>
            </td>
            <td class="px-3 py-3.5">
              <span class="font-mono text-xs text-foreground">{{ org.member_role ?? 'read' }}</span>
            </td>
            <td class="px-3 py-3.5">
              <span class="font-mono text-xs text-muted">{{ formatDate(org.created_at) }}</span>
            </td>
            <td class="px-5 py-3.5">
              <div class="flex items-center gap-0.5 justify-end">
                <button
                  type="button"
                  :title="t('adm.orgs.manageMembers')"
                  :aria-label="`${t('adm.orgs.manageMembers')}: ${org.name}`"
                  class="p-1.5 rounded-md text-muted hover:text-primary hover:bg-primary/10 transition-colors"
                  @click="openMembers(org)"
                >
                  <UsersIcon class="w-4 h-4" aria-hidden="true" />
                </button>
                <button
                  type="button"
                  :title="t('adm.orgs.edit')"
                  :aria-label="`${t('adm.orgs.edit')}: ${org.name}`"
                  class="p-1.5 rounded-md text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
                  @click="openEdit(org)"
                >
                  <PencilIcon class="w-4 h-4" aria-hidden="true" />
                </button>
                <button
                  type="button"
                  :title="t('adm.orgs.delete')"
                  :aria-label="`${t('adm.orgs.delete')}: ${org.name}`"
                  class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors"
                  @click="openDelete(org)"
                >
                  <TrashIcon class="w-4 h-4" aria-hidden="true" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Org drawer (create / edit) -->
    <AdminDialog
      :open="orgDrawerOpen"
      drawer
      max-width="max-w-lg"
      :eyebrow="orgDrawerMode === 'create' ? t('adm.orgs.new') : t('adm.orgs.edit')"
      :title="orgDrawerMode === 'create' ? t('adm.orgs.createTitle') : `${t('adm.orgs.edit')} · ${editingOrg?.name ?? ''}`"
      @close="closeOrgDrawer"
    >
      <form id="org-form" class="space-y-4" @submit.prevent="saveOrg">
        <div v-if="orgDrawerMode === 'create'">
          <label for="org-name" class="block text-xs font-semibold text-foreground mb-1.5">
            {{ t('adm.orgs.field.name') }} <span class="text-danger ml-0.5">*</span>
          </label>
          <input
            id="org-name"
            v-model="orgForm.name"
            type="text"
            required
            placeholder="my-org"
            class="w-full px-3 py-2 rounded-lg border border-border bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:outline-none focus:ring-2 focus:ring-primary transition-colors"
          />
          <p class="text-[11px] text-muted mt-1 font-mono">{{ t('adm.orgs.field.nameHint') }}</p>
        </div>
        <div>
          <label for="org-display-name" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.orgs.field.displayName') }}</label>
          <input
            id="org-display-name"
            v-model="orgForm.display_name"
            type="text"
            class="w-full px-3 py-2 rounded-lg border border-border bg-bg text-foreground text-sm placeholder:text-muted focus:outline-none focus:ring-2 focus:ring-primary transition-colors"
          />
        </div>
        <div>
          <label for="org-member-role" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.orgs.field.memberRole') }}</label>
          <select
            id="org-member-role"
            v-model="orgForm.member_role"
            class="w-full px-3 py-2 rounded-lg border border-border bg-bg text-foreground text-sm font-mono focus:outline-none focus:ring-2 focus:ring-primary transition-colors"
          >
            <option v-for="level in MEMBER_ROLES" :key="level" :value="level">{{ t(`adm.orgs.memberRole.${level}`) }}</option>
          </select>
          <p class="text-[11px] text-muted mt-1">{{ t('adm.orgs.field.memberRoleHint') }}</p>
        </div>
        <div>
          <label for="org-description" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.orgs.field.description') }}</label>
          <textarea
            id="org-description"
            v-model="orgForm.description"
            rows="3"
            class="w-full px-3 py-2 rounded-lg border border-border bg-bg text-foreground text-sm placeholder:text-muted focus:outline-none focus:ring-2 focus:ring-primary resize-none transition-colors"
          />
        </div>
      </form>
      <template #footer>
        <button
          type="button"
          class="px-3 py-1.5 rounded-lg text-sm font-medium text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
          @click="closeOrgDrawer"
        >
          {{ t('common.cancel') }}
        </button>
        <button
          type="submit"
          form="org-form"
          :disabled="orgSaving || (orgDrawerMode === 'create' && !orgForm.name.trim())"
          class="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-lg text-sm font-medium bg-primary text-primary-contrast hover:opacity-90 transition-opacity disabled:opacity-50 disabled:cursor-not-allowed"
        >
          <ArrowPathIcon v-if="orgSaving" class="w-4 h-4 animate-spin" aria-hidden="true" />
          {{ orgDrawerMode === 'create' ? t('adm.orgs.createAction') : t('adm.orgs.saveAction') }}
        </button>
      </template>
    </AdminDialog>

    <!-- Members drawer -->
    <AdminDialog
      :open="membersDrawerOpen"
      drawer
      max-width="max-w-lg"
      :eyebrow="t('adm.orgs.column.members')"
      :title="managingOrg?.name ?? ''"
      @close="closeMembersDrawer"
    >
      <div class="-mx-6 -my-5">
        <form class="px-6 py-4 border-b border-border bg-surface-hover/20 space-y-3" @submit.prevent="addMember">
          <p class="text-xs font-semibold text-foreground">{{ t('adm.orgs.addMember') }}</p>
          <div class="flex items-center gap-2">
            <select
              v-model="newMemberUserId"
              :aria-label="t('adm.orgs.field.user')"
              class="flex-1 min-w-0 px-3 py-2 rounded-lg border border-border bg-bg text-foreground text-sm font-mono focus:outline-none focus:ring-2 focus:ring-primary transition-colors"
            >
              <option value="">{{ t('adm.orgs.selectUser') }}</option>
              <option v-for="u in availableUsers" :key="u.id" :value="u.id">{{ u.username }}</option>
            </select>
            <select
              v-model="newMemberRole"
              :aria-label="t('adm.orgs.field.role')"
              class="px-2.5 py-2 rounded-lg border border-border bg-bg text-foreground text-sm focus:outline-none focus:ring-2 focus:ring-primary shrink-0"
            >
              <option v-for="role in ORG_ROLES" :key="role" :value="role">{{ role }}</option>
            </select>
            <button
              type="submit"
              :disabled="!newMemberUserId || addingMember"
              class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium bg-primary text-primary-contrast hover:opacity-90 transition-opacity disabled:opacity-50 disabled:cursor-not-allowed shrink-0"
            >
              <ArrowPathIcon v-if="addingMember" class="w-4 h-4 animate-spin" aria-hidden="true" />
              <PlusIcon v-else class="w-4 h-4" aria-hidden="true" />
              {{ t('adm.orgs.add') }}
            </button>
          </div>
        </form>

        <div>
          <div v-if="membersLoading" class="px-6 py-8 text-center text-sm text-muted">{{ t('adm.orgs.loadingMembers') }}</div>
          <div v-else-if="members.length === 0" class="px-6 py-12 text-center">
            <UsersIcon class="w-10 h-10 text-muted mx-auto mb-3" aria-hidden="true" />
            <p class="text-sm font-medium text-foreground">{{ t('adm.orgs.noMembers') }}</p>
            <p class="text-xs text-muted mt-1">{{ t('adm.orgs.noMembersHint') }}</p>
          </div>
          <table v-else class="w-full">
            <thead>
              <tr class="bg-surface-hover/30">
                <th scope="col" class="text-left text-[11px] font-semibold text-muted px-6 py-2.5">{{ t('adm.orgs.column.user') }}</th>
                <th scope="col" class="text-left text-[11px] font-semibold text-muted px-3 py-2.5">{{ t('adm.orgs.column.role') }}</th>
                <th scope="col" class="text-left text-[11px] font-semibold text-muted px-3 py-2.5">{{ t('adm.orgs.column.joined') }}</th>
                <th scope="col" class="w-10"><span class="sr-only">{{ t('common.actions') }}</span></th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border">
              <tr v-for="member in members" :key="member.user_id" class="hover:bg-surface-hover/30 transition-colors group">
                <td class="px-6 py-3">
                  <div class="flex items-center gap-2">
                    <div class="w-7 h-7 rounded-full bg-primary/20 flex items-center justify-center shrink-0" aria-hidden="true">
                      <span class="text-primary text-xs font-medium">{{ memberName(member)[0]?.toUpperCase() ?? '?' }}</span>
                    </div>
                    <span class="font-mono text-[12px] text-foreground">{{ memberName(member) }}</span>
                  </div>
                </td>
                <td class="px-3 py-3">
                  <span
                    class="inline-flex items-center px-2 py-0.5 rounded-full text-[11px] font-medium"
                    :class="member.role === 'owner' ? 'bg-warning-soft text-warning' : 'bg-surface-hover text-muted'"
                  >
                    {{ member.role }}
                  </span>
                </td>
                <td class="px-3 py-3">
                  <span class="font-mono text-xs text-muted">{{ formatDate(member.created_at) }}</span>
                </td>
                <td class="px-3 py-3">
                  <button
                    type="button"
                    :title="t('adm.orgs.removeMember')"
                    :aria-label="`${t('adm.orgs.removeMember')}: ${memberName(member)}`"
                    class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors opacity-60 group-hover:opacity-100 focus:opacity-100"
                    @click="removeMember(member)"
                  >
                    <TrashIcon class="w-4 h-4" aria-hidden="true" />
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
      <template #footer>
        <button
          type="button"
          class="px-3 py-1.5 rounded-lg text-sm font-medium text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
          @click="closeMembersDrawer"
        >
          {{ t('common.close') }}
        </button>
      </template>
    </AdminDialog>

    <!-- Delete confirmation -->
    <AdminDialog
      :open="!!deleteTarget"
      :title="t('adm.orgs.deleteTitle')"
      :description="t('adm.orgs.deleteHint')"
      @close="cancelDelete"
    >
      <div v-if="deleteTarget" class="space-y-4">
        <div class="rounded-lg border border-danger/30 bg-danger-soft p-3">
          <div class="flex items-start gap-2.5">
            <ExclamationCircleIcon class="w-4 h-4 text-danger mt-0.5 shrink-0" aria-hidden="true" />
            <div>
              <p class="text-xs font-semibold text-danger">{{ t('adm.orgs.deleteWarnTitle') }}</p>
              <p class="text-[11px] text-danger/80 mt-0.5 leading-relaxed">{{ t('adm.orgs.deleteWarnBody') }}</p>
            </div>
          </div>
        </div>
        <div>
          <label for="org-delete-confirm" class="block text-xs font-semibold text-foreground mb-1.5">
            {{ t('adm.orgs.typeToConfirm') }}
            <code class="font-mono text-danger bg-danger-soft px-1 py-0.5 rounded mx-1">{{ deleteTarget.name }}</code>
          </label>
          <input
            id="org-delete-confirm"
            v-model="deleteConfirmName"
            type="text"
            :placeholder="deleteTarget.name"
            class="w-full px-3 py-2 rounded-lg border bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:outline-none focus:ring-2 transition-colors border-border focus:ring-danger"
          />
        </div>
      </div>
      <template #footer>
        <button
          type="button"
          class="px-3 py-1.5 rounded-lg text-sm font-medium text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
          @click="cancelDelete"
        >
          {{ t('common.cancel') }}
        </button>
        <button
          type="button"
          :disabled="!deleteTarget || deleteConfirmName !== deleteTarget.name || deleting"
          class="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-lg text-sm font-medium bg-danger text-danger-contrast hover:opacity-90 transition-opacity disabled:opacity-40 disabled:cursor-not-allowed"
          @click="confirmDelete"
        >
          <ArrowPathIcon v-if="deleting" class="w-4 h-4 animate-spin" aria-hidden="true" />
          <TrashIcon v-else class="w-4 h-4" aria-hidden="true" />
          {{ t('adm.orgs.delete') }}
        </button>
      </template>
    </AdminDialog>
  </div>
</template>

<script setup lang="ts">
import PageHeader from '../../components/PageHeader.vue'
import { formatDate } from '../../lib/datetime'
import { ref, computed, onMounted } from 'vue'
import { request } from '../../api/client'
import { listAllUsers, type UserItem } from '../../api/admin'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'
import AdminDialog from '../../components/AdminDialog.vue'
import {
  BuildingOfficeIcon,
  UsersIcon,
  PlusIcon,
  PencilIcon,
  TrashIcon,
  ArrowPathIcon,
  ExclamationCircleIcon,
} from '@heroicons/vue/24/outline'

interface Org {
  id: string
  name: string
  display_name?: string | null
  description?: string | null
  member_role?: MemberRole
  created_at?: string
}

interface OrgMember {
  id?: string
  user_id: string
  role: string
  created_at?: string
}

const ORG_ROLES = ['member', 'admin', 'owner'] as const
const MEMBER_ROLES = ['read', 'write', 'admin'] as const
type MemberRole = (typeof MEMBER_ROLES)[number]

const toasts = useToastStore()
const items = ref<Org[]>([])
const users = ref<UserItem[]>([])
const memberCounts = ref<Record<string, number>>({})
const loading = ref(true)
const loadError = ref('')

const totalMembers = computed(() => Object.values(memberCounts.value).reduce((s, n) => s + n, 0))

// Org drawer
const orgDrawerOpen = ref(false)
const orgDrawerMode = ref<'create' | 'edit'>('create')
const editingOrg = ref<Org | null>(null)
const orgSaving = ref(false)
const orgForm = ref<{ name: string; display_name: string; description: string; member_role: MemberRole }>({
  name: '',
  display_name: '',
  description: '',
  member_role: 'read',
})

// Members drawer
const membersDrawerOpen = ref(false)
const managingOrg = ref<Org | null>(null)
const members = ref<OrgMember[]>([])
const membersLoading = ref(false)
const newMemberUserId = ref('')
const newMemberRole = ref<(typeof ORG_ROLES)[number]>('member')
const addingMember = ref(false)

// Delete
const deleteTarget = ref<Org | null>(null)
const deleteConfirmName = ref('')
const deleting = ref(false)

const availableUsers = computed(() => {
  const memberIds = new Set(members.value.map((m) => m.user_id))
  return users.value.filter((u) => u.id && !memberIds.has(u.id) && !u.disabled)
})


// The member payload carries only user_id; resolve the username from the user list.
function memberName(member: OrgMember): string {
  return users.value.find((u) => u.id === member.user_id)?.username ?? member.user_id
}

async function fetchMembers(orgName: string): Promise<OrgMember[]> {
  const res = await request<{ members?: OrgMember[] }>(`/api/v1/orgs/${encodeURIComponent(orgName)}/members`)
  return res?.members ?? []
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const [res, usersRes] = await Promise.all([
      request<{ organizations?: Org[] }>('/api/v1/orgs'),
      // Listing users is admin-only on some deployments; names fall back to ids.
      listAllUsers().catch(() => ({ total: 0, items: [] as UserItem[] })),
    ])
    items.value = res?.organizations ?? []
    users.value = usersRes.items
    const lists = await Promise.all(items.value.map((o) => fetchMembers(o.name).catch(() => null)))
    const counts: Record<string, number> = {}
    items.value.forEach((o, i) => {
      const list = lists[i]
      if (list) counts[o.id] = list.length
    })
    memberCounts.value = counts
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : t('adm.orgs.loadFailed')
    toasts.error(loadError.value)
  } finally {
    loading.value = false
  }
}

function openCreate(): void {
  orgForm.value = { name: '', display_name: '', description: '', member_role: 'read' }
  orgDrawerMode.value = 'create'
  editingOrg.value = null
  orgDrawerOpen.value = true
}

function openEdit(org: Org): void {
  orgForm.value = {
    name: org.name,
    display_name: org.display_name ?? '',
    description: org.description ?? '',
    member_role: org.member_role ?? 'read',
  }
  orgDrawerMode.value = 'edit'
  editingOrg.value = org
  orgDrawerOpen.value = true
}

function closeOrgDrawer(): void {
  orgDrawerOpen.value = false
}

async function saveOrg(): Promise<void> {
  if (orgSaving.value) return
  if (orgDrawerMode.value === 'create' && !orgForm.value.name.trim()) return
  orgSaving.value = true
  try {
    if (orgDrawerMode.value === 'create') {
      await request('/api/v1/orgs', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name: orgForm.value.name.trim(),
          display_name: orgForm.value.display_name.trim() || undefined,
          description: orgForm.value.description.trim() || undefined,
          member_role: orgForm.value.member_role,
        }),
      })
      toasts.success(t('adm.orgs.created'))
    } else if (editingOrg.value) {
      // Empty strings are sent on purpose so a field can be cleared.
      await request(`/api/v1/orgs/${encodeURIComponent(editingOrg.value.name)}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          display_name: orgForm.value.display_name.trim(),
          description: orgForm.value.description.trim(),
          member_role: orgForm.value.member_role,
        }),
      })
      toasts.success(t('adm.orgs.updated'))
    }
    closeOrgDrawer()
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.orgs.saveFailed'))
  } finally {
    orgSaving.value = false
  }
}

async function refreshMembers(org: Org): Promise<void> {
  members.value = await fetchMembers(org.name)
  memberCounts.value = { ...memberCounts.value, [org.id]: members.value.length }
}

async function openMembers(org: Org): Promise<void> {
  managingOrg.value = org
  members.value = []
  newMemberUserId.value = ''
  newMemberRole.value = 'member'
  membersDrawerOpen.value = true
  membersLoading.value = true
  try {
    await refreshMembers(org)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.orgs.loadMembersFailed'))
  } finally {
    membersLoading.value = false
  }
}

function closeMembersDrawer(): void {
  membersDrawerOpen.value = false
}

async function addMember(): Promise<void> {
  const org = managingOrg.value
  if (!newMemberUserId.value || !org || addingMember.value) return
  addingMember.value = true
  try {
    await request(`/api/v1/orgs/${encodeURIComponent(org.name)}/members`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ user_id: newMemberUserId.value, role: newMemberRole.value }),
    })
    newMemberUserId.value = ''
    toasts.success(t('adm.orgs.memberAdded'))
    await refreshMembers(org)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.orgs.memberAddFailed'))
  } finally {
    addingMember.value = false
  }
}

async function removeMember(member: OrgMember): Promise<void> {
  const org = managingOrg.value
  if (!org) return
  if (!window.confirm(`${t('adm.orgs.confirmRemoveMember')} "${memberName(member)}"?`)) return
  try {
    await request(
      `/api/v1/orgs/${encodeURIComponent(org.name)}/members/${encodeURIComponent(member.user_id)}`,
      { method: 'DELETE' },
    )
    toasts.success(t('adm.orgs.memberRemoved'))
    await refreshMembers(org)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.orgs.memberRemoveFailed'))
  }
}

function openDelete(org: Org): void {
  deleteTarget.value = org
  deleteConfirmName.value = ''
}

function cancelDelete(): void {
  deleteTarget.value = null
  deleteConfirmName.value = ''
}

async function confirmDelete(): Promise<void> {
  const org = deleteTarget.value
  if (!org || deleteConfirmName.value !== org.name) return
  deleting.value = true
  try {
    await request(`/api/v1/orgs/${encodeURIComponent(org.name)}`, { method: 'DELETE' })
    toasts.success(t('adm.orgs.deleted'))
    cancelDelete()
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.orgs.deleteFailed'))
  } finally {
    deleting.value = false
  }
}

onMounted(load)
</script>
