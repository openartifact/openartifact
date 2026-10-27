// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { clientSetup, dockerDigestPull, pullCommand, repoBaseUrl } from './installCommands'

/** One way to fetch the artifact; `id` is stable for tests and focus handling. */
export interface InstallCommand {
  id: 'tag' | 'digest'
  code: string
}

export interface InstallBlock {
  /** The primary command first; Docker also offers the digest-pinned pull when the checksum is a sha256 digest. */
  commands: InstallCommand[]
  registryUrl: string
  clients: ReturnType<typeof clientSetup>
}

/**
 * Everything the install block of the artifact page shows, built from the
 * real server origin. `pkg` is the listed package name (for Docker the full
 * image path, `<repo>/<image path>`); `sha256` is the primary file's checksum.
 */
export function installBlock(
  format: string,
  origin: string,
  repo: string,
  pkg: string,
  version: string,
  sha256: string | undefined,
  repoType = 'hosted',
): InstallBlock {
  const commands: InstallCommand[] = [{ id: 'tag', code: pullCommand(format, origin, repo, pkg, version, repoType) }]
  if (format === 'docker' && sha256) {
    const pinned = dockerDigestPull(origin, pkg, sha256)
    if (pinned) commands.push({ id: 'digest', code: pinned })
  }
  return { commands, registryUrl: repoBaseUrl(format, origin, repo), clients: clientSetup(format, origin, repo, repoType) }
}
