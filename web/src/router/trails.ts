// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { CrumbSpec } from '../lib/crumbs'

// Breadcrumb trails shared by the route files. A list page's trail is a single
// crumb: PageHeader always renders it, so even top-level pages show where they are.
export const reposTrail: CrumbSpec[] = [{ key: 'nav.repositories', to: '/repos' }]
export const repoTrail: CrumbSpec[] = [...reposTrail, { param: 'repo', to: '/repos/:repo' }]
export const projectsTrail: CrumbSpec[] = [{ key: 'nav.projects', to: '/projects' }]
export const projectTrail: CrumbSpec[] = [...projectsTrail, { param: 'project', to: '/projects/:project' }]
export const bundlesTrail: CrumbSpec[] = [{ key: 'bundles.nav', to: '/bundles' }]
