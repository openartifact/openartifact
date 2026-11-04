// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

export const inputClass =
  'w-full px-3 py-2 border border-border-strong rounded-lg text-sm bg-surface-hover/40 text-foreground placeholder-muted focus:ring-2 focus:ring-primary/50'

export function errorMessage(err: unknown, fallback: string): string {
  return err instanceof Error ? err.message : fallback
}
