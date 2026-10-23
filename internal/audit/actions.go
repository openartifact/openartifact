// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package audit

// Audit action names. The audit_logs.chk_audit_action constraint admits exactly
// the values in KnownActions; every action an emitter can produce MUST be
// declared here, listed in KnownActions and allowed by the latest migration
// that rewrites the constraint (see migrations/031_audit_actions.sql). The
// integration test TestKnownActionsAcceptedByDatabase inserts each entry of
// KnownActions into a freshly migrated database.
const (
	// Artifact lifecycle.

	ActionArtifactUploaded    = "artifact.uploaded"
	ActionArtifactDownloaded  = "artifact.downloaded"
	ActionArtifactDeleted     = "artifact.deleted"
	ActionArtifactMoved       = "artifact.moved"
	ActionArtifactPromoted    = "artifact.promoted"
	ActionArtifactQuarantined = "artifact.quarantined"
	ActionArtifactReleased    = "artifact.released"
	ActionArtifactRestored    = "artifact.restored"
	ActionArtifactPurged      = "artifact.purged"

	// Repository management.

	ActionRepositoryCreated   = "repository.created"
	ActionRepositoryUpdated   = "repository.updated"
	ActionRepositoryDeleted   = "repository.deleted"
	ActionRepositoryBlocked   = "repository.blocked"
	ActionRepositoryUnblocked = "repository.unblocked"
	ActionRepositoryRestored  = "repository.restored"
	ActionRepositoryPurged    = "repository.purged"

	// Auth events.

	ActionAuthLogin           = "auth.login"
	ActionAuthLogout          = "auth.logout"
	ActionAuthLoginFailed     = "auth.login_failed"
	ActionAuthTokenCreated    = "auth.token_created" //nolint:gosec // action name, not a credential
	ActionAuthTokenRevoked    = "auth.token_revoked" //nolint:gosec // action name, not a credential
	ActionAuthPasswordChanged = "auth.password_changed"
	ActionAuthSSOLogin        = "auth.sso_login"
	ActionAuthTokenValidate   = "auth.token_validate" //nolint:gosec // action name, not a credential

	// Permission management (legacy permission.* names are never emitted).

	ActionPermissionTargetCreated = "permission.target_created"
	ActionPermissionTargetUpdated = "permission.target_updated"
	ActionPermissionTargetDeleted = "permission.target_deleted"
	ActionPermissionAssigned      = "permission.assigned"
	ActionPermissionRevoked       = "permission.revoked"

	// Organization management (legacy, never emitted).

	ActionOrgCreated           = "org.created"
	ActionOrgUpdated           = "org.updated"
	ActionOrgDeleted           = "org.deleted"
	ActionOrgMemberAdded       = "org.member_added"
	ActionOrgMemberRemoved     = "org.member_removed"
	ActionOrgMemberRoleChanged = "org.member_role_changed"

	// Project management.

	ActionProjectCreated               = "project.created"
	ActionProjectUpdated               = "project.updated"
	ActionProjectDeleted               = "project.deleted"
	ActionProjectPolicyChanged         = "project.policy_changed"
	ActionProjectQuotaChanged          = "project.quota_changed"
	ActionProjectMemberAdded           = "project.member_added"
	ActionProjectMemberRemoved         = "project.member_removed"
	ActionProjectMemberRoleChanged     = "project.member_role_changed"
	ActionProjectGroupAdded            = "project.group_added"
	ActionProjectGroupRemoved          = "project.group_removed"
	ActionProjectGroupRoleChanged      = "project.group_role_changed"
	ActionProjectTokenCreated          = "project.token_created"
	ActionProjectTokenRevoked          = "project.token_revoked"
	ActionProjectRepositoryCreated     = "project.repository_created"
	ActionProjectRepositoryDeleted     = "project.repository_deleted"
	ActionProjectRepositoryTransferred = "project.repository_transferred"
	ActionProjectCleanupPolicyChanged  = "project.cleanup_policy_changed"

	// System configuration.

	ActionSystemSettingChanged        = "system.setting_changed"
	ActionSystemStorageBackendChanged = "system.storage_backend_changed"

	// Security scanning.

	ActionScanVulnDetected    = "scan.vuln_detected"
	ActionScanSecretDetected  = "scan.secret_detected" //nolint:gosec // action name, not a credential
	ActionScanQuarantineApply = "scan.quarantine_applied"

	// Bundle/promotion.

	ActionBundleCreated  = "bundle.created"
	ActionBundleSealed   = "bundle.sealed"
	ActionBundleArchived = "bundle.archived"
	ActionBundlePromoted = "bundle.promoted"

	// HTTP audit: the middleware logs method:path for generic request auditing.

	ActionHTTPRequest = "http.request"

	// Admin-plane mutations (auditAdmin).

	ActionUserCreated  = "user.created"
	ActionUserUpdated  = "user.updated"
	ActionUserDisabled = "user.disabled"

	ActionGroupCreated       = "group.created"
	ActionGroupUpdated       = "group.updated"
	ActionGroupDeleted       = "group.deleted"
	ActionGroupMemberAdded   = "group.member_added"
	ActionGroupMemberRemoved = "group.member_removed"
	ActionGroupMembersSet    = "group.members_set"

	ActionPermissionTargetObjCreated    = "permission_target.created"
	ActionPermissionTargetObjUpdated    = "permission_target.updated"
	ActionPermissionTargetObjDeleted    = "permission_target.deleted"
	ActionPermissionTargetObjAssigned   = "permission_target.assigned"
	ActionPermissionTargetObjUnassigned = "permission_target.unassigned"

	ActionSSOProviderCreated = "sso_provider.created"
	ActionSSOProviderUpdated = "sso_provider.updated"
	ActionSSOProviderDeleted = "sso_provider.deleted"

	ActionTokenCreatedForUser = "token.created_for_user"

	// GPG key management.

	ActionGPGKeyCreated     = "gpg_key.created"
	ActionGPGKeyDeleted     = "gpg_key.deleted"
	ActionGPGKeyActivated   = "gpg_key.activated"
	ActionGPGKeyDeactivated = "gpg_key.deactivated"
	ActionGPGKeyExported    = "gpg_key.exported"
)

// KnownActions lists every action the audit_logs constraint accepts.
//
//nolint:gochecknoglobals // immutable registry read by tests
var KnownActions = []string{
	ActionArtifactUploaded, ActionArtifactDownloaded, ActionArtifactDeleted, ActionArtifactMoved,
	ActionArtifactPromoted, ActionArtifactQuarantined, ActionArtifactReleased,
	ActionArtifactRestored, ActionArtifactPurged,

	ActionRepositoryCreated, ActionRepositoryUpdated, ActionRepositoryDeleted,
	ActionRepositoryBlocked, ActionRepositoryUnblocked,
	ActionRepositoryRestored, ActionRepositoryPurged,

	ActionAuthLogin, ActionAuthLogout, ActionAuthLoginFailed, ActionAuthTokenCreated,
	ActionAuthTokenRevoked, ActionAuthPasswordChanged, ActionAuthSSOLogin, ActionAuthTokenValidate,

	ActionPermissionTargetCreated, ActionPermissionTargetUpdated, ActionPermissionTargetDeleted,
	ActionPermissionAssigned, ActionPermissionRevoked,

	ActionOrgCreated, ActionOrgUpdated, ActionOrgDeleted,
	ActionOrgMemberAdded, ActionOrgMemberRemoved, ActionOrgMemberRoleChanged,

	ActionProjectCreated, ActionProjectUpdated, ActionProjectDeleted,
	ActionProjectPolicyChanged, ActionProjectQuotaChanged,
	ActionProjectMemberAdded, ActionProjectMemberRemoved, ActionProjectMemberRoleChanged,
	ActionProjectGroupAdded, ActionProjectGroupRemoved, ActionProjectGroupRoleChanged,
	ActionProjectTokenCreated, ActionProjectTokenRevoked,
	ActionProjectRepositoryCreated, ActionProjectRepositoryDeleted, ActionProjectRepositoryTransferred,
	ActionProjectCleanupPolicyChanged,

	ActionSystemSettingChanged, ActionSystemStorageBackendChanged,

	ActionScanVulnDetected, ActionScanSecretDetected, ActionScanQuarantineApply,

	ActionBundleCreated, ActionBundleSealed, ActionBundleArchived, ActionBundlePromoted,

	ActionHTTPRequest,

	ActionUserCreated, ActionUserUpdated, ActionUserDisabled,

	ActionGroupCreated, ActionGroupUpdated, ActionGroupDeleted,
	ActionGroupMemberAdded, ActionGroupMemberRemoved, ActionGroupMembersSet,

	ActionPermissionTargetObjCreated, ActionPermissionTargetObjUpdated, ActionPermissionTargetObjDeleted,
	ActionPermissionTargetObjAssigned, ActionPermissionTargetObjUnassigned,

	ActionSSOProviderCreated, ActionSSOProviderUpdated, ActionSSOProviderDeleted,

	ActionTokenCreatedForUser,

	ActionGPGKeyCreated, ActionGPGKeyDeleted, ActionGPGKeyActivated, ActionGPGKeyDeactivated, ActionGPGKeyExported,
}
