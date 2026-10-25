// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, type ComputedRef } from 'vue'
import { useRoute, useRouter } from 'vue-router'

/**
 * A `?<key>=1` flag (default `new`: "open the create drawer"), so a palette link or a reload opens the drawer
 * and Back closes it. Other query parameters are left alone.
 */
export function useUrlFlag(key = 'new'): { on: ComputedRef<boolean>; set(value: boolean): Promise<void> } {
  const route = useRoute()
  const router = useRouter()
  const on = computed(() => route.query[key] === '1')

  async function set(value: boolean): Promise<void> {
    const query = { ...router.currentRoute.value.query }
    if (value) query[key] = '1'
    else delete query[key]
    await router.replace({ query })
  }

  return { on, set }
}
