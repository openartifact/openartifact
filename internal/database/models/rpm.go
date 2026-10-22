// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

type Repository struct {
	ID                      string
	Name                    string
	Format                  string
	Description             string
	Public                  bool
	AllowFullUnpublish      bool
	RepoType                string // "hosted", "proxy", "group"
	UpstreamURL             string // proxy only
	UpstreamUsername        string // proxy only
	UpstreamPasswordEnc     string // proxy only — AES-256-GCM encrypted
	CacheTTLSeconds         int    // proxy only, default 3600
	NegativeCacheTTLSeconds int    // proxy only, default 60
	MaxStaleSeconds         int    // proxy only: how long past expiry a cached copy may be served when the upstream fails; 0 disables, default 604800
	Blocked                 bool   // proxy only
	ProjectID               *string
	// PresignDownloads overrides storage.presignDownloads for this repository:
	// nil inherits the global setting, true/false force redirect/streaming.
	PresignDownloads *bool
	// TrashRetentionDays is how long deleted items stay restorable; nil uses
	// the server default, 0 deletes immediately (no trash).
	TrashRetentionDays *int
	// ImmutableReleases forbids deleting or overwriting released versions
	// without an admin's force.
	ImmutableReleases bool
	// MergeProxyVersions is a group-only setting. false (the default) makes a
	// hosted member win a package name: proxy members' versions and files of
	// a name any hosted member holds are hidden. true merges all members.
	MergeProxyVersions bool
	// DeletedAt/DeletedBy are set while the repository is in the trash. Every
	// lookup except the trash API hides such a repository.
	DeletedAt *time.Time
	DeletedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type RPMRepo struct {
	ID                    string
	RepositoryID          string
	BaseArch              string
	MetadataStale         bool
	SigningKeyID          string
	SigningKeyFingerprint string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// ComponentBlob locates the downloadable blob behind one row of the
// repository package listing (any format). SHA256 is empty when the row has
// no blob.
type ComponentBlob struct {
	ID        string
	Format    string
	Filename  string // the listing's filename for the row
	Name      string // the listing's package name for the row
	Version   string // the listing's version for the row ("" for raw and proxy-cache rows)
	SHA256    string
	Size      int64
	MediaType string
}

type Component struct {
	ID            string
	RepositoryID  string
	ArtifactID    string
	SHA256        string
	Format        string
	Name          string
	Version       string
	Release       string
	Epoch         int
	Arch          string
	Summary       string
	Description   string
	License       string
	URL           string
	Vendor        string
	Group         string
	Packager      string
	SourceRPM     string
	BuildHost     string
	BuildTime     int64
	PackageSize   int64
	InstalledSize int64
	ArchiveSize   int64
	Filename      string
	// HeaderStart/HeaderEnd are the RPM header byte range (rpm packages only).
	HeaderStart int64
	HeaderEnd   int64
	// RPMExtra is JSON-encoded dependency/file/changelog data (rpm packages only).
	RPMExtra  []byte
	CreatedAt time.Time
	UpdatedAt time.Time
}

type RepoMetadata struct {
	ID            string
	RepositoryID  string
	Path          string
	BlobSHA256    string
	BlobSize      int64
	LastGenerated time.Time
}

type RepositoryGroupMember struct {
	GroupRepoID  string
	MemberRepoID string
	Priority     int
	CreatedAt    time.Time
}

type ProxyCacheEntry struct {
	ID           string
	RepositoryID string
	CacheKey     string
	BlobKey      string
	ContentType  string
	StatusCode   int
	SizeBytes    int64
	HitCount     int
	LastHitAt    *time.Time
	ExpiresAt    time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// RpmPackage represents an RPM package stored in the rpm_packages table.
type RpmPackage struct {
	ID            string
	RepositoryID  string
	BlobID        string
	Name          string
	Version       string
	Release       string
	Epoch         int
	Arch          string
	Summary       string
	Description   string
	License       string
	URL           string
	Vendor        string
	GroupName     string
	Packager      string
	SourceRPM     string
	BuildHost     string
	BuildTime     *time.Time
	Filename      string
	PackageSize   int64
	InstalledSize int64
	ArchiveSize   int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
