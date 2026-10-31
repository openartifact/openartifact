// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Form model shared by the repository create drawer and the repository settings
// tab: validation, request bodies and the one-line summaries of the collapsed rows.

import type { Repository } from '../api/repositories'
import { presignModeOf, presignValueOf, type PresignMode } from '../api/repositories'
import { SYSTEM_PROJECT_NAME } from '../api/projects'
import type { FormatId } from './formats'
import { toBytes, splitBytes, formatBytes, type SizeUnit } from './quota'
import { parseRetentionInput } from './trash'
import { federationAvailable, type RepoType } from './repoTypes'
import { t, tp, tpn } from '../i18n'

export interface RepoForm {
  name: string
  description: string
  format: FormatId
  type: RepoType
  isPublic: boolean
  /** Project name; '' is the built-in system project. */
  project: string
  upstreamUrl: string
  upstreamUsername: string
  /** Write-only: empty keeps the stored secret on update. */
  upstreamPassword: string
  /** Ordered member repository names (group). */
  members: string[]
  /** Trash retention in days as typed; '' is the server default. */
  retentionDays: string
  /** Storage quota as typed (create only); '' is unlimited. */
  quotaValue: string
  quotaUnit: SizeUnit
  immutableReleases: boolean
  signPackages: boolean
  mergeProxyVersions: boolean
  federated: boolean
  presign: PresignMode
}

export function emptyRepoForm(overrides: Partial<RepoForm> = {}): RepoForm {
  return {
    name: '',
    description: '',
    format: 'docker',
    type: 'hosted',
    isPublic: false,
    project: '',
    upstreamUrl: '',
    upstreamUsername: '',
    upstreamPassword: '',
    members: [],
    retentionDays: '',
    quotaValue: '',
    quotaUnit: 'GB',
    immutableReleases: false,
    signPackages: false,
    mergeProxyVersions: false,
    federated: false,
    presign: 'inherit',
    ...overrides,
  }
}

/** Edit form seeded from a repository. */
export function repoFormFrom(r: Repository): RepoForm {
  return emptyRepoForm({
    name: r.name,
    description: r.description ?? '',
    format: r.format as FormatId,
    type: r.repo_type ?? 'hosted',
    isPublic: r.public ?? false,
    project: r.project && r.project !== SYSTEM_PROJECT_NAME ? r.project : '',
    members: [...(r.member_repos ?? [])],
    retentionDays: r.trash_retention_days == null ? '' : String(r.trash_retention_days),
    immutableReleases: r.immutable_releases ?? false,
    signPackages: r.sign_packages ?? false,
    mergeProxyVersions: r.merge_proxy_versions ?? false,
    federated: r.federated ?? false,
    presign: presignModeOf(r),
  })
}

const NAME_RE = /^[a-zA-Z0-9_-]{1,100}$/

/** Why the name is not acceptable ('' when it is, or while still empty). Mirrors the server rule plus the project prefix. */
export function nameError(name: string, prefix: string): string {
  const n = name.trim()
  if (n === '') return ''
  if (!NAME_RE.test(n)) return t('repos.new.nameHint')
  if (!n.startsWith(prefix)) return `${t('repos.project.prefix')} "${prefix}"`
  return ''
}

export function nameValid(name: string, prefix: string): boolean {
  const n = name.trim()
  return NAME_RE.test(n) && n.startsWith(prefix)
}

/** Retention error text ('' when the input is empty or a whole number of days). */
export function retentionError(raw: string): string {
  return parseRetentionInput(raw) === undefined ? t('repos.settings.trashRetentionInvalid') : ''
}

/** Quota in bytes from the typed value; 0 is unlimited. */
export function quotaBytesOf(form: Pick<RepoForm, 'quotaValue' | 'quotaUnit'>): number {
  const n = Number(form.quotaValue)
  return Number.isFinite(n) ? toBytes(n, form.quotaUnit) : 0
}

export function formValid(form: RepoForm, prefix: string, upstreamRequired: boolean): boolean {
  if (!nameValid(form.name, prefix)) return false
  if (retentionError(form.retentionDays) !== '') return false
  if (form.quotaValue !== '' && !(Number(form.quotaValue) >= 0)) return false
  if (form.type === 'proxy' && upstreamRequired && form.upstreamUrl.trim() === '') return false
  return true
}

type CreateBody = Partial<Repository> & { upstream_password?: string }

/** Body of POST /repositories (and of the project-scoped create). */
export function buildCreateBody(form: RepoForm, ctx: { isAdmin: boolean }): CreateBody {
  const retention = parseRetentionInput(form.retentionDays)
  return {
    name: form.name.trim(),
    format: form.format,
    public: form.isPublic,
    description: form.description.trim(),
    repo_type: form.type,
    ...(retention !== undefined && retention !== null ? { trash_retention_days: retention } : {}),
    ...(form.immutableReleases ? { immutable_releases: true } : {}),
    ...(form.type === 'hosted' && form.format === 'rpm' && form.signPackages ? { sign_packages: true } : {}),
    ...(form.type === 'group' && ctx.isAdmin && form.mergeProxyVersions ? { merge_proxy_versions: true } : {}),
    ...(federationAvailable(form.type, form.format) && form.federated ? { federated: true } : {}),
    ...(form.project && ctx.isAdmin ? { project: form.project } : {}),
    ...(form.type === 'proxy'
      ? {
          upstream_url: form.upstreamUrl.trim() || undefined,
          upstream_username: form.upstreamUsername || undefined,
          upstream_password: form.upstreamPassword || undefined,
        }
      : {}),
    ...(form.type === 'group' ? { member_repos: form.members } : {}),
  }
}

/** Body of PATCH /repository/{name}: only what the repository's type and the caller's role allow. A proxy's upstream is edited by `ProxySettingsForm`. */
export function buildUpdateBody(form: RepoForm, repo: Repository, ctx: { isAdmin: boolean }): CreateBody {
  const type = repo.repo_type ?? 'hosted'
  return {
    description: form.description,
    public: form.isPublic,
    presign_downloads: presignValueOf(form.presign),
    // Explicit null resets to the server default.
    trash_retention_days: parseRetentionInput(form.retentionDays) ?? null,
    immutable_releases: form.immutableReleases,
    // The API answers 403 to non-admins that send these, so only admins send them.
    ...(type === 'hosted' && repo.format === 'rpm' ? { sign_packages: form.signPackages } : {}),
    ...(type === 'group' && ctx.isAdmin ? { merge_proxy_versions: form.mergeProxyVersions } : {}),
    ...(ctx.isAdmin && form.project !== (repo.project === SYSTEM_PROJECT_NAME ? '' : (repo.project ?? ''))
      ? { project: form.project || SYSTEM_PROJECT_NAME }
      : {}),
  }
}

// ---- one-line summaries of the collapsed rows ----

export function typeSummary(form: Pick<RepoForm, 'type'>): string {
  return t(`repos.new.type.${form.type}` as 'repos.new.type.hosted')
}

export function visibilitySummary(form: Pick<RepoForm, 'isPublic'>): string {
  return form.isPublic ? t('rl.vis.public') : t('rl.vis.private')
}

export function projectSummary(form: Pick<RepoForm, 'project'>): string {
  return form.project || t('rl.project.system')
}

export function retentionSummary(form: Pick<RepoForm, 'retentionDays' | 'quotaValue' | 'quotaUnit'>, showQuota: boolean): string {
  const days = parseRetentionInput(form.retentionDays)
  const retention =
    days === undefined || days === null
      ? t('rl.retention.default')
      : days === 0
        ? t('rl.retention.none')
        : tpn('rl.retention.days', days)
  if (!showQuota) return retention
  const bytes = quotaBytesOf(form)
  const quota = bytes > 0 ? tp('rl.quota.limit', { size: formatBytes(bytes) }) : t('rl.quota.unlimited')
  return `${retention} · ${quota}`
}

export function policiesSummary(form: RepoForm): string {
  const parts: string[] = []
  if (form.type === 'hosted' && form.format === 'rpm') parts.push(form.signPackages ? t('rl.policies.signed') : t('rl.policies.unsigned'))
  parts.push(form.immutableReleases ? t('rl.policies.immutable') : t('rl.policies.mutable'))
  if (federationAvailable(form.type, form.format) && form.federated) parts.push(t('rl.policies.federated'))
  return parts.join(' · ')
}

export function upstreamSummary(form: Pick<RepoForm, 'upstreamUrl' | 'upstreamUsername'>, required: boolean): string {
  if (form.upstreamUrl.trim() === '') return t(required ? 'rl.upstream.needed' : 'rl.upstream.default')
  return form.upstreamUsername ? `${form.upstreamUrl.trim()} · ${form.upstreamUsername}` : form.upstreamUrl.trim()
}

export function membersSummary(form: Pick<RepoForm, 'members'>): string {
  return form.members.length === 0 ? t('rl.members.none') : tpn('rl.members.count', form.members.length)
}

/** Quota seeded into an input pair from a byte limit (settings quota editor). */
export function quotaInputOf(bytes: number | undefined): Pick<RepoForm, 'quotaValue' | 'quotaUnit'> {
  if (!bytes || bytes <= 0) return { quotaValue: '', quotaUnit: 'GB' }
  const s = splitBytes(bytes)
  return { quotaValue: String(s.value), quotaUnit: s.unit }
}
