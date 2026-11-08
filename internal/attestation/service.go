// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package attestation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/attest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// maxEnvelopeBytes bounds how much of one envelope layer is read.
const maxEnvelopeBytes = 4 << 20

// maxManifestLayers bounds how many layers of one manifest are indexed.
const maxManifestLayers = 64

// reverifyPage is the page size of a repository-wide reverification.
const reverifyPage = 200

// Errors of the service.
var (
	// ErrProvenanceRequired is returned by CheckProvenance for an image without
	// a verified SLSA provenance while the repository requires one.
	ErrProvenanceRequired = errors.New("attestation: verified SLSA provenance required")
	// ErrNoStatement is returned when an attestation has no statement to show.
	ErrNoStatement = errors.New("attestation: the envelope carries no statement")

	// ErrInvalidSubject is returned for a subject that is not a sha256 digest.
	ErrInvalidSubject = errors.New("attestation: invalid subject digest")

	errManifestMissing = errors.New("manifest is not stored")
	errBlobMissing     = errors.New("blob is not in the repository")
	errTooLarge        = errors.New("envelope is larger than 4 MiB")
)

// Store is the part of the database the service reads and writes: the index of
// attachments and the trust configuration. A nil trust document means none is set.
type Store interface {
	UpsertOCIAttestation(ctx context.Context, a *models.OCIAttestation) error
	GetOCIAttestationFirstSeen(ctx context.Context, repoID, image, subject, attestationDigest string) (time.Time, bool, error)
	ListOCIAttestations(ctx context.Context, repoID, subject, image string) ([]*models.OCIAttestation, error)
	ListOCIAttestationsByDigest(ctx context.Context, repoID, attestationDigest string) ([]*models.OCIAttestation, error)
	ListOCIAttestationsPage(ctx context.Context, repoID, afterID string, limit int) ([]*models.OCIAttestation, error)
	HasVerifiedOCIAttestation(ctx context.Context, repoID, subject string, predicateTypes []string) (bool, error)
	GetRepositoryAttestationTrust(ctx context.Context, repoID string) (json.RawMessage, error)
	SetRepositoryAttestationTrust(ctx context.Context, repoID string, config json.RawMessage) error
}

// Source is what the service reads from the docker registry tables.
type Source interface {
	GetDockerBlob(ctx context.Context, repoID, digest string) (*models.DockerBlob, error)
	GetDockerManifest(ctx context.Context, repoID, image, digest string) (*models.DockerManifest, error)
	GetDockerTag(ctx context.Context, repoID, image, name string) (*models.DockerTag, error)
	ListDockerManifestsBySubject(ctx context.Context, repoID, image, subjectDigest string) ([]*models.DockerManifest, error)
}

// DB is Store and Source: what *database.PostgresDB provides.
type DB interface {
	Store
	Source
}

// BlobReader reads stored blobs by their blob store key.
type BlobReader interface {
	Get(ctx context.Context, id string) (io.ReadCloser, error)
}

// Service indexes and verifies attestations. It holds no state besides its
// collaborators and may be created once per consumer.
type Service struct {
	log      *logger.Logger
	db       DB
	blobs    BlobReader
	verifier Verifier
	now      func() time.Time
}

// NewService returns a Service reading manifests and layers from db and blobs.
func NewService(log *logger.Logger, db DB, blobs BlobReader) *Service {
	return &Service{log: log, db: db, blobs: blobs, now: time.Now}
}

// trust loads and compiles the repository's trust configuration; nil when none is set.
func (s *Service) trust(ctx context.Context, repoID string) (*Trust, error) {
	raw, err := s.db.GetRepositoryAttestationTrust(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("load attestation trust: %w", err)
	}

	if len(raw) == 0 {
		return nil, nil //nolint:nilnil // no trust configured is a valid state
	}

	var cfg TrustConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("decode attestation trust: %w", err)
	}

	return cfg.Compile()
}

// TrustConfig returns the repository's trust configuration (empty when none is set).
func (s *Service) TrustConfig(ctx context.Context, repoID string) (*TrustConfig, error) {
	raw, err := s.db.GetRepositoryAttestationTrust(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("load attestation trust: %w", err)
	}

	cfg := &TrustConfig{Keys: []string{}}
	if len(raw) == 0 {
		return cfg, nil
	}

	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("decode attestation trust: %w", err)
	}

	if cfg.Keys == nil {
		cfg.Keys = []string{}
	}

	return cfg, nil
}

// SetTrustConfig validates and stores the repository's trust configuration. It
// does not re-verify what is indexed: use Reverify.
func (s *Service) SetTrustConfig(ctx context.Context, repoID string, cfg *TrustConfig) error {
	if _, err := cfg.Compile(); err != nil {
		return err
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode attestation trust: %w", err)
	}

	if err := s.db.SetRepositoryAttestationTrust(ctx, repoID, raw); err != nil {
		return fmt.Errorf("store attestation trust: %w", err)
	}

	return nil
}

// List returns the attestations of a subject digest, optionally of one image only.
func (s *Service) List(ctx context.Context, repoID, subject, image string) ([]*models.OCIAttestation, error) {
	rows, err := s.db.ListOCIAttestations(ctx, repoID, subject, image)
	if err != nil {
		return nil, fmt.Errorf("list attestations: %w", err)
	}

	return rows, nil
}

// Lookup returns the rows of an attestation digest (one per image that holds it).
func (s *Service) Lookup(ctx context.Context, repoID, attestationDigest string) ([]*models.OCIAttestation, error) {
	rows, err := s.db.ListOCIAttestationsByDigest(ctx, repoID, attestationDigest)
	if err != nil {
		return nil, fmt.Errorf("look up attestation: %w", err)
	}

	return rows, nil
}

// Statement decodes the statement of an indexed attachment without verifying
// it. An unsigned SBOM or a cosign signature payload is wrapped into a statement
// whose predicate is the document.
func (s *Service) Statement(ctx context.Context, row *models.OCIAttestation) (*attest.Statement, error) {
	blob, err := s.readStored(ctx, row.RepositoryID, row.AttestationDigest)
	if err != nil {
		return nil, err
	}

	if row.Kind != models.AttestationKindAttestation {
		return wrapDocument(row, blob)
	}

	dec := decodeAttestationBlob(blob, "", nil)
	if dec.st == nil {
		return nil, ErrNoStatement
	}

	return dec.st, nil
}

// wrapDocument presents a raw SBOM or signature payload as the predicate of a
// statement about the row's subject.
func wrapDocument(row *models.OCIAttestation, blob []byte) (*attest.Statement, error) {
	if !json.Valid(blob) {
		return nil, ErrNoStatement
	}

	algo, hexDigest, _ := strings.Cut(row.SubjectDigest, ":")

	return &attest.Statement{
		Type:          attest.StatementType,
		Subject:       []attest.Subject{{Digest: map[string]string{algo: hexDigest}}},
		PredicateType: row.PredicateType,
		Predicate:     blob,
	}, nil
}

// readStored reads an envelope layer through its blob row.
func (s *Service) readStored(ctx context.Context, repoID, digest string) ([]byte, error) {
	b, err := s.db.GetDockerBlob(ctx, repoID, digest)
	if err != nil {
		return nil, fmt.Errorf("look up blob %s: %w", digest, err)
	}

	if b == nil {
		return nil, fmt.Errorf("%w: %s", errBlobMissing, digest)
	}

	return s.readBlobKey(ctx, b.BlobSHA256, b.Size)
}

func (s *Service) readBlobKey(ctx context.Context, key string, size int64) ([]byte, error) {
	if size > maxEnvelopeBytes {
		return nil, errTooLarge
	}

	rc, err := s.blobs.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", key, err)
	}

	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(io.LimitReader(rc, maxEnvelopeBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", key, err)
	}

	if len(data) > maxEnvelopeBytes {
		return nil, errTooLarge
	}

	return data, nil
}

// CheckProvenance enforces require_verified_provenance on the promotion of the
// image with manifest digest from srcRepoID into dstRepoID: when either
// repository's trust configuration requires it, a verified SLSA provenance of
// the image must be indexed in the source repository.
func (s *Service) CheckProvenance(ctx context.Context, srcRepoID, dstRepoID, digest string) error {
	required := false

	for _, id := range []string{srcRepoID, dstRepoID} {
		cfg, err := s.TrustConfig(ctx, id)
		if err != nil {
			return err
		}

		required = required || cfg.RequireVerifiedProvenance
	}

	if !required {
		return nil
	}

	ok, err := s.db.HasVerifiedOCIAttestation(ctx, srcRepoID, digest, []string{PredicateSLSAv02, PredicateSLSAv1})
	if err != nil {
		return fmt.Errorf("check provenance: %w", err)
	}

	if !ok {
		return ErrProvenanceRequired
	}

	return nil
}
