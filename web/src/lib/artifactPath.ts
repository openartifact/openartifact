// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

/** Encodes each `/`-separated segment: package names keep their raw slashes in the URL. */
function encodeSegments(value: string): string {
  // `@` is legal in a path and keeps npm scopes readable.
  return value.split('/').map((s) => encodeURIComponent(s).replaceAll('%40', '@')).join('/')
}

/**
 * Path of an artifact page: `/repos/:repo/packages/:pkg` or, with a version,
 * `/repos/:repo/packages/:pkg/versions/:v`. The package name (npm scope, docker
 * image path, go module) and the version keep raw slashes, which the
 * `:pkg(.+)` / `:v(.+)` route params match. Always build artifact links with
 * this, never through `{ name, params }` (the router would encode `/` as `%2F`).
 */
export function artifactPath(repo: string, pkg: string, version?: string): string {
  const base = `/repos/${encodeURIComponent(repo)}/packages/${encodeSegments(pkg)}`
  return version ? `${base}/versions/${encodeSegments(version)}` : base
}
