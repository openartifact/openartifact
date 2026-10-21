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
	LastLoginAt        *string  `json:"last_login_at"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
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
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`

	// Proxy repositories only. The upstream password is never returned.
	UpstreamURL             string `json:"upstream_url,omitempty"`
	UpstreamUsername        string `json:"upstream_username,omitempty"`
	CacheTTLSeconds         *int   `json:"cache_ttl_seconds,omitempty"`
	NegativeCacheTTLSeconds *int   `json:"negative_cache_ttl_seconds,omitempty"`
	MaxStaleSeconds         *int   `json:"max_stale_seconds,omitempty"`
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

// OrganizationResponse represents an organization in API responses.
type OrganizationResponse struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	DisplayName *string `json:"display_name"`
	Description *string `json:"description"`
	OwnerID     *string `json:"owner_id"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// OrgMemberResponse represents an org member in API responses.
type OrgMemberResponse struct {
	ID        string `json:"id"`
	OrgID     string `json:"org_id"`
	UserID    string `json:"user_id"`
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
	ScanError       *string `json:"scan_error"`
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
