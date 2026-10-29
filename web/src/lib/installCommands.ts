// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Builds the native client URL / install command for a package. Repository
// routes mirror cmd/server/main.go: most formats live under /repos/{repo};
// ivy, maven, raw and sbt use an isolated /repos/{format}/{repo} prefix, and
// docker serves the registry API at the host root (/v2/). The repository base
// URL and the per-client configure/publish snippets are templates generated
// from internal/clientsetup (clientsetup.generated.ts); this file only
// substitutes them. The install commands (pullCommand) are web-only.

import {
  BASE_TEMPLATES,
  CLIENT_SETUP,
  DEFAULT_BASE_TEMPLATE,
  FALLBACK_CLIENT_SETUP,
  type ClientSetupTemplate,
} from './clientsetup.generated'

/**
 * Substitutes a generated template: `{{#http}}...{{/http}}` blocks are kept for
 * a plain-http origin and dropped otherwise, then each `{{name}}` becomes its
 * value in one pass (substituted values are never rescanned). Mirrors `render`
 * in internal/clientsetup.
 */
function renderTemplate(template: string, values: Readonly<Record<string, string>>): string {
  const plainHttp = (values.origin ?? '').startsWith('http://')
  return template
    .replace(/\{\{#(\w+)\}\}([\s\S]*?)\{\{\/\w+\}\}/g, (_, name: string, body: string) =>
      name === 'http' && plainHttp ? body : '',
    )
    .replace(/\{\{(\w+)\}\}/g, (_, name: string) => values[name] ?? '')
}

/** Native base URL of a repository for its format (no trailing slash). */
export function repoBaseUrl(format: string, origin: string, repo: string): string {
  const template = Object.hasOwn(BASE_TEMPLATES, format) ? BASE_TEMPLATES[format] : DEFAULT_BASE_TEMPLATE
  return renderTemplate(template, { origin, esc: encodeURIComponent(repo) })
}

function hostOf(origin: string): string {
  try {
    return new URL(origin).host
  } catch {
    return origin.replace(/^https?:\/\//, '')
  }
}

// sbt and nix's netrc match credentials on the host alone, so the port is left out.
function hostnameOf(origin: string): string {
  try {
    return new URL(origin).hostname
  } catch {
    return hostOf(origin).replace(/:\d+$/, '')
  }
}

const SHELL_SAFE = /^[A-Za-z0-9._@/+:=-]+$/

/**
 * One shell word: left bare when it only has characters no shell treats
 * specially, single-quoted otherwise so a name copied from the page cannot
 * split, expand or chain commands.
 */
export function shellQuote(token: string): string {
  if (SHELL_SAFE.test(token)) return token
  return `'${token.replace(/'/g, `'\\''`)}'`
}

/** A raw artifact path as a URL path: every segment encoded, slashes kept. */
function encodePath(path: string): string {
  return path.split('/').map(encodeURIComponent).join('/')
}

/**
 * HCL usage block for a terraform row. The catalog names a module
 * `modules/NAME/SYSTEM` and a provider platform `providers/TYPE`; terraform's
 * registry namespace is the repository name.
 */
function terraformUsage(host: string, repo: string, pkg: string, version: string): string {
  const [kind, name, system] = pkg.split('/')
  if (kind === 'modules' && name && system) {
    return `module "${name}" {\n  source  = "${host}/${repo}/${name}/${system}"\n  version = "${version}"\n}`
  }
  if (kind === 'providers' && name) {
    return `terraform {\n  required_providers {\n    ${name} = {\n      source  = "${host}/${repo}/${name}"\n      version = "${version}"\n    }\n  }\n}`
  }
  return `${host}/${repo}`
}

/** Install/pull command for a package version, built from the real server origin. */
export function pullCommand(
  format: string,
  origin: string,
  repo: string,
  pkg: string,
  version: string,
  repoType = 'hosted',
): string {
  const base = repoBaseUrl(format, origin, repo)
  const q = shellQuote
  switch (format) {
    case 'ansible':
      return `ansible-galaxy collection install ${q(`${pkg}:${version}`)} --server ${q(`${base}/api/`)}`
    case 'apk':
      return `apk add ${q(`${pkg}=${version}`)}`
    case 'cargo':
      return `cargo install ${q(pkg)} --version ${q(version)} --index ${q(`sparse+${base}/index/`)}`
    case 'composer':
      return `composer require ${q(`${pkg}:${version}`)}`
    case 'conan':
      return `conan install ${q(`--requires=${pkg}/${version}`)} -r ${q(repo)}`
    case 'conda':
      return `conda install -c ${q(base)} ${q(`${pkg}=${version}`)}`
    case 'deb':
      return `apt install ${q(`${pkg}=${version}`)}`
    case 'docker':
      // The listed package name already is the registry name, `<repo>` or
      // `<repo>/<image path>` (internal/ocipath), so it is the pull path.
      return `docker pull ${q(`${hostOf(origin)}/${pkg}:${version}`)}`
    case 'go':
      // A quoted value is still a valid environment assignment; a quoted word is not.
      return `GOPROXY=${q(base)} go get ${q(`${pkg}@${version}`)}`
    case 'helm':
      return `helm pull ${q(pkg)} --repo ${q(base)} --version ${q(version)}`
    case 'ivy':
      return `<ibiblio name="${repo}" m2compatible="true" root="${base}"/>`
    case 'maven':
      return `mvn dependency:get ${q(`-Dartifact=${pkg}:${version}`)} ${q(`-DremoteRepositories=${base}`)}`
    case 'nix':
      // The version is the 32 character store hash; the path is `<hash>-<name>`.
      return `nix copy --from ${q(base)} ${q(`/nix/store/${version}-${pkg}`)}`
    case 'npm':
      return `npm install ${q(`${pkg}@${version}`)} ${q(`--registry=${base}/`)}`
    case 'nuget':
      return `dotnet add package ${q(pkg)} --version ${q(version)} --source ${q(`${base}/v3/index.json`)}`
    case 'pacman':
      // A proxy mirrors the whole Arch mirror: the package is named without a repository qualifier.
      return repoType === 'proxy' ? `sudo pacman -Sy ${q(pkg)}` : `sudo pacman -Sy ${q(`${repo}/${pkg}`)}`
    case 'pypi':
      return `pip install ${q(`${pkg}==${version}`)} --index-url ${q(`${base}/simple/`)}`
    case 'raw':
      return `curl -fO ${q(`${base}/${encodePath(pkg)}`)}`
    case 'rpm':
      return `dnf install ${q(`${pkg}-${version}`)}`
    case 'rubygems':
      return `gem install ${q(pkg)} -v ${q(version)} --source ${q(`${base}/`)}`
    case 'sbt':
      return `resolvers += "${repo}" at "${base}"`
    case 'terraform':
      return terraformUsage(hostOf(origin), repo, pkg, version)
    default:
      return `${base}/`
  }
}

/**
 * Docker pull pinned to a manifest digest (`sha256:<hex>`; a bare hex digest is
 * accepted), or null when the digest is not a sha256 one. The pinned form does
 * not move when the tag is re-pushed.
 */
export function dockerDigestPull(origin: string, pkg: string, digest: string): string | null {
  const hex = digest.replace(/^sha256:/, '')
  if (!/^[0-9a-f]{64}$/.test(hex)) return null
  return `docker pull ${shellQuote(`${hostOf(origin)}/${pkg}@sha256:${hex}`)}`
}

interface ClientSetup {
  client: string
  configure: string
  publish?: string
}

/**
 * Per-client configure/publish snippets for a repository, built from the real
 * server origin. Credentials are always the literal `<USERNAME>` / `<TOKEN>`
 * placeholders. The text comes from the generated templates of the repository
 * type; proxy and group fall back to the hosted ones.
 */
export function clientSetup(format: string, origin: string, repo: string, repoType = 'hosted'): ClientSetup[] {
  const variants = Object.hasOwn(CLIENT_SETUP, format) ? CLIENT_SETUP[format] : FALLBACK_CLIENT_SETUP
  const typed = repoType === 'proxy' || repoType === 'group' ? variants[repoType] : undefined
  const templates: readonly ClientSetupTemplate[] = typed ?? variants.hosted
  const base = repoBaseUrl(format, origin, repo)
  const values = {
    origin,
    host: hostOf(origin),
    hostname: hostnameOf(origin),
    repo,
    esc: encodeURIComponent(repo),
    base,
    authBase: base.replace(/^([a-z][a-z0-9+.-]*:\/\/)/i, '$1<USERNAME>:<TOKEN>@'),
  }
  return templates.map((t) => {
    const out: ClientSetup = { client: t.client, configure: renderTemplate(t.configure, values) }
    if (t.publish) out.publish = renderTemplate(t.publish, values)
    return out
  })
}
