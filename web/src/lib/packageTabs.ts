// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { TabDef } from '../composables/useUrlTab'
import { t } from '../i18n/en'

/** Tabs of the artifact page, in order; the first is the default (`?tab=` absent or unknown). */
export const PACKAGE_TABS = ['overview', 'versions', 'files', 'dependencies', 'security', 'sbom', 'activity'] as const

export type PackageTab = (typeof PACKAGE_TABS)[number]

interface PackageTabInputs {
  /** Versions of the package, once loaded. */
  versionCount?: number
  /** Files of the open version, once loaded. */
  fileCount?: number
  /** Worst severity of the open version ("3 critical" chip), when it has findings. */
  securityFlag?: string
  /** Activity is for admins and project maintainers; hidden for everyone else. */
  canSeeActivity: boolean
  /** SBOM reports are served to global admins only. */
  canSeeSbom: boolean
}

/** The tab bar of the artifact page. Counts are shown only once they are known. */
export function packageTabDefs(input: PackageTabInputs): TabDef[] {
  return PACKAGE_TABS.map((id): TabDef => {
    const label = t(`artifact.tab.${id}`)
    switch (id) {
      case 'versions':
        return { id, label, count: input.versionCount }
      case 'files':
        return { id, label, count: input.fileCount }
      case 'security':
        return { id, label, flag: input.securityFlag }
      case 'activity':
        return { id, label, hidden: !input.canSeeActivity }
      case 'sbom':
        return { id, label, hidden: !input.canSeeSbom }
      default:
        return { id, label }
    }
  })
}
