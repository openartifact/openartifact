// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { InjectionKey, Ref } from 'vue'

/** The element in the section header where a section page places its action buttons (provided by SettingsSectionPage). */
export const SETTINGS_ACTIONS_TARGET: InjectionKey<Ref<HTMLElement | null>> = Symbol('settings-actions-target')
