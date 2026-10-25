// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { defineAsyncComponent, type Component } from 'vue'
import TabSkeleton from '../components/TabSkeleton.vue'

/**
 * Lazy-load a tab panel: its chunk is fetched the first time the tab is shown,
 * so the page chunk only carries the default tab. The skeleton shows from the
 * first frame, so switching tabs never flashes an empty panel.
 */
export function asyncTab(loader: () => Promise<{ default: Component }>): Component {
  return defineAsyncComponent({ loader, loadingComponent: TabSkeleton, delay: 0 })
}
