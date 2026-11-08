// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package imagesbom

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/ociref"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// annotationBundlePredicate is set by cosign and OpenArtifact on a Sigstore
	// bundle referrer to name the predicate type it carries.
	annotationBundlePredicate = "dev.sigstore.bundle.predicateType"
	// annotationAttestationKind marks the attestations OpenArtifact generated
	// and attached itself; they describe what the server already knows.
	annotationAttestationKind = "dev.openartifact.attestation.kind"
	// layerPredicateAnnotation is set by cosign on the layer of a ".att" image.
	layerPredicateAnnotation = "predicateType"

	bundleArtifactPrefix = "application/vnd.dev.sigstore.bundle"
	dsseLayerType        = "application/vnd.dsse.envelope.v1+json"

	// maxManifestLayers bounds the layers one ingest looks at.
	maxManifestLayers = 16

	// ingestTimeout bounds the work one pushed manifest triggers.
	ingestTimeout = 2 * time.Minute

	// originTag and originReferrer name, in the log, where an SBOM came from.
	originTag      = "cosign tag"
	originReferrer = "referrer"
)

// Push is a manifest that was just stored in a docker repository.
type Push struct {
	RepoID   string
	RepoName string
	// Image is the image path inside the repository.
	Image string
	// Tag is the tag the manifest was pushed under, "" for a push by digest.
	Tag string
	// ArtifactType and Annotations are the manifest's own.
	ArtifactType string
	Annotations  map[string]string
	// SubjectDigest is the manifest's subject ("sha256:<hex>"), "" for none.
	SubjectDigest string
	// Manifest is the raw manifest.
	Manifest []byte
}

// ReportStore is the part of the database the ingest writes to.
type ReportStore interface {
	CreateSBOMReport(ctx context.Context, report *models.SBOMReport) error
}

// Ingestor turns the SBOMs pushed beside an image into SBOM reports.
type Ingestor struct {
	db     ReportStore
	locker any
	blobs  storage.BlobStore
	log    *logger.Logger
	// onIngested, when set, is told about every image that gained SBOM
	// content (the vulnerability scan of the image starts from it).
	onIngested func(ctx context.Context, repoID, digest string)
}

// NewIngestor builds the ingestor. locker is the database handle that serialises
// blob writes (the one storage.CommitBlob takes).
func NewIngestor(db ReportStore, locker any, blobs storage.BlobStore, log *logger.Logger) *Ingestor {
	return &Ingestor{db: db, locker: locker, blobs: blobs, log: log}
}

// OnIngested sets the function called after an image gained SBOM content.
func (i *Ingestor) OnIngested(f func(ctx context.Context, repoID, digest string)) *Ingestor {
	i.onIngested = f
	return i
}

// ArtifactID is the id an image's SBOM reports and scans carry: its manifest
// digest, the key the registry's quarantine check also looks up.
func ArtifactID(digest string) string { return digest }

// manifestDoc is the part of an image or artifact manifest the ingest reads.
type manifestDoc struct {
	Layers []layerDesc `json:"layers"`
	// Blobs is the layer list of an OCI 1.1 artifact manifest.
	Blobs []layerDesc `json:"blobs"`
}

// layerDesc: Go matches JSON keys case-insensitively, so the lower-case tag
// below reads the camelCase key of the OCI image spec.
type layerDesc struct {
	MediaType   string            `json:"mediatype"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations"`
}

// Classify reports whether the pushed manifest may carry an SBOM and, if so, of
// which image ("sha256:<hex>"): a cosign ".sbom" or ".att" tag, or a referrer
// whose artifact type names an SBOM or a Sigstore bundle of an SBOM predicate.
// It returns the subject digest, the origin (tag or referrer) and ok.
func Classify(p Push) (string, string, bool) {
	if subj, kind, isCosign := ociref.ParseCosignTag(p.Tag); isCosign && (kind == ociref.KindSBOM || kind == ociref.KindAttestation) {
		return subj, originTag, true
	}

	if p.SubjectDigest == "" || p.Annotations[annotationAttestationKind] != "" {
		return "", "", false
	}

	switch {
	case IsSBOMArtifactType(p.ArtifactType):
		return p.SubjectDigest, originReferrer, true
	case strings.HasPrefix(p.ArtifactType, bundleArtifactPrefix) && IsSBOMPredicate(p.Annotations[annotationBundlePredicate]):
		return p.SubjectDigest, originReferrer, true
	}

	return "", "", false
}

// Handle ingests the SBOMs of a pushed manifest. A manifest that carries none
// is ignored; a failure never fails the push, the caller logs the error.
func (i *Ingestor) Handle(ctx context.Context, p Push) error {
	subject, origin, ok := Classify(p)
	if !ok {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ingestTimeout)
	defer cancel()

	var m manifestDoc
	if err := json.Unmarshal(p.Manifest, &m); err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	layers := append(m.Layers, m.Blobs...)
	if len(layers) > maxManifestLayers {
		layers = layers[:maxManifestLayers]
	}

	stored := 0

	var errs []error

	for _, l := range layers {
		doc, err := i.readLayer(ctx, l)
		if errors.Is(err, ErrNotSBOM) {
			continue
		}

		if err != nil {
			errs = append(errs, fmt.Errorf("layer %s: %w", l.Digest, err))
			continue
		}

		if err := i.store(ctx, p.RepoID, subject, doc); err != nil {
			errs = append(errs, err)
			continue
		}

		stored++

		i.log.Infow("container SBOM ingested", "repository", p.RepoName, "image", p.Image, "subject", subject,
			"origin", origin, "format", doc.Format, "components", len(doc.Components))
	}

	if stored > 0 && i.onIngested != nil {
		i.onIngested(ctx, p.RepoID, subject)
	}

	return errors.Join(errs...)
}

// readLayer reads and decodes one layer, or returns ErrNotSBOM for a layer that
// cannot be one.
func (i *Ingestor) readLayer(ctx context.Context, l layerDesc) (*Document, error) {
	if !mayBeSBOM(l) {
		return nil, ErrNotSBOM
	}

	if l.Size > MaxDocumentBytes {
		return nil, ErrTooLarge
	}

	hexDigest, ok := strings.CutPrefix(l.Digest, "sha256:")
	if !ok {
		return nil, ErrNotSBOM
	}

	rc, err := i.blobs.Get(ctx, hexDigest)
	if err != nil {
		return nil, fmt.Errorf("read layer: %w", err)
	}

	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(io.LimitReader(rc, MaxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read layer: %w", err)
	}

	return Decode(data)
}

// mayBeSBOM reports whether a layer is worth reading: an SBOM media type, a
// DSSE envelope (of an SBOM predicate when the layer says so) or a layer of
// unknown type.
func mayBeSBOM(l layerDesc) bool {
	if pt := l.Annotations[layerPredicateAnnotation]; pt != "" && !IsSBOMPredicate(pt) {
		return false
	}

	base, _, _ := strings.Cut(strings.ToLower(l.MediaType), ";")

	return IsSBOMArtifactType(base) || base == dsseLayerType || strings.HasPrefix(base, bundleArtifactPrefix) ||
		base == "application/json" || base == "text/plain"
}

// store writes one decoded SBOM as the report of the image.
func (i *Ingestor) store(ctx context.Context, repoID, subject string, doc *Document) error {
	sum := sha256.Sum256(doc.Body)
	sha := hex.EncodeToString(sum[:])
	now := time.Now()

	report := &models.SBOMReport{
		RepositoryID:   repoID,
		ArtifactID:     ArtifactID(subject),
		Format:         doc.Format,
		SpecVersion:    doc.SpecVersion,
		ContentType:    doc.ContentType,
		BlobSHA256:     sha,
		BlobSizeBytes:  int64(len(doc.Body)),
		ComponentCount: len(doc.Components),
		GeneratedAt:    now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	blob := storage.BytesBlob(sha, doc.Body, map[string]string{
		"sha256": sha, "content-type": doc.ContentType, "format": "sbom",
	})

	err := storage.CommitBlob(ctx, i.locker, i.blobs, blob, func(ctx context.Context, _ string) error {
		return i.db.CreateSBOMReport(ctx, report)
	})
	if err != nil {
		return fmt.Errorf("store SBOM report: %w", err)
	}

	return nil
}
