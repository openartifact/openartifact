<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- The fade-and-scale of a popup menu panel (Headless UI Menu/Popover); the panel goes in the slot. -->
<template>
  <transition
    enter-active-class="transition duration-100 ease-out"
    enter-from-class="opacity-0 scale-95"
    enter-to-class="opacity-100 scale-100"
    leave-active-class="transition duration-75 ease-in"
    leave-from-class="opacity-100 scale-100"
    leave-to-class="opacity-0 scale-95"
  >
    <slot />
  </transition>
</template>
