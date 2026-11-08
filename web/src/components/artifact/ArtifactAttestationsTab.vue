<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Attestations tab of a docker image: the panel, fed with the shown tag's manifest digest. -->
<template>
  <ImageAttestationsPanel :repo="ctx.repoName.value" :image="ctx.pkgName.value" :subject="subject" :can-admin="ctx.caps.value.maintain" />
</template>

<script setup lang="ts">
import { computed } from 'vue'
import ImageAttestationsPanel from '../ImageAttestationsPanel.vue'
import { useArtifactContext } from '../../composables/useArtifact'
import { subjectDigest } from '../../lib/attestations'

const ctx = useArtifactContext()
const subject = computed(() => subjectDigest(ctx.detail.value?.sha256))
</script>
