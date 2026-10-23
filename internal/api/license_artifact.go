// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/rubygems"
	"github.com/openartifact/openartifact/internal/license"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/licensepolicy"
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

// errUnderivable means a stored package's licenses cannot be read from its
// file and a blocking license policy forbids judging it on what clients
// declared.
var errUnderivable = errors.New("licenses cannot be derived from the artifact")

// resolvedLicenses are the licenses an artifact is evaluated with and where
// they come from.
type resolvedLicenses struct {
	Licenses []string
	// Source is models.LicenseSourceArtifact when the stored package file
	// declared licenses, models.LicenseSourceClientDeclared otherwise.
	Source string
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
// not resolve to a stored package file (or is not a kind of package that
// declares licenses) is judged on its properties alone, as client-declared.
//
// Errors: licensemeta.ErrUnsafe for a file that cannot be read safely;
// errUnderivable for a package whose file cannot be read while a blocking
// policy applies. Any other failure reading the file, with no blocking policy,
// is logged and leaves the properties as the only (client-declared) source.
func (s artifactLicenseSource) licenses(ctx context.Context, repoID, artifactID string) (resolvedLicenses, error) {
	metadata, err := storedArtifactMetadata(ctx, s.db, repoID, artifactID)
	if err != nil {
		return resolvedLicenses{}, err
	}

	fromFile, err := s.fromFile(ctx, repoID, artifactID)
	if err != nil {
		return resolvedLicenses{}, err
	}

	source := models.LicenseSourceClientDeclared
	if len(fromFile) > 0 {
		source = models.LicenseSourceArtifact
	}

	return resolvedLicenses{
		Licenses: append(fromFile, s.detector.DetectFromMetadata(metadata)...),
		Source:   source,
	}, nil
}

func (s artifactLicenseSource) fromFile(ctx context.Context, repoID, artifactID string) ([]string, error) {
	if s.blobs == nil {
		return nil, nil
	}

	blob, err := s.db.GetRepoComponentBlob(ctx, repoID, artifactID)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact: %w", err)
	}

	if blob == nil || blob.SHA256 == "" || !licensemeta.Expected(blob.Format, blob.Filename) {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, licenseReadTimeout)
	defer cancel()

	licenses, err := s.read(ctx, repoID, blob)

	switch {
	case err == nil:
		return licenses, nil
	case licensemeta.Unsafe(err):
		return nil, err
	}

	blocking, polErr := licensepolicy.HasBlocking(ctx, s.db, repoID)
	if polErr != nil {
		return nil, fmt.Errorf("read license policies: %w", polErr)
	}

	if blocking {
		s.log.Warnw("license scan: artifact file unreadable under a blocking policy",
			"repository_id", repoID, "artifact_id", artifactID, "format", blob.Format, "error", err)

		return nil, errUnderivable
	}

	s.log.Warnw("license scan: artifact file unreadable, using properties only",
		"repository_id", repoID, "artifact_id", artifactID, "format", blob.Format, "error", err)

	return nil, nil
}

// read returns the licenses the stored file declares (for a Maven pom with none,
// the ones inherited from its parent pom in the repository); licensemeta.ErrUnsupported
// for a file kind that cannot be read.
func (s artifactLicenseSource) read(ctx context.Context, repoID string, blob *models.ComponentBlob) ([]string, error) {
	open := func() (io.ReadCloser, error) { return s.blobs.Get(ctx, blob.SHA256) }

	if blob.Format == "rubygems" {
		return readGemLicenses(ctx, open)
	}

	res, err := licensemeta.Read(ctx, blob.Format, blob.Filename, open)
	if err != nil {
		return nil, err //nolint:wrapcheck // callers test the licensemeta sentinels
	}

	if res.Parent == nil {
		return res.Licenses, nil
	}

	// A pom that declares no license takes its parent's. An unresolved chain
	// (parent not stored yet) leaves the pom with no license until the parent
	// is uploaded and the pom is evaluated again.
	inherited, _, err := inheritedPOMLicenses(ctx, s.db, s.blobs, repoID, res.Parent)
	if err != nil {
		return nil, err
	}

	return inherited, nil
}

// writeLicenseSourceError answers 422 for the errors licenses returns that mean
// "refuse" and reports whether it did. prefix leads the message (for example
// "properties saved, but ").
func writeLicenseSourceError(w http.ResponseWriter, err error, prefix string) bool {
	switch {
	case licensemeta.Unsafe(err):
		writeError(w, http.StatusUnprocessableEntity, "unsafe_artifact",
			prefix+"the artifact's metadata is too large or ambiguous to be read safely")
	case errors.Is(err, errUnderivable):
		writeError(w, http.StatusUnprocessableEntity, "unreadable_artifact",
			prefix+"the repository has a blocking license policy and the artifact's license cannot be read from its file")
	default:
		return false
	}

	return true
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
