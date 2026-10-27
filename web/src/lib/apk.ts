// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Alpine mirror-tree coordinates: a repository holds `<branch>/<repo>/<arch>/` directories.

/** Mirrors pkg/apk ValidSegment: a branch or repo path segment. */
const SEGMENT = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/

export const APK_DEFAULT_BRANCH = 'edge'
export const APK_DEFAULT_REPO = 'main'

export function validApkSegment(value: string): boolean {
  return SEGMENT.test(value)
}
