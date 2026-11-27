// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package promote

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/trash"
)

// Errors of a copy or move that promotion does not have.
var (
	// ErrNoItems: the request selects nothing.
	ErrNoItems = errors.New("copy: no items")
	// ErrBadItem: an item selects neither a package version nor a content digest,
	// or selects a package version of a format that is only selected by digest.
	ErrBadItem = errors.New("copy: an item must name a package and version, or a sha256")
	// ErrNotPermitted: the caller may not read (or, for a move, delete) a source artifact.
	ErrNotPermitted = errors.New("copy: not permitted on the source artifact")
	// ErrSourceImmutable: a move would take a release out of a source repository with immutable releases.
	ErrSourceImmutable = errors.New("move: the source repository has immutable releases")
	// ErrSourceHeld: a sealed release bundle holds the artifact in the source repository.
	ErrSourceHeld = errors.New("move: a sealed release bundle holds the artifact in the source repository")
)

// CopyItem selects what to copy: every live file of a package version
// (Package and Version), or the content of a bundle item (SHA256 with an
// optional Path, read as bundle items are; for docker the manifest digest with
// an optional tag).
type CopyItem struct {
	Package, Version string
	SHA256, Path     string
}

// CopyParams is one copy or move request. The caller has authorized the
// repositories; Authorize decides per source artifact.
type CopyParams struct {
	Source, Target *models.Repository
	Items          []CopyItem
	// Move takes the source files to the source's trash once copied.
	Move bool
	// DryRun computes the report, then rolls everything back.
	DryRun bool
	// Force replaces conflicting target content (to the target's trash) and, for a
	// move, lets a release leave a source repository with immutable releases. The
	// caller has checked the principal is an administrator.
	Force bool
	// Actor is the username recorded on trashed rows.
	Actor string
	// Authorize is asked for every source artifact an item resolves to; it fails
	// with ErrNotPermitted when the caller may not copy (move) it.
	Authorize func(ctx context.Context, a Artifact) error
}

// CopyReport is the outcome of a copy or move.
type CopyReport struct {
	Items []ItemReport
	// Copied, Existing and Replaced count artifacts written, already present
	// and (forced) replaced in the target.
	Copied, Existing, Replaced int
	// Moved lists the source artifacts a move took out of the source repository.
	Moved []Artifact
	// Uploaded lists every artifact written to the target (all items), for the
	// events that follow a committed copy.
	Uploaded []Artifact
}

// Copy copies the items from the source into the target repository, and for a
// move takes them out of the source (into its trash), in one transaction: on any
// error nothing is changed, and a dry run changes nothing either.
func (e *Engine) Copy(ctx context.Context, p CopyParams) (*CopyReport, error) {
	if len(p.Items) == 0 {
		return nil, ErrNoItems
	}

	if err := p.validate(); err != nil {
		return nil, err
	}

	tx, err := e.db.BeginTx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin copy: %w", err)
	}

	defer func() { _ = tx.Rollback() }() // no-op once committed

	run := copyRun{e: e, p: p, report: &CopyReport{}}

	for _, it := range p.Items {
		if err := run.item(ctx, tx, it); err != nil {
			return nil, err
		}
	}

	if p.Move {
		if err := run.moveOut(ctx, tx); err != nil {
			return nil, err
		}
	}

	if err := copyProperties(ctx, tx, p, run.copies); err != nil {
		return nil, err
	}

	if p.DryRun {
		if err := e.precheck(ctx, p.Target.ID, run.total); err != nil {
			return nil, err
		}

		return run.report, nil
	}

	// See Promote for the reservation protocol around the commit.
	if err := e.charge(ctx, p.Target.ID, run.total); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		e.refund(ctx, p.Target.ID, run.total)

		return nil, fmt.Errorf("commit copy: %w", err)
	}

	e.settle(ctx, p.Target.ID, run.total)

	e.notify(run.regen)

	if p.Move {
		for i := range run.sourceRegen {
			run.sourceRegen[i].RepoID = p.Source.ID
		}

		e.notify(run.sourceRegen)
	}

	return run.report, nil
}

// validate checks the repositories and the items before anything is read.
func (p CopyParams) validate() error {
	for _, r := range []*models.Repository{p.Source, p.Target} {
		if r.RepoType != models.ProjectRepoTypeHosted || r.DeletedAt != nil {
			return fmt.Errorf("%w: %s", ErrNotHosted, r.Name)
		}
	}

	if p.Source.ID == p.Target.ID {
		return ErrSameRepository
	}

	if p.Source.Format != p.Target.Format {
		return fmt.Errorf(
			"%w: %s is %s, target %s is %s",
			ErrFormatMismatch,
			p.Source.Name,
			p.Source.Format,
			p.Target.Name,
			p.Target.Format,
		)
	}

	for _, it := range p.Items {
		byVersion := it.Package != "" || it.Version != ""

		switch {
		case byVersion && (it.Package == "" || it.Version == "" || it.SHA256 != ""):
			return ErrBadItem
		case byVersion && p.Source.Format == dockerFormat:
			return fmt.Errorf("%w: docker images are selected by manifest digest", ErrBadItem)
		case !byVersion && !isHexDigest(it.SHA256):
			return ErrBadItem
		}
	}

	return nil
}

func isHexDigest(s string) bool {
	if len(s) != 64 {
		return false
	}

	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

// copyRun accumulates what the items of one Copy did.
type copyRun struct {
	e      *Engine
	p      CopyParams
	report *CopyReport

	total       Usage
	regen       []Regen
	sourceRegen []Regen
	copies      []RowCopy
	files       []string
	manifests   []string
	// moved are the source artifacts a move takes out, with the item each came from.
	moved []movedArtifact
}

type movedArtifact struct {
	Artifact

	item *CopyItem
}

// item copies one item and applies the source quarantine, the caller's
// authorization and the target license policy to what it found.
func (c *copyRun) item(ctx context.Context, tx Tx, it CopyItem) error {
	itemErr := func(err error) error {
		return &ItemError{SHA256: it.SHA256, Path: it.Path, Package: it.Package, Version: it.Version, Err: err}
	}

	req := Request{
		Src: c.p.Source, Dst: c.p.Target, SHA256: it.SHA256, Path: it.Path,
		Package: it.Package, Version: it.Version, Force: c.p.Force, Actor: c.p.Actor,
	}

	res, err := promoteItem(ctx, tx, c.p.Source.Format, req)
	if err != nil {
		return itemErr(err)
	}

	for _, a := range sourceArtifacts(res) {
		if c.p.Authorize == nil {
			break
		}

		if err := c.p.Authorize(ctx, c.sourceName(a)); err != nil {
			return itemErr(err)
		}
	}

	if err := c.e.checkPolicies(ctx, c.p.Source, c.p.Target, res); err != nil {
		return itemErr(err)
	}

	c.total.Bytes += res.Usage.Bytes
	c.total.Artifacts += res.Usage.Artifacts
	c.regen = append(c.regen, res.Regen...)
	c.sourceRegen = append(c.sourceRegen, res.SourceRegen...)
	c.copies = append(c.copies, res.Copies...)
	c.files = append(c.files, res.SourceFiles...)
	c.manifests = append(c.manifests, res.SourceManifests...)

	c.report.Uploaded = append(c.report.Uploaded, res.Copied...)

	if c.p.Move {
		item := it
		for _, a := range sourceArtifacts(res) {
			c.moved = append(c.moved, movedArtifact{Artifact: c.sourceName(a), item: &item})
		}
	}

	c.report.Copied += len(res.Copied)
	c.report.Existing += len(res.Existing)
	c.report.Replaced += len(res.Replaced)
	c.report.Items = append(c.report.Items, ItemReport{
		SHA256: it.SHA256, ArtifactPath: it.Path, Package: it.Package, Version: it.Version, Format: c.p.Source.Format,
		Copied: nonNil(res.Copied), Existing: nonNil(res.Existing), Replaced: nonNil(res.Replaced),
	})

	return nil
}

// sourceArtifacts are the artifacts of the source repository an item resolved
// to: what was copied and what the target already held. File formats name them
// by their source rows; docker names them by the target (see sourceName).
func sourceArtifacts(res *Result) []Artifact {
	out := make([]Artifact, 0, len(res.Copied)+len(res.Existing))
	out = append(out, res.Copied...)

	return append(out, res.Existing...)
}

// sourceName renames a docker artifact, which the promoter names by the target
// repository, to the source repository.
func (c *copyRun) sourceName(a Artifact) Artifact {
	if c.p.Source.Format != dockerFormat {
		return a
	}

	for _, f := range []*string{&a.Name, &a.Path} {
		if rest, ok := strings.CutPrefix(*f, c.p.Target.Name); ok && (rest == "" || rest[0] == '/' || rest[0] == ':' || rest[0] == '@') {
			*f = c.p.Source.Name + rest
		}
	}

	return a
}

// moveOut takes the copied items out of the source: refused for a release of a
// repository with immutable releases (unless forced) and for content a sealed
// release bundle holds, otherwise the rows go to the source's trash.
func (c *copyRun) moveOut(ctx context.Context, tx Tx) error {
	var shas []string

	for _, m := range c.moved {
		if c.p.Source.ImmutableReleases && !c.p.Force && trash.IsRelease(c.p.Source.Format, m.Version) {
			return &ItemError{
				SHA256: m.item.SHA256, Path: m.item.Path, Package: m.item.Package, Version: m.item.Version,
				Err: fmt.Errorf("%w: %s %s", ErrSourceImmutable, m.Name, m.Version),
			}
		}

		if m.SHA256 != "" {
			shas = append(shas, m.SHA256)
		}
	}

	held, err := heldBySealedBundle(ctx, tx, c.p.Source.ID, shas)
	if err != nil {
		return err
	}

	if held != "" {
		return fmt.Errorf("%w: %s", ErrSourceHeld, held)
	}

	for _, m := range c.moved {
		c.report.Moved = append(c.report.Moved, m.Artifact)
	}

	if len(c.files) > 0 {
		if err := database.TrashFilesInTx(ctx, tx, c.p.Source.ID, c.files, c.p.Actor); err != nil {
			return fmt.Errorf("move %s files to the source trash: %w", c.p.Source.Format, err)
		}
	}

	if len(c.manifests) > 0 {
		if err := database.TrashRowsInTx(ctx, tx, c.p.Source.ID, models.TrashKindDockerManifest, c.manifests, c.p.Actor); err != nil {
			return fmt.Errorf("move docker manifests to the source trash: %w", err)
		}
	}

	return nil
}

// heldBySealedBundle returns "name version" of a sealed release bundle that has
// an item of the source repository with one of the digests, or "".
func heldBySealedBundle(ctx context.Context, tx Tx, repoID string, shas []string) (string, error) {
	if len(shas) == 0 {
		return "", nil
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT rb.name, rb.version
		   FROM release_bundle_items rbi
		   JOIN release_bundles rb ON rb.id = rbi.bundle_id
		  WHERE rb.state = 'sealed' AND rbi.source_repo_id = $1::uuid AND rbi.sha256 = ANY($2::text[])
		  ORDER BY rb.name, rb.version LIMIT 1`,
		repoID, "{"+strings.Join(shas, ",")+"}")
	if err != nil {
		return "", fmt.Errorf("look up sealed bundles holding the source artifacts: %w", err)
	}

	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		return "", rows.Err() //nolint:wrapcheck // nil, or the iteration error
	}

	var name, version string
	if err := rows.Scan(&name, &version); err != nil {
		return "", fmt.Errorf("scan sealed bundle: %w", err)
	}

	return name + " " + version, nil
}

// copyProperties gives each copy the artifact properties of its source row:
// properties hang on the id of the package file (docker: the tag) they were set
// on, so the new row gets its own set under its own id.
func copyProperties(ctx context.Context, tx Tx, p CopyParams, copies []RowCopy) error {
	for _, c := range copies {
		if _, err := tx.ExecContext(ctx, copyPropertyRefSQL, p.Target.ID, p.Source.ID, c.Dst, c.Src); err != nil {
			return fmt.Errorf("copy artifact reference of %s: %w", c.Src, err)
		}

		if _, err := tx.ExecContext(ctx, copyPropertiesSQL, p.Target.ID, p.Source.ID, c.Dst, c.Src); err != nil {
			return fmt.Errorf("copy artifact properties of %s: %w", c.Src, err)
		}
	}

	return nil
}

// $1 target repository, $2 source repository, $3 target artifact id, $4 source artifact id.
const (
	copyPropertyRefSQL = `
INSERT INTO artifact_refs (repository_id, artifact_path, format, sha256)
SELECT $1::uuid, $3::text, sr.format, sr.sha256
  FROM artifact_refs sr
 WHERE sr.repository_id = $2::uuid AND sr.artifact_path = $4::text
   AND EXISTS (SELECT 1 FROM artifact_properties ap WHERE ap.artifact_ref_id = sr.id)
ON CONFLICT (repository_id, artifact_path) DO NOTHING`

	copyPropertiesSQL = `
INSERT INTO artifact_properties (repository_id, artifact_ref_id, key, value)
SELECT $1::uuid, dr.id, ap.key, ap.value
  FROM artifact_refs sr
  JOIN artifact_properties ap ON ap.artifact_ref_id = sr.id AND ap.repository_id = $2::uuid
  JOIN artifact_refs dr ON dr.repository_id = $1::uuid AND dr.artifact_path = $3::text
 WHERE sr.repository_id = $2::uuid AND sr.artifact_path = $4::text
   AND NOT EXISTS (SELECT 1 FROM artifact_properties x WHERE x.artifact_ref_id = dr.id AND x.key = ap.key)`
)

// precheck is the dry run's advisory look at the target's quota.
func (e *Engine) precheck(ctx context.Context, repoID string, u Usage) error {
	if u.Bytes == 0 && u.Artifacts == 0 {
		return nil
	}

	if err := quota.Precheck(ctx, e.db, repoID, u.Bytes); err != nil {
		return err //nolint:wrapcheck // quota.ErrExceeded is matched by the caller
	}

	return nil
}
