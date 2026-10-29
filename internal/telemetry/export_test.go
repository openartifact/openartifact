// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

// Sampler and HasCredentials expose the internals to the external test package.
//
//nolint:gochecknoglobals // test-only export shim
var (
	Sampler        = sampler
	HasCredentials = hasCredentials
)
