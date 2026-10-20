<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <div class="flex items-start justify-between flex-wrap gap-3">
      <div>
        <div class="flex items-center gap-2 text-[11px] uppercase tracking-[0.14em] text-muted font-mono">
          <span>{{ t('nav.admin') }}</span>
          <ChevronRightIcon class="w-3 h-3" />
          <span class="text-foreground/70">{{ t('nav.storage') }}</span>
        </div>
        <h1 class="mt-1 text-2xl font-bold tracking-tight">{{ t('admin.storage.title') }}</h1>
        <p class="mt-1 text-sm text-muted">{{ t('adm.storage.description') }}</p>
      </div>
      <div class="flex items-center gap-2 shrink-0">
        <button
          type="button"
          :disabled="testingAll || items.length === 0"
          class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium border border-border bg-surface text-foreground hover:bg-surface-hover transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          @click="testAll"
        >
          <ArrowPathIcon class="w-4 h-4" :class="{ 'animate-spin': testingAll }" />
          {{ t('adm.storage.testAll') }}
        </button>
        <button
          type="button"
          class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium bg-primary text-primary-contrast hover:opacity-90 transition-opacity"
          @click="openCreate"
        >
          <PlusIcon class="w-4 h-4" />
          {{ t('adm.storage.addTitle') }}
        </button>
      </div>
    </div>

    <!-- ─── Stat Tiles ──────────────────────────────────────────────────────── -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-3 mt-5">
      <div
        v-for="tile in statTiles"
        :key="tile.label"
        class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3"
      >
        <div
          class="w-9 h-9 rounded-lg flex items-center justify-center shrink-0"
          :style="{ background: tile.iconBg }"
        >
          <component :is="tile.icon" class="w-5 h-5" :style="{ color: tile.iconColor }" />
        </div>
        <div class="min-w-0">
          <p class="text-[10px] uppercase tracking-[0.12em] font-mono font-semibold text-muted truncate">
            {{ tile.label }}
          </p>
          <p class="text-xl font-bold tabular-nums text-foreground leading-tight">{{ tile.value }}</p>
        </div>
      </div>
    </div>

    <!-- ─── Loading Skeleton ────────────────────────────────────────────────── -->
    <div v-if="loading" class="mt-5 bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-5 py-4 border-b border-border">
        <div class="h-4 w-40 rounded skel" />
      </div>
      <div v-for="i in 3" :key="i" class="flex items-center gap-4 px-5 py-4 border-b border-border last:border-0">
        <div class="w-8 h-8 rounded-md skel" />
        <div class="h-3 w-32 rounded skel" />
        <div class="h-5 w-24 rounded skel" />
        <div class="h-3 w-40 rounded skel" />
        <div class="flex-1" />
        <div class="h-5 w-16 rounded-full skel" />
        <div class="flex gap-1">
          <div class="w-7 h-7 rounded-md skel" />
          <div class="w-7 h-7 rounded-md skel" />
          <div class="w-7 h-7 rounded-md skel" />
        </div>
      </div>
    </div>

    <!-- ─── Table ───────────────────────────────────────────────────────────── -->
    <div v-else class="bg-surface rounded-xl border border-border overflow-hidden mt-5">
      <!-- Table caption row -->
      <div class="px-5 py-4 flex items-center gap-2 border-b border-border">
        <h2 class="text-sm font-semibold text-foreground">{{ t('adm.storage.stat.configured') }}</h2>
        <span class="px-1.5 py-0.5 rounded-md bg-surface-hover text-[11px] font-mono font-medium text-muted">
          {{ items.length }}
        </span>
      </div>

      <!-- Empty state -->
      <div v-if="items.length === 0" class="py-16 text-center">
        <div class="w-12 h-12 rounded-full bg-surface-hover mx-auto flex items-center justify-center mb-3">
          <ServerIcon class="w-6 h-6 text-muted" />
        </div>
        <p class="text-sm font-medium text-foreground">{{ t('adm.storage.empty') }}</p>
        <p class="text-xs text-muted mt-1 mb-4">{{ t('adm.storage.emptyHint') }}</p>
        <button
          type="button"
          class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium bg-primary text-primary-contrast hover:opacity-90 transition-opacity"
          @click="openCreate"
        >
          <PlusIcon class="w-4 h-4" />
          {{ t('adm.storage.addTitle') }}
        </button>
      </div>

      <!-- Data table -->
      <table v-else class="w-full">
        <thead>
          <tr class="bg-surface-hover/30">
            <th scope="col" class="text-left text-[11px] uppercase tracking-[0.12em] font-semibold text-muted px-5 py-2.5">
              {{ t('adm.storage.column.name') }}
            </th>
            <th scope="col" class="text-left text-[11px] uppercase tracking-[0.12em] font-semibold text-muted px-3 py-2.5">
              {{ t('adm.storage.column.type') }}
            </th>
            <th scope="col" class="text-left text-[11px] uppercase tracking-[0.12em] font-semibold text-muted px-3 py-2.5">
              {{ t('adm.storage.column.location') }}
            </th>
            <th scope="col" class="text-left text-[11px] uppercase tracking-[0.12em] font-semibold text-muted px-3 py-2.5">
              {{ t('adm.storage.column.status') }}
            </th>
            <th scope="col" class="text-right text-[11px] uppercase tracking-[0.12em] font-semibold text-muted px-5 py-2.5">
              {{ t('adm.storage.column.actions') }}
            </th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          <tr
            v-for="backend in items"
            :key="backend.id"
            class="hover:bg-surface-hover/30 transition-colors"
          >
            <!-- Name cell -->
            <td class="px-5 py-3.5">
              <div class="flex items-center gap-2.5">
                <div
                  class="w-8 h-8 rounded-md flex items-center justify-center shrink-0"
                  :style="{ background: typeColorBg(backend.kind) }"
                >
                  <component
                    :is="typeIcon(backend.kind)"
                    class="w-4 h-4"
                    :style="{ color: typeColor(backend.kind) }"
                  />
                </div>
                <div class="flex items-center gap-1.5 min-w-0">
                  <span class="font-mono text-[13px] font-medium text-foreground">{{ backend.name }}</span>
                  <span
                    v-if="backend.is_default"
                    class="inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded-md bg-primary-soft text-primary text-[10px] font-semibold shrink-0"
                  >
                    <StarIcon class="w-2.5 h-2.5" />
                    default
                  </span>
                </div>
              </div>
            </td>

            <!-- Type chip cell -->
            <td class="px-3 py-3.5">
              <span
                class="inline-flex items-center pl-1.5 pr-2 py-1 rounded-md bg-surface-hover/60 border border-border relative overflow-hidden"
              >
                <span
                  class="absolute left-0 top-0 bottom-0 w-[3px] rounded-l-md"
                  :style="{ background: typeColor(backend.kind) }"
                />
                <span class="pl-1.5 flex items-center gap-1.5">
                  <component
                    :is="typeIcon(backend.kind)"
                    class="w-3.5 h-3.5"
                    :style="{ color: typeColor(backend.kind) }"
                  />
                  <span class="font-mono text-[11px] font-medium text-foreground whitespace-nowrap">
                    {{ typeMeta(backend.kind)?.label ?? backend.kind }}
                  </span>
                </span>
              </span>
            </td>

            <!-- Location cell -->
            <td class="px-3 py-3.5 max-w-[260px]">
              <span class="font-mono text-[12px] text-foreground/80 truncate block max-w-[260px]">
                {{ locationSummary(backend) }}
              </span>
            </td>

            <!-- Status cell -->
            <td class="px-3 py-3.5">
              <div>
                <span
                  class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-[11px] font-medium"
                  :class="statusBadgeClass(backend.status)"
                >
                  <span class="w-1.5 h-1.5 rounded-full" :class="statusDotClass(backend.status)" />
                  {{ statusLabel(backend.status) }}
                </span>
                <p v-if="backend.last_checked_at" class="text-[10px] text-muted font-mono mt-0.5 whitespace-nowrap">
                  {{ t('adm.storage.checked') }} {{ timeAgo(backend.last_checked_at) }}
                </p>
              </div>
            </td>

            <!-- Actions cell -->
            <td class="px-5 py-3.5">
              <div class="flex items-center gap-0.5 justify-end">
                <!-- Test -->
                <button
                  type="button"
                  :title="t('adm.storage.testConnection')"
                  :aria-label="`${t('adm.storage.testConnection')}: ${backend.name}`"
                  :disabled="testingOne === backend.name"
                  class="p-1.5 rounded-md text-muted hover:text-primary hover:bg-primary/10 transition-colors disabled:opacity-40"
                  @click="testOne(backend.name)"
                >
                  <ArrowPathIcon v-if="testingOne === backend.name" class="w-4 h-4 animate-spin" />
                  <BoltIcon v-else class="w-4 h-4" />
                </button>
                <!-- Edit -->
                <button
                  type="button"
                  :title="t('common.edit')"
                  :aria-label="`${t('common.edit')}: ${backend.name}`"
                  class="p-1.5 rounded-md text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
                  @click="openEdit(backend)"
                >
                  <PencilIcon class="w-4 h-4" />
                </button>
                <!-- Set default -->
                <button
                  v-if="!backend.is_default"
                  type="button"
                  :title="t('adm.storage.setDefault')"
                  :aria-label="`${t('adm.storage.setDefault')}: ${backend.name}`"
                  class="p-1.5 rounded-md text-muted hover:text-warning hover:bg-warning/10 transition-colors"
                  @click="setDefault(backend)"
                >
                  <StarIcon class="w-4 h-4" />
                </button>
                <!-- Delete -->
                <button
                  type="button"
                  :title="t('adm.storage.deleteAction')"
                  :aria-label="`${t('adm.storage.deleteAction')}: ${backend.name}`"
                  :disabled="backend.is_default"
                  class="p-1.5 rounded-md text-muted hover:text-danger hover:bg-danger/10 transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
                  @click="openDelete(backend)"
                >
                  <TrashIcon class="w-4 h-4" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- ═══════════════════════════════════════════════════════════════════════
         SLIDE-RIGHT DRAWER
    ════════════════════════════════════════════════════════════════════════════ -->
    <AdminDialog
      :open="drawerOpen"
      drawer
      :eyebrow="drawerMode === 'create' ? t('adm.storage.newBackend') : t('adm.storage.editBackend')"
      :title="drawerMode === 'create' ? t('adm.storage.addTitle') : `${t('common.edit')} · ${editingBackend?.name ?? ''}`"
      @close="closeDrawer"
    >
          <div class="space-y-6">
            <!-- Name -->
            <div>
              <label class="block text-xs font-semibold text-foreground mb-1.5">
                Name <span class="text-danger ml-0.5">*</span>
              </label>
              <input
                v-model="form.name"
                type="text"
                :disabled="drawerMode === 'edit'"
                placeholder="e.g. primary-storage"
                class="w-full px-3 py-2 rounded-lg border border-border bg-bg text-foreground text-sm font-mono placeholder:text-muted/50 focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-60 disabled:cursor-not-allowed transition-colors"
              />
              <p v-if="drawerMode === 'edit'" class="text-[11px] text-muted mt-1 font-mono">
                Name cannot be changed after creation.
              </p>
            </div>

            <!-- Backend type picker -->
            <div>
              <div class="flex items-baseline gap-2 mb-2">
                <label class="text-xs font-semibold text-foreground">
                  Backend type <span class="text-danger ml-0.5">*</span>
                </label>
                <span v-if="drawerMode === 'edit'" class="text-[11px] text-muted font-mono">
                  (locked after creation)
                </span>
              </div>
              <div class="grid grid-cols-2 gap-2">
                <button
                  v-for="(bt, key) in BACKEND_TYPES"
                  :key="key"
                  type="button"
                  :disabled="drawerMode === 'edit'"
                  class="relative text-left p-3 rounded-lg border overflow-hidden transition-all focus:outline-none focus-visible:ring-2 focus-visible:ring-primary"
                  :class="[
                    form.kind === key
                      ? 'border-primary bg-primary-soft/40 ring-1 ring-primary/30'
                      : 'border-border bg-surface hover:bg-surface-hover',
                    drawerMode === 'edit' ? 'cursor-default opacity-70' : 'cursor-pointer',
                  ]"
                  @click="drawerMode === 'create' && selectType(key)"
                >
                  <!-- Accent bar -->
                  <span
                    class="absolute left-0 top-0 bottom-0 w-[3px] rounded-l-lg"
                    :style="{ background: bt.color }"
                  />
                  <div class="pl-3 flex items-start gap-2.5">
                    <div
                      class="w-7 h-7 rounded-md flex items-center justify-center shrink-0 mt-0.5"
                      :style="{ background: bt.color + '20' }"
                    >
                      <component :is="typeIcon(key)" class="w-4 h-4" :style="{ color: bt.color }" />
                    </div>
                    <div class="min-w-0">
                      <p class="text-[12px] font-semibold text-foreground leading-tight">{{ bt.label }}</p>
                      <p class="text-[11px] text-muted leading-snug mt-0.5">{{ bt.desc }}</p>
                    </div>
                  </div>
                </button>
              </div>
            </div>

            <!-- Dynamic config fields -->
            <div v-if="form.kind && BACKEND_TYPES[form.kind]">
              <label class="block text-xs font-semibold text-foreground mb-3">Configuration</label>
              <div class="grid grid-cols-2 gap-x-3 gap-y-4">
                <template v-for="field in visibleFields" :key="field.key">
                  <div :class="field.half ? 'col-span-1' : 'col-span-2'">
                    <!-- Toggle -->
                    <template v-if="field.type === 'toggle'">
                      <div class="flex items-center justify-between py-1.5">
                        <div class="min-w-0 pr-3">
                          <p class="text-xs font-semibold text-foreground">{{ field.label }}</p>
                          <p v-if="field.hint" class="text-[11px] text-muted mt-0.5">{{ field.hint }}</p>
                        </div>
                        <button
                          type="button"
                          role="switch"
                          :aria-checked="!!form.config[field.key]"
                          class="relative inline-flex h-5 w-9 shrink-0 rounded-full transition-colors duration-200 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary"
                          :class="form.config[field.key] ? 'bg-primary' : 'bg-border'"
                          @click="form.config[field.key] = !form.config[field.key]"
                        >
                          <span
                            class="inline-block h-4 w-4 rounded-full bg-white shadow transition-transform duration-200 mt-0.5"
                            :class="form.config[field.key] ? 'translate-x-4' : 'translate-x-0.5'"
                          />
                        </button>
                      </div>
                    </template>

                    <!-- Segmented control -->
                    <template v-else-if="field.type === 'segmented'">
                      <label class="block text-xs font-semibold text-foreground mb-1.5">{{ field.label }}</label>
                      <div class="inline-flex p-0.5 rounded-lg bg-surface-hover border border-border">
                        <button
                          v-for="opt in field.options"
                          :key="opt.v"
                          type="button"
                          class="px-3 py-1 rounded-md text-xs font-medium transition-all"
                          :class="
                            (form.config[field.key] ?? field.default) === opt.v
                              ? 'bg-surface text-foreground shadow-sm'
                              : 'text-muted hover:text-foreground'
                          "
                          @click="form.config[field.key] = opt.v"
                        >
                          {{ opt.label }}
                        </button>
                      </div>
                    </template>

                    <!-- Textarea -->
                    <template v-else-if="field.type === 'textarea'">
                      <label class="block text-xs font-semibold text-foreground mb-1.5">
                        {{ field.label }}
                        <span v-if="field.required" class="text-danger ml-0.5">*</span>
                      </label>
                      <textarea
                        v-model="(form.config[field.key] as string)"
                        :placeholder="field.placeholder"
                        :rows="field.rows ?? 4"
                        class="w-full px-3 py-2 rounded-lg border border-border bg-bg text-foreground text-sm placeholder:text-muted/50 focus:outline-none focus:ring-2 focus:ring-primary resize-none transition-colors"
                        :class="field.mono ? 'font-mono text-[12px]' : ''"
                      />
                      <p v-if="field.hint" class="text-[11px] text-muted mt-1">{{ field.hint }}</p>
                    </template>

                    <!-- Text / Secret -->
                    <template v-else>
                      <label class="block text-xs font-semibold text-foreground mb-1.5">
                        {{ field.label }}
                        <span v-if="field.required" class="text-danger ml-0.5">*</span>
                      </label>
                      <div class="relative">
                        <input
                          v-model="(form.config[field.key] as string)"
                          :type="field.type === 'secret'
                            ? (revealedSecrets[field.key] ? 'text' : 'password')
                            : 'text'"
                          :placeholder="field.type === 'secret' && form.config['has_' + field.key] ? t('adm.storage.secretStored') : field.placeholder"
                          :aria-label="field.label"
                          class="w-full px-3 py-2 rounded-lg border border-border bg-bg text-foreground text-sm placeholder:text-muted/50 focus:outline-none focus:ring-2 focus:ring-primary transition-colors"
                          :class="[
                            field.mono ? 'font-mono text-[12px]' : '',
                            field.type === 'secret' ? 'pr-12' : '',
                          ]"
                        />
                        <button
                          v-if="field.type === 'secret'"
                          type="button"
                          :aria-label="`${revealedSecrets[field.key] ? t('adm.ai.hideKey') : t('adm.ai.showKey')}: ${field.label}`"
                          :aria-pressed="!!revealedSecrets[field.key]"
                          class="absolute right-2.5 top-1/2 -translate-y-1/2 text-[11px] font-medium text-muted hover:text-foreground transition-colors"
                          @click="revealedSecrets[field.key] = !revealedSecrets[field.key]"
                        >
                          {{ revealedSecrets[field.key] ? t('adm.ai.hide') : t('adm.ai.show') }}
                        </button>
                      </div>
                      <p v-if="field.hint" class="text-[11px] text-muted mt-1">{{ field.hint }}</p>
                    </template>
                  </div>
                </template>
              </div>
            </div>

            <!-- Set as default toggle -->
            <div class="flex items-start gap-3 p-3.5 rounded-lg border border-border bg-surface-hover/30">
              <button
                type="button"
                role="switch"
                :aria-checked="form.is_default"
                class="relative inline-flex h-5 w-9 shrink-0 rounded-full transition-colors duration-200 mt-0.5 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary"
                :class="form.is_default ? 'bg-primary' : 'bg-border'"
                @click="form.is_default = !form.is_default"
              >
                <span
                  class="inline-block h-4 w-4 rounded-full bg-white shadow transition-transform duration-200 mt-0.5"
                  :class="form.is_default ? 'translate-x-4' : 'translate-x-0.5'"
                />
              </button>
              <div>
                <p class="text-xs font-semibold text-foreground">Set as default backend</p>
                <p class="text-[11px] text-muted mt-0.5 leading-relaxed">
                  New repositories without an explicit backend will use the default.
                  Only one backend can be default at a time.
                </p>
              </div>
            </div>

            <!-- Test result alert -->
            <div
              v-if="testResult !== null"
              class="rounded-lg border p-3 flex items-start gap-2.5"
              :class="testResult.ok
                ? 'border-success/40 bg-success-soft text-success'
                : 'border-danger/40 bg-danger-soft text-danger'"
            >
              <CheckIcon v-if="testResult.ok" class="w-4 h-4 mt-0.5 shrink-0" />
              <ExclamationCircleIcon v-else class="w-4 h-4 mt-0.5 shrink-0" />
              <div class="min-w-0">
                <p class="text-xs font-semibold">
                  {{ testResult.ok ? 'Connection successful' : 'Connection failed' }}
                </p>
                <p v-if="testResult.message" class="text-[11px] mt-0.5 font-mono break-all">
                  {{ testResult.message }}
                </p>
              </div>
            </div>
          </div>

          <template #footer>
            <button
              type="button"
              :disabled="drawerMode === 'create' || !form.name || testingDrawer"
              class="mr-auto inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium border border-border bg-surface text-foreground hover:bg-surface-hover transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
              :title="drawerMode === 'create' ? t('adm.storage.saveFirst') : t('adm.storage.testSaved')"
              @click="testDrawer"
            >
              <ArrowPathIcon v-if="testingDrawer" class="w-4 h-4 animate-spin" aria-hidden="true" />
              <BoltIcon v-else class="w-4 h-4" aria-hidden="true" />
              {{ t('adm.storage.testConnection') }}
            </button>
            <button
              type="button"
              class="px-3 py-1.5 rounded-lg text-sm font-medium text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
              @click="closeDrawer"
            >
              {{ t('common.cancel') }}
            </button>
            <button
              type="button"
              :disabled="savingDrawer || !form.name || !form.kind"
              class="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-lg text-sm font-medium bg-primary text-primary-contrast hover:opacity-90 transition-opacity disabled:opacity-50 disabled:cursor-not-allowed"
              @click="saveDrawer"
            >
              <ArrowPathIcon v-if="savingDrawer" class="w-4 h-4 animate-spin" aria-hidden="true" />
              {{ drawerMode === 'create' ? t('adm.storage.createAction') : t('adm.orgs.saveAction') }}
            </button>
          </template>
    </AdminDialog>

    <!-- ═══════════════════════════════════════════════════════════════════════
         DELETE CONFIRMATION MODAL
    ════════════════════════════════════════════════════════════════════════════ -->
    <AdminDialog
      :open="!!deleteTarget"
      max-width="max-w-md"
      :title="t('adm.storage.deleteTitle')"
      :description="t('adm.orgs.deleteHint')"
      @close="cancelDelete"
    >
          <div v-if="deleteTarget" class="space-y-4">
            <!-- Danger alert -->
            <div class="rounded-lg border border-danger/30 bg-danger-soft p-3">
              <div class="flex items-start gap-2.5">
                <ExclamationCircleIcon class="w-4 h-4 text-danger mt-0.5 shrink-0" aria-hidden="true" />
                <div>
                  <p class="text-xs font-semibold text-danger">{{ t('adm.storage.deleteWarnTitle') }}</p>
                  <p class="text-[11px] text-danger/80 mt-0.5 leading-relaxed">{{ t('adm.storage.deleteWarnBody') }}</p>
                </div>
              </div>
            </div>

            <!-- Backend summary -->
            <div class="flex items-center gap-2.5 p-2.5 rounded-lg bg-surface-hover">
              <span class="inline-flex items-center pl-1.5 pr-2 py-1 rounded-md bg-surface border border-border relative overflow-hidden shrink-0">
                <span
                  class="absolute left-0 top-0 bottom-0 w-[3px] rounded-l-md"
                  :style="{ background: typeColor(deleteTarget.kind) }"
                />
                <span class="pl-1.5 flex items-center gap-1.5">
                  <component
                    :is="typeIcon(deleteTarget.kind)"
                    class="w-3.5 h-3.5"
                    :style="{ color: typeColor(deleteTarget.kind) }"
                  />
                  <span class="font-mono text-[11px] font-medium text-foreground">
                    {{ typeMeta(deleteTarget.kind)?.label }}
                  </span>
                </span>
              </span>
              <span class="font-mono text-[12px] text-muted truncate">
                {{ locationSummary(deleteTarget) }}
              </span>
            </div>

            <!-- Confirmation input -->
            <div>
              <label for="storage-delete-confirm" class="block text-xs font-semibold text-foreground mb-1.5">
                {{ t('adm.orgs.typeToConfirm') }}
                <code class="font-mono text-danger bg-danger-soft px-1 py-0.5 rounded mx-1">{{ deleteTarget.name }}</code>
              </label>
              <input
                id="storage-delete-confirm"
                v-model="deleteConfirmName"
                type="text"
                :placeholder="deleteTarget.name"
                class="w-full px-3 py-2 rounded-lg border bg-bg text-foreground text-sm font-mono placeholder:text-muted/50 focus:outline-none focus:ring-2 transition-colors border-border focus:ring-danger"
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
              class="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-lg text-sm font-medium bg-danger text-white hover:opacity-90 transition-opacity disabled:opacity-40 disabled:cursor-not-allowed"
              @click="confirmDelete"
            >
              <ArrowPathIcon v-if="deleting" class="w-4 h-4 animate-spin" aria-hidden="true" />
              <TrashIcon v-else class="w-4 h-4" aria-hidden="true" />
              {{ t('adm.storage.deleteAction') }}
            </button>
          </template>
    </AdminDialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, reactive } from 'vue'
import type { Component } from 'vue'
import {
  listStorageBackends,
  createStorageBackend,
  updateStorageBackend,
  deleteStorageBackend,
  testStorageBackend,
  type StorageBackend as StorageApi,
} from '../../api/storage'
import { useToastStore } from '../../stores/toast'
import AdminDialog from '../../components/AdminDialog.vue'
import { t } from '../../i18n/en'
import {
  ServerIcon,
  CloudIcon,
  BoltIcon,
  PencilIcon,
  StarIcon,
  TrashIcon,
  CheckIcon,
  PlusIcon,
  ArrowPathIcon,
  ExclamationCircleIcon,
  ChevronRightIcon,
} from '@heroicons/vue/24/outline'

// ─── Types ─────────────────────────────────────────────────────────────────────

// The generated StorageBackend has optional fields; page code expects non-optional
type StorageBackend = StorageApi & {
  name: string
  kind: string
  // Vocabulary reported by the backend (`healthy` / `unreachable` / `unknown`).
  status: 'healthy' | 'unreachable' | 'unknown'
  is_default: boolean
}

interface FieldOption {
  v: string
  label: string
}

interface FieldDef {
  key: string
  label: string
  type: 'text' | 'secret' | 'textarea' | 'toggle' | 'segmented'
  placeholder?: string
  mono?: boolean
  required?: boolean
  hint?: string
  half?: boolean
  rows?: number
  options?: FieldOption[]
  default?: string
  when?: Record<string, string>
}

interface BackendType {
  label: string
  desc: string
  color: string
  fields: FieldDef[]
  summary: (c: Record<string, unknown>) => string
}

// ─── Backend Type Registry ─────────────────────────────────────────────────────

const BACKEND_TYPES: Record<string, BackendType> = {
  filesystem: {
    label: 'Local Filesystem',
    desc: 'Path on the server disk.',
    color: '#64748b',
    fields: [
      {
        key: 'path',
        label: 'Path',
        type: 'text',
        placeholder: '/var/lib/openartifact/storage',
        mono: true,
        required: true,
        hint: 'Absolute path. Must be writable.',
      },
    ],
    summary: (c) => (c.path as string) || '',
  },
  s3: {
    label: 'S3 / S3-Compatible',
    desc: 'AWS S3, MinIO, Ceph RGW, Backblaze B2.',
    color: '#f59e0b',
    fields: [
      {
        key: 'endpoint',
        label: 'Endpoint',
        type: 'text',
        placeholder: 'https://s3.amazonaws.com',
        mono: true,
        hint: 'Leave default for AWS S3.',
      },
      {
        key: 'bucket',
        label: 'Bucket',
        type: 'text',
        placeholder: 'oa-prod-artifacts',
        mono: true,
        required: true,
      },
      {
        key: 'region',
        label: 'Region',
        type: 'text',
        placeholder: 'eu-west-1',
        mono: true,
        required: true,
        half: true,
      },
      {
        key: 'pathStyle',
        label: 'Path-style URLs',
        type: 'toggle',
        half: true,
        hint: 'Required for MinIO/Ceph.',
      },
      {
        key: 'accessKey',
        label: 'Access key ID',
        type: 'text',
        placeholder: 'AKIA…',
        mono: true,
        required: true,
      },
      {
        key: 'secretKey',
        label: 'Secret access key',
        type: 'secret',
        placeholder: '••••••••',
        mono: true,
        required: true,
      },
    ],
    summary: (c) => `${(c.bucket as string) || 'bucket'} · ${(c.region as string) || 'region'}`,
  },
  gcs: {
    label: 'Google Cloud Storage',
    desc: 'GCS bucket with service-account credentials.',
    color: '#4285f4',
    fields: [
      {
        key: 'project',
        label: 'Project ID',
        type: 'text',
        placeholder: 'my-org-prod',
        mono: true,
        required: true,
        half: true,
      },
      {
        key: 'bucket',
        label: 'Bucket',
        type: 'text',
        placeholder: 'oa-artifacts',
        mono: true,
        required: true,
        half: true,
      },
      {
        key: 'creds',
        label: 'Service account JSON',
        type: 'textarea',
        rows: 5,
        mono: true,
        required: true,
        placeholder: '{ "type": "service_account", ... }',
        hint: 'Paste the full JSON key.',
      },
    ],
    summary: (c) =>
      `${(c.bucket as string) || 'bucket'} · ${(c.project as string) || 'project'}`,
  },
  azure: {
    label: 'Azure Blob Storage',
    desc: 'Azure storage account, container.',
    color: '#0078d4',
    fields: [
      {
        key: 'authMode',
        label: 'Auth mode',
        type: 'segmented',
        options: [
          { v: 'key', label: 'Account key' },
          { v: 'conn', label: 'Connection string' },
        ],
        default: 'key',
      },
      {
        key: 'account',
        label: 'Account name',
        type: 'text',
        placeholder: 'oaprodstorage',
        mono: true,
        required: true,
        half: true,
        when: { authMode: 'key' },
      },
      {
        key: 'container',
        label: 'Container',
        type: 'text',
        placeholder: 'artifacts',
        mono: true,
        required: true,
        half: true,
      },
      {
        key: 'accountKey',
        label: 'Account key',
        type: 'secret',
        placeholder: '••••••••',
        mono: true,
        required: true,
        when: { authMode: 'key' },
      },
      {
        key: 'connStr',
        label: 'Connection string',
        type: 'secret',
        placeholder: 'DefaultEndpointsProtocol=https;…',
        mono: true,
        required: true,
        when: { authMode: 'conn' },
      },
    ],
    summary: (c) =>
      `${(c.container as string) || 'container'} · ${(c.account as string) || ''}`,
  },
  garage: {
    label: 'Garage',
    desc: 'Self-hosted geo-distributed object storage',
    color: '#f97316',
    fields: [
      {
        key: 'endpoint',
        label: 'Endpoint',
        type: 'text',
        placeholder: 'https://garage.example.com',
        mono: true,
        required: true,
        hint: 'HTTP(S) endpoint of your Garage node.',
      },
      {
        key: 'bucket',
        label: 'Bucket',
        type: 'text',
        placeholder: 'oa-artifacts',
        mono: true,
        required: true,
        half: true,
      },
      {
        key: 'region',
        label: 'Region',
        type: 'text',
        placeholder: 'garage',
        mono: true,
        required: true,
        half: true,
        default: 'garage',
      },
      {
        key: 'accessKey',
        label: 'Access key ID',
        type: 'text',
        placeholder: 'GKabcdef…',
        mono: true,
        required: true,
      },
      {
        key: 'secretKey',
        label: 'Secret access key',
        type: 'secret',
        placeholder: '••••••••',
        mono: true,
        required: true,
      },
    ],
    summary: (c) => `${(c.bucket as string) || 'bucket'} · ${(c.endpoint as string) || 'endpoint'}`,
  },
  seaweedfs: {
    label: 'SeaweedFS',
    desc: 'Distributed blob store with O(1) disk access',
    color: '#8b5cf6',
    fields: [
      {
        key: 'endpoint',
        label: 'Endpoint',
        type: 'text',
        placeholder: 'https://seaweedfs.example.com',
        mono: true,
        required: true,
        hint: 'SeaweedFS S3 gateway endpoint.',
      },
      {
        key: 'bucket',
        label: 'Bucket',
        type: 'text',
        placeholder: 'oa-artifacts',
        mono: true,
        required: true,
        half: true,
      },
      {
        key: 'region',
        label: 'Region',
        type: 'text',
        placeholder: 'us-east-1',
        mono: true,
        required: true,
        half: true,
        default: 'us-east-1',
      },
      {
        key: 'accessKey',
        label: 'Access key ID',
        type: 'text',
        placeholder: 'admin',
        mono: true,
        required: true,
      },
      {
        key: 'secretKey',
        label: 'Secret access key',
        type: 'secret',
        placeholder: '••••••••',
        mono: true,
        required: true,
      },
    ],
    summary: (c) => `${(c.bucket as string) || 'bucket'} · ${(c.endpoint as string) || 'endpoint'}`,
  },
  oci: {
    label: 'OCI Registry',
    desc: 'OCI Distribution Spec v1.1 compliant registry',
    color: '#ec4899',
    fields: [
      {
        key: 'endpoint',
        label: 'Endpoint',
        type: 'text',
        placeholder: 'registry.example.com',
        mono: true,
        required: true,
        hint: 'Hostname (and optional port) of the OCI registry.',
      },
      {
        key: 'bucket',
        label: 'Repository',
        type: 'text',
        placeholder: 'org/artifacts',
        mono: true,
        required: true,
      },
      {
        key: 'username',
        label: 'Username',
        type: 'text',
        placeholder: 'robot$push',
        mono: true,
        half: true,
      },
      {
        key: 'password',
        label: 'Password / Token',
        type: 'secret',
        placeholder: '••••••••',
        mono: true,
        half: true,
      },
      {
        key: 'insecure',
        label: 'Allow insecure HTTP',
        type: 'toggle',
        hint: 'Disable TLS verification (not recommended for production).',
      },
    ],
    summary: (c) => `${(c.bucket as string) || 'repository'} · ${(c.endpoint as string) || 'endpoint'}`,
  },
}

// ─── Reactive State ────────────────────────────────────────────────────────────

const toasts = useToastStore()
const items = ref<StorageBackend[]>([])
const loading = ref(true)
const testingOne = ref<string | null>(null)
const testingAll = ref(false)

// Drawer
const drawerOpen = ref(false)
const drawerMode = ref<'create' | 'edit'>('create')
const editingBackend = ref<StorageBackend | null>(null)
const savingDrawer = ref(false)
const testingDrawer = ref(false)
const testResult = ref<{ ok: boolean; message?: string } | null>(null)
const revealedSecrets = reactive<Record<string, boolean>>({})

const form = reactive<{
  name: string
  kind: string
  is_default: boolean
  config: Record<string, unknown>
}>({
  name: '',
  kind: '',
  is_default: false,
  config: {},
})

// Delete
const deleteTarget = ref<StorageBackend | null>(null)
const deleteConfirmName = ref('')
const deleting = ref(false)

// ─── Computed ──────────────────────────────────────────────────────────────────

const statTiles = computed(() => [
  {
    label: t('adm.storage.stat.configured'),
    value: items.value.length,
    icon: ServerIcon,
    iconBg: 'rgb(var(--color-primary) / 0.1)',
    iconColor: 'rgb(var(--color-primary))',
  },
  {
    label: t('adm.storage.status.healthy'),
    value: items.value.filter((i) => i.status === 'healthy').length,
    icon: CheckIcon,
    iconBg: 'rgb(var(--color-success-soft))',
    iconColor: 'rgb(var(--color-success))',
  },
  {
    label: t('adm.storage.status.unknown'),
    value: items.value.filter((i) => i.status !== 'healthy' && i.status !== 'unreachable').length,
    icon: ExclamationCircleIcon,
    iconBg: 'rgb(var(--color-warning-soft))',
    iconColor: 'rgb(var(--color-warning))',
  },
  {
    label: t('adm.storage.status.unreachable'),
    value: items.value.filter((i) => i.status === 'unreachable').length,
    icon: ExclamationCircleIcon,
    iconBg: 'rgb(var(--color-danger-soft))',
    iconColor: 'rgb(var(--color-danger))',
  },
])

const visibleFields = computed<FieldDef[]>(() => {
  if (!form.kind || !BACKEND_TYPES[form.kind]) return []
  return BACKEND_TYPES[form.kind].fields.filter((field) => {
    if (!field.when) return true
    return Object.entries(field.when).every(([k, v]) => {
      const cfgVal = form.config[k]
      const fieldDef = BACKEND_TYPES[form.kind].fields.find((f) => f.key === k)
      const actual = cfgVal ?? fieldDef?.default
      return actual === v
    })
  })
})

// ─── Helpers ───────────────────────────────────────────────────────────────────

function typeMeta(kind: string): BackendType | undefined {
  return BACKEND_TYPES[kind]
}

function typeColor(kind: string): string {
  return BACKEND_TYPES[kind]?.color ?? '#6b7280'
}

function typeColorBg(kind: string): string {
  // Hex color with ~10% opacity
  return typeColor(kind) + '1a'
}

function typeIcon(kind: string): Component {
  return kind === 'filesystem' ? ServerIcon : CloudIcon
}

function locationSummary(b: StorageBackend): string {
  const meta = BACKEND_TYPES[b.kind]
  return meta ? meta.summary(b.config as Record<string, unknown>) : ''
}

function statusLabel(s: string): string {
  const map: Record<string, string> = {
    healthy: t('adm.storage.status.healthy'),
    unreachable: t('adm.storage.status.unreachable'),
    unknown: t('adm.storage.status.unknown'),
  }
  return map[s] ?? s
}

function statusBadgeClass(s: string): string {
  const map: Record<string, string> = {
    healthy: 'bg-success-soft text-success',
    unreachable: 'bg-danger-soft text-danger',
    unknown: 'bg-surface-hover text-muted',
  }
  return map[s] ?? 'bg-surface-hover text-muted'
}

function statusDotClass(s: string): string {
  const map: Record<string, string> = {
    healthy: 'bg-success',
    unreachable: 'bg-danger',
    unknown: 'bg-muted',
  }
  return map[s] ?? 'bg-muted'
}

function timeAgo(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime()
  const minutes = Math.floor(diff / 60_000)
  if (minutes < 1) return t('adm.backup.justNow')
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h`
  return `${Math.floor(hours / 24)}d`
}

// ─── Data Loading ──────────────────────────────────────────────────────────────

async function load(): Promise<void> {
  loading.value = true
  try {
    const res = await listStorageBackends()
    items.value = (res.items ?? []) as StorageBackend[]
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.storage.loadFailed'))
  } finally {
    loading.value = false
  }
}

// ─── Test Actions ──────────────────────────────────────────────────────────────

async function testOne(name: string): Promise<void> {
  testingOne.value = name
  try {
    const res = await testStorageBackend(name)
    if (res.ok) toasts.success(`${name}: ${t('adm.storage.status.healthy')}`)
    else toasts.error(`${name}: ${res.message ?? t('adm.storage.testFailed')}`)
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.storage.testFailed'))
  } finally {
    testingOne.value = null
  }
}

// testStorageBackend resolves with {ok:false} for failed checks, so failures
// are counted from the results rather than from rejections.
async function testAll(): Promise<void> {
  testingAll.value = true
  try {
    const results = await Promise.allSettled(items.value.map((b) => testStorageBackend(b.name)))
    const failed = results.filter((r) => r.status === 'rejected' || !r.value.ok).length
    if (failed === 0) toasts.success(t('adm.storage.allHealthy'))
    else toasts.warning(`${failed} ${t('adm.storage.someFailed')}`)
    await load()
  } finally {
    testingAll.value = false
  }
}

async function testDrawer(): Promise<void> {
  if (drawerMode.value === 'create') return
  if (!editingBackend.value) return
  testResult.value = null
  testingDrawer.value = true
  try {
    const res = await testStorageBackend(editingBackend.value.name)
    testResult.value = res
    await load()
  } catch (err) {
    testResult.value = { ok: false, message: err instanceof Error ? err.message : t('adm.storage.testFailed') }
  } finally {
    testingDrawer.value = false
  }
}

// ─── Set Default ───────────────────────────────────────────────────────────────

async function setDefault(backend: StorageBackend): Promise<void> {
  try {
    await updateStorageBackend(backend.name, { is_default: true })
    toasts.success(`${t('adm.storage.nowDefault')}: ${backend.name}`)
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.storage.saveFailed'))
  }
}

// ─── Drawer Actions ────────────────────────────────────────────────────────────

function resetForm(): void {
  form.name = ''
  form.kind = ''
  form.is_default = false
  form.config = {}
  testResult.value = null
  // Clear revealed secrets
  Object.keys(revealedSecrets).forEach((k) => {
    delete revealedSecrets[k]
  })
}

function openCreate(): void {
  resetForm()
  drawerMode.value = 'create'
  editingBackend.value = null
  drawerOpen.value = true
}

function openEdit(backend: StorageBackend): void {
  resetForm()
  drawerMode.value = 'edit'
  editingBackend.value = backend
  form.name = backend.name
  form.kind = backend.kind
  form.is_default = backend.is_default
  form.config = { ...backend.config }
  drawerOpen.value = true
}

function closeDrawer(): void {
  drawerOpen.value = false
  editingBackend.value = null
}

function selectType(kind: string): void {
  form.kind = kind
  form.config = {}
  testResult.value = null
}

// The server never returns secrets; it reports `has_<field>: true` instead and
// keeps the stored secret when the field is omitted or empty on update.
function submittableConfig(): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(form.config)) {
    if (k.startsWith('has_')) continue
    if (v === '' || v === undefined || v === null) continue
    out[k] = v
  }
  return out
}

async function saveDrawer(): Promise<void> {
  if (!form.name || !form.kind || savingDrawer.value) return
  savingDrawer.value = true
  try {
    if (drawerMode.value === 'create') {
      await createStorageBackend({
        name: form.name,
        kind: form.kind,
        is_default: form.is_default,
        config: submittableConfig(),
      })
      toasts.success(`${t('adm.storage.created')}: ${form.name}`)
    } else if (editingBackend.value) {
      await updateStorageBackend(editingBackend.value.name, {
        is_default: form.is_default,
        config: submittableConfig(),
      })
      toasts.success(`${t('adm.storage.updated')}: ${form.name}`)
    }
    closeDrawer()
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.storage.saveFailed'))
  } finally {
    savingDrawer.value = false
  }
}

// ─── Delete Actions ────────────────────────────────────────────────────────────

function openDelete(backend: StorageBackend): void {
  deleteTarget.value = backend
  deleteConfirmName.value = ''
}

function cancelDelete(): void {
  deleteTarget.value = null
  deleteConfirmName.value = ''
}

async function confirmDelete(): Promise<void> {
  if (!deleteTarget.value || deleteConfirmName.value !== deleteTarget.value.name) return
  deleting.value = true
  try {
    await deleteStorageBackend(deleteTarget.value.name)
    toasts.success(`${t('adm.storage.deleted')}: ${deleteTarget.value.name}`)
    deleteTarget.value = null
    deleteConfirmName.value = ''
    await load()
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.storage.deleteFailed'))
  } finally {
    deleting.value = false
  }
}

// ─── Lifecycle ─────────────────────────────────────────────────────────────────

onMounted(load)
</script>
