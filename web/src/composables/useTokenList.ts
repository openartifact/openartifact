// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, ref } from 'vue'
import { listTokens, revokeEndpoint, revokeToken, type UnifiedToken } from '../api/tokens'
import { useListQuery } from './useListQuery'
import { enumParam, strParam } from './useUrlState'
import { EXPIRING_FILTERS, TOKEN_TYPE_FILTERS, toListParams } from '../lib/tokens'
import { useSessionStore } from '../stores/session'
import { useToastStore } from '../stores/toast'
import { t, tp } from '../i18n'

/**
 * The Access tokens table: type, search, expiry and project filters bound to
 * the URL, plus revoking through the endpoint of each token's type.
 */
export function useTokenList() {
  const session = useSessionStore()
  const toasts = useToastStore()

  const list = useListQuery({
    filters: {
      type: enumParam(TOKEN_TYPE_FILTERS, 'all'),
      q: strParam(),
      expiring: enumParam(EXPIRING_FILTERS, ''),
      project: strParam(),
    },
    debounce: ['q'],
    paging: 'page',
    limit: 25,
    fetch: async (req) => {
      const res = await listTokens({ ...toListParams(req.filters), page: req.page, limit: req.limit })
      return { rows: res.tokens, total: res.total }
    },
  })

  const who = computed(() => ({ username: session.user?.username ?? '', isAdmin: session.isAdmin }))

  function canRevoke(token: UnifiedToken): boolean {
    return revokeEndpoint(token, who.value) !== null
  }

  const revoking = ref<UnifiedToken | null>(null)
  const revokeBusy = ref(false)

  async function confirmRevoke(): Promise<void> {
    const token = revoking.value
    if (!token) return
    revokeBusy.value = true
    try {
      await revokeToken(token, who.value)
      toasts.success(tp('tok.revoke.done', { name: token.name }))
      revoking.value = null
      // Revoking the last row of a page steps back one page.
      if (list.rows.value.length === 1 && list.page.value > 1) list.setPage(list.page.value - 1)
      else await list.reload()
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('tok.revoke.failed'))
    } finally {
      revokeBusy.value = false
    }
  }

  return { list, who, canRevoke, revoking, revokeBusy, confirmRevoke }
}
