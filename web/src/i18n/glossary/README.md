<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Translation glossary and style

One `<locale>.csv` per language: a `source,target` header, then the English term and the preferred translation
(UTF-8, quoted; see [the translation guide](../../../../docs/development/translations.md)).
Use these terms every time the concept appears; when a sentence needs another grammatical
form (plural, case, article), inflect the glossary word instead of choosing a synonym.

## Never translated

- Product and project names: OpenArtifact, Docker, npm, Maven, PyPI, Helm, NuGet, Cargo, Go, RPM, Debian/deb,
  RubyGems, Conan, Conda, Ivy, sbt, OIDC, SAML, S3, GCS, Azure, GPG, SBOM, VEX, CycloneDX, SPDX, Dependency-Track,
  Trivy, Grype, Prometheus, Kubernetes, and every other proper noun.
- Package format names, CLI commands and flags, code, file names and paths, URLs, environment variables,
  API field names, HTTP methods and status codes, glob and regex examples, keyboard keys (Ctrl, Esc, ⌘).
- Placeholders in curly braces (`{name}`, `{count}`, `{n}`, `{product}`): keep them exactly, move them
  where the target grammar needs them.
- The fixed attribution (it is not in the catalog).

## Plurals

Keys named `<key>_one` / `<key>_other` (and `_zero`, `_two`, `_few`, `_many` where the language has them in
CLDR) are plural forms of one message. Provide exactly the forms CLDR defines for the language:

| Language | Forms |
|---|---|
| de | `one`, `other` |
| it, es, fr, pt-BR | `one`, `other` (CLDR also defines `many` for round millions: omit it, `other` covers it) |
| ja, zh-Hans | only `other`: no `_one` entry |

In fr and pt-BR the `one` form also covers 0, so write the number as `{n}` there, never a literal "1".

## Tone and conventions

| Locale | Address | Notes |
|---|---|---|
| it | impersonal or "tu"-less imperatives ("Crea repository") | sentence case; "Accedi" / "Esci" |
| de | formal "Sie", or impersonal | nouns capitalised; compound nouns with hyphen for English terms ("Audit-Protokoll") |
| fr | "vous", or impersonal | sentence case; non-breaking space before `:` `;` `?` `!` is NOT required in UI strings (do not add) |
| es | "tú" form, neutral Latin American Spanish | sentence case |
| pt-BR | "você", or impersonal | Brazilian spelling and vocabulary |
| zh-Hans | no pronoun; "您" only when needed | full-width punctuation for Chinese sentences; a space between Chinese and Latin/digits is not needed |
| ja | です・ます form | no spaces around Latin words unless the original has them; "サインイン" for sign in |

Keep the register short: buttons and menu items are verbs or nouns, never sentences. Keep a translation about as
long as the English where possible (labels sit in fixed-width columns); prefer a common short word.
