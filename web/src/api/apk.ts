// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'
import type { ApkSigningKey } from './generated/types.gen'

/** The abuild-format RSA signing key of an apk repository (never carries the private half). */
export type ApkKey = ApkSigningKey

/** Both fields are optional: an empty body generates a fresh key. */
export interface ApkKeyInput {
  /** PKCS#8 or PKCS#1 PEM private key to import. */
  private_key?: string
  /** Key file name, `<name>.rsa.pub`. */
  key_name?: string
}

function keyPath(repo: string): string {
  return `/api/v1/repositories/${encodeURIComponent(repo)}/apk-key`
}

export function getApkKey(repo: string): Promise<ApkKey> {
  return request<ApkKey>(keyPath(repo))
}

/** Generates a new key, or imports `private_key`; clients must install the new public key afterwards. */
export function replaceApkKey(repo: string, body: ApkKeyInput): Promise<ApkKey> {
  return request<ApkKey>(keyPath(repo), { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify(body) })
}
