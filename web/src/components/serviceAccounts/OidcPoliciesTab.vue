<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Trust policies of every service account: loads the accounts a policy can map to, then the panel. -->
<template>
  <StateBlock v-if="error" variant="error" :hint="error" />
  <OidcPoliciesPanel v-else-if="accounts" :accounts="accounts" />
  <TabSkeleton v-else />
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { listAllServiceAccounts, type ServiceAccountItem } from '../../api/serviceAccounts'
import TabSkeleton from '../TabSkeleton.vue'
import StateBlock from '../ui/StateBlock.vue'
import OidcPoliciesPanel from './OidcPoliciesPanel.vue'

const accounts = ref<ServiceAccountItem[] | null>(null)
const error = ref('')

listAllServiceAccounts()
  .then((res) => (accounts.value = res.items))
  .catch((err: unknown) => (error.value = err instanceof Error ? err.message : String(err)))
</script>
