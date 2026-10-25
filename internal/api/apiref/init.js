// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Starts the Scalar API reference. A separate same-origin file so the page
// needs no inline script (CSP script-src 'self').
(function () {
  'use strict'

  var CSRF_COOKIE = 'oa_csrf'
  var SAFE_METHODS = ['GET', 'HEAD', 'OPTIONS', 'TRACE']

  function cookie(name) {
    var parts = document.cookie ? document.cookie.split('; ') : []
    for (var i = 0; i < parts.length; i++) {
      var eq = parts[i].indexOf('=')
      if (eq > 0 && parts[i].slice(0, eq) === name) {
        return decodeURIComponent(parts[i].slice(eq + 1))
      }
    }
    return ''
  }

  // Requests sent with the browser session need the double-submit CSRF header
  // on state-changing methods; a Bearer token needs neither.
  function apiFetch(input, init) {
    var request = new Request(input, init)
    var token = cookie(CSRF_COOKIE)
    if (token && SAFE_METHODS.indexOf(request.method.toUpperCase()) === -1 && !request.headers.has('X-CSRF-Token')) {
      request.headers.set('X-CSRF-Token', token)
    }
    // The client sends "Authorization: Bearer" with no token until one is
    // entered; drop it so the session cookie is what authenticates the call.
    if (/^Bearer\s*$/i.test(request.headers.get('Authorization') || '')) {
      request.headers.delete('Authorization')
    }
    return fetch(request, { credentials: 'same-origin' })
  }

  Scalar.createApiReference('#app', {
    url: '/api/v1/openapi.json',
    customFetch: apiFetch,
    // Nothing leaves the origin: no telemetry, no hosted proxy, no external
    // fonts, no AI agent or MCP promotion.
    telemetry: false,
    proxyUrl: '',
    withDefaultFonts: false,
    agent: { disabled: true },
    mcp: { disabled: true },
    showDeveloperTools: 'never',
    authentication: { preferredSecurityScheme: 'bearerAuth' },
  })
})()
