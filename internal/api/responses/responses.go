// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package responses provides typed response structs for all API handler endpoints.
// They avoid ad-hoc map[string]interface{} payloads in the API handlers.
package responses

// UserResponse represents a user in API responses.
type UserResponse struct {
	ID                 string  `json:"id"`
	Username           string  `json:"username"`
	Email              *string `json:"email"`
	FullName           *string `json:"full_name"`
	Disabled           bool    `json:"disabled"`
	MustChangePassword bool    `json:"must_change_password"`
	LastLoginAt        *string `json:"last_login_at"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
}

// UserMeResponse extends UserResponse with admin status and groups (for /auth/me).
type UserMeResponse struct {
	ID                 string   `json:"id"`
	Username           string   `json:"username"`
	Email              *string  `json:"email"`
	FullName           *string  `json:"full_name"`
	Disabled           bool     `json:"disabled"`
	MustChangePassword bool     `json:"must_change_password"`
	IsAdmin            bool     `json:"is_admin"`
	Groups             []string `json:"groups"`
	// Projects are the caller's project roles (global admins are implicit
	// owners everywhere and are not listed unless they hold a role).
	Projects    []ProjectRoleResponse `json:"projects"`
	LastLoginAt *string               `json:"last_login_at"`
	CreatedAt   string                `json:"created_at"`
	UpdatedAt   string                `json:"updated_at"`
}

// GroupResponse represents a group in API responses.
type GroupResponse struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// TokenResponse represents an API token in responses.
type TokenResponse struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Scopes     []string `json:"scopes"`
	ExpiresAt  *string  `json:"expires_at"`
	LastUsedAt *string  `json:"last_used_at"`
	CreatedAt  string   `json:"created_at"`
}

// TokenCreateResponse includes the clear-text token on creation.
type TokenCreateResponse struct {
	TokenResponse

	Token string `json:"token"`
}

// AdminTokenResponse extends TokenResponse with user_id for admin views.
type AdminTokenResponse struct {
	TokenResponse

	UserID string `json:"user_id"`
}

// RepositoryResponse represents a repository in API responses.
type RepositoryResponse struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Format      string  `json:"format"`
	Description *string `json:"description"`
	Public      bool    `json:"public"`
	RepoType    string  `json:"repo_type"`
	// Project is the name of the project that owns the repository. Its
	// members get access to the repository (see docs/security/rbac.md).
	Project   string `json:"project,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`

	// Permissions is the caller's effective repository-scoped permissions. It
	// is only set by the single-repository GET, so the UI can show the controls
	// the API would accept (the trash tab needs delete, emptying it manage).
	Permissions *RepositoryPermissions `json:"permissions,omitempty"`

	// PresignDownloads overrides the global storage.presignDownloads setting:
	// null inherits it, true redirects blob downloads to presigned URLs, false
	// always streams through the server.
	PresignDownloads *bool `json:"presign_downloads"`

	// TrashRetentionDays is how long deleted items stay restorable; null means
	// the server default, 0 deletes immediately. ImmutableReleases forbids
	// deleting or overwriting released versions (an admin can force).
	TrashRetentionDays *int `json:"trash_retention_days"`
	ImmutableReleases  bool `json:"immutable_releases"`

	// Proxy repositories only. The upstream password is never returned.
	UpstreamURL             string `json:"upstream_url,omitempty"`
	UpstreamUsername        string `json:"upstream_username,omitempty"`
	CacheTTLSeconds         *int   `json:"cache_ttl_seconds,omitempty"`
	NegativeCacheTTLSeconds *int   `json:"negative_cache_ttl_seconds,omitempty"`
	MaxStaleSeconds         *int   `json:"max_stale_seconds,omitempty"`
}

// RepositoryPermissions reports which actions the calling principal may perform on one repository.
type RepositoryPermissions struct {
	Write  bool `json:"write"`
	Delete bool `json:"delete"`
	Manage bool `json:"manage"`
	// Admin: the caller may configure and delete the repository and run its
	// cleanup policies, quota and trash: a global admin, or a maintainer or
	// owner of its project (a repository-scoped manage target does not count).
	Admin bool `json:"admin"`
}

// PackageItem represents a package in list responses.
type PackageItem struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Version   string  `json:"version"`
	Format    string  `json:"format"`
	Arch      *string `json:"arch,omitempty"`
	Size      *int64  `json:"size,omitempty"`
	Filename  *string `json:"filename,omitempty"`
	CreatedAt string  `json:"created_at"`
}

// PermissionTargetResponse represents a permission target in API responses.
type PermissionTargetResponse struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	RepositoryFilter string   `json:"repository_filter"`
	PathPattern      string   `json:"path_pattern"`
	Actions          []string `json:"actions"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
}

// StorageBackendResponse represents a storage backend in API responses.
type StorageBackendResponse struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Kind          string         `json:"kind"`
	IsDefault     bool           `json:"is_default"`
	Config        map[string]any `json:"config"`
	Status        string         `json:"status"`
	StatusMessage *string        `json:"status_message"`
	LastCheckedAt *string        `json:"last_checked_at"`
	CreatedAt     string         `json:"created_at"`
	UpdatedAt     string         `json:"updated_at"`
}

// SSOProviderResponse represents an SSO provider in API responses.
type SSOProviderResponse struct {
	ID                 string            `json:"id"`
	Slug               string            `json:"slug"`
	Kind               string            `json:"kind"`
	DisplayName        string            `json:"display_name"`
	Enabled            bool              `json:"enabled"`
	OIDCConfig         map[string]any    `json:"oidc_config,omitempty"`
	SAMLConfig         map[string]any    `json:"saml_config,omitempty"`
	ClaimMapping       map[string]any    `json:"claim_mapping,omitempty"`
	GroupMapping       map[string]string `json:"group_mapping,omitempty"`
	PermissionMapping  map[string]string `json:"permission_mapping,omitempty"`
	AutoProvisionUsers bool              `json:"auto_provision_users"`
	LinkByEmail        bool              `json:"link_by_email"`
	CreatedAt          string            `json:"created_at"`
	UpdatedAt          string            `json:"updated_at"`
}

// ProjectResponse represents a project in API responses.
type ProjectResponse struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	DisplayName *string `json:"display_name"`
	Description *string `json:"description"`
	OwnerID     *string `json:"owner_id"`
	// Slug is the URL-safe unique identifier derived from the name.
	Slug string `json:"slug"`
	// AllowedFormats limits the formats of self-service repositories; null
	// allows every format.
	AllowedFormats   []string `json:"allowed_formats"`
	AllowedRepoTypes []string `json:"allowed_repo_types"`
	RepoNamePrefix   string   `json:"repo_name_prefix"`
	AllowPublicRepos bool     `json:"allow_public_repos"`
	MaxRepositories  int      `json:"max_repositories"`
	TokenMaxTTLDays  int      `json:"token_max_ttl_days"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
	// MyRole is the caller's effective role in the project (viewer, developer,
	// maintainer or owner); a global admin who holds no role is an implicit owner.
	MyRole string `json:"my_role"`
	// RepositoryCount is the number of live (not trashed) repositories.
	RepositoryCount int `json:"repository_count"`
	// IsSystem marks the built-in system project ("unassigned"): it grants
	// nothing and cannot be deleted.
	IsSystem bool `json:"is_system"`
}

// ProjectMemberResponse represents a project member in API responses.
type ProjectMemberResponse struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}

// ProjectRoleResponse is one project role of the caller (GET /api/v1/auth/me).
type ProjectRoleResponse struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// ProjectGroupResponse represents a group attached to a project.
type ProjectGroupResponse struct {
	ProjectID string `json:"project_id"`
	GroupID   string `json:"group_id"`
	GroupName string `json:"group_name"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}

// BackupJobResponse represents a backup job in API responses.
type BackupJobResponse struct {
	ID           string  `json:"id"`
	Status       string  `json:"status"`
	Type         string  `json:"type"`
	SizeBytes    int64   `json:"size_bytes"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	ErrorMessage *string `json:"error_message"`
	StartedAt    *string `json:"started_at"`
	CompletedAt  *string `json:"completed_at"`
}

// CleanupPolicyResponse represents a cleanup policy in API responses.
type CleanupPolicyResponse struct {
	ID                      string  `json:"id"`
	RepositoryID            string  `json:"repo_id"`
	Name                    string  `json:"name"`
	Enabled                 bool    `json:"enabled"`
	KeepLastN               *int    `json:"keep_last_n"`
	KeepSinceDays           *int    `json:"keep_since_days"`
	KeepDownloadedSinceDays *int    `json:"keep_downloaded_since_days"`
	NamePattern             *string `json:"name_pattern"`
	ScheduleCron            *string `json:"schedule_cron"`
	CreatedAt               string  `json:"created_at"`
	UpdatedAt               string  `json:"updated_at"`
	LastRunAt               *string `json:"last_run_at"`
	LastRunDeleted          *int    `json:"last_run_deleted"`
	LastRunError            *string `json:"last_run_error"`
}

// StorageQuotaResponse represents a storage quota in API responses.
type StorageQuotaResponse struct {
	ID                   string `json:"id"`
	RepositoryID         string `json:"repo_id"`
	MaxSizeBytes         int64  `json:"max_size_bytes"`
	MaxArtifactCount     int    `json:"max_artifact_count"`
	WarnThresholdPercent int    `json:"warn_threshold_percent"`
	CurrentSizeBytes     int64  `json:"current_size_bytes"`
	CurrentArtifactCount int    `json:"current_artifact_count"`
	UsagePercent         int    `json:"usage_percent"`
	IsExceeded           bool   `json:"is_exceeded"`
	CreatedAt            string `json:"created_at"`
	UpdatedAt            string `json:"updated_at"`
}

// VulnScanResultResponse represents a vulnerability scan result.
type VulnScanResultResponse struct {
	ID              string  `json:"id"`
	RepositoryID    string  `json:"repo_id"`
	ArtifactID      string  `json:"artifact_id"`
	PackageName     string  `json:"package_name"`
	PackageVersion  string  `json:"package_version"`
	Ecosystem       string  `json:"ecosystem"`
	Scanner         string  `json:"scanner"`
	ScanStatus      string  `json:"scan_status"`
	Vulnerabilities any     `json:"vulnerabilities"`
	VulnCount       int     `json:"vuln_count"`
	CriticalCount   int     `json:"critical_count"`
	HighCount       int     `json:"high_count"`
	MediumCount     int     `json:"medium_count"`
	LowCount        int     `json:"low_count"`
	Quarantined     bool    `json:"quarantined"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	ScannedAt       *string `json:"scanned_at"`
	// ScanError is a sanitized summary of the failing scanners; it never holds
	// raw scanner output or error text.
	ScanError *string `json:"scan_error"`
	// FailedScanners lists the scanners that produced no result (scan_status
	// "failed" or "partial").
	FailedScanners []string `json:"failed_scanners"`
}

// SecretScanResultResponse represents a secret scan result.
type SecretScanResultResponse struct {
	ID           string  `json:"id"`
	RepositoryID string  `json:"repo_id"`
	ArtifactID   string  `json:"artifact_id"`
	ScanStatus   string  `json:"scan_status"`
	Findings     any     `json:"findings"`
	FindingCount int     `json:"finding_count"`
	Quarantined  bool    `json:"quarantined"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	ScannedAt    *string `json:"scanned_at"`
	ScanError    *string `json:"scan_error"`
}

// SBOMReportResponse represents an SBOM report in API responses.
type SBOMReportResponse struct {
	ID             string `json:"id"`
	RepositoryID   string `json:"repo_id"`
	ArtifactID     string `json:"artifact_id"`
	Format         string `json:"format"`
	SpecVersion    string `json:"spec_version"`
	ContentType    string `json:"content_type"`
	ComponentCount int    `json:"component_count"`
	GeneratedAt    string `json:"generated_at"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	SBOMData       any    `json:"sbom_data,omitempty"`
}

// LicenseScanResultResponse represents a license scan result.
type LicenseScanResultResponse struct {
	ID              string   `json:"id"`
	RepositoryID    string   `json:"repo_id"`
	ArtifactID      string   `json:"artifact_id"`
	Licenses        []string `json:"licenses"`
	ScanStatus      string   `json:"scan_status"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
	ScannedAt       *string  `json:"scanned_at"`
	ScanError       *string  `json:"scan_error"`
	SPDXExpressions []string `json:"spdx_expressions"`
	// Quarantined: downloads of the artifact answer 403 until it is approved
	// (a blocking license policy matched its licenses).
	Quarantined bool `json:"quarantined"`
	// QuarantineApproved: an admin approved the artifact for these licenses.
	QuarantineApproved bool `json:"quarantine_approved"`
}

// LicensePolicyResponse represents a license policy.
type LicensePolicyResponse struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Description     *string  `json:"description"`
	Action          *string  `json:"action"`
	AllowedLicenses []string `json:"allowed_licenses"`
	DeniedLicenses  []string `json:"denied_licenses"`
	Enabled         bool     `json:"enabled"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
	RepositoryID    *string  `json:"repo_id"`
}

// RepositoryMemberResponse represents a repository member.
type RepositoryMemberResponse struct {
	ID       string `json:"id"`
	Priority int    `json:"priority"`
}

// GPGKeyResponse represents a GPG key in API responses.
type GPGKeyResponse struct {
	Fingerprint string  `json:"fingerprint"`
	UID         *string `json:"uid"`
	CreatedAt   *string `json:"created_at"`
	ExpiresAt   *string `json:"expires_at"`
	IsDefault   *bool   `json:"is_default"`
}

// AuditEntryResponse represents an audit log entry.
type AuditEntryResponse struct {
	ID        string         `json:"id"`
	Timestamp string         `json:"timestamp"`
	Actor     *string        `json:"actor"`
	Action    *string        `json:"action"`
	Target    *string        `json:"target"`
	Result    *string        `json:"result"`
	IP        *string        `json:"ip"`
	Details   map[string]any `json:"details"`
}

// SystemInfoResponse represents system information.
type SystemInfoResponse struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// PaginatedResponse is a generic paginated response envelope.
type PaginatedResponse struct {
	Data    any `json:"data"`
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
	Total   int `json:"total"`
}

// TestResultResponse represents a test connection result.
type TestResultResponse struct {
	OK      bool    `json:"ok"`
	Error   *string `json:"error,omitempty"`
	Message *string `json:"message,omitempty"`
}

// SSOProviderPublicResponse represents a public SSO provider (no secrets).
type SSOProviderPublicResponse struct {
	Slug        string `json:"slug"`
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
}
