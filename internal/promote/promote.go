// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package promote copies the content of release bundle items from one hosted
// repository into another (bundle promotion).
//
// Every format keeps its metadata in its own tables, so each hosted format has
// a Promoter: it finds the live rows of the source repository that store the
// item's content, and writes the same rows (new ids, the destination's
// repository id) into the destination. Blobs are content-addressed, so a copy
// is metadata only: the new rows reference the existing blobs row and the bytes
// are never read or duplicated. The Engine runs every promoter of a promotion
// inside ONE database transaction together with the bundle's promotion record
// and stage change, so a promotion either lands completely or not at all.
//
// A promoter never overwrites: a destination row that holds the same content is
// reported as Existing (so retrying a promotion is idempotent), one that holds
// different content is a ConflictError (409; Immutable when the destination has
// immutable_releases and the version is a release).
package promote

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/trash"
)

// Errors a promotion can fail with. Use errors.Is; most are wrapped in an
// ItemError that names the bundle item.
var (
	// ErrItemNotFound: the source repository holds no live artifact with the item's content.
	ErrItemNotFound = errors.New("promote: artifact not found in the source repository")
	// ErrFormatMismatch: the item's source repository and the target repository have different formats.
	ErrFormatMismatch = errors.New("promote: source and target repository formats differ")
	// ErrNotHosted: a source or target repository is not a hosted repository.
	ErrNotHosted = errors.New("promote: only hosted repositories can be promoted from or into")
	// ErrSameRepository: an item's source repository is the target repository.
	ErrSameRepository = errors.New("promote: the target repository is the source repository of an item")
	// ErrUnsupportedFormat: no promoter exists for the item's format.
	ErrUnsupportedFormat = errors.New("promote: promotion is not supported for this format")
	// ErrBundleState: the bundle is no longer a sealed bundle that can be promoted.
	ErrBundleState = errors.New("promote: bundle is not sealed")
	// ErrStageChanged: the bundle's stage changed while the request was validated (a concurrent promotion).
	ErrStageChanged = errors.New("promote: bundle stage changed concurrently")
	// ErrQuarantined: an item is quarantined in its source repository.
	ErrQuarantined = errors.New("promote: artifact is quarantined in the source repository")
	// ErrConflict: the target repository holds different content at the same coordinates.
	ErrConflict = errors.New("promote: the target repository holds different content at the same coordinates")
	// ErrEmpty: the bundle has no items.
	ErrEmpty = errors.New("promote: bundle has no items")
)

// Tx is the transaction a Promoter writes through (database.Transaction and
// *sql.Tx both satisfy it).
type Tx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Promoter copies one format's artifacts between hosted repositories.
type Promoter interface {
	// Format is the repository format the promoter handles.
	Format() string
	// Promote copies the content req selects from req.Src to req.Dst inside
	// tx. It returns ErrItemNotFound when req.Src holds no live artifact with
	// that content, and a *ConflictError when req.Dst holds different content
	// at the same coordinates. It must not commit or roll back tx; on any
	// error the Engine rolls everything back.
	Promote(ctx context.Context, tx Tx, req Request) (*Result, error)
}

// Request selects the content to copy.
type Request struct {
	// Src and Dst are the hosted repositories (same format) to copy from and into.
	Src, Dst *models.Repository
	// SHA256 is the content digest of the bundle item (lower-case hex): the
	// sha256 of the blob the artifact's file is stored as.
	SHA256 string
	// Path is the item's artifact_path, a free-form hint. When it names one of
	// several artifacts that share the content, only that one is copied;
	// otherwise every artifact with the content is.
	Path string
	// Force replaces content the destination holds at the same coordinates
	// instead of failing with a ConflictError (an admin's `?force=true`): the
	// replaced rows are moved to the destination's trash, see displace.
	Force bool
	// Actor is the username recorded on rows a forced promotion replaces.
	Actor string
}

// displace moves the live destination rows ids of kind to the trash so a
// forced promotion can write its own in their place.
func (r Request) displace(ctx context.Context, tx Tx, kind models.TrashKind, ids ...string) error {
	if err := database.TrashRowsInTx(ctx, tx, r.Dst.ID, kind, ids, r.Actor); err != nil {
		return fmt.Errorf("replace %s content in the target: %w", kind, err)
	}

	return nil
}

// Artifact describes one artifact row a promotion wrote or found.
type Artifact struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// Usage is the storage a promotion adds to the destination's quota.
type Usage struct {
	Bytes     int64
	Artifacts int
}

// RegenKind says which generated metadata a promotion made stale.
type RegenKind string

// The generated-metadata kinds that need a worker refresh after a promotion.
// Every other format computes its indexes live from the tables.
const (
	RegenDeb   RegenKind = "deb"
	RegenRPM   RegenKind = "rpm"
	RegenMaven RegenKind = "maven"
	RegenNPM   RegenKind = "npm"
)

// Regen is one metadata refresh to enqueue once the promotion has committed.
type Regen struct {
	Kind RegenKind
	// RepoID is the destination repository (deb, rpm, maven).
	RepoID string
	// GroupID and ArtifactID name the maven artifact.
	GroupID, ArtifactID string
	// PackageID is the destination npm package row.
	PackageID string
}

// Result is what a Promoter did for one request.
type Result struct {
	// Copied are the artifacts newly written to the destination.
	Copied []Artifact
	// Existing are artifacts the destination already held with the same content.
	Existing []Artifact
	// Replaced are the destination artifacts a forced promotion moved to the
	// trash because they held different content (their replacements are in Copied).
	Replaced []Artifact
	// Usage is what Copied adds to the destination's quota.
	Usage Usage
	// Licenses are the licenses the copied content declares, checked against
	// the destination's license policies.
	Licenses []string
	// QuarantineKeys are the identifiers (row id, paths) under which the
	// source content may have been quarantined.
	QuarantineKeys []string
	// Regen lists the metadata refreshes the destination needs.
	Regen []Regen
}

// merge appends o to r.
func (r *Result) merge(o *Result) {
	r.Copied = append(r.Copied, o.Copied...)
	r.Existing = append(r.Existing, o.Existing...)
	r.Replaced = append(r.Replaced, o.Replaced...)
	r.Usage.Bytes += o.Usage.Bytes
	r.Usage.Artifacts += o.Usage.Artifacts
	r.Licenses = append(r.Licenses, o.Licenses...)
	r.QuarantineKeys = append(r.QuarantineKeys, o.QuarantineKeys...)
	r.Regen = append(r.Regen, o.Regen...)
}

// ConflictError reports different content at the coordinates being promoted.
type ConflictError struct {
	Format  string
	Name    string
	Version string
	// Immutable is set when the destination has immutable_releases and the
	// version is a release, i.e. the conflict is a refused overwrite.
	Immutable bool
}

func (e *ConflictError) Error() string {
	what := e.Format + " " + e.Name
	if e.Version != "" {
		what += " " + e.Version
	}

	if e.Immutable {
		return "promote: " + what + " already exists in the target repository with different content and releases are immutable there"
	}

	return "promote: " + what + " already exists in the target repository with different content"
}

// Is makes errors.Is(err, ErrConflict) true.
func (e *ConflictError) Is(target error) bool { return target == ErrConflict }

// conflict builds the ConflictError for coordinates of req.Dst.
func (r Request) conflict(format, name, version string) error {
	return &ConflictError{
		Format:    format,
		Name:      name,
		Version:   version,
		Immutable: r.Dst.ImmutableReleases && trash.IsRelease(format, version),
	}
}

// LicenseBlockedError reports licenses a blocking policy of the destination refuses.
type LicenseBlockedError struct {
	Licenses   []string
	Violations []licensepolicy.Violation
}

func (e *LicenseBlockedError) Error() string {
	return "promote: a license policy of the target repository blocks " + strings.Join(e.Licenses, ", ")
}

// ItemError attaches the bundle item a failure belongs to.
type ItemError struct {
	SHA256 string
	Path   string
	Err    error
}

func (e *ItemError) Error() string {
	return fmt.Sprintf("bundle item %s (%s): %v", e.SHA256, e.Path, e.Err)
}

func (e *ItemError) Unwrap() error { return e.Err }
