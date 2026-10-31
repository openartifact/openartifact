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
	// Project, ProjectRole and CreatedBy are set for project tokens (CreatedBy
	// is the minting user's name); null for personal tokens.
	Project     *string `json:"project"`
	ProjectRole *string `json:"project_role"`
	CreatedBy   *string `json:"created_by"`
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

	// Stats is the repository's catalog figures. It is only set by the
	// repository list.
	Stats *RepositoryStats `json:"stats,omitempty"`

	// PresignDownloads overrides the global storage.presignDownloads setting:
	// null inherits it, true redirects blob downloads to presigned URLs, false
	// always streams through the server.
	PresignDownloads *bool `json:"presign_downloads"`

	// TrashRetentionDays is how long deleted items stay restorable; null means
	// the server default, 0 deletes immediately. ImmutableReleases forbids
	// deleting or overwriting released versions (an admin can force).
	TrashRetentionDays *int `json:"trash_retention_days"`
	ImmutableReleases  bool `json:"immutable_releases"`

	// MergeProxyVersions (groups only): false means hosted members win a
	// package name and proxy members' versions of it are hidden; true merges
	// every member's versions.
	MergeProxyVersions bool `json:"merge_proxy_versions"`
	// SignPackages (hosted rpm only): uploads are signed with the active GPG key.
	SignPackages bool `json:"sign_packages"`
	// Federated: the repository replicates with peer instances (hosted only).
	Federated bool `json:"federated"`

	// Proxy repositories only. The upstream password is never returned.
	UpstreamURL      string `json:"upstream_url,omitempty"`
	UpstreamUsername string `json:"upstream_username,omitempty"`
	// UpstreamPasswordSet says a password is stored for the upstream. The
	// password itself is write-only: no response ever carries it.
	UpstreamPasswordSet     bool `json:"upstream_password_set,omitempty"`
	CacheTTLSeconds         *int `json:"cache_ttl_seconds,omitempty"`
	NegativeCacheTTLSeconds *int `json:"negative_cache_ttl_seconds,omitempty"`
	MaxStaleSeconds         *int `json:"max_stale_seconds,omitempty"`
	MinAgeHours             *int `json:"min_age_hours,omitempty"`
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
	Config        map[string]any `json:"config"`
	Status        string         `json:"status"`
	StatusMessage *string        `json:"status_message"`
	LastCheckedAt *string        `json:"last_checked_at"`
	CreatedAt     string         `json:"created_at"`
	UpdatedAt     string         `json:"updated_at"`
	// Usage is what the backend holds. Present on the list and get endpoints.
	Usage *StorageBackendUsage `json:"usage,omitempty"`
}

// StoragePrimaryResponse is the primary blob store the server is configured
// with (storage.* settings). It is read-only and holds no credentials.
type StoragePrimaryResponse struct {
	// Name is the reserved name "primary", usable in the /admin/storage/{name} paths.
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Source is always "config": the store is set by the server configuration.
	Source string `json:"source"`
	// Location is where the blobs live (path, endpoint, bucket, region, ...); never a credential.
	Location      map[string]string `json:"location"`
	Status        string            `json:"status"`
	StatusMessage *string           `json:"status_message"`
	LastCheckedAt *string           `json:"last_checked_at"`
	// Usage is the blobs placed on the primary store (those not moved to a tier).
	Usage *StorageBackendUsage `json:"usage,omitempty"`
}

// StorageBackendUsage is the blob count and byte total placed on one backend.
type StorageBackendUsage struct {
	BlobCount int64 `json:"blob_count"`
	SizeBytes int64 `json:"size_bytes"`
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

// ProjectListItem is one project of GET /api/v1/projects: the project with
// its member count, usage and quota.
type ProjectListItem struct {
	ProjectResponse

	// MemberCount is the number of direct members (group grants not counted).
	MemberCount int               `json:"member_count"`
	Usage       ProjectListUsage  `json:"usage"`
	Quota       *ProjectListQuota `json:"quota,omitempty"`
}

// ProjectListUsage is the stored size across the project's repositories.
type ProjectListUsage struct {
	SizeBytes int64 `json:"size_bytes"`
}

// ProjectListQuota is the project's size ceiling; the object is omitted when
// the project has no size limit.
type ProjectListQuota struct {
	LimitBytes int64 `json:"limit_bytes"`
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
	ID string `json:"id"`
	// RepositoryID is empty for a project default policy, which carries ProjectID.
	RepositoryID            string  `json:"repo_id,omitempty"`
	ProjectID               string  `json:"project_id,omitempty"`
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

// QuotaLine is a quota next to its live usage. Limits are omitted when
// unlimited.
type QuotaLine struct {
	LimitBytes *int64 `json:"limit_bytes,omitempty"`
	UsedBytes  int64  `json:"used_bytes"`
	// SoftLimitBytes is the usage at which the warning threshold is crossed
	// (LimitBytes times WarnThresholdPercent), set with LimitBytes.
	SoftLimitBytes       *int64 `json:"soft_limit_bytes,omitempty"`
	WarnThresholdPercent int    `json:"warn_threshold_percent"`
	MaxArtifactCount     *int64 `json:"max_artifact_count,omitempty"`
	ArtifactCount        int64  `json:"artifact_count"`
	// UsagePercent is UsedBytes of LimitBytes (0 when unlimited); Exceeded is
	// true once the size or the artifact count limit is reached.
	UsagePercent int  `json:"usage_percent"`
	Exceeded     bool `json:"exceeded"`
}

// RepositoryUsageResponse is GET /api/v1/repositories/{name}/usage.
type RepositoryUsageResponse struct {
	PackageCount    int64     `json:"package_count"`
	VersionCount    int64     `json:"version_count"`
	SizeBytes       int64     `json:"size_bytes"`
	LastPublishedAt *string   `json:"last_published_at,omitempty"`
	Quota           QuotaLine `json:"quota"`
}

// QuotaOverviewItem is one repository or project of GET /api/v1/admin/quotas.
type QuotaOverviewItem struct {
	QuotaLine

	// Scope is "repository" or "project".
	Scope string `json:"scope"`
	Name  string `json:"name"`
	// Project is the project owning a repository (omitted for a project line
	// and for a repository without one).
	Project string `json:"project,omitempty"`
}

// QuotaOverviewList is GET /api/v1/admin/quotas.
type QuotaOverviewList struct {
	Quotas []QuotaOverviewItem `json:"quotas"`
	Total  int                 `json:"total"`
	Page   int                 `json:"page"`
	Limit  int                 `json:"limit"`
}

// ProjectQuotaResponse is a project's storage ceiling and its live usage (the
// sum of its repositories' counters). Limits of 0 mean unlimited.
type ProjectQuotaResponse struct {
	Project              string `json:"project"`
	MaxSizeBytes         int64  `json:"max_size_bytes"`
	MaxArtifactCount     int64  `json:"max_artifact_count"`
	WarnThresholdPercent int    `json:"warn_threshold_percent"`
	CurrentSizeBytes     int64  `json:"current_size_bytes"`
	CurrentArtifactCount int64  `json:"current_artifact_count"`
	UsagePercent         int    `json:"usage_percent"`
	// Enforced is true when a limit is set, i.e. uploads must fit the project ceiling.
	Enforced  bool   `json:"enforced"`
	UpdatedAt string `json:"updated_at"`
}

// ProjectRepoUsageResponse is one repository's line in a project usage report.
type ProjectRepoUsageResponse struct {
	Name                 string `json:"name"`
	Format               string `json:"format"`
	RepoType             string `json:"repo_type"`
	SizeBytes            int64  `json:"size_bytes"`
	ArtifactCount        int64  `json:"artifact_count"`
	MaxSizeBytes         int64  `json:"max_size_bytes"`
	MaxArtifactCount     int64  `json:"max_artifact_count"`
	WarnThresholdPercent int    `json:"warn_threshold_percent"`
	// QuotaEnforced is false for repositories whose bytes are not metered
	// (proxy and group repositories hold no uploaded packages).
	QuotaEnforced bool `json:"quota_enforced"`
}

// ProjectUsageResponse is GET /api/v1/projects/{project}/usage.
type ProjectUsageResponse struct {
	Project       string                     `json:"project"`
	SizeBytes     int64                      `json:"size_bytes"`
	ArtifactCount int64                      `json:"artifact_count"`
	Quota         ProjectQuotaResponse       `json:"quota"`
	Repositories  []ProjectRepoUsageResponse `json:"repositories"`
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
	// KEVCount is the number of findings whose CVE is in the CISA KEV catalog.
	KEVCount int `json:"kev_count"`
	// MaxEPSS is the highest EPSS score among the findings; null when none is scored.
	MaxEPSS *float64 `json:"max_epss"`
	// Priority is the scan's priority: kev, epss, or its worst severity (critical
	// ... low, none).
	Priority string `json:"priority"`
}

// VulnScannerResponse reports whether one vulnerability scanner can run on the
// server. Fixed, client-safe text only: no paths, versions or scanner output.
type VulnScannerResponse struct {
	// Name is osv, trivy or grype.
	Name string `json:"name"`
	// Available is false when the scanner binary cannot be started.
	Available bool `json:"available"`
	// Ready is true when a scan with this scanner can run now (available and,
	// for trivy and grype, a vulnerability database is present).
	Ready bool `json:"ready"`
	// DBState is ready, updating, failed or missing for trivy and grype when
	// the binary is available; null otherwise.
	DBState *string `json:"db_state"`
	// DBUpdatedAt is when the vulnerability database was last refreshed; null
	// when there is none or the time is unknown.
	DBUpdatedAt *string `json:"db_updated_at"`
	// InstallState is installing, ready or failed for a scanner whose install
	// mode is auto and whose binary comes from the installer; null otherwise.
	InstallState *string `json:"install_state"`
	// Code is scanner_unavailable, scanner_installing, scanner_install_failed,
	// scanner_install_noexec or scanner_db_unavailable when the scanner is not
	// ready, else null.
	Code *string `json:"code"`
	// Message says what the operator must do when the scanner is not ready.
	Message *string `json:"message"`
	// DocsURL links the installation guide when the scanner is not ready.
	DocsURL *string `json:"docs_url"`
}

// OSVMirrorEcosystemResponse is the state of one ecosystem of the local OSV mirror.
type OSVMirrorEcosystemResponse struct {
	// Ecosystem is the OSV ecosystem name, e.g. npm or crates.io.
	Ecosystem string `json:"ecosystem"`
	// Populated is true once an import completed: only then does the osv
	// scanner match against the mirror instead of the online API.
	Populated bool `json:"populated"`
	// Advisories counts the advisories that affect the ecosystem.
	Advisories int `json:"advisories"`
	// HighWater is the newest advisory modification imported.
	HighWater *string `json:"high_water"`
	// LastCheckedAt is when the export was last requested; LastImportedAt when
	// new data was last applied.
	LastCheckedAt  *string `json:"last_checked_at"`
	LastImportedAt *string `json:"last_imported_at"`
	// LastImportSeconds and LastRematchSeconds are the durations of the last
	// import and of the last re-match against the stored packages.
	LastImportSeconds  float64 `json:"last_import_seconds"`
	LastRematchSeconds float64 `json:"last_rematch_seconds"`
	// LastError is the failure of the last import, null when it succeeded.
	LastError *string `json:"last_error"`
	// RematchPending is true while changed advisories are not yet matched.
	RematchPending bool `json:"rematch_pending"`
}

// OSVAttributionResponse names the source and license of the advisory data.
type OSVAttributionResponse struct {
	Source  string `json:"source"`
	License string `json:"license"`
	URL     string `json:"url"`
}

// OSVMirrorStatusResponse reports the local OSV mirror.
type OSVMirrorStatusResponse struct {
	// Enabled is false when osvMirror is off: the osv scanner then uses the online API.
	Enabled bool `json:"enabled"`
	// SourceURL is the configured export location, without credentials.
	SourceURL string `json:"source_url"`
	// IntervalSeconds is the import schedule.
	IntervalSeconds int                          `json:"interval_seconds"`
	Ecosystems      []OSVMirrorEcosystemResponse `json:"ecosystems"`
	Attribution     OSVAttributionResponse       `json:"attribution"`
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

// VEXStatementResponse is an admin VEX decision in API responses.
type VEXStatementResponse struct {
	ID              string `json:"id"`
	ArtifactID      string `json:"artifact_id"`
	VulnID          string `json:"vuln_id"`
	Status          string `json:"status"`
	Justification   string `json:"justification"`
	ImpactStatement string `json:"impact_statement"`
	ActionStatement string `json:"action_statement"`
	CreatedBy       string `json:"created_by"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
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
	// UnresolvedLicenses are the entries of licenses that are not SPDX
	// expressions: raw text read from the artifact ("BSD", "BSD License"),
	// kept as written and flagged for review.
	UnresolvedLicenses []string `json:"unresolved_licenses"`
	// LicenseSource is "artifact" when the licenses include ones read from the
	// package file, "client_declared" when they only come from what a client
	// declared (license properties).
	LicenseSource string `json:"license_source"`
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

// ProjectTokenResponse represents a project token in API responses.
type ProjectTokenResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Project string `json:"project"`
	// Role is viewer or developer.
	Role       string  `json:"role"`
	ExpiresAt  *string `json:"expires_at"`
	LastUsedAt *string `json:"last_used_at"`
	CreatedAt  string  `json:"created_at"`
	// CreatedBy is the ID of the user who minted the token.
	CreatedBy         string `json:"created_by"`
	CreatedByUsername string `json:"created_by_username"`
}

// ProjectTokenCreateResponse includes the clear-text token on creation.
type ProjectTokenCreateResponse struct {
	ProjectTokenResponse

	Token string `json:"token"`
}

// ProjectActivityEntry is one audit event of a project or of a repository it
// owns. It carries no client address: members see what happened, not from where.
type ProjectActivityEntry struct {
	ID           string  `json:"id"`
	Timestamp    string  `json:"timestamp"`
	Action       string  `json:"action"`
	Actor        string  `json:"actor"`
	ActorType    *string `json:"actor_type"`
	Message      string  `json:"message"`
	ResourceType *string `json:"resource_type"`
	ResourceID   *string `json:"resource_id"`
	RepositoryID *string `json:"repository_id"`
}

// UnifiedTokenResponse is one token of GET /api/v1/tokens, whatever its kind:
// personal (type "personal", Owner is the user), project (Project, Role, Owner
// is the creator) or federation (Repository, Project when the repository
// belongs to one). The secret is never returned.
type UnifiedTokenResponse struct {
	ID         string   `json:"id"`
	Type       string   `json:"type"`
	Name       string   `json:"name"`
	Owner      string   `json:"owner"`
	Project    string   `json:"project,omitempty"`
	Repository string   `json:"repository,omitempty"`
	Role       string   `json:"role,omitempty"`
	Scopes     []string `json:"scopes,omitempty"`
	CreatedAt  string   `json:"created_at"`
	ExpiresAt  *string  `json:"expires_at,omitempty"`
	LastUsedAt *string  `json:"last_used_at,omitempty"`
	// Expired is true once ExpiresAt has passed; a token without an expiry never expires.
	Expired bool `json:"expired"`
}

// UnifiedTokenList is GET /api/v1/tokens.
type UnifiedTokenList struct {
	Tokens []UnifiedTokenResponse `json:"tokens"`
	Total  int                    `json:"total"`
	Page   int                    `json:"page"`
	Limit  int                    `json:"limit"`
}

// AuditSubject names what an audit event acted on, resolved server-side so
// clients do not look ids up. Name is null when the subject is gone and the
// event did not record its name.
type AuditSubject struct {
	// Kind is repository, package, project, user, group, token, bundle,
	// permission_target, gpg_key, sso_provider, setting, or the raw
	// resource_type of an event kind without a dedicated one.
	Kind    string  `json:"kind"`
	Name    *string `json:"name"`
	Version string  `json:"version,omitempty"`
	// Repository and Project name the repository and project the event belongs to.
	Repository string `json:"repository,omitempty"`
	Project    string `json:"project,omitempty"`
}

// ActivityEntry is a ProjectActivityEntry with the actor's and the subject's
// names resolved. It is the entry of the project and repository activity feeds.
type ActivityEntry struct {
	ProjectActivityEntry

	// ActorName is the user, token owner or "system" behind the event ("" when unknown).
	ActorName string        `json:"actor_name"`
	Subject   *AuditSubject `json:"subject"`
}

// ActivityPage is GET /api/v1/projects/{project}/activity and
// GET /api/v1/repositories/{name}/activity: keyset paging only, no total.
// NextCursor is empty when HasMore is false.
type ActivityPage struct {
	Data       []ActivityEntry `json:"data"`
	PerPage    int             `json:"per_page"`
	HasMore    bool            `json:"has_more"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// VulnScanOverviewRowResponse is one scan of the cross-repository overview:
// a VulnScanResultResponse without its findings, with the repository's name
// and format.
type VulnScanOverviewRowResponse struct {
	ID               string  `json:"id"`
	RepositoryID     string  `json:"repo_id"`
	RepositoryName   string  `json:"repository_name"`
	RepositoryFormat string  `json:"repository_format"`
	ArtifactID       string  `json:"artifact_id"`
	PackageName      string  `json:"package_name"`
	PackageVersion   string  `json:"package_version"`
	Ecosystem        string  `json:"ecosystem"`
	Scanner          string  `json:"scanner"`
	ScanStatus       string  `json:"scan_status"`
	VulnCount        int     `json:"vuln_count"`
	CriticalCount    int     `json:"critical_count"`
	HighCount        int     `json:"high_count"`
	MediumCount      int     `json:"medium_count"`
	LowCount         int     `json:"low_count"`
	Quarantined      bool    `json:"quarantined"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
	ScannedAt        *string `json:"scanned_at"`
	// ScanError is a sanitized summary of the failing scanners.
	ScanError      *string  `json:"scan_error"`
	FailedScanners []string `json:"failed_scanners"`
	// KEVCount, MaxEPSS and Priority are as in VulnScanResultResponse.
	KEVCount int      `json:"kev_count"`
	MaxEPSS  *float64 `json:"max_epss"`
	Priority string   `json:"priority"`
}

// VulnGateCriteria echoes the conditions a gate check applied.
type VulnGateCriteria struct {
	KEV         bool     `json:"kev"`
	EPSSMin     *float64 `json:"epss_min"`
	MinPriority string   `json:"min_priority"`
}

// VulnGateFailure is one finding that trips the gate.
type VulnGateFailure struct {
	ID string `json:"id"`
	// CVE is the CVE id the signals were looked up by; empty when the finding has none.
	CVE      string `json:"cve"`
	Priority string `json:"priority"`
	// Reasons names the conditions it meets ("kev", "epss>=0.5", "priority>=high").
	Reasons []string `json:"reasons"`
}

// VulnGateResponse is the verdict of a gate check on one scan: Pass is true
// when no finding meets any condition.
type VulnGateResponse struct {
	Pass     bool              `json:"pass"`
	Criteria VulnGateCriteria  `json:"criteria"`
	Checked  int               `json:"checked"`
	Failed   []VulnGateFailure `json:"failed"`
}

// ExploitFeedResponse is the state of one exploitation-signal feed.
type ExploitFeedResponse struct {
	// Name is kev or epss.
	Name      string `json:"name"`
	SourceURL string `json:"source_url"`
	Version   string `json:"version"`
	// PublishedAt is when the feed says the data was released; null when never loaded.
	PublishedAt *string `json:"published_at"`
	EntryCount  int     `json:"entry_count"`
	// ImportedAt is when the data was last replaced; CheckedAt when the source
	// was last asked.
	ImportedAt *string `json:"imported_at"`
	CheckedAt  *string `json:"checked_at"`
	// LastError is the sanitized reason the last check failed; empty after a success.
	LastError string `json:"last_error"`
}

// ExploitFeedsResponse is GET /api/v1/security/exploit-feeds.
type ExploitFeedsResponse struct {
	Feeds []ExploitFeedResponse `json:"feeds"`
	// EPSSThreshold is the EPSS score from which a finding has priority epss.
	EPSSThreshold float64 `json:"epss_threshold"`
	// Enabled is true when the feeds are loaded into this instance's database.
	Enabled bool `json:"enabled"`
}

// VulnScanExportResponse is one scan of the cross-repository JSON export: the
// scan with its findings and the name of its repository.
type VulnScanExportResponse struct {
	VulnScanResultResponse

	RepositoryName string `json:"repository_name"`
}

// VulnScanOverviewResponse is one keyset page of the overview.
type VulnScanOverviewResponse struct {
	Scans   []VulnScanOverviewRowResponse `json:"scans"`
	HasMore bool                          `json:"has_more"`
	// NextCursor is the opaque cursor of the next page, present when HasMore.
	NextCursor *string `json:"next_cursor,omitempty"`
}

// VulnScanSeverityCounts counts scans by their worst finding: each field is
// the number of scans with at least one finding of that severity or worse.
type VulnScanSeverityCounts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
}

// VulnScanSummaryResponse counts the scans of the overview.
type VulnScanSummaryResponse struct {
	Total           int                    `json:"total"`
	Completed       int                    `json:"completed"`
	Partial         int                    `json:"partial"`
	Failed          int                    `json:"failed"`
	Scanning        int                    `json:"scanning"`
	Quarantined     int                    `json:"quarantined"`
	SeverityAtLeast VulnScanSeverityCounts `json:"severity_at_least"`
}

// SecretFindingExport is a secret finding as exported: its type and severity
// only, never the (redacted) value or the text around it.
type SecretFindingExport struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
}

// SecretScanExportResponse is a secret scan in the JSON export.
type SecretScanExportResponse struct {
	ID           string                `json:"id"`
	ArtifactID   string                `json:"artifact_id"`
	ScanStatus   string                `json:"scan_status"`
	Findings     []SecretFindingExport `json:"findings"`
	FindingCount int                   `json:"finding_count"`
	Quarantined  bool                  `json:"quarantined"`
	CreatedAt    string                `json:"created_at"`
	ScannedAt    *string               `json:"scanned_at"`
	ScanError    *string               `json:"scan_error"`
}

// AttestationKeyResponse describes the key SBOM and VEX attestations are signed with.
type AttestationKeyResponse struct {
	KeyID        string `json:"key_id"`
	Algorithm    string `json:"algorithm"`
	Source       string `json:"source"`
	PublicKeyPEM string `json:"public_key_pem"`
	PublicKeyURL string `json:"public_key_url"`
}

// DependencyTrackSettingsResponse is the global Dependency-Track configuration.
// The API key is write-only; APIKeySet tells whether one is stored.
type DependencyTrackSettingsResponse struct {
	Enabled        bool   `json:"enabled"`
	BaseURL        string `json:"base_url"`
	APIKeySet      bool   `json:"api_key_set"`
	ProjectName    string `json:"project_name"`
	ProjectVersion string `json:"project_version"`
	AutoPush       bool   `json:"auto_push"`
	PushVEX        bool   `json:"push_vex"`
}

// DependencyTrackOverrideResponse is a repository's override of the global
// configuration: empty strings and null booleans inherit.
type DependencyTrackOverrideResponse struct {
	Enabled        bool   `json:"enabled"`
	BaseURL        string `json:"base_url"`
	APIKeySet      bool   `json:"api_key_set"`
	ProjectName    string `json:"project_name"`
	ProjectVersion string `json:"project_version"`
	AutoPush       *bool  `json:"auto_push"`
	PushVEX        *bool  `json:"push_vex"`
}

// DependencyTrackEffectiveResponse is the configuration that applies to a repository.
type DependencyTrackEffectiveResponse struct {
	Enabled        bool   `json:"enabled"`
	Ready          bool   `json:"ready"`
	Source         string `json:"source"`
	BaseURL        string `json:"base_url"`
	APIKeySet      bool   `json:"api_key_set"`
	ProjectName    string `json:"project_name"`
	ProjectVersion string `json:"project_version"`
	AutoPush       bool   `json:"auto_push"`
	PushVEX        bool   `json:"push_vex"`
}

// DependencyTrackRepositoryResponse is a repository's override and the effective configuration.
type DependencyTrackRepositoryResponse struct {
	Override  *DependencyTrackOverrideResponse `json:"override"`
	Effective DependencyTrackEffectiveResponse `json:"effective"`
}

// DependencyTrackPushResponse is the last push of one artifact.
type DependencyTrackPushResponse struct {
	ArtifactID     string  `json:"artifact_id"`
	State          string  `json:"state"`
	Detail         string  `json:"detail"`
	ProjectUUID    string  `json:"project_uuid"`
	ProjectName    string  `json:"project_name"`
	ProjectVersion string  `json:"project_version"`
	Components     int     `json:"components"`
	VEXStatements  int     `json:"vex_statements"`
	VEXApplied     int     `json:"vex_applied"`
	BOMPushedAt    *string `json:"bom_pushed_at"`
	VEXPushedAt    *string `json:"vex_pushed_at"`
	UpdatedAt      string  `json:"updated_at"`
}

// DependencyTrackTestResponse is the outcome of a connection test.
type DependencyTrackTestResponse struct {
	Version            string   `json:"version"`
	Permissions        []string `json:"permissions"`
	MissingPermissions []string `json:"missing_permissions"`
}

// AttestationAttachSettingsResponse is a repository's referrer-attach switch.
type AttestationAttachSettingsResponse struct {
	Enabled bool `json:"enabled"`
}

// AttestationAttachResponse reports an attestation attached to a docker image
// as an OCI referrer.
type AttestationAttachResponse struct {
	ArtifactID    string `json:"artifact_id"`
	Kind          string `json:"kind"`
	PredicateType string `json:"predicate_type"`
	// Subject is the digest of the image manifest the attestation is attached to.
	Subject string `json:"subject"`
	// ArtifactType is the referrer's OCI artifactType.
	ArtifactType string `json:"artifact_type"`
	// ReferrerDigest is the digest of the referrer manifest.
	ReferrerDigest string `json:"referrer_digest"`
	// Created is false when an identical attestation was already attached.
	Created bool `json:"created"`
	// Replaced is the digest of the earlier attestation this one replaced.
	Replaced string `json:"replaced,omitempty"`
}
