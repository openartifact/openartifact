// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request } from './client'
import type { FrontendTelemetryConfig } from './generated/types.gen'

export type { FrontendTelemetryConfig }

/** Experimental frontend telemetry settings; rejects with a 404 ApiError while the server has it off. */
export const getFrontendTelemetry = () => request<FrontendTelemetryConfig>('/api/v1/telemetry/frontend')
