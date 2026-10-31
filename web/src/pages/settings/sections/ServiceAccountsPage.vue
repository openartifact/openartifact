<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Settings > Service accounts: the accounts, and the CI OIDC trust policies that map CI identities to them. -->
<template>
  <AppTabs :tabs="tabs" :label="t('sa.tabs')">
    <template #panel-accounts><ServiceAccountsTab /></template>
    <template #panel-policies><OidcPoliciesTab /></template>
  </AppTabs>
</template>

<script setup lang="ts">
import ServiceAccountsTab from '../../../components/serviceAccounts/ServiceAccountsTab.vue'
import AppTabs from '../../../components/ui/AppTabs.vue'
import type { TabDef } from '../../../composables/useUrlTab'
import { asyncTab } from '../../../lib/asyncTab'
import { t } from '../../../i18n'

const OidcPoliciesTab = asyncTab(() => import('../../../components/serviceAccounts/OidcPoliciesTab.vue'))

const tabs: TabDef[] = [
  { id: 'accounts', label: t('sa.tab.accounts') },
  { id: 'policies', label: t('sa.tab.policies') },
]
</script>
