// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// FOUC guard: apply the stored theme before Vue mounts. A separate file (not
// an inline <script>) because the server's CSP is script-src 'self'.
;(function () {
  try {
    var raw = localStorage.getItem('oa.theme')
    var mode = raw ? JSON.parse(raw) : 'auto'
    if (mode !== 'light' && mode !== 'dark') mode = 'auto'
    var prefersDark = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches
    var dark = mode === 'dark' || (mode === 'auto' && prefersDark)
    if (dark) document.documentElement.classList.add('dark')
  } catch (_) {
    // ignore; default light
  }
})()
