<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Settings > Security > Attestations: the key that signs SBOM and VEX attestations, how to verify with it, and which docker repositories attach them to images. -->
<template>
  <div class="space-y-5">
    <SectionRefresh :loading="loading" @refresh="load" />

    <div v-if="loading && !key && !problem" role="status" class="rounded-lg border border-border bg-surface px-4 py-5 text-sm text-muted">
      {{ t('common.loading') }}
    </div>

    <div v-else-if="problem" class="rounded-lg border border-border bg-surface" :data-testid="`attestation-${problem}`">
      <StateBlock
        :variant="problem === 'off' ? 'off' : 'error'"
        :title="t(`sx.attestation.${problem}.title`)"
        :hint="problem === 'error' ? message : t(`sx.attestation.${problem}.body`)"
        :command="problem === 'secret-required' ? 'OPENARTIFACT_SECRET_KEY=<random secret>' : undefined"
      >
        <template #action>
          <AppButton v-if="problem === 'error'" variant="secondary" @click="load">{{ t('ui.state.retry') }}</AppButton>
          <DocsLink :url="docsUrl" />
        </template>
      </StateBlock>
    </div>

    <template v-else-if="key">
      <CardSection :title="t('sx.attestation.key')" :description="t('sx.attestation.key.hint')">
        <dl class="grid gap-x-6 gap-y-3 px-5 py-4 text-sm sm:grid-cols-2 lg:grid-cols-3" data-testid="attestation-key">
          <div class="min-w-0">
            <dt class="text-muted">{{ t('sx.attestation.keyId') }}</dt>
            <dd class="flex items-center gap-1">
              <span class="min-w-0 break-all font-mono text-xs text-foreground" data-testid="key-id">{{ key.key_id }}</span>
              <CopyButton v-if="key.key_id" :text="key.key_id" :label="t('sx.attestation.keyId')" />
            </dd>
          </div>
          <div>
            <dt class="text-muted">{{ t('sx.attestation.algorithm') }}</dt>
            <dd class="font-mono text-foreground">{{ key.algorithm }}</dd>
          </div>
          <div>
            <dt class="text-muted">{{ t('sx.attestation.source') }}</dt>
            <dd class="text-foreground" data-testid="key-source">{{ key.source === 'file' ? t('sx.attestation.source.file') : t('sx.attestation.source.generated') }}</dd>
          </div>
        </dl>
        <div class="border-t border-border px-5 py-4">
          <div class="flex items-center justify-between gap-2">
            <h3 class="text-sm font-medium text-foreground">{{ t('sx.attestation.publicKey') }}</h3>
            <div class="flex items-center gap-1">
              <CopyButton :text="key.public_key_pem ?? ''" :label="t('sx.attestation.publicKey.copy')" />
              <AppButton variant="secondary" size="sm" @click="downloadPem">
                <ArrowDownTrayIcon class="h-4 w-4" aria-hidden="true" />
                {{ t('sx.attestation.publicKey.download') }}
              </AppButton>
            </div>
          </div>
          <pre class="mt-2 max-h-48 overflow-auto rounded-md bg-surface-hover p-3 font-mono text-xs text-foreground" data-testid="key-pem">{{ key.public_key_pem }}</pre>
          <p class="mt-2 flex flex-wrap items-center gap-1 text-xs text-muted">
            {{ t('sx.attestation.publicUrl') }}
            <code class="break-all font-mono text-foreground" data-testid="key-url">{{ keyUrl }}</code>
            <CopyButton :text="keyUrl" :label="t('sx.attestation.publicUrl')" />
          </p>
        </div>
      </CardSection>

      <CardSection :title="t('sx.attestation.verify')" :description="t('sx.attestation.verify.hint')">
        <div class="flex items-start gap-2 px-5 py-4">
          <pre class="min-w-0 flex-1 overflow-x-auto rounded-md bg-surface-hover p-3 font-mono text-xs text-foreground" data-testid="verify-snippet">{{ snippet }}</pre>
          <CopyButton :text="snippet" :label="t('sx.attestation.verify')" />
        </div>
        <p class="border-t border-border px-5 py-3"><DocsLink :url="docsUrl" /></p>
      </CardSection>

      <AttestationAttachTable :rows="rows" :error="rowsError" @toggle="toggle" />
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ArrowDownTrayIcon } from '@heroicons/vue/20/solid'
import AppButton from '../../../components/AppButton.vue'
import AttestationAttachTable from '../../../components/AttestationAttachTable.vue'
import DocsLink from '../../../components/DocsLink.vue'
import SectionRefresh from '../../../components/SectionRefresh.vue'
import CardSection from '../../../components/ui/CardSection.vue'
import CopyButton from '../../../components/ui/CopyButton.vue'
import StateBlock from '../../../components/ui/StateBlock.vue'
import { useAttestation } from '../../../composables/useAttestation'
import { useDocsLink } from '../../../composables/useDocsLink'
import { absoluteUrl, verifySnippet } from '../../../lib/attestation'
import { t } from '../../../i18n'

const { key, problem, message, loading, rows, rowsError, load, toggle } = useAttestation()
const docsUrl = useDocsLink('settings-attestation')

const keyUrl = computed(() => absoluteUrl(key.value?.public_key_url ?? '', window.location.origin))
const snippet = computed(() => verifySnippet(keyUrl.value))

function downloadPem(): void {
  const url = URL.createObjectURL(new Blob([key.value?.public_key_pem ?? ''], { type: 'application/x-pem-file' }))
  const link = document.createElement('a')
  link.href = url
  link.download = 'attestation-key.pem'
  document.body.appendChild(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
}
</script>
