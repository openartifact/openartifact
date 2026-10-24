// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, toValue, type ComputedRef, type MaybeRefOrGetter } from 'vue'
import { useDocsUrl } from './useAppVersion'
import { docsUrlFor } from '../lib/docs'

/**
 * Docs URL of the page that explains `routeName` (see `lib/docs.ts`), or null
 * while the server's docs base is unknown, when docs links are disabled, or when
 * the route has no page. Empty states pass the route of the screen they sit on,
 * so a link and the header `?` of that screen agree.
 */
export function useDocsLink(routeName: MaybeRefOrGetter<string | symbol | null | undefined>): ComputedRef<string | null> {
  const base = useDocsUrl()
  return computed(() => docsUrlFor(base.value, toValue(routeName)))
}
