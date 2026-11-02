// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// How each hosted format is uploaded from the browser: the inputs the user fills in and the request built
// for a picked file. Every request goes to a route the server already serves to the native client and is
// authenticated by the browser session (cookie plus CSRF header), so nothing here is browser-only.
//
// Formats without a plan cannot be published from a browser: cargo (the crate's dependency metadata is
// computed by `cargo publish`), conan, docker, ivy and sbt (many files in a client-specific layout) and nix
// (`nix copy`). `uploadMode` says which of the three a format is.

import type { UploadStartOptions } from '../composables/useUpload'
import type { TranslationKey } from '../i18n'
import { APK_DEFAULT_BRANCH, APK_DEFAULT_REPO, validApkSegment } from './apk'
import { buildNpmPublish } from './npmPublish'
import { PACMAN_DEFAULT_ARCH, pacmanUploadOrder, pacmanUploadUrl } from './pacmanUpload'
import { UploadInputError } from './uploadInput'

export type UploadValues = Record<string, string>

export interface UploadField {
  key: string
  /** Catalog keys of the label and the optional hint and placeholder. */
  label: TranslationKey
  hint?: TranslationKey
  placeholder?: string
  value: string
  optional?: boolean
  /** Catalog key of the message shown for an invalid value. */
  invalid?: TranslationKey
  /** A fixed list of choices (catalog key of each label) instead of free text. */
  options?: { value: string; label: TranslationKey }[]
  /** The field only applies when this holds for the other values. */
  when?: (values: UploadValues) => boolean
  valid?: (value: string) => boolean
}

export interface UploadPlan {
  /** Catalog key of the line under the dropzone. */
  hint: TranslationKey
  accept?: string
  fields: UploadField[]
  /** Present: files are uploaded one at a time in this order. */
  order?: (files: File[]) => File[]
  request: (repo: string, file: File, values: UploadValues) => UploadStartOptions | Promise<UploadStartOptions>
}

const enc = encodeURIComponent
const repoRoot = (repo: string) => `/repos/${enc(repo)}`
const encPath = (path: string) => path.split('/').map(enc).join('/')

const SEGMENT = /^[A-Za-z0-9][A-Za-z0-9._-]*$/
const NAME = /^[A-Za-z0-9]([A-Za-z0-9_-]{0,62}[A-Za-z0-9])?$/
const SEMVER = /^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/
// Accepts what pkg/composer nameRe accepts, written without nested optional separators so it cannot backtrack.
const COMPOSER_NAME = /^[a-z0-9]+(?:[_.-][a-z0-9]+)*\/[a-z0-9]+(?:(?:[_.]|-{1,2})[a-z0-9]+)*$/

/** The message key of a field's value, '' when it is acceptable (or the field does not apply). */
export function fieldError(field: UploadField, values: UploadValues): TranslationKey | '' {
  if (field.when && !field.when(values)) return ''
  const value = (values[field.key] ?? '').trim()
  if (value === '') return field.optional ? '' : (field.invalid ?? 'upload.invalid')
  return field.valid && !field.valid(value) ? (field.invalid ?? 'upload.invalid') : ''
}

export function initialValues(plan: UploadPlan): UploadValues {
  return Object.fromEntries(plan.fields.map((f) => [f.key, f.value]))
}

function directory(value: string): string {
  return value.trim().replace(/^\/+|\/+$/g, '')
}

function validDirectory(value: string): boolean {
  const dir = directory(value)
  return dir === '' || dir.split('/').every((s) => s !== '' && s !== '.' && s !== '..' && !/[\\\u0000-\u001f\u007f]/.test(s))
}

/** Go's module path escaping: an upper-case letter is written as `!` and its lower-case form. */
export function goEscape(value: string): string {
  return value.replace(/[A-Z]/g, (c) => `!${c.toLowerCase()}`)
}

/** `<module>@<version>` read from the name of the first entry of a Go module zip. */
export async function readGoModule(file: Blob): Promise<{ module: string; version: string }> {
  const head = new Uint8Array(await file.slice(0, 30).arrayBuffer())
  if (head.length === 30 && head[0] === 0x50 && head[1] === 0x4b && head[2] === 3 && head[3] === 4) {
    const nameLen = head[26] | (head[27] << 8)
    const raw = new Uint8Array(await file.slice(30, 30 + nameLen).arrayBuffer())
    const m = /^(.+?)@(v[^/]+)\//.exec(new TextDecoder().decode(raw))
    if (m) return { module: m[1], version: m[2] }
  }
  throw new UploadInputError('upload.error.goModule')
}

/** Name, version, file type and Python tag of a wheel or sdist, from its file name. */
export function parsePythonDist(filename: string): { name: string; version: string; filetype: string; pyversion: string } {
  if (filename.endsWith('.whl')) {
    const parts = filename.slice(0, -4).split('-')
    if (parts.length >= 5) return { name: parts[0], version: parts[1], filetype: 'bdist_wheel', pyversion: parts[parts.length - 3] }
  } else {
    const stem = /^(.+)\.(?:tar\.gz|tar\.bz2|tar\.xz|tgz|zip)$/.exec(filename)?.[1]
    const cut = stem?.lastIndexOf('-') ?? -1
    if (stem && cut > 0) return { name: stem.slice(0, cut), version: stem.slice(cut + 1), filetype: 'sdist', pyversion: 'source' }
  }
  throw new UploadInputError('upload.error.pypiName')
}

const TERRAFORM_PROVIDER = /^terraform-provider-(.+)_(\d[^_]*)_([a-z0-9]+)_([a-z0-9]+)\.zip$/

const PLANS: Record<string, UploadPlan> = {
  apk: {
    hint: 'upload.hint.apk',
    accept: '.apk',
    fields: [
      { key: 'branch', label: 'upload.apk.branch', hint: 'upload.apk.branchHint', value: APK_DEFAULT_BRANCH, invalid: 'upload.apk.invalid', valid: validApkSegment },
      { key: 'repo', label: 'upload.apk.repo', hint: 'upload.apk.repoHint', value: APK_DEFAULT_REPO, invalid: 'upload.apk.invalid', valid: validApkSegment },
      { key: 'arch', label: 'upload.apk.arch', hint: 'upload.apk.archHint', placeholder: 'x86_64', value: '', optional: true, invalid: 'upload.apk.invalid', valid: validApkSegment },
    ],
    request: (repo, _file, v) => ({
      url: `/api/v1/repositories/${enc(repo)}/apks`,
      fields: { branch: v.branch.trim(), repo: v.repo.trim(), ...(v.arch.trim() ? { arch: v.arch.trim() } : {}) },
    }),
  },
  deb: { hint: 'upload.hint.deb', accept: '.deb', fields: [], request: (repo) => ({ url: `/api/v1/repositories/${enc(repo)}/packages` }) },
  rpm: { hint: 'upload.hint.rpm', accept: '.rpm', fields: [], request: (repo) => ({ url: `/api/v1/repositories/${enc(repo)}/packages` }) },
  pacman: {
    hint: 'upload.hint.pacman',
    accept: '.zst,.xz,.gz,.sig',
    fields: [{ key: 'arch', label: 'upload.pacman.arch', hint: 'upload.pacman.archHint', value: PACMAN_DEFAULT_ARCH, optional: true }],
    order: pacmanUploadOrder,
    request: (repo, file, v) => ({ url: pacmanUploadUrl(repo, file.name, v.arch.trim() || PACMAN_DEFAULT_ARCH), method: 'PUT', mode: 'raw' }),
  },
  raw: {
    hint: 'upload.hint.raw',
    fields: [{ key: 'dir', label: 'upload.raw.dir', hint: 'upload.raw.dirHint', placeholder: 'releases/1.0', value: '', optional: true, invalid: 'upload.raw.dirInvalid', valid: validDirectory }],
    request: (repo, file, v) => {
      const dir = directory(v.dir)
      return { url: `/repos/raw/${enc(repo)}/${dir ? `${encPath(dir)}/` : ''}${enc(file.name)}`, method: 'PUT', mode: 'raw' }
    },
  },
  npm: {
    hint: 'upload.hint.npm',
    accept: '.tgz,.tar.gz',
    fields: [{ key: 'tag', label: 'upload.npm.tag', hint: 'upload.npm.tagHint', value: 'latest', invalid: 'upload.npm.tagInvalid', valid: (s) => SEGMENT.test(s) && !/^v?\d/.test(s) }],
    request: async (repo, file, v) => {
      const { name, body } = await buildNpmPublish(file, v.tag.trim())
      return { url: `${repoRoot(repo)}/${name.startsWith('@') ? `@${enc(name.slice(1))}` : enc(name)}`, method: 'PUT', mode: 'raw', payload: body }
    },
  },
  pypi: {
    hint: 'upload.hint.pypi',
    accept: '.whl,.tar.gz,.zip,.tar.bz2,.tar.xz',
    fields: [],
    request: (repo, file) => {
      const dist = parsePythonDist(file.name)
      return {
        url: `${repoRoot(repo)}/legacy/`,
        field: 'content',
        fields: { ':action': 'file_upload', protocol_version: '1', name: dist.name, version: dist.version, filetype: dist.filetype, pyversion: dist.pyversion },
      }
    },
  },
  maven: {
    hint: 'upload.hint.maven',
    fields: [
      { key: 'group', label: 'upload.maven.group', hint: 'upload.maven.groupHint', placeholder: 'com.example', value: '', valid: (s) => /^[A-Za-z0-9_$-]+(\.[A-Za-z0-9_$-]+)*$/.test(s) },
      { key: 'artifact', label: 'upload.maven.artifact', placeholder: 'my-lib', value: '', valid: (s) => SEGMENT.test(s) },
      { key: 'version', label: 'upload.maven.version', hint: 'upload.maven.versionHint', placeholder: '1.0.0', value: '', valid: (s) => SEGMENT.test(s) },
    ],
    request: (repo, file, v) => {
      const artifact = v.artifact.trim()
      const version = v.version.trim()
      const stem = `${artifact}-${version}`
      if (!file.name.startsWith(stem) || !/^[.-]/.test(file.name.slice(stem.length))) {
        throw new UploadInputError('upload.error.mavenName')
      }
      const group = v.group.trim().replaceAll('.', '/')
      return { url: `/repos/maven/${enc(repo)}/${group}/${enc(artifact)}/${enc(version)}/${enc(file.name)}`, method: 'PUT', mode: 'raw' }
    },
  },
  helm: { hint: 'upload.hint.helm', accept: '.tgz', fields: [], request: (repo) => ({ url: `${repoRoot(repo)}/charts`, mode: 'raw' }) },
  rubygems: { hint: 'upload.hint.rubygems', accept: '.gem', fields: [], request: (repo) => ({ url: `${repoRoot(repo)}/api/v1/gems`, mode: 'raw' }) },
  nuget: { hint: 'upload.hint.nuget', accept: '.nupkg', fields: [], request: (repo) => ({ url: `${repoRoot(repo)}/v3/package`, method: 'PUT', field: 'package' }) },
  ansible: {
    hint: 'upload.hint.ansible',
    accept: '.tar.gz,.tgz',
    fields: [],
    request: (repo) => ({ url: `${repoRoot(repo)}/api/v3/artifacts/collections/` }),
  },
  conda: {
    hint: 'upload.hint.conda',
    accept: '.conda,.tar.bz2',
    fields: [{ key: 'platform', label: 'upload.conda.platform', hint: 'upload.conda.platformHint', value: 'noarch', valid: (s) => SEGMENT.test(s) && s.length <= 50 }],
    request: (repo, file, v) => ({ url: `${repoRoot(repo)}/${enc(v.platform.trim())}?filename=${enc(file.name)}`, mode: 'raw' }),
  },
  composer: {
    hint: 'upload.hint.composer',
    accept: '.zip',
    fields: [
      { key: 'package', label: 'upload.composer.package', hint: 'upload.composer.packageHint', placeholder: 'vendor/name', value: '', invalid: 'upload.composer.packageInvalid', valid: (s) => s.length <= 128 && COMPOSER_NAME.test(s) },
      { key: 'version', label: 'upload.composer.version', placeholder: '1.0.0', value: '', valid: (s) => !/[/\\\s]/.test(s) },
    ],
    request: (repo, _file, v) => ({ url: `${repoRoot(repo)}/packages/upload/${v.package.trim()}/${enc(v.version.trim())}`, method: 'PUT', mode: 'raw' }),
  },
  terraform: {
    hint: 'upload.hint.terraform',
    accept: '.zip,.tar.gz,.tgz',
    fields: [
      {
        key: 'kind',
        label: 'upload.terraform.kind',
        value: 'module',
        options: [
          { value: 'module', label: 'upload.terraform.module' },
          { value: 'provider', label: 'upload.terraform.provider' },
        ],
      },
      { key: 'name', label: 'upload.terraform.name', placeholder: 'vpc', value: '', when: (v) => v.kind === 'module', valid: (s) => NAME.test(s) },
      { key: 'system', label: 'upload.terraform.system', hint: 'upload.terraform.systemHint', placeholder: 'aws', value: '', when: (v) => v.kind === 'module', valid: (s) => NAME.test(s) },
      { key: 'version', label: 'upload.terraform.version', placeholder: '1.0.0', value: '', when: (v) => v.kind === 'module', invalid: 'upload.terraform.versionInvalid', valid: (s) => SEMVER.test(s) },
    ],
    request: (repo, file, v) => {
      const base = `${repoRoot(repo)}/_api`
      if (v.kind === 'module') {
        return { url: `${base}/modules/${enc(v.name.trim())}/${enc(v.system.trim())}/${enc(v.version.trim())}`, method: 'PUT', mode: 'raw' }
      }
      const m = TERRAFORM_PROVIDER.exec(file.name)
      if (!m) throw new UploadInputError('upload.error.terraformProvider')
      return { url: `${base}/providers/${enc(m[1])}/${enc(m[2])}/${enc(m[3])}/${enc(m[4])}`, method: 'PUT', mode: 'raw' }
    },
  },
  go: {
    hint: 'upload.hint.go',
    accept: '.zip',
    fields: [],
    request: async (repo, file) => {
      const { module, version } = await readGoModule(file)
      return { url: `${repoRoot(repo)}/${encPath(goEscape(module))}/@v/${enc(goEscape(version))}`, method: 'PUT', mode: 'raw' }
    },
  },
}

/** Formats whose repositories show publish instructions instead of a dropzone. */
const PUBLISH_ONLY = new Set(['nix'])

export type UploadMode = 'upload' | 'publish' | 'none'

export function uploadMode(format: string | undefined): UploadMode {
  if (format && Object.hasOwn(PLANS, format)) return 'upload'
  return format && PUBLISH_ONLY.has(format) ? 'publish' : 'none'
}

/** True when the repository page offers an Upload action for the format (a dropzone or publish instructions). */
export function canUpload(format: string | undefined): boolean {
  return uploadMode(format) !== 'none'
}

export function uploadPlan(format: string | undefined): UploadPlan | undefined {
  return format && Object.hasOwn(PLANS, format) ? PLANS[format] : undefined
}
