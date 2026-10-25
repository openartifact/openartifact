<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  About dialog. The product name is the instance's; the "Powered by OpenArtifact"
  line, the licence, the source link and the upstream link are not configurable.
  The commit comes from the admin-only system info, so only admins see it.
-->
<template>
  <AdminDialog :open="open" :title="tp('about.title', { product: store.productName })" max-width="max-w-md" @close="emit('close')">
    <div class="space-y-4 text-sm" data-testid="about-dialog">
      <div>
        <p class="font-medium text-foreground" data-testid="about-powered-by">{{ poweredBy }}</p>
        <p class="mt-0.5 text-muted" data-testid="about-license">{{ t('brand.license') }}</p>
        <p class="mt-0.5 text-xs text-muted">{{ t('about.sourceHint') }}</p>
      </div>
      <ul class="space-y-1.5">
        <li>
          <a :href="sourceHref" target="_blank" rel="noopener noreferrer" data-testid="about-source" class="inline-flex items-center gap-1.5 text-primary hover:underline">
            <CodeBracketIcon class="h-4 w-4 shrink-0" aria-hidden="true" />{{ t('brand.sourceCode') }}
          </a>
        </li>
        <li>
          <a :href="DEFAULT_SOURCE_URL" target="_blank" rel="noopener noreferrer" data-testid="about-upstream" class="inline-flex items-center gap-1.5 text-primary hover:underline">
            <ArrowTopRightOnSquareIcon class="h-4 w-4 shrink-0" aria-hidden="true" />{{ t('about.upstream') }}
          </a>
        </li>
        <li v-if="docsUrl">
          <a :href="docsUrl" target="_blank" rel="noopener noreferrer" data-testid="about-docs" class="inline-flex items-center gap-1.5 text-primary hover:underline">
            <BookOpenIcon class="h-4 w-4 shrink-0" aria-hidden="true" />{{ t('about.docs') }}
          </a>
        </li>
        <li v-if="apiReferenceEnabled">
          <a href="/api/reference" target="_blank" rel="noopener noreferrer" data-testid="about-api-reference" class="inline-flex items-center gap-1.5 text-primary hover:underline">
            <CommandLineIcon class="h-4 w-4 shrink-0" aria-hidden="true" />{{ t('about.apiReference') }}
          </a>
        </li>
        <li v-if="supportHref">
          <a :href="supportHref" target="_blank" rel="noopener noreferrer" data-testid="about-support" class="inline-flex items-center gap-1.5 text-primary hover:underline">
            <LifebuoyIcon class="h-4 w-4 shrink-0" aria-hidden="true" />{{ t('about.support') }}
          </a>
        </li>
      </ul>
    </div>
    <template #footer>
      <AppButton variant="secondary" size="sm" @click="emit('close')">{{ t('common.close') }}</AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ArrowTopRightOnSquareIcon, BookOpenIcon, CodeBracketIcon, CommandLineIcon, LifebuoyIcon } from '@heroicons/vue/24/outline'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import { getAdminSystemInfo } from '../api/admin'
import { useBrandingStore } from '../stores/branding'
import { useSessionStore } from '../stores/session'
import { useAppVersion, useApiReferenceEnabled, useDocsUrl, formatVersion } from '../composables/useAppVersion'
import { formatCommit } from '../lib/buildInfo'
import { DEFAULT_SOURCE_URL, safeUrl } from '../lib/branding'
import { t, tp } from '../i18n/en'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const store = useBrandingStore()
const session = useSessionStore()
const version = useAppVersion()
const docsUrl = useDocsUrl()
const apiReferenceEnabled = useApiReferenceEnabled()

const commit = ref<string | null>(null)
watch(
  () => props.open,
  async (open) => {
    if (!open || commit.value || !session.isAdmin) return
    try {
      commit.value = formatCommit((await getAdminSystemInfo()).commit)
    } catch {
      // the commit is a nicety: the dialog stands without it
    }
  },
  { immediate: true },
)

const sourceHref = computed(() => safeUrl(store.sourceUrl) || DEFAULT_SOURCE_URL)
const supportHref = computed(() => safeUrl(store.supportUrl, 'contact'))
const poweredBy = computed(() => {
  let text = t('brand.poweredBy')
  if (version.value) text += ` ${formatVersion(version.value)}`
  if (commit.value) text += ` (${commit.value})`
  return text
})
</script>
