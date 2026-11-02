// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ApiError } from '../api/client'

/** Why the signing key cannot be shown: attestations are off, the server lacks the secret key, or the call failed. */
export type AttestationProblem = 'off' | 'secret-required' | 'error'

export function attestationProblem(err: unknown): AttestationProblem {
  if (err instanceof ApiError && err.status === 501) return 'off'
  if (err instanceof ApiError && err.status === 409 && err.code === 'secret_key_required') return 'secret-required'
  return 'error'
}

/** The commands that fetch the public key and verify a signed SBOM bundle with it (docs/security/sbom.md). */
export function verifySnippet(keyUrl: string): string {
  return [
    `curl -sS ${keyUrl} -o attestation-key.pem`,
    'cosign verify-blob-attestation --bundle sbom.cdx.json.sigstore.json --key attestation-key.pem \\',
    '  --type cyclonedx --insecure-ignore-tlog my-lib-1.2.3.tgz',
  ].join('\n')
}

/** `path` on the origin the page is served from (the key URL the API answers is a path). */
export function absoluteUrl(path: string, origin: string): string {
  return /^https?:\/\//.test(path) ? path : origin + path
}
