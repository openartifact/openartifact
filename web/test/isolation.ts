// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Per-test isolation, registered as a global hook for every test file.
//
// `bun test` runs all files in a single process that shares one global object
// and one module registry, and `--randomize` (or simply a different file order,
// e.g. on CI) shuffles which file observes which leftovers. Anything a test
// leaves behind on a process-wide object therefore leaks into unrelated files.
// This hook puts every such object back after each test:
//
//   - globals replaced by assignment (fetch, XMLHttpRequest)
//   - spies created with spyOn()
//   - the active Pinia
//   - the shared system info (version, docs base) of useAppVersion
//   - fake timers
//   - DOM state shared through the one happy-dom window: body, <html>
//     attributes, localStorage/sessionStorage, cookies, window.location
//
// Not covered, deliberately: `mock.module()`. It rewrites Bun's module registry
// for the rest of the run and neither `mock.restore()` nor anything here can
// undo it, so test files must not use it. Stub a module's exports with
// `spyOn(moduleNamespace, 'name')` instead; `mock.restore()` below reverts it.
import { afterEach, jest, mock } from 'bun:test'
import { setActivePinia } from 'pinia'
import { enableAutoUnmount } from '@vue/test-utils'
import { resetAppInfo } from '../src/composables/useAppVersion'

const originalFetch = globalThis.fetch
const originalXMLHttpRequest = globalThis.XMLHttpRequest

// Unmount every wrapper created with mount() once the test is over.
enableAutoUnmount(afterEach)

afterEach(() => {
  mock.restore()
  jest.useRealTimers()

  globalThis.fetch = originalFetch
  globalThis.XMLHttpRequest = originalXMLHttpRequest

  setActivePinia(undefined)
  // The server version and docs base are fetched once per process; start every test unloaded.
  resetAppInfo()

  document.body.innerHTML = ''
  document.documentElement.removeAttribute('class')
  document.documentElement.removeAttribute('style')
  window.localStorage.clear()
  window.sessionStorage.clear()
  for (const cookie of document.cookie.split(';')) {
    const name = cookie.split('=')[0]?.trim()
    if (name) document.cookie = `${name}=; expires=Thu, 01 Jan 1970 00:00:00 GMT; path=/`
  }
  // Keep history.state as is: vue-router keeps its own bookkeeping in it.
  window.history.replaceState(window.history.state, '', '/')
})
