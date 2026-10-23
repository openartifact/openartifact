// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

// DefaultRegistry returns the registry with every supported kind.
func DefaultRegistry() *Registry {
	return NewRegistry(
		NewUserKind(),
		NewGroupKind(),
		NewProjectKind(),
		NewRepositoryKind(),
		NewPermissionTargetKind(),
		NewSSOProviderKind(),
		NewWebhookKind(),
		NewStorageTierKind(),
		NewTierPolicyKind(),
	)
}
