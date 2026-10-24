// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { plugin } from 'bun'
import { compileScript, compileTemplate, parse } from '@vue/compiler-sfc'
import { readFileSync } from 'node:fs'
import { GlobalRegistrator } from '@happy-dom/global-registrator'

// DOM setup
GlobalRegistrator.register({ url: 'http://localhost/' })

// No test talks to a server: a component that loads data on mount (the docs
// base behind every page header, the version in the sidebar) must be answered
// by a spy or fail at once instead of dialling localhost:80. isolation.ts
// restores this stub after each test.
globalThis.fetch = (() => Promise.reject(new TypeError('network disabled in tests: spy on fetch'))) as typeof fetch

// Vue SFC loader for `*.vue` imports under `bun test`.
plugin({
  name: 'vue-sfc',
  setup(build) {
    build.onLoad({ filter: /\.vue$/ }, (args) => {
      const source = readFileSync(args.path, 'utf8')
      const id = args.path
      const { descriptor } = parse(source, { filename: id })

      const hasScriptSetup = !!descriptor.scriptSetup
      const hasScript = !!descriptor.script
      const scopeId = `data-v-${Math.random().toString(36).slice(2, 10)}`

      let scriptContent = ''
      if (hasScriptSetup || hasScript) {
        const compiled = compileScript(descriptor, {
          id: scopeId,
          inlineTemplate: false,
          genDefaultAs: '__sfc_main',
        })
        scriptContent = compiled.content
      } else {
        scriptContent = `const __sfc_main = {}\nexport default __sfc_main`
      }

      // Strip the `export default` from the script — we'll add our own.
      scriptContent = scriptContent.replace(
        /export\s+default\s+/m,
        'const __sfc_main_default = ',
      )
      // Some compileScript outputs use `export default __sfc_main` (already named).
      // The genDefaultAs above renames it; ensure we have __sfc_main.
      if (!/__sfc_main\b/.test(scriptContent)) {
        scriptContent += `\nconst __sfc_main = (typeof __sfc_main_default !== 'undefined') ? __sfc_main_default : {}`
      }

      let templateContent = ''
      if (descriptor.template) {
        const compiledTemplate = compileTemplate({
          source: descriptor.template.content,
          filename: id,
          id: scopeId,
          compilerOptions: {
            bindingMetadata: descriptor.scriptSetup
              ? (compileScript(descriptor, { id: scopeId }) as any).bindings
              : undefined,
          },
        })
        templateContent = compiledTemplate.code.replace(
          /export\s+(function|const)\s+render/,
          '$1 __sfc_render',
        )
      } else {
        templateContent = `function __sfc_render() { return null }`
      }

      const contents = `
${scriptContent}
${templateContent}
${'__sfc_main'}.render = __sfc_render
${'__sfc_main'}.__file = ${JSON.stringify(id)}
export default __sfc_main
`
      return { contents, loader: 'ts' }
    })
  },
})
