// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Repository } from '../../api/generated/types.gen'
import { ago, uid } from './time'

const GiB = 1024 ** 3

/** A repository and the quota the demo reports for it (the stats are computed from its packages). */
export interface DemoRepository extends Repository {
  quota_limit_bytes?: number
}

export const REPOSITORIES: DemoRepository[] = (
  [
    { name: 'npm-internal', format: 'npm', project: 'web-platform', description: 'Design system and internal JavaScript libraries', quota_limit_bytes: 5 * GiB },
    {
      name: 'npmjs',
      format: 'npm',
      repo_type: 'proxy',
      project: 'web-platform',
      description: 'Pull-through cache of registry.npmjs.org',
      upstream_url: 'https://registry.npmjs.org',
      cache_ttl_seconds: 3600,
      negative_cache_ttl_seconds: 300,
      min_age_hours: 72,
      curation_mode: 'denylist',
    },
    {
      name: 'npm',
      format: 'npm',
      repo_type: 'group',
      project: 'web-platform',
      description: 'Internal packages first, then npmjs: point npm here',
      member_repos: ['npm-internal', 'npmjs'],
    },
    { name: 'maven-releases', format: 'maven', project: 'payments', description: 'Release builds of the payments services', immutable_releases: true, quota_limit_bytes: 20 * GiB },
    { name: 'pypi-ml', format: 'pypi', project: 'data-science', description: 'Feature store clients and model serving wheels', quota_limit_bytes: 2 * GiB },
    { name: 'containers', format: 'docker', project: 'payments', description: 'Service images built by CI', quota_limit_bytes: 50 * GiB },
    { name: 'charts', format: 'helm', project: 'web-platform', description: 'Helm charts of the deployed services', sign_packages: true },
    { name: 'crates', format: 'cargo', description: 'Shared Rust crates' },
    { name: 'go-modules', format: 'go', project: 'payments', description: 'Go modules under go.acme.example' },
    { name: 'debian', format: 'deb', description: 'Agent and CLI packages for Debian and Ubuntu', sign_packages: true },
    { name: 'rpm', format: 'rpm', description: 'Agent and CLI packages for RHEL 9 and Fedora', sign_packages: true },
    { name: 'downloads', format: 'raw', public: true, description: 'Installers and release archives, public' },
  ] satisfies Omit<DemoRepository, 'id'>[]
).map((r, i) => ({
  id: uid('repo', i + 1),
  repo_type: 'hosted',
  project: 'system',
  public: false,
  created_at: ago(24 * (330 - i * 17)),
  updated_at: ago(24 * (i + 1)),
  ...r,
}))
