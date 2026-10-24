<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <PageHeader :title="t('adm.projects.title')" :description="t('adm.projects.description')">
      <template #actions>
        <AppButton variant="primary" size="sm" @click="openCreate">
          <PlusIcon class="w-4 h-4" aria-hidden="true" />
          {{ t('adm.projects.new') }}
        </AppButton>
      </template>
    </PageHeader>

    <!-- Self-service creation -->
    <div class="bg-surface rounded-xl border border-border p-4 flex items-start justify-between gap-4" data-testid="self-create">
      <div class="min-w-0">
        <p id="self-create-label" class="text-sm font-semibold text-foreground">{{ t('adm.projects.selfCreate.title') }}</p>
        <p class="text-xs text-muted mt-0.5">{{ t('adm.projects.selfCreate.hint') }}</p>
        <p v-if="selfCreateError" class="text-xs text-danger mt-1" role="alert">{{ selfCreateError }}</p>
      </div>
      <AppToggle
        v-model="selfCreate"
        labelledby="self-create-label"
        :disabled="selfCreateLoading || selfCreateSaving"
        @update:model-value="saveSelfCreate"
      />
    </div>

    <!-- What project roles grant -->
    <div class="bg-surface rounded-xl border border-border p-4 text-xs text-muted space-y-1.5" data-testid="project-effect">
      <p class="font-semibold text-foreground">{{ t('adm.projects.effect.title') }}</p>
      <p>{{ t('adm.projects.effect.viewer') }}</p>
      <p>{{ t('adm.projects.effect.developer') }}</p>
      <p>{{ t('adm.projects.effect.maintainer') }}</p>
      <p>{{ t('adm.projects.effect.limits') }}</p>
    </div>

    <!-- Stat Tiles -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <div class="w-9 h-9 rounded-lg flex items-center justify-center shrink-0 bg-primary/10">
          <BuildingOfficeIcon class="w-5 h-5 text-primary" aria-hidden="true" />
        </div>
        <div class="min-w-0">
          <p class="text-micro font-mono font-semibold text-muted truncate">{{ t('adm.projects.stat.projects') }}</p>
          <p class="text-xl font-bold tabular-nums text-foreground leading-tight">{{ items.length }}</p>
        </div>
      </div>
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <div class="w-9 h-9 rounded-lg flex items-center justify-center shrink-0 bg-success/10">
          <UsersIcon class="w-5 h-5 text-success" aria-hidden="true" />
        </div>
        <div class="min-w-0">
          <p class="text-micro font-mono font-semibold text-muted truncate">{{ t('adm.projects.stat.memberships') }}</p>
          <p class="text-xl font-bold tabular-nums text-foreground leading-tight">{{ totalMembers }}</p>
        </div>
      </div>
      <div class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
        <div class="w-9 h-9 rounded-lg flex items-center justify-center shrink-0 bg-warning-soft">
          <UserGroupIcon class="w-5 h-5 text-warning" aria-hidden="true" />
        </div>
        <div class="min-w-0">
          <p class="text-micro font-mono font-semibold text-muted truncate">{{ t('adm.projects.stat.groups') }}</p>
          <p class="text-xl font-bold tabular-nums text-foreground leading-tight">{{ totalGroups }}</p>
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
        <h2 class="text-sm font-semibold text-foreground">{{ t('adm.projects.title') }}</h2>
        <span class="px-1.5 py-0.5 rounded-md bg-surface-hover text-caption font-mono font-medium text-muted">
          {{ items.length }}
        </span>
      </div>

      <div v-if="items.length === 0" class="py-16 text-center">
        <div class="w-12 h-12 rounded-full bg-surface-hover mx-auto flex items-center justify-center mb-3">
          <BuildingOfficeIcon class="w-6 h-6 text-muted" aria-hidden="true" />
        </div>
        <p class="text-sm font-medium text-foreground">{{ t('adm.projects.empty') }}</p>
        <p class="text-xs text-muted mt-1 mb-4">{{ t('adm.projects.emptyHint') }}</p>
        <AppButton variant="primary" size="sm" @click="openCreate">
          <PlusIcon class="w-4 h-4" aria-hidden="true" />
          {{ t('adm.projects.new') }}
        </AppButton>
        <div class="mt-3"><DocsLink :url="docsUrl">{{ t('docs.projects') }}</DocsLink></div>
      </div>

      <table v-else class="w-full">
        <thead>
          <tr class="bg-surface-hover/30">
            <th scope="col" class="text-left text-caption font-semibold text-muted px-5 py-2.5">{{ t('adm.projects.column.name') }}</th>
            <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('adm.projects.column.members') }}</th>
            <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('adm.projects.column.groups') }}</th>
            <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('adm.projects.column.created') }}</th>
            <th scope="col" class="text-right text-caption font-semibold text-muted px-5 py-2.5">{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          <tr v-for="project in items" :key="project.id" class="hover:bg-surface-hover/30 transition-colors">
            <td class="px-5 py-3.5">
              <div class="flex items-center gap-2.5">
                <div class="w-8 h-8 rounded-md bg-primary/10 flex items-center justify-center shrink-0">
                  <BuildingOfficeIcon class="w-4 h-4 text-primary" aria-hidden="true" />
                </div>
                <div class="min-w-0">
                  <p class="font-mono text-ui font-medium text-foreground truncate">{{ project.name }}</p>
                  <p v-if="project.display_name && project.display_name !== project.name" class="text-caption text-muted truncate">
                    {{ project.display_name }}
                  </p>
                </div>
              </div>
            </td>
            <td class="px-3 py-3.5">
              <button
                type="button"
                :aria-label="`${t('adm.projects.manageAccess')}: ${project.name}`"
                class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-xs font-mono bg-surface-hover text-foreground hover:bg-surface-hover/80 transition-colors"
                @click="openAccess(project)"
              >
                <UsersIcon class="w-3 h-3 text-muted" aria-hidden="true" />
                {{ memberCounts[project.id ?? ''] ?? '—' }}
              </button>
            </td>
            <td class="px-3 py-3.5">
              <button
                type="button"
                :aria-label="`${t('adm.projects.manageAccess')}: ${project.name}`"
                class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-xs font-mono bg-surface-hover text-foreground hover:bg-surface-hover/80 transition-colors"
                @click="openAccess(project)"
              >
                <UserGroupIcon class="w-3 h-3 text-muted" aria-hidden="true" />
                {{ groupCounts[project.id ?? ''] ?? '—' }}
              </button>
            </td>
            <td class="px-3 py-3.5">
              <span class="font-mono text-xs text-muted">{{ formatDate(project.created_at) }}</span>
            </td>
            <td class="px-5 py-3.5">
              <div class="flex items-center gap-0.5 justify-end">
                <button
                  type="button"
                  :title="t('adm.projects.manageAccess')"
                  :aria-label="`${t('adm.projects.manageAccess')}: ${project.name}`"
                  class="p-1.5 rounded-md text-muted hover:text-primary hover:bg-primary/10 transition-colors"
                  @click="openAccess(project)"
                >
                  <UsersIcon class="w-4 h-4" aria-hidden="true" />
                </button>
                <button
                  type="button"
                  :title="t('adm.projects.edit')"
                  :aria-label="`${t('adm.projects.edit')}: ${project.name}`"
                  class="p-1.5 rounded-md text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
                  @click="openEdit(project)"
                >
                  <PencilIcon class="w-4 h-4" aria-hidden="true" />
                </button>
                <button
                  type="button"
                  :title="t('adm.projects.delete')"
                  :aria-label="`${t('adm.projects.delete')}: ${project.name}`"
                  class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors"
                  @click="openDelete(project)"
                >
                  <TrashIcon class="w-4 h-4" aria-hidden="true" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Project drawer (create / edit) -->
    <AdminDialog
      :open="projectDrawerOpen"
      drawer
      max-width="max-w-lg"
      :eyebrow="projectDrawerMode === 'create' ? t('adm.projects.new') : t('adm.projects.edit')"
      :title="projectDrawerMode === 'create' ? t('adm.projects.createTitle') : `${t('adm.projects.edit')} · ${editingProject?.name ?? ''}`"
      @close="closeProjectDrawer"
    >
      <form id="project-form" class="space-y-4" @submit.prevent="saveProject">
        <div v-if="projectDrawerMode === 'create'">
          <label for="project-name" class="block text-xs font-semibold text-foreground mb-1.5">
            {{ t('adm.projects.field.name') }} <span class="text-danger ml-0.5">*</span>
          </label>
          <input
            id="project-name"
            v-model="projectForm.name"
            type="text"
            required
            placeholder="my-project"
            class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary transition-colors"
          />
          <p class="text-caption text-muted mt-1 font-mono">{{ t('adm.projects.field.nameHint') }}</p>
        </div>
        <div>
          <label for="project-display-name" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.projects.field.displayName') }}</label>
          <input
            id="project-display-name"
            v-model="projectForm.display_name"
            type="text"
            class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm placeholder:text-muted focus:ring-2 focus:ring-primary transition-colors"
          />
        </div>
        <div>
          <label for="project-description" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.projects.field.description') }}</label>
          <textarea
            id="project-description"
            v-model="projectForm.description"
            rows="3"
            class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm placeholder:text-muted focus:ring-2 focus:ring-primary resize-none transition-colors"
          />
        </div>

        <div class="pt-2 border-t border-border">
          <p class="text-sm font-semibold text-foreground">{{ t('adm.projects.policy.title') }}</p>
          <p class="text-caption text-muted mt-0.5">{{ t('adm.projects.policy.hint') }}</p>
        </div>

        <fieldset class="space-y-2">
          <legend class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.projects.policy.allowedFormats') }}</legend>
          <label class="flex items-center gap-2 text-sm text-foreground cursor-pointer">
            <input v-model="projectForm.all_formats" type="checkbox" class="rounded border-border-strong" data-testid="project-all-formats" />
            <span>{{ t('adm.projects.policy.allFormats') }}</span>
          </label>
          <div v-if="!projectForm.all_formats" class="grid grid-cols-2 sm:grid-cols-3 gap-x-4 gap-y-2">
            <label v-for="format in ALL_FORMATS" :key="format" class="flex items-center gap-2 text-sm text-foreground cursor-pointer">
              <input v-model="projectForm.allowed_formats" :value="format" type="checkbox" class="rounded border-border-strong" />
              <span>{{ formatLabel(format) }}</span>
            </label>
          </div>
          <p v-if="!projectForm.all_formats" class="text-caption text-muted">{{ t('adm.projects.policy.allowedFormatsHint') }}</p>
        </fieldset>

        <fieldset class="space-y-2">
          <legend class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.projects.policy.allowedRepoTypes') }}</legend>
          <div class="flex flex-wrap gap-4">
            <label v-for="type in PROJECT_REPO_TYPES" :key="type" class="flex items-center gap-2 text-sm text-foreground cursor-pointer">
              <input v-model="projectForm.allowed_repo_types" :value="type" type="checkbox" class="rounded border-border-strong" />
              <span>{{ t(`adm.projects.policy.repoType.${type}`) }}</span>
            </label>
          </div>
        </fieldset>

        <div>
          <label for="project-name-prefix" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.projects.policy.repoNamePrefix') }}</label>
          <input
            id="project-name-prefix"
            v-model="projectForm.repo_name_prefix"
            type="text"
            class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary transition-colors"
          />
          <p class="text-caption text-muted mt-1">{{ t('adm.projects.policy.repoNamePrefixHint') }}</p>
        </div>

        <label class="flex items-center gap-2 text-sm text-foreground cursor-pointer">
          <input v-model="projectForm.allow_public_repos" type="checkbox" class="rounded border-border-strong" />
          <span>{{ t('adm.projects.policy.allowPublicRepos') }}</span>
        </label>

        <div class="grid grid-cols-2 gap-3">
          <div>
            <label for="project-max-repos" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.projects.policy.maxRepositories') }}</label>
            <input
              id="project-max-repos"
              v-model.number="projectForm.max_repositories"
              type="number"
              min="0"
              step="1"
              class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono focus:ring-2 focus:ring-primary transition-colors"
            />
            <p class="text-caption text-muted mt-1">{{ t('adm.projects.policy.maxRepositoriesHint') }}</p>
          </div>
          <div>
            <label for="project-token-ttl" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.projects.policy.tokenMaxTtlDays') }}</label>
            <input
              id="project-token-ttl"
              v-model.number="projectForm.token_max_ttl_days"
              type="number"
              min="0"
              step="1"
              class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono focus:ring-2 focus:ring-primary transition-colors"
            />
            <p class="text-caption text-muted mt-1">{{ t('adm.projects.policy.tokenMaxTtlDaysHint') }}</p>
          </div>
        </div>
      </form>
      <template #footer>
        <button
          type="button"
          class="px-3 py-1.5 rounded-lg text-sm font-medium text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
          @click="closeProjectDrawer"
        >
          {{ t('common.cancel') }}
        </button>
        <AppButton variant="primary" size="sm" type="submit" form="project-form" :loading="projectSaving" :disabled="(projectDrawerMode === 'create' && !projectForm.name.trim())">
          {{ projectDrawerMode === 'create' ? t('adm.projects.createAction') : t('adm.projects.saveAction') }}
        </AppButton>
      </template>
    </AdminDialog>

    <!-- Members and groups drawer -->
    <AdminDialog
      :open="accessDrawerOpen"
      drawer
      max-width="max-w-lg"
      :eyebrow="t('adm.projects.accessEyebrow')"
      :title="managingProject?.name ?? ''"
      @close="closeAccessDrawer"
    >
      <div class="-mx-6 -my-5">
        <!-- Members -->
        <form class="px-6 py-4 border-b border-border bg-surface-hover/20 space-y-3" @submit.prevent="addMember">
          <p class="text-xs font-semibold text-foreground">{{ t('adm.projects.addMember') }}</p>
          <div class="flex items-center gap-2">
            <select
              v-model="newMemberUserId"
              :aria-label="t('adm.projects.field.user')"
              class="flex-1 min-w-0 px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono focus:ring-2 focus:ring-primary transition-colors"
            >
              <option value="">{{ t('adm.projects.selectUser') }}</option>
              <option v-for="u in availableUsers" :key="u.id" :value="u.id">{{ u.username }}</option>
            </select>
            <select
              v-model="newMemberRole"
              :aria-label="t('adm.projects.field.role')"
              class="px-2.5 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm focus:ring-2 focus:ring-primary shrink-0"
            >
              <option v-for="role in PROJECT_MEMBER_ROLES" :key="role" :value="role">{{ role }}</option>
            </select>
            <AppButton variant="primary" size="sm" type="submit" class="shrink-0" :loading="addingMember" :disabled="!newMemberUserId">
              <PlusIcon class="w-4 h-4" aria-hidden="true" />
              {{ t('adm.projects.add') }}
            </AppButton>
          </div>
        </form>

        <div>
          <div v-if="membersLoading" class="px-6 py-8 text-center text-sm text-muted">{{ t('adm.projects.loadingMembers') }}</div>
          <div v-else-if="members.length === 0" class="px-6 py-12 text-center">
            <UsersIcon class="w-10 h-10 text-muted mx-auto mb-3" aria-hidden="true" />
            <p class="text-sm font-medium text-foreground">{{ t('adm.projects.noMembers') }}</p>
            <p class="text-xs text-muted mt-1">{{ t('adm.projects.noMembersHint') }}</p>
          </div>
          <table v-else class="w-full">
            <thead>
              <tr class="bg-surface-hover/30">
                <th scope="col" class="text-left text-caption font-semibold text-muted px-6 py-2.5">{{ t('adm.projects.column.user') }}</th>
                <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('adm.projects.column.role') }}</th>
                <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('adm.projects.column.joined') }}</th>
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
                    <span class="font-mono text-xs text-foreground">{{ memberName(member) }}</span>
                  </div>
                </td>
                <td class="px-3 py-3">
                  <select
                    :value="member.role"
                    :aria-label="`${t('adm.projects.memberRole')}: ${memberName(member)}`"
                    class="px-2 py-1 rounded-md border border-border-strong bg-bg text-foreground text-xs focus:ring-2 focus:ring-primary"
                    @change="changeMemberRole(member, ($event.target as HTMLSelectElement).value as ProjectMemberRole)"
                  >
                    <option v-for="role in PROJECT_MEMBER_ROLES" :key="role" :value="role">{{ role }}</option>
                  </select>
                </td>
                <td class="px-3 py-3">
                  <span class="font-mono text-xs text-muted">{{ formatDate(member.created_at) }}</span>
                </td>
                <td class="px-3 py-3">
                  <button
                    type="button"
                    :title="t('adm.projects.removeMember')"
                    :aria-label="`${t('adm.projects.removeMember')}: ${memberName(member)}`"
                    class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors opacity-60 group-hover:opacity-100 focus:opacity-100"
                    @click="removeMemberTarget = member"
                  >
                    <TrashIcon class="w-4 h-4" aria-hidden="true" />
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- Groups -->
        <form class="px-6 py-4 border-y border-border bg-surface-hover/20 space-y-3" data-testid="project-groups" @submit.prevent="attachGroup">
          <p class="text-xs font-semibold text-foreground">{{ t('adm.projects.groups.attach') }}</p>
          <div class="flex items-center gap-2">
            <select
              v-model="newGroupId"
              :aria-label="t('adm.projects.field.group')"
              class="flex-1 min-w-0 px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono focus:ring-2 focus:ring-primary transition-colors"
            >
              <option value="">{{ t('adm.projects.groups.select') }}</option>
              <option v-for="g in availableGroups" :key="g.id" :value="g.id">{{ g.name }}</option>
            </select>
            <select
              v-model="newGroupRole"
              :aria-label="t('adm.projects.field.role')"
              class="px-2.5 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm focus:ring-2 focus:ring-primary shrink-0"
            >
              <option v-for="role in PROJECT_GROUP_ROLES" :key="role" :value="role">{{ role }}</option>
            </select>
            <AppButton variant="primary" size="sm" type="submit" class="shrink-0" :loading="attachingGroup" :disabled="!newGroupId">
              <PlusIcon class="w-4 h-4" aria-hidden="true" />
              {{ t('adm.projects.add') }}
            </AppButton>
          </div>
        </form>

        <div>
          <div v-if="projectGroupsLoading" class="px-6 py-8 text-center text-sm text-muted">{{ t('adm.projects.groups.loading') }}</div>
          <div v-else-if="projectGroups.length === 0" class="px-6 py-12 text-center">
            <UserGroupIcon class="w-10 h-10 text-muted mx-auto mb-3" aria-hidden="true" />
            <p class="text-sm font-medium text-foreground">{{ t('adm.projects.groups.empty') }}</p>
            <p class="text-xs text-muted mt-1">{{ t('adm.projects.groups.emptyHint') }}</p>
          </div>
          <table v-else class="w-full">
            <thead>
              <tr class="bg-surface-hover/30">
                <th scope="col" class="text-left text-caption font-semibold text-muted px-6 py-2.5">{{ t('adm.projects.column.group') }}</th>
                <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('adm.projects.column.role') }}</th>
                <th scope="col" class="text-left text-caption font-semibold text-muted px-3 py-2.5">{{ t('adm.projects.column.attached') }}</th>
                <th scope="col" class="w-10"><span class="sr-only">{{ t('common.actions') }}</span></th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border">
              <tr v-for="grant in projectGroups" :key="grant.group_id" class="hover:bg-surface-hover/30 transition-colors group">
                <td class="px-6 py-3">
                  <div class="flex items-center gap-2">
                    <UserGroupIcon class="w-4 h-4 text-muted shrink-0" aria-hidden="true" />
                    <span class="font-mono text-xs text-foreground">{{ grant.group_name || grant.group_id }}</span>
                  </div>
                </td>
                <td class="px-3 py-3">
                  <select
                    :value="grant.role"
                    :aria-label="`${t('adm.projects.groups.groupRole')}: ${grant.group_name || grant.group_id}`"
                    class="px-2 py-1 rounded-md border border-border-strong bg-bg text-foreground text-xs focus:ring-2 focus:ring-primary"
                    @change="changeGroupRole(grant, ($event.target as HTMLSelectElement).value as ProjectGroupRole)"
                  >
                    <option v-for="role in PROJECT_GROUP_ROLES" :key="role" :value="role">{{ role }}</option>
                  </select>
                </td>
                <td class="px-3 py-3">
                  <span class="font-mono text-xs text-muted">{{ formatDate(grant.created_at) }}</span>
                </td>
                <td class="px-3 py-3">
                  <button
                    type="button"
                    :title="t('adm.projects.groups.remove')"
                    :aria-label="`${t('adm.projects.groups.remove')}: ${grant.group_name || grant.group_id}`"
                    class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors opacity-60 group-hover:opacity-100 focus:opacity-100"
                    @click="removeGroupTarget = grant"
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
          @click="closeAccessDrawer"
        >
          {{ t('common.close') }}
        </button>
      </template>
    </AdminDialog>

    <!-- Delete confirmation -->
    <ConfirmDialog
      :open="!!deleteTarget"
      :title="t('adm.projects.deleteTitle')"
      :description="t('adm.projects.deleteHint')"
      :confirm-label="t('adm.projects.delete')"
      :confirm-disabled="!deleteTarget || deleteConfirmName !== deleteTarget.name"
      :busy="deleting"
      @confirm="confirmDelete"
      @close="cancelDelete"
    >
      <div v-if="deleteTarget" class="space-y-4">
        <div class="rounded-lg border border-danger/30 bg-danger-soft p-3">
          <div class="flex items-start gap-2.5">
            <ExclamationCircleIcon class="w-4 h-4 text-danger mt-0.5 shrink-0" aria-hidden="true" />
            <div>
              <p class="text-xs font-semibold text-danger">{{ t('adm.projects.deleteWarnTitle') }}</p>
              <p class="text-caption text-danger mt-0.5 leading-relaxed">{{ t('adm.projects.deleteWarnBody') }}</p>
            </div>
          </div>
        </div>
        <div>
          <label for="project-delete-confirm" class="block text-xs font-semibold text-foreground mb-1.5">
            {{ t('adm.projects.typeToConfirm') }}
            <code class="font-mono text-danger bg-danger-soft px-1 py-0.5 rounded mx-1">{{ deleteTarget.name }}</code>
          </label>
          <input
            id="project-delete-confirm"
            v-model="deleteConfirmName"
            type="text"
            :placeholder="deleteTarget.name"
            class="w-full px-3 py-2 rounded-lg border bg-surface text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 transition-colors border-border-strong focus:ring-danger"
          />
        </div>
      </div>
    </ConfirmDialog>

    <ConfirmDialog
      :open="!!removeMemberTarget"
      :title="t('adm.projects.confirmRemoveMember')"
      :description="removeMemberTarget ? memberName(removeMemberTarget) : ''"
      :confirm-label="t('adm.projects.removeMember')"
      :busy="removingMember"
      @confirm="confirmRemoveMember"
      @close="removeMemberTarget = null"
    />

    <ConfirmDialog
      :open="!!removeGroupTarget"
      :title="t('adm.projects.groups.confirmRemove')"
      :description="removeGroupTarget ? (removeGroupTarget.group_name || removeGroupTarget.group_id || '') : ''"
      :confirm-label="t('adm.projects.groups.remove')"
      :busy="removingGroup"
      @confirm="confirmRemoveGroup"
      @close="removeGroupTarget = null"
    />
  </div>
</template>

<script setup lang="ts">
import AppButton from '../../components/AppButton.vue'
import AppToggle from '../../components/AppToggle.vue'
import ConfirmDialog from '../../components/ConfirmDialog.vue'
import PageHeader from '../../components/PageHeader.vue'
import { formatDate } from '../../lib/datetime'
import { ALL_FORMATS, formatLabel } from '../../lib/formats'
import { ref, computed, onMounted } from 'vue'
import { listAllUsers, listGroups, type GroupItem, type UserItem } from '../../api/admin'
import {
  PROJECT_GROUP_ROLES,
  PROJECT_MEMBER_ROLES,
  PROJECT_REPO_TYPES,
  addProjectMember,
  createProject,
  deleteProject,
  getProjectSettings,
  listProjectGroups,
  listProjectMembers,
  listProjects,
  removeProjectGroup,
  removeProjectMember,
  setProjectGroup,
  updateProject,
  updateProjectMember,
  updateProjectSettings,
  type Project,
  type ProjectGroup,
  type ProjectGroupRole,
  type ProjectMember,
  type ProjectMemberRole,
  type ProjectRepoType,
} from '../../api/projects'
import { useDocsLink } from '../../composables/useDocsLink'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'
import AdminDialog from '../../components/AdminDialog.vue'
import DocsLink from '../../components/DocsLink.vue'
import {
  BuildingOfficeIcon,
  UsersIcon,
  UserGroupIcon,
  PlusIcon,
  PencilIcon,
  TrashIcon,
  ExclamationCircleIcon,
} from '@heroicons/vue/24/outline'

interface ProjectForm {
  name: string
  display_name: string
  description: string
  all_formats: boolean
  allowed_formats: string[]
  allowed_repo_types: ProjectRepoType[]
  repo_name_prefix: string
  allow_public_repos: boolean
  max_repositories: number
  token_max_ttl_days: number
}

const toasts = useToastStore()
const docsUrl = useDocsLink('admin-projects')
const items = ref<Project[]>([])
const users = ref<UserItem[]>([])
const allGroups = ref<GroupItem[]>([])
const memberCounts = ref<Record<string, number>>({})
const groupCounts = ref<Record<string, number>>({})
const loading = ref(true)
const loadError = ref('')

const totalMembers = computed(() => Object.values(memberCounts.value).reduce((s, n) => s + n, 0))
const totalGroups = computed(() => Object.values(groupCounts.value).reduce((s, n) => s + n, 0))

function emptyForm(): ProjectForm {
  return {
    name: '',
    display_name: '',
    description: '',
    all_formats: true,
    allowed_formats: [],
    allowed_repo_types: [...PROJECT_REPO_TYPES],
    repo_name_prefix: '',
    allow_public_repos: true,
    max_repositories: 0,
    token_max_ttl_days: 0,
  }
}

// Project drawer
const projectDrawerOpen = ref(false)
const projectDrawerMode = ref<'create' | 'edit'>('create')
const editingProject = ref<Project | null>(null)
const projectSaving = ref(false)
const projectForm = ref<ProjectForm>(emptyForm())

// Members and groups drawer
const accessDrawerOpen = ref(false)
const managingProject = ref<Project | null>(null)
const members = ref<ProjectMember[]>([])
const membersLoading = ref(false)
const newMemberUserId = ref('')
const newMemberRole = ref<ProjectMemberRole>('viewer')
const addingMember = ref(false)
const projectGroups = ref<ProjectGroup[]>([])
const projectGroupsLoading = ref(false)
const newGroupId = ref('')
const newGroupRole = ref<ProjectGroupRole>('viewer')
const attachingGroup = ref(false)

// Delete
const deleteTarget = ref<Project | null>(null)
const deleteConfirmName = ref('')
const deleting = ref(false)

const availableUsers = computed(() => {
  const memberIds = new Set(members.value.map((m) => m.user_id))
  return users.value.filter((u) => u.id && !memberIds.has(u.id) && !u.disabled)
})

const availableGroups = computed(() => {
  const attached = new Set(projectGroups.value.map((g) => g.group_id))
  return allGroups.value.filter((g) => g.id && !attached.has(g.id))
})

function memberName(member: ProjectMember): string {
  return member.username || (users.value.find((u) => u.id === member.user_id)?.username ?? member.user_id ?? '')
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const [{ projects: list }, usersRes, groupsRes] = await Promise.all([
      listProjects({ all: true }),
      // Names fall back to ids when the user or group list cannot be read.
      listAllUsers().catch(() => ({ total: 0, items: [] as UserItem[] })),
      listGroups().catch(() => ({ total: 0, items: [] as GroupItem[] })),
    ])
    items.value = list
    users.value = usersRes.items
    allGroups.value = groupsRes.items
    const [memberLists, groupLists] = await Promise.all([
      Promise.all(list.map((p) => listProjectMembers(p.name ?? '').catch(() => null))),
      Promise.all(list.map((p) => listProjectGroups(p.name ?? '').catch(() => null))),
    ])
    const mCounts: Record<string, number> = {}
    const gCounts: Record<string, number> = {}
    list.forEach((p, i) => {
      const id = p.id ?? ''
      const m = memberLists[i]
      const g = groupLists[i]
      if (m) mCounts[id] = m.length
      if (g) gCounts[id] = g.length
    })
    memberCounts.value = mCounts
    groupCounts.value = gCounts
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : t('adm.projects.loadFailed')
    toasts.error(loadError.value)
  } finally {
    loading.value = false
  }
}

function openCreate(): void {
  projectForm.value = emptyForm()
  projectDrawerMode.value = 'create'
  editingProject.value = null
  projectDrawerOpen.value = true
}

function openEdit(project: Project): void {
  projectForm.value = {
    name: project.name ?? '',
    display_name: project.display_name ?? '',
    description: project.description ?? '',
    all_formats: project.allowed_formats == null,
    allowed_formats: [...(project.allowed_formats ?? [])],
    allowed_repo_types: [...(project.allowed_repo_types ?? PROJECT_REPO_TYPES)],
    repo_name_prefix: project.repo_name_prefix ?? '',
    allow_public_repos: project.allow_public_repos ?? true,
    max_repositories: project.max_repositories ?? 0,
    token_max_ttl_days: project.token_max_ttl_days ?? 0,
  }
  projectDrawerMode.value = 'edit'
  editingProject.value = project
  projectDrawerOpen.value = true
}

function closeProjectDrawer(): void {
  projectDrawerOpen.value = false
}

/** Number inputs yield '' when cleared; the API wants a non-negative integer. */
function nonNegativeInt(value: unknown): number {
  const n = Math.floor(Number(value))
  return Number.isFinite(n) && n > 0 ? n : 0
}

async function saveProject(): Promise<void> {
  if (projectSaving.value) return
  const form = projectForm.value
  if (projectDrawerMode.value === 'create' && !form.name.trim()) return
  projectSaving.value = true
  const policy = {
    // An empty list means every format (stored as null on the server).
    allowed_formats: form.all_formats ? [] : form.allowed_formats,
    allowed_repo_types: form.allowed_repo_types,
    repo_name_prefix: form.repo_name_prefix.trim(),
    allow_public_repos: form.allow_public_repos,
    max_repositories: nonNegativeInt(form.max_repositories),
    token_max_ttl_days: nonNegativeInt(form.token_max_ttl_days),
  }
  try {
    if (projectDrawerMode.value === 'create') {
      await createProject({
        name: form.name.trim(),
        display_name: form.display_name.trim() || undefined,
        description: form.description.trim() || undefined,
        ...policy,
      })
      toasts.success(t('adm.projects.created'))
    } else if (editingProject.value?.name) {
      // Empty strings are sent on purpose so a field can be cleared.
      await updateProject(editingProject.value.name, {
        display_name: form.display_name.trim(),
        description: form.description.trim(),
        ...policy,
      })
      toasts.success(t('adm.projects.updated'))
    }
    closeProjectDrawer()
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.projects.saveFailed'))
  } finally {
    projectSaving.value = false
  }
}

async function refreshMembers(project: Project): Promise<void> {
  members.value = await listProjectMembers(project.name ?? '')
  memberCounts.value = { ...memberCounts.value, [project.id ?? '']: members.value.length }
}

async function refreshGroups(project: Project): Promise<void> {
  projectGroups.value = await listProjectGroups(project.name ?? '')
  groupCounts.value = { ...groupCounts.value, [project.id ?? '']: projectGroups.value.length }
}

async function openAccess(project: Project): Promise<void> {
  managingProject.value = project
  members.value = []
  projectGroups.value = []
  newMemberUserId.value = ''
  newMemberRole.value = 'viewer'
  newGroupId.value = ''
  newGroupRole.value = 'viewer'
  accessDrawerOpen.value = true
  membersLoading.value = true
  projectGroupsLoading.value = true
  await Promise.all([
    refreshMembers(project)
      .catch((err: unknown) => toasts.error(err instanceof Error ? err.message : t('adm.projects.loadMembersFailed')))
      .finally(() => {
        membersLoading.value = false
      }),
    refreshGroups(project)
      .catch((err: unknown) => toasts.error(err instanceof Error ? err.message : t('adm.projects.groups.loadFailed')))
      .finally(() => {
        projectGroupsLoading.value = false
      }),
  ])
}

function closeAccessDrawer(): void {
  accessDrawerOpen.value = false
}

async function addMember(): Promise<void> {
  const project = managingProject.value
  if (!newMemberUserId.value || !project?.name || addingMember.value) return
  addingMember.value = true
  try {
    await addProjectMember(project.name, { user_id: newMemberUserId.value, role: newMemberRole.value })
    newMemberUserId.value = ''
    toasts.success(t('adm.projects.memberAdded'))
    await refreshMembers(project)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.projects.memberAddFailed'))
  } finally {
    addingMember.value = false
  }
}

async function changeMemberRole(member: ProjectMember, role: ProjectMemberRole): Promise<void> {
  const project = managingProject.value
  if (!project?.name || !member.user_id || member.role === role) return
  try {
    await updateProjectMember(project.name, member.user_id, { role })
    toasts.success(t('adm.projects.memberRoleUpdated'))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.projects.memberRoleUpdateFailed'))
  }
  // Reload either way so the select reflects what the server holds.
  await refreshMembers(project).catch(() => undefined)
}

const removeMemberTarget = ref<ProjectMember | null>(null)
const removingMember = ref(false)

async function confirmRemoveMember(): Promise<void> {
  const project = managingProject.value
  const member = removeMemberTarget.value
  if (!project?.name || !member?.user_id) return
  removingMember.value = true
  try {
    await removeProjectMember(project.name, member.user_id)
    toasts.success(t('adm.projects.memberRemoved'))
    removeMemberTarget.value = null
    await refreshMembers(project)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.projects.memberRemoveFailed'))
  } finally {
    removingMember.value = false
  }
}

async function attachGroup(): Promise<void> {
  const project = managingProject.value
  if (!newGroupId.value || !project?.name || attachingGroup.value) return
  attachingGroup.value = true
  try {
    await setProjectGroup(project.name, newGroupId.value, { role: newGroupRole.value })
    newGroupId.value = ''
    toasts.success(t('adm.projects.groups.attached'))
    await refreshGroups(project)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.projects.groups.attachFailed'))
  } finally {
    attachingGroup.value = false
  }
}

async function changeGroupRole(grant: ProjectGroup, role: ProjectGroupRole): Promise<void> {
  const project = managingProject.value
  if (!project?.name || !grant.group_id || grant.role === role) return
  try {
    await setProjectGroup(project.name, grant.group_id, { role })
    toasts.success(t('adm.projects.groups.roleUpdated'))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.projects.groups.roleUpdateFailed'))
  }
  await refreshGroups(project).catch(() => undefined)
}

const removeGroupTarget = ref<ProjectGroup | null>(null)
const removingGroup = ref(false)

async function confirmRemoveGroup(): Promise<void> {
  const project = managingProject.value
  const grant = removeGroupTarget.value
  if (!project?.name || !grant?.group_id) return
  removingGroup.value = true
  try {
    await removeProjectGroup(project.name, grant.group_id)
    toasts.success(t('adm.projects.groups.removed'))
    removeGroupTarget.value = null
    await refreshGroups(project)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.projects.groups.removeFailed'))
  } finally {
    removingGroup.value = false
  }
}

function openDelete(project: Project): void {
  deleteTarget.value = project
  deleteConfirmName.value = ''
}

function cancelDelete(): void {
  deleteTarget.value = null
  deleteConfirmName.value = ''
}

async function confirmDelete(): Promise<void> {
  const project = deleteTarget.value
  if (!project?.name || deleteConfirmName.value !== project.name) return
  deleting.value = true
  try {
    await deleteProject(project.name)
    toasts.success(t('adm.projects.deleted'))
    cancelDelete()
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.projects.deleteFailed'))
  } finally {
    deleting.value = false
  }
}

// Self-service project creation (global setting)
const selfCreate = ref(false)
const selfCreateLoading = ref(true)
const selfCreateSaving = ref(false)
const selfCreateError = ref('')

async function loadSelfCreate(): Promise<void> {
  try {
    selfCreate.value = (await getProjectSettings()).self_create === true
  } catch {
    selfCreateError.value = t('adm.projects.selfCreate.loadFailed')
  } finally {
    selfCreateLoading.value = false
  }
}

async function saveSelfCreate(value: boolean): Promise<void> {
  selfCreateSaving.value = true
  selfCreateError.value = ''
  try {
    selfCreate.value = (await updateProjectSettings({ self_create: value })).self_create === true
    toasts.success(t('adm.projects.selfCreate.saved'))
  } catch {
    // Show what the server still holds.
    selfCreate.value = !value
    selfCreateError.value = t('adm.projects.selfCreate.saveFailed')
  } finally {
    selfCreateSaving.value = false
  }
}

onMounted(() => {
  void load()
  void loadSelfCreate()
})
</script>
