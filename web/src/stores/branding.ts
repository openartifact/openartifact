// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { getBranding } from '../api/branding'
import {
  BRAND_STYLE_ID,
  DEFAULT_PRODUCT_NAME,
  DEFAULT_SOURCE_URL,
  OFFLINE_STORAGE_KEY,
  buildBrandCss,
  composeTitle,
  normalizeBranding,
  offlineBrand,
  readEmbeddedBranding,
  type Branding,
} from '../lib/branding'
import type { ResolvedTheme } from './theme'

const ICON_SELECTOR = 'link[rel~="icon"], link[rel="apple-touch-icon"]'

export const useBrandingStore = defineStore('branding', () => {
  /** Null until the embedded payload or the API answered: everything then falls back to the built-in values. */
  const branding = ref<Branding | null>(null)
  const pageTitle = ref('')

  const productName = computed(() => branding.value?.product_name || DEFAULT_PRODUCT_NAME)
  /** Never empty: the server guarantees it and the normaliser enforces it. */
  const sourceUrl = computed(() => branding.value?.source_url || DEFAULT_SOURCE_URL)
  const supportUrl = computed(() => branding.value?.support_url ?? '')
  const loginMessage = computed(() => branding.value?.login_message ?? '')
  const faviconUrl = computed(() => branding.value?.favicon_url ?? '')

  /** Custom logo for a theme; a single uploaded logo serves both themes. '' means the built-in lockup. */
  function logoFor(theme: ResolvedTheme): string {
    const b = branding.value
    if (!b) return ''
    return theme === 'dark' ? b.logo_dark_url || b.logo_light_url : b.logo_light_url || b.logo_dark_url
  }

  const hasCustomLogo = computed(() => logoFor('light') !== '' || logoFor('dark') !== '')

  function applyTitle(): void {
    document.title = composeTitle(pageTitle.value, productName.value)
    document
      .querySelector('meta[name="apple-mobile-web-app-title"]')
      ?.setAttribute('content', productName.value)
  }

  function applyIcons(): void {
    const favicon = faviconUrl.value
    for (const link of document.querySelectorAll<HTMLLinkElement>(ICON_SELECTOR)) {
      // Remember what the shell shipped so removing the favicon restores it.
      if (link.dataset.oaHref === undefined) {
        link.dataset.oaHref = link.getAttribute('href') ?? ''
        link.dataset.oaType = link.getAttribute('type') ?? ''
      }
      if (favicon) {
        link.setAttribute('href', favicon)
        // The built-in type (svg) would be wrong for an uploaded PNG.
        link.removeAttribute('type')
      } else {
        link.setAttribute('href', link.dataset.oaHref)
        if (link.dataset.oaType) link.setAttribute('type', link.dataset.oaType)
      }
    }
  }

  function applyColors(): void {
    const css = buildBrandCss(branding.value?.theme)
    let style = document.getElementById(BRAND_STYLE_ID)
    if (!style) {
      if (!css) return
      style = document.createElement('style')
      style.id = BRAND_STYLE_ID
    }
    style.textContent = css
    // Last in <head> so it wins over the bundled stylesheet at equal specificity.
    if (style.parentNode !== document.head || document.head.lastElementChild !== style) {
      document.head.appendChild(style)
    }
  }

  function persistOffline(): void {
    try {
      const entry = branding.value ? offlineBrand(branding.value) : null
      if (entry) localStorage.setItem(OFFLINE_STORAGE_KEY, JSON.stringify(entry))
      else localStorage.removeItem(OFFLINE_STORAGE_KEY)
    } catch {
      // storage unavailable: the offline page keeps the built-in look
    }
  }

  /** Writes title, apple title, favicon, colour variables and the offline entry into the document. */
  function applyToDocument(): void {
    if (typeof document === 'undefined') return
    applyTitle()
    applyIcons()
    applyColors()
    persistOffline()
  }

  function set(next: unknown): void {
    const normalized = normalizeBranding(next)
    if (!normalized) return
    branding.value = normalized
    applyToDocument()
  }

  /** Synchronous boot step: uses the payload the server embedded in index.html, if any. */
  function initFromDocument(): void {
    const embedded = readEmbeddedBranding()
    if (embedded) {
      branding.value = embedded
    }
    applyToDocument()
  }

  /** Revalidates against GET /api/v1/branding; a failure keeps what is shown. */
  async function refresh(): Promise<void> {
    try {
      set(await getBranding())
    } catch {
      // keep the embedded or default branding
    }
  }

  function setPageTitle(title: string): void {
    pageTitle.value = title
    if (typeof document !== 'undefined') applyTitle()
  }

  return {
    branding,
    pageTitle,
    productName,
    sourceUrl,
    supportUrl,
    loginMessage,
    faviconUrl,
    hasCustomLogo,
    logoFor,
    applyToDocument,
    set,
    initFromDocument,
    refresh,
    setPageTitle,
  }
})
