// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

/**
 * Error codes of the backend API that the UI branches on. Maintained by hand:
 * add a code here only when a Go `writeError` really sends it and the UI acts on it.
 */
export const ErrorCodes = {
  PASSWORD_CHANGE_REQUIRED: 'password_change_required',
} as const
