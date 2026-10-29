// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package responses

// RepositorySecurity is the security state of a repository, from its artifacts'
// latest scans. Counts are raw: VEX statements do not suppress any.
type RepositorySecurity struct {
	Critical int64 `json:"critical"`
	High     int64 `json:"high"`
	Medium   int64 `json:"medium"`
	Low      int64 `json:"low"`
	Secrets  int64 `json:"secrets"`
	// LicenseViolations is the number of artifacts whose license scan is
	// quarantined; the server stores no other license violation count.
	LicenseViolations int64 `json:"license_violations"`
	// ScannedAt is the latest completed scan; absent when never scanned.
	ScannedAt *string `json:"scanned_at,omitempty"`
}

// RepositoryQuota is a repository's storage use against its quota.
type RepositoryQuota struct {
	// LimitBytes is the size quota; absent when unlimited.
	LimitBytes *int64 `json:"limit_bytes,omitempty"`
	UsedBytes  int64  `json:"used_bytes"`
}

// RepositoryStats are the catalog figures of a repository in the list.
type RepositoryStats struct {
	// PackageCount is the number of distinct package names.
	PackageCount int64 `json:"package_count"`
	// SizeBytes is the stored size.
	SizeBytes int64 `json:"size_bytes"`
	// LastPublishedAt is the newest package; absent for an empty repository.
	LastPublishedAt *string            `json:"last_published_at,omitempty"`
	Security        RepositorySecurity `json:"security"`
	Quota           RepositoryQuota    `json:"quota"`
}

// PackageSecurity is the vulnerability count of a package row by severity.
type PackageSecurity struct {
	Critical int64 `json:"critical"`
	High     int64 `json:"high"`
	Medium   int64 `json:"medium"`
	Low      int64 `json:"low"`
}

// PackageResponse is one file of a repository's package listing.
type PackageResponse struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Version   string          `json:"version"`
	Format    string          `json:"format"`
	Arch      string          `json:"arch"`
	Size      int64           `json:"size"`
	SHA256    string          `json:"sha256"`
	Filename  string          `json:"filename"`
	CreatedAt string          `json:"created_at"`
	Security  PackageSecurity `json:"security"`
	// Flagged: a critical or high vulnerability, a secret finding or a
	// quarantined scan result.
	Flagged bool `json:"flagged"`
	// ScannedAt is the latest completed scan; absent when never scanned.
	ScannedAt *string `json:"scanned_at,omitempty"`
}

// PackageListResponse is a page of PackageResponse.
type PackageListResponse struct {
	// Total is the number of matching files; absent on a request that carries a
	// cursor, which does not count.
	Total *int `json:"total,omitempty"`
	Page  int  `json:"page"`
	Limit int  `json:"limit"`
	// HasMore: the listing continues after this page.
	HasMore bool `json:"has_more"`
	// NextCursor, set whenever HasMore is true on the newest-first listing,
	// is the cursor query parameter that fetches the next page.
	NextCursor string            `json:"next_cursor,omitempty"`
	Packages   []PackageResponse `json:"packages"`
}

// PackageGroupResponse is one package (repository, format, name) across its
// versions: the rollup of GET /api/v1/repositories/{name}/packages?group=package.
type PackageGroupResponse struct {
	Name          string `json:"name"`
	Format        string `json:"format"`
	LatestVersion string `json:"latest_version"`
	VersionCount  int    `json:"version_count"`
	// SizeBytes is the size of the latest version, all its files summed.
	SizeBytes int64 `json:"size_bytes"`
	// UpdatedAt is the latest modification of any version.
	UpdatedAt string `json:"updated_at"`
	// Security sums the latest version's files.
	Security PackageSecurity `json:"security"`
	Flagged  bool            `json:"flagged"`
	// ScannedAt is the latest completed scan of the latest version; absent when never scanned.
	ScannedAt *string `json:"scanned_at,omitempty"`
}

// PackageGroupListResponse is a page of PackageGroupResponse.
type PackageGroupListResponse struct {
	Total    int                    `json:"total"`
	Page     int                    `json:"page"`
	Limit    int                    `json:"limit"`
	Packages []PackageGroupResponse `json:"packages"`
}

// RecentPackageResponse is a recently published package version.
type RecentPackageResponse struct {
	Repository string `json:"repository"`
	Format     string `json:"format"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	CreatedAt  string `json:"created_at"`
	// CreatedByName is the publisher. The server does not record who published
	// a package, so it is never set.
	CreatedByName string `json:"created_by_name,omitempty"`
}

// RecentPackagesResponse is the list of recent package versions.
type RecentPackagesResponse struct {
	Packages []RecentPackageResponse `json:"packages"`
}

// PackageFileResponse is one file of a package version.
type PackageFileResponse struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Arch     string `json:"arch"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

// PackageDependencyResponse is one declared dependency of a package version.
type PackageDependencyResponse struct {
	Name string `json:"name"`
	// Version is the version requirement as the format writes it.
	Version string `json:"version,omitempty"`
	// Kind is the role: runtime, dev, peer, optional, build, ... as the format names it.
	Kind string `json:"kind,omitempty"`
}

// PackageMetadataEntry is one format-specific field of a package version.
type PackageMetadataEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// PackageDetailResponse is one package version with the metadata its format
// stores: GET /api/v1/repositories/{name}/packages/{id}.
type PackageDetailResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Format      string `json:"format"`
	Arch        string `json:"arch"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	MD5         string `json:"md5,omitempty"`
	SHA1        string `json:"sha1,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	Summary     string `json:"summary,omitempty"`
	Description string `json:"description,omitempty"`
	License     string `json:"license,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
	// Metadata is the format-specific fields, ordered by key.
	Metadata []PackageMetadataEntry `json:"metadata"`
	// Dependencies are the declared dependencies, for the formats that store them.
	Dependencies []PackageDependencyResponse `json:"dependencies"`
	// Files are all the files of this version (one per architecture,
	// classifier or distribution), this one included.
	Files []PackageFileResponse `json:"files"`
	// VersionCount and LatestVersion describe the package this version belongs to.
	VersionCount  int    `json:"version_count"`
	LatestVersion string `json:"latest_version"`
}
