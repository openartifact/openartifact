// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { version } from '../../../package.json'
import type { AdminSystemInfo } from '../../api/admin'
import type { Branding, PublicSystemInfo } from '../../api/generated/types.gen'
import type { User } from '../../stores/session'
import { DEFAULT_PRODUCT_NAME, DEFAULT_SOURCE_URL } from '../../lib/branding'
import { BUILTIN_PRIMARY } from '../../lib/brandingForm'
import { failure, type DemoRoute } from '../http'
import type { DemoState } from '../state'

/** The signed-in admin as `/auth/me` returns it. */
function me(state: DemoState): User {
  const admin = state.users.find((u) => u.username === 'admin')
  return {
    ...admin,
    is_admin: true,
    projects: state.projects.filter((p) => p.members.some((m) => m.username === 'admin')).map((p) => ({ name: p.name, role: 'owner' })),
  }
}

const branding: Branding = {
  product_name: DEFAULT_PRODUCT_NAME,
  primary_color: BUILTIN_PRIMARY.light,
  logo_light_url: '',
  logo_dark_url: '',
  favicon_url: '',
  login_message: '',
  support_url: '',
  source_url: DEFAULT_SOURCE_URL,
  revision: 'demo',
}

const [major, minor] = version.split('.')

const systemInfo: PublicSystemInfo = {
  version,
  status: 'ok',
  docs_url: `https://openartifact.github.io/v${major}.${minor}/`,
  api_reference_enabled: false,
  ai_available: false,
  frontend_telemetry_enabled: false,
}

const adminSystemInfo: AdminSystemInfo = {
  version,
  commit: 'demo',
  os: 'linux',
  arch: 'amd64',
  db_status: 'ok',
  storage_kind: 's3',
  storage_status: 'ok',
}

// Any credentials sign in again after a sign-out: the demo has one account.
export const sessionRoutes: DemoRoute[] = [
  ['GET', '/api/v1/auth/me', ({ state }) => (state.signedIn ? me(state) : failure(401, 'not signed in', 'unauthorized'))],
  [
    'POST',
    '/api/v1/auth/login',
    ({ state }) => {
      state.signedIn = true
      return me(state)
    },
  ],
  [
    'POST',
    '/api/v1/auth/logout',
    ({ state }) => {
      state.signedIn = false
      return {}
    },
  ],
  ['GET', '/api/v1/auth/sso/providers', () => ({ providers: [] })],
  ['GET', '/api/v1/system/info', () => systemInfo],
  ['GET', '/api/v1/branding', () => branding],
  ['GET', '/api/v1/admin/system/info', () => adminSystemInfo],
]
