// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { DemoRoute } from '../http'
import { accessRoutes } from './access'
import { packageRoutes } from './packages'
import { repoRoutes } from './repos'
import { securityRoutes } from './security'
import { sessionRoutes } from './session'

/** Every call the demo answers; anything else gets the demo's "not available" error. */
export const ROUTES: readonly DemoRoute[] = [...sessionRoutes, ...repoRoutes, ...packageRoutes, ...securityRoutes, ...accessRoutes]
