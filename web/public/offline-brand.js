// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Applies the instance branding to the offline page. The SPA stores
// {product_name, primary_color, light?, dark?} under `oa.branding` whenever it
// learns the branding; nothing is stored for the built-in look, and then the
// page keeps its defaults. A separate file because the server's CSP is
// script-src 'self'. Everything is written with textContent / style.setProperty.
;(function () {
  try {
    var raw = localStorage.getItem('oa.branding')
    if (!raw) return
    var brand = JSON.parse(raw)
    if (!brand || typeof brand !== 'object') return

    var name = typeof brand.product_name === 'string' ? brand.product_name.trim() : ''
    if (name) {
      document.title = 'Offline - ' + name
      var targets = document.querySelectorAll('[data-product-name]')
      for (var i = 0; i < targets.length; i++) targets[i].textContent = name
    }

    var hex = /^#[0-9a-f]{6}$/i
    var dark = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches
    var palette = dark ? brand.dark : brand.light
    var primary = palette && hex.test(palette.primary) ? palette.primary : hex.test(brand.primary_color) ? brand.primary_color : ''
    if (primary) {
      var root = document.documentElement.style
      root.setProperty('--primary', primary)
      if (palette && hex.test(palette.primary_contrast)) root.setProperty('--primary-fg', palette.primary_contrast)
    }
  } catch (_) {
    // ignore; the default look stays
  }
})()
