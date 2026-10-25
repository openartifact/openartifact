// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Registry of the storage backend kinds the admin UI can configure: their
// labels, config field definitions and location summaries, plus the pure
// helpers the storage pages build on.

import type { Component } from 'vue'
import { CloudIcon, ServerIcon } from '@heroicons/vue/24/outline'
import type { StorageBackend as StorageApi } from '../api/storage'
import { t } from '../i18n/en'
import { storageBackendColor, tint } from './brandColors'

// ─── Types ─────────────────────────────────────────────────────────────────────

/** The generated StorageBackend has optional fields; the pages expect them present. */
export type StorageBackend = StorageApi & {
  name: string
  kind: string
  /** Vocabulary reported by the backend (`healthy` / `unreachable` / `unknown`). */
  status: 'healthy' | 'unreachable' | 'unknown'
  is_default: boolean
}

export interface FieldOption {
  v: string
  label: string
}

export interface FieldDef {
  key: string
  label: string
  type: 'text' | 'secret' | 'textarea' | 'toggle' | 'segmented'
  placeholder?: string
  mono?: boolean
  required?: boolean
  hint?: string
  half?: boolean
  rows?: number
  options?: FieldOption[]
  default?: string
  /** Show the field only while every listed config key holds the given value. */
  when?: Record<string, string>
}

export interface BackendType {
  label: string
  desc: string
  fields: FieldDef[]
  summary: (c: Record<string, unknown>) => string
}

type FieldOpts = Partial<Pick<FieldDef, 'placeholder' | 'required' | 'half' | 'hint' | 'default' | 'when'>>

// ─── Field factories ───────────────────────────────────────────────────────────

const SECRET_PLACEHOLDER = '••••••••'

function textField(key: string, label: string, placeholder: string, opts: FieldOpts = {}): FieldDef {
  return { key, label, type: 'text', placeholder, mono: true, ...opts }
}

function secretField(key: string, label: string, opts: FieldOpts = {}): FieldDef {
  return { key, label, type: 'secret', placeholder: SECRET_PLACEHOLDER, mono: true, ...opts }
}

function toggleField(key: string, label: string, hint: string, opts: FieldOpts = {}): FieldDef {
  return { key, label, type: 'toggle', hint, ...opts }
}

interface S3LikeInit {
  endpointPlaceholder: string
  endpointHint: string
  region: string
  accessKeyPlaceholder: string
}

/** Endpoint + bucket + region + key pair, shared by the self-hosted S3 clones. */
function s3LikeFields(init: S3LikeInit): FieldDef[] {
  return [
    textField('endpoint', t('adm.storage.field.endpoint'), init.endpointPlaceholder, {
      required: true,
      hint: init.endpointHint,
    }),
    textField('bucket', t('adm.storage.field.bucket'), 'oa-artifacts', { required: true, half: true }),
    textField('region', t('adm.storage.field.region'), init.region, {
      required: true,
      half: true,
      default: init.region,
    }),
    textField('accessKey', t('adm.storage.field.accessKey'), init.accessKeyPlaceholder, { required: true }),
    secretField('secretKey', t('adm.storage.field.secretKey'), { required: true }),
  ]
}

/** `<primary> · <secondary>` with the placeholder word shown while a value is unset. */
function pairSummary(
  primaryKey: string,
  primaryFallback: string,
  secondaryKey: string,
  secondaryFallback: string,
): (c: Record<string, unknown>) => string {
  return (c) => `${(c[primaryKey] as string) || primaryFallback} · ${(c[secondaryKey] as string) || secondaryFallback}`
}

// ─── Registry ──────────────────────────────────────────────────────────────────

export function buildBackendTypes(): Record<string, BackendType> {
  return {
    filesystem: {
      label: t('adm.storage.type.filesystem.label'),
      desc: t('adm.storage.type.filesystem.desc'),
      fields: [
        textField('path', t('adm.storage.field.path'), '/var/lib/openartifact/storage', {
          required: true,
          hint: t('adm.storage.field.pathHint'),
        }),
      ],
      summary: (c) => (c.path as string) || '',
    },
    s3: {
      label: 'S3 / S3-Compatible',
      desc: t('adm.storage.type.s3.desc'),
      fields: [
        textField('endpoint', t('adm.storage.field.endpoint'), 'https://s3.amazonaws.com', {
          hint: t('adm.storage.field.endpointHint'),
        }),
        textField('bucket', t('adm.storage.field.bucket'), 'oa-prod-artifacts', { required: true }),
        textField('region', t('adm.storage.field.region'), 'eu-west-1', { required: true, half: true }),
        toggleField('pathStyle', t('adm.storage.field.pathStyle'), t('adm.storage.field.pathStyleHint'), {
          half: true,
        }),
        textField('accessKey', t('adm.storage.field.accessKey'), 'AKIA…', { required: true }),
        secretField('secretKey', t('adm.storage.field.secretKey'), { required: true }),
      ],
      summary: pairSummary('bucket', 'bucket', 'region', 'region'),
    },
    gcs: {
      label: 'Google Cloud Storage',
      desc: t('adm.storage.type.gcs.desc'),
      fields: [
        textField('project', t('adm.storage.field.project'), 'my-org-prod', { required: true, half: true }),
        textField('bucket', t('adm.storage.field.bucket'), 'oa-artifacts', { required: true, half: true }),
        {
          key: 'creds',
          label: t('adm.storage.field.creds'),
          type: 'textarea',
          rows: 5,
          mono: true,
          required: true,
          placeholder: '{ "type": "service_account", ... }',
          hint: t('adm.storage.field.credsHint'),
        },
      ],
      summary: pairSummary('bucket', 'bucket', 'project', 'project'),
    },
    azure: {
      label: 'Azure Blob Storage',
      desc: t('adm.storage.type.azure.desc'),
      fields: [
        {
          key: 'authMode',
          label: t('adm.storage.field.authMode'),
          type: 'segmented',
          options: [
            { v: 'key', label: t('adm.storage.field.authMode.key') },
            { v: 'conn', label: t('adm.storage.field.authMode.conn') },
          ],
          default: 'key',
        },
        textField('account', t('adm.storage.field.account'), 'oaprodstorage', {
          required: true,
          half: true,
          when: { authMode: 'key' },
        }),
        textField('container', t('adm.storage.field.container'), 'artifacts', { required: true, half: true }),
        secretField('accountKey', t('adm.storage.field.accountKey'), {
          required: true,
          when: { authMode: 'key' },
        }),
        secretField('connStr', t('adm.storage.field.connStr'), {
          placeholder: 'DefaultEndpointsProtocol=https;…',
          required: true,
          when: { authMode: 'conn' },
        }),
      ],
      summary: pairSummary('container', 'container', 'account', ''),
    },
    garage: {
      label: 'Garage',
      desc: t('adm.storage.type.garage.desc'),
      fields: s3LikeFields({
        endpointPlaceholder: 'https://garage.example.com',
        endpointHint: t('adm.storage.field.endpointHint.garage'),
        region: 'garage',
        accessKeyPlaceholder: 'GKabcdef…',
      }),
      summary: pairSummary('bucket', 'bucket', 'endpoint', 'endpoint'),
    },
    seaweedfs: {
      label: 'SeaweedFS',
      desc: t('adm.storage.type.seaweedfs.desc'),
      fields: s3LikeFields({
        endpointPlaceholder: 'https://seaweedfs.example.com',
        endpointHint: t('adm.storage.field.endpointHint.seaweedfs'),
        region: 'us-east-1',
        accessKeyPlaceholder: 'admin',
      }),
      summary: pairSummary('bucket', 'bucket', 'endpoint', 'endpoint'),
    },
    oci: {
      label: t('adm.storage.type.oci.label'),
      desc: t('adm.storage.type.oci.desc'),
      fields: [
        textField('endpoint', t('adm.storage.field.endpoint'), 'registry.example.com', {
          required: true,
          hint: t('adm.storage.field.endpointHint.oci'),
        }),
        textField('bucket', t('adm.storage.field.bucket.oci'), 'org/artifacts', { required: true }),
        textField('username', t('adm.storage.field.username'), 'robot$push', { half: true }),
        secretField('password', t('adm.storage.field.password'), { half: true }),
        toggleField('insecure', t('adm.storage.field.insecure'), t('adm.storage.field.insecureHint')),
      ],
      summary: pairSummary('bucket', 'repository', 'endpoint', 'endpoint'),
    },
  }
}

/** Built once: the English catalog is static. */
export const BACKEND_TYPES: Record<string, BackendType> = buildBackendTypes()

// ─── Config helpers ────────────────────────────────────────────────────────────

/** Fields of `kind` whose `when` conditions hold for the current `config`. */
export function visibleFields(kind: string, config: Record<string, unknown>): FieldDef[] {
  const fields = BACKEND_TYPES[kind]?.fields
  if (!fields) return []
  return fields.filter((field) => {
    if (!field.when) return true
    return Object.entries(field.when).every(([k, v]) => {
      const actual = config[k] ?? fields.find((f) => f.key === k)?.default
      return actual === v
    })
  })
}

/**
 * The server never returns secrets; it reports `has_<field>: true` instead and
 * keeps the stored secret when the field is omitted or empty on update.
 */
export function submittableConfig(config: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(config)) {
    if (k.startsWith('has_')) continue
    if (v === '' || v === undefined || v === null) continue
    out[k] = v
  }
  return out
}

export function locationSummary(b: StorageBackend): string {
  const meta = BACKEND_TYPES[b.kind]
  return meta ? meta.summary(b.config as Record<string, unknown>) : ''
}

// ─── Presentation helpers ──────────────────────────────────────────────────────

export function typeMeta(kind: string): BackendType | undefined {
  return BACKEND_TYPES[kind]
}

export function typeColor(kind: string): string {
  return storageBackendColor(kind)
}

export function typeColorBg(kind: string): string {
  return tint(typeColor(kind), 10)
}

export function typeIcon(kind: string): Component {
  return kind === 'filesystem' ? ServerIcon : CloudIcon
}

export function statusLabel(s: string): string {
  const map: Record<string, string> = {
    healthy: t('adm.storage.status.healthy'),
    unreachable: t('adm.storage.status.unreachable'),
    unknown: t('adm.storage.status.unknown'),
  }
  return map[s] ?? s
}

export function statusBadgeClass(s: string): string {
  const map: Record<string, string> = {
    healthy: 'bg-success-soft text-success',
    unreachable: 'bg-danger-soft text-danger',
    unknown: 'bg-surface-hover text-muted',
  }
  return map[s] ?? 'bg-surface-hover text-muted'
}

export function statusDotClass(s: string): string {
  const map: Record<string, string> = {
    healthy: 'bg-success',
    unreachable: 'bg-danger',
    unknown: 'bg-muted',
  }
  return map[s] ?? 'bg-muted'
}
