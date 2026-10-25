<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Settings > Storage > Trash: the instance-wide trash (every repository), the shared trash list without a repository scope. -->
<template>
  <TrashList />
</template>

<script setup lang="ts">
import TrashList from '../../../components/trash/TrashList.vue'
</script>
