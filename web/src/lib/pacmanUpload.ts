// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Upload paths of the hosted pacman repository: a package or its detached
// signature is PUT to /repos/{repo}/os/{arch}/{file}, where {arch} is the
// architecture directory pacman expands `$arch` to.

export const PACMAN_DEFAULT_ARCH = 'x86_64'

const PACKAGE_FILE = /^.+-([A-Za-z0-9_]+)\.pkg\.tar\.(?:zst|xz|gz)(\.sig)?$/

/** True for a `.pkg.tar.{zst,xz,gz}` package or its `.sig`. */
export function isPacmanFile(name: string): boolean {
  return PACKAGE_FILE.test(name)
}

/** True for a detached signature (`<package>.sig`). */
export function isPacmanSignature(name: string): boolean {
  return name.endsWith('.sig')
}

/**
 * Architecture directory for a file: the `-ARCH.pkg.tar.*` suffix of its
 * name, except `any` (a package every architecture can install), which goes
 * to the chosen directory like a file without a recognizable suffix.
 */
export function pacmanArchFor(name: string, chosen: string): string {
  const arch = PACKAGE_FILE.exec(name)?.[1]
  return arch && arch !== 'any' ? arch : chosen
}

/** Server route a pacman package or signature is uploaded to. */
export function pacmanUploadUrl(repo: string, name: string, chosen: string): string {
  return `/repos/${encodeURIComponent(repo)}/os/${encodeURIComponent(pacmanArchFor(name, chosen))}/${encodeURIComponent(name)}`
}

/** Packages first, signatures after: a signature belongs to an already stored package. */
export function pacmanUploadOrder(files: File[]): File[] {
  return [...files.filter((f) => !isPacmanSignature(f.name)), ...files.filter((f) => isPacmanSignature(f.name))]
}
