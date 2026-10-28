// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'
import type { NixKey } from './generated'

export type { NixKey }

/** Both fields are optional: an empty body generates a fresh key. */
export interface NixKeyInput {
  /** `name:base64` secret key as written by `nix-store --generate-binary-cache-key`. */
  private_key?: string
  /** Key name, the part before the colon. */
  key_name?: string
}

function keyPath(repo: string): string {
  return `/api/v1/repositories/${encodeURIComponent(repo)}/nix-key`
}

export function getNixKey(repo: string): Promise<NixKey> {
  return request<NixKey>(keyPath(repo))
}

/** Generates a new key, or imports `private_key`; clients must trust the new public key afterwards. */
export function replaceNixKey(repo: string, body: NixKeyInput): Promise<NixKey> {
  return request<NixKey>(keyPath(repo), { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify(body) })
}
