// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { setTransport } from '../api/client'
import { createDemoBackend } from './backend'

/** Puts the in-memory backend under every API call of the app. */
export function installDemo(): void {
  setTransport(createDemoBackend().transport)
}
