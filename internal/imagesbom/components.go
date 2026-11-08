// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package imagesbom

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/storage"
)

// ErrNoSBOM is returned for an image that has no SBOM report.
var ErrNoSBOM = errors.New("imagesbom: the image has no SBOM")

// ReportLister reads the SBOM reports of one artifact.
type ReportLister interface {
	GetSBOMReportsByArtifact(ctx context.Context, repoID, artifactID string) ([]*models.SBOMReport, error)
}

// Reader returns the packages of an image from its stored SBOM reports.
type Reader struct {
	db    ReportLister
	blobs storage.BlobStore
}

// NewReader builds the reader.
func NewReader(db ReportLister, blobs storage.BlobStore) *Reader {
	return &Reader{db: db, blobs: blobs}
}

// HasSBOM reports whether the image (named by its manifest digest) has a stored
// SBOM report.
func (r *Reader) HasSBOM(ctx context.Context, repoID, digest string) (bool, error) {
	reports, err := r.db.GetSBOMReportsByArtifact(ctx, repoID, ArtifactID(digest))
	if err != nil {
		return false, fmt.Errorf("list SBOM reports: %w", err)
	}

	return slices.ContainsFunc(reports, func(rep *models.SBOMReport) bool { return rep.BlobSHA256 != "" }), nil
}

// Components returns the union of the packages every SBOM report of the image
// (named by its manifest digest) lists, without duplicates, in document order.
// It returns ErrNoSBOM when the image has no report.
func (r *Reader) Components(ctx context.Context, repoID, digest string) ([]sbom.Component, error) {
	reports, err := r.db.GetSBOMReportsByArtifact(ctx, repoID, ArtifactID(digest))
	if err != nil {
		return nil, fmt.Errorf("list SBOM reports: %w", err)
	}

	var (
		out  []sbom.Component
		seen = map[string]bool{}
		read int
	)

	for _, rep := range reports {
		if rep.BlobSHA256 == "" {
			continue
		}

		comps, err := r.read(ctx, rep)
		if err != nil {
			return nil, err
		}

		read++

		for _, c := range comps {
			key := c.PURL
			if key == "" {
				key = c.Name + "@" + c.Version
			}

			if seen[key] {
				continue
			}

			seen[key] = true

			out = append(out, c)
			if len(out) > MaxComponents {
				return nil, ErrTooLarge
			}
		}
	}

	if read == 0 {
		return nil, ErrNoSBOM
	}

	return out, nil
}

func (r *Reader) read(ctx context.Context, rep *models.SBOMReport) ([]sbom.Component, error) {
	rc, err := r.blobs.Get(ctx, rep.BlobSHA256)
	if err != nil {
		return nil, fmt.Errorf("read SBOM blob: %w", err)
	}

	defer func() { _ = rc.Close() }()

	comps, truncated, err := sbom.ReadComponents(rc, rep.Format, MaxComponents)
	if err != nil {
		return nil, fmt.Errorf("read SBOM components: %w", err)
	}

	if truncated {
		return nil, ErrTooLarge
	}

	return comps, nil
}
