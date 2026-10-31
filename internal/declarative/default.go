// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

// DefaultRegistry returns the registry with every supported kind.
func DefaultRegistry() *Registry {
	return NewRegistry(
		NewUserKind(),
		NewServiceAccountKind(),
		NewGroupKind(),
		NewProjectKind(),
		NewRepositoryKind(),
		NewPermissionTargetKind(),
		NewOIDCTrustPolicyKind(),
		NewSSOProviderKind(),
		NewWebhookKind(),
		NewStorageTierKind(),
		NewTierPolicyKind(),
	)
}
