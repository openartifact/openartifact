// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/format/rubygems"
	"github.com/openartifact/openartifact/internal/license"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// licenseReadTimeout bounds reading the licenses out of one stored artifact.
const licenseReadTimeout = 2 * time.Minute

// nonNilStrings returns s, or an empty slice for nil, so JSON renders [].
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}

// artifactLicenseSource decides which licenses an artifact is evaluated with.
// Properties are what an admin or an importer recorded; the package file is
// what the artifact says about itself, and is read from the blob store so an
// upload that carried no (or false) license fields is still judged on its
// contents.
type artifactLicenseSource struct {
	db       database.Database
	blobs    storage.BlobStore
	log      *logger.Logger
	detector *license.Detector
}

// licenses returns the licenses to evaluate for artifactID: the ones its stored
// package file declares, then the ones of its license properties. Properties
// can add a license; they cannot hide the file's own. An artifact id that does
// not resolve to a stored package file (or whose format has no reader) is
// judged on its properties alone. licensemeta.ErrUnsafe is returned for a file
// that cannot be read safely; any other failure reading the file is logged and
// leaves the properties as the only source.
func (s artifactLicenseSource) licenses(ctx context.Context, repoID, artifactID string) ([]string, error) {
	metadata, err := storedArtifactMetadata(ctx, s.db, repoID, artifactID)
	if err != nil {
		return nil, err
	}

	fromFile, err := s.fromFile(ctx, repoID, artifactID)
	if err != nil {
		return nil, err
	}

	return append(fromFile, s.detector.DetectFromMetadata(metadata)...), nil
}

func (s artifactLicenseSource) fromFile(ctx context.Context, repoID, artifactID string) ([]string, error) {
	if s.blobs == nil {
		return nil, nil
	}

	blob, err := s.db.GetRepoComponentBlob(ctx, repoID, artifactID)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact: %w", err)
	}

	if blob == nil || blob.SHA256 == "" || !(licensemeta.Supported(blob.Format, blob.Filename) || blob.Format == "rubygems") {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, licenseReadTimeout)
	defer cancel()

	open := func() (io.ReadCloser, error) { return s.blobs.Get(ctx, blob.SHA256) }

	var licenses []string

	if blob.Format == "rubygems" {
		licenses, err = readGemLicenses(ctx, open)
	} else {
		var res licensemeta.Result

		res, err = licensemeta.Read(ctx, blob.Format, blob.Filename, open)
		licenses = res.Licenses
	}

	switch {
	case err == nil:
		return licenses, nil
	case licensemeta.Unsafe(err):
		return nil, err
	default:
		s.log.Warnw("license scan: artifact file unreadable, using properties only",
			"repository_id", repoID, "artifact_id", artifactID, "format", blob.Format, "error", err)

		return nil, nil
	}
}

func readGemLicenses(ctx context.Context, open func() (io.ReadCloser, error)) ([]string, error) {
	rc, err := open()
	if err != nil {
		return nil, fmt.Errorf("open gem: %w", err)
	}

	defer func() { _ = rc.Close() }()

	licenses, err := rubygems.ParseLicenses(ctx, rc)
	if err != nil {
		return nil, fmt.Errorf("read gem: %w", err)
	}

	return licenses, nil
}
