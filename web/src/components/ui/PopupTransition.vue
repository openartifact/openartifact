<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- The fade-and-scale of a popup menu panel (Headless UI Menu/Popover); the panel goes in the slot. -->
<template>
  <transition
    enter-active-class="motion-safe:transition motion-safe:duration-100 motion-safe:ease-out"
    enter-from-class="motion-safe:opacity-0 motion-safe:scale-95"
    enter-to-class="motion-safe:opacity-100 motion-safe:scale-100"
    leave-active-class="motion-safe:transition motion-safe:duration-75 motion-safe:ease-in"
    leave-from-class="motion-safe:opacity-100 motion-safe:scale-100"
    leave-to-class="motion-safe:opacity-0 motion-safe:scale-95"
  >
    <slot />
  </transition>
</template>
