// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package promote copies the content of release bundle items from one hosted
// repository into another (bundle promotion).
//
// Every package format except docker stores its files in the generic tables
// (packages, package_versions, package_files and the side tables of its storage
// descriptor), so one promoter serves them all: it finds the live source files
// that store the item's content and writes the same rows (new ids, the
// destination's repository id) into the destination. What differs per format is
// data, the descriptor's storage.Promotion. Docker keeps its own tables and its
// own promoter. Blobs are content-addressed, so a copy is metadata only: the new
// rows reference the existing blobs row and the bytes are never read or
// duplicated. The Engine runs every item inside ONE database transaction
// together with the bundle's promotion record and stage change, so a promotion
// either lands completely or not at all.
//
// A promotion never overwrites: a destination file that holds the same content
// is reported as Existing (so retrying a promotion is idempotent), one that
// holds different content is a ConflictError (409; Immutable when the
// destination has immutable_releases and the version is a release), unless the
// promotion is forced, which moves the replaced files to the trash.
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
	// ErrUnsupportedFormat: the item's format has no storage descriptor to promote by.
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

// displace moves the live destination docker rows ids of kind to the trash so a
// forced promotion can write its own in their place; package files go through
// database.TrashFilesInTx.
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

// Regen is one metadata refresh to enqueue once the promotion has committed.
type Regen struct {
	// Format is the format whose Changed lifecycle hook runs it.
	Format string
	// RepoID is the destination repository (every kind but npm).
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

// candidate is a live source row holding the item's content, with the
// identifiers Request.Path is matched against.
type candidate struct {
	id     string
	idents []string
}

// narrow keeps the candidates whose identifiers equal path (ignoring a leading
// slash); with no such candidate every candidate stays.
func narrow(cands []candidate, path string) []candidate {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return cands
	}

	var hit []candidate

	for _, c := range cands {
		for _, id := range c.idents {
			if strings.TrimPrefix(id, "/") == path {
				hit = append(hit, c)

				break
			}
		}
	}

	if len(hit) == 0 {
		return cands
	}

	return hit
}
