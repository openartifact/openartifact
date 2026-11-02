<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Settings > Security > Scan gate: which vulnerability scans meet the exploitation criteria, and which are blocked (quarantined). -->
<template>
  <div class="space-y-5">
    <SectionRefresh :loading="gate.blocked.loading.value" @refresh="refresh" />

    <p class="text-sm text-muted" data-testid="gate-intro">
      {{ t('sx.gate.intro') }}
      <DocsLink :url="docsUrl" />
    </p>

    <div v-if="feedsOff" class="rounded-lg border border-border bg-surface" data-testid="gate-feeds-off">
      <StateBlock variant="off" :title="t('sx.gate.feedsOff.title')" :hint="t('sx.gate.feedsOff.body')" command="exploit.enabled: true" />
    </div>

    <template v-else>
      <CardSection :title="t('sx.gate.criteria')" :description="t('sx.gate.criteria.hint')">
        <form class="space-y-4 px-5 py-4" @submit.prevent="gate.search()">
          <label class="flex items-start gap-2 text-sm">
            <input v-model="gate.form.kev" type="checkbox" class="mt-0.5 h-4 w-4 rounded border-border-strong" data-testid="gate-kev" />
            <span>
              <span class="block font-medium text-foreground">{{ t('sx.gate.kev') }}</span>
              <span class="block text-xs text-muted">{{ t('sx.gate.kev.hint') }}</span>
            </span>
          </label>
          <FormField id="gate-epss" :label="t('sx.gate.epss')" :hint="epssHint" :error="gate.invalid.value ? t('sx.gate.epss.invalid') : undefined">
            <input
              id="gate-epss"
              v-model="gate.form.epssPercent"
              type="text"
              inputmode="decimal"
              :class="[INPUT_CLASS, 'max-w-40']"
              :placeholder="t('sx.gate.epss.placeholder')"
              data-testid="gate-epss"
            />
          </FormField>
          <AppButton type="submit" :loading="gate.searching.value" :disabled="!gate.criteria.value || gate.invalid.value" data-testid="gate-search">
            {{ t('sx.gate.search') }}
          </AppButton>
        </form>
        <ExploitFeedStatus class="border-t border-border px-5 py-3" />
      </CardSection>

      <CardSection :title="t('sx.gate.matching')" :description="t('sx.gate.matching.hint')">
        <p v-if="!gate.criteria.value" class="px-5 py-6 text-center text-sm text-muted" data-testid="gate-nocriteria">{{ t('sx.gate.noCriteria') }}</p>
        <template v-else>
          <ScanGateTable
            mode="candidates"
            :rows="gate.candidates.value"
            :caption="t('sx.gate.matching')"
            :empty-title="t('sx.gate.matching.empty')"
            :empty-hint="t('sx.gate.matching.emptyHint')"
            :loading="gate.searching.value"
            :error="gate.searchError.value"
            :verdicts="gate.verdicts.value"
            :changing="gate.changing.value"
            @check="gate.check"
            @block="blocking = $event"
            @retry="gate.search()"
          />
          <p v-if="gate.truncated.value" class="border-t border-border px-5 py-2 text-xs text-muted" data-testid="gate-truncated">{{ t('sx.gate.truncated') }}</p>
        </template>
      </CardSection>
    </template>

    <CardSection :title="t('sx.gate.blockedTitle')" :description="t('sx.gate.blockedTitle.hint')">
      <ScanGateTable
        mode="blocked"
        :rows="gate.blocked.data.value"
        :caption="t('sx.gate.blockedTitle')"
        :empty-title="t('sx.gate.blocked.empty')"
        :empty-hint="t('sx.gate.blocked.emptyHint')"
        :loading="gate.blocked.loading.value"
        :error="gate.blocked.error.value"
        :changing="gate.changing.value"
        @release="releasing = $event"
        @retry="gate.blocked.reload()"
      />
    </CardSection>

    <ConfirmDialog
      :open="blocking !== null"
      :title="t('sx.gate.block.title')"
      :description="tp('sx.gate.block.body', { name: blocking ? `${blocking.package_name}@${blocking.package_version}` : '' })"
      :confirm-label="t('sx.gate.block')"
      :busy="busy"
      @close="blocking = null"
      @confirm="confirm(blocking, true)"
    />
    <ConfirmDialog
      :open="releasing !== null"
      :title="t('sx.gate.release.title')"
      :description="tp('sx.gate.release.body', { name: releasing ? `${releasing.package_name}@${releasing.package_version}` : '' })"
      :confirm-label="t('sx.gate.release')"
      tone="primary"
      :busy="busy"
      @close="releasing = null"
      @confirm="confirm(releasing, false)"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import type { SecurityFinding } from '../../../api/security'
import AppButton from '../../../components/AppButton.vue'
import ConfirmDialog from '../../../components/ConfirmDialog.vue'
import DocsLink from '../../../components/DocsLink.vue'
import FormField from '../../../components/FormField.vue'
import ScanGateTable from '../../../components/ScanGateTable.vue'
import SectionRefresh from '../../../components/SectionRefresh.vue'
import ExploitFeedStatus from '../../../components/security/ExploitFeedStatus.vue'
import CardSection from '../../../components/ui/CardSection.vue'
import StateBlock from '../../../components/ui/StateBlock.vue'
import { useDocsLink } from '../../../composables/useDocsLink'
import { useScanGate } from '../../../composables/useScanGate'
import { INPUT_CLASS } from '../../../lib/formClasses'
import { formatEpssThreshold } from '../../../lib/exploitPriority'
import { t, tp } from '../../../i18n'

const gate = useScanGate()
const docsUrl = useDocsLink('settings-scan-gate')

const blocking = ref<SecurityFinding | null>(null)
const releasing = ref<SecurityFinding | null>(null)
const busy = ref(false)

const feedsOff = computed(() => gate.feeds.data.value?.enabled === false)
const epssHint = computed(() => {
  const threshold = gate.feeds.data.value?.epss_threshold
  return threshold === undefined ? t('sx.gate.epss.hint') : tp('sx.gate.epss.hintThreshold', { threshold: formatEpssThreshold(String(threshold)) })
})

function refresh(): void {
  void gate.feeds.reload()
  void gate.blocked.reload()
  void gate.search()
}

async function confirm(f: SecurityFinding | null, quarantined: boolean): Promise<void> {
  if (!f) return
  busy.value = true
  try {
    await gate.setBlocked(f, quarantined)
  } finally {
    busy.value = false
    blocking.value = null
    releasing.value = null
  }
}
</script>
