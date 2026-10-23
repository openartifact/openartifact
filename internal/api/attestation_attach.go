// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/attest"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/docker"
	"github.com/openartifact/openartifact/internal/ocipath"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Annotations OpenArtifact sets on an attached attestation. The first two are the
// ones cosign sets on its bundle referrers.
const (
	annotationBundleContent   = "dev.sigstore.bundle.content"
	annotationBundlePredicate = "dev.sigstore.bundle.predicateType"
	annotationKind            = "dev.openartifact.attestation.kind"
	annotationDocumentSHA256  = "dev.openartifact.attestation.document-sha256"

	// onScanTimeout bounds the work a finished scan triggers.
	onScanTimeout = 2 * time.Minute

	attachKindSBOM = "sbom"
	attachKindVEX  = "vex"
)

var (
	errAttachNotDocker = errors.New("referrer attachment needs a hosted docker repository")
	errAttachNoSubject = errors.New("no image manifest for the artifact")
)

// ReferrerRegistry is the docker registry's way of storing a referrer.
type ReferrerRegistry interface {
	AttachReferrer(ctx context.Context, repo *models.Repository, req docker.ReferrerRequest) (*docker.ReferrerResult, error)
	FindReferrer(ctx context.Context, repo *models.Repository, image, subjectDigest, artifactType string, annotations map[string]string) (string, error)
}

// AttestationAttacher signs SBOMs and VEX documents and attaches them to the
// image they describe as OCI referrers, so `cosign verify-attestation`,
// `cosign tree` and `oras discover` find them next to the image.
type AttestationAttacher struct {
	db  database.Database
	log *logger.Logger
	gen *sbom.Generator
	svc *attest.Service
	reg ReferrerRegistry
}

// NewAttestationAttacher builds the attacher.
func NewAttestationAttacher(db database.Database, log *logger.Logger, gen *sbom.Generator, svc *attest.Service, reg ReferrerRegistry) *AttestationAttacher {
	return &AttestationAttacher{db: db, log: log, gen: gen, svc: svc, reg: reg}
}

// Enabled reports whether the repository attaches attestations automatically.
func (a *AttestationAttacher) Enabled(ctx context.Context, repo *models.Repository) bool {
	if repo.Format != "docker" || repo.RepoType != "hosted" {
		return false
	}

	on, err := a.db.GetRepositoryAttestationAttach(ctx, repo.ID)
	if err != nil {
		a.log.Warnw("attestation attach: reading the repository setting failed", "repository", repo.Name, "error", err)
		return false
	}

	return on
}

// attachSubject is the image an artifact id names.
type attachSubject struct {
	image  string // image path inside the repository
	digest string // manifest digest
	name   string // the name the attestation gives the image (its purl)
}

// subject resolves the image an artifact id names.
func (a *AttestationAttacher) subject(ctx context.Context, repo *models.Repository, artifactID string) (attachSubject, error) {
	if repo.Format != "docker" || repo.RepoType != "hosted" {
		return attachSubject{}, errAttachNotDocker
	}

	id, err := a.gen.Identity(ctx, repo.ID, repo.Format, artifactID)
	if err != nil {
		return attachSubject{}, fmt.Errorf("resolve artifact: %w", err)
	}

	repoName, image := ocipath.SplitName(id.Name)
	if uuid.Validate(artifactID) != nil && repoName != repo.Name {
		return attachSubject{}, errAttachNoSubject
	}

	out := attachSubject{image: image, name: id.PURL}
	if out.name == "" {
		out.name = artifactID
	}

	_, version := splitDockerArtifact(artifactID)

	switch {
	case id.SHA256 != "":
		out.digest = "sha256:" + id.SHA256
	case strings.HasPrefix(version, "sha256:"):
		out.digest = version
	default:
		tag, tagErr := a.db.GetDockerTag(ctx, repo.ID, image, version)
		if tagErr != nil {
			return attachSubject{}, fmt.Errorf("resolve tag: %w", tagErr)
		}

		if tag == nil {
			return attachSubject{}, errAttachNoSubject
		}

		out.digest = tag.ManifestDigest
	}

	return out, nil
}

func splitDockerArtifact(artifactID string) (string, string) {
	if i := strings.LastIndex(artifactID, "@"); i > 0 {
		return artifactID[:i], artifactID[i+1:]
	}

	return artifactID, ""
}

// attach signs doc as an attestation about the artifact's image and attaches it.
func (a *AttestationAttacher) attach(ctx context.Context, repo *models.Repository, artifactID, kind, predicateType string,
	doc []byte, extra ...attest.Subject,
) (*responses.AttestationAttachResponse, error) {
	if a.svc == nil || a.reg == nil {
		return nil, errAttachNotDocker
	}

	subj, err := a.subject(ctx, repo, artifactID)
	if err != nil {
		return nil, err
	}

	image, digest, name := subj.image, subj.digest, subj.name
	docSum := sha256.Sum256(doc)
	annotations := map[string]string{
		annotationBundleContent:   "dsse-envelope",
		annotationBundlePredicate: predicateType,
		annotationKind:            kind,
		annotationDocumentSHA256:  hex.EncodeToString(docSum[:]),
	}
	out := &responses.AttestationAttachResponse{
		ArtifactID: artifactID, Kind: kind, PredicateType: predicateType, Subject: digest, ArtifactType: attest.BundleContentType,
	}

	// The same document is already attached: signing again would only add a new
	// (randomized) signature to the registry.
	found, err := a.reg.FindReferrer(ctx, repo, image, digest, attest.BundleContentType, annotations)
	if err != nil {
		return nil, fmt.Errorf("look up attached attestation: %w", err)
	}

	if found != "" {
		out.ReferrerDigest = found
		return out, nil
	}

	signer, err := a.svc.Signer(ctx)
	if err != nil {
		return nil, fmt.Errorf("signing key: %w", err)
	}

	subjects := append([]attest.Subject{attest.SHA256Subject(name, strings.TrimPrefix(digest, "sha256:"))}, extra...)

	env, err := signer.Attest(predicateType, doc, subjects)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}

	bundle, _, err := signer.Encode(env, true)
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}

	res, err := a.reg.AttachReferrer(ctx, repo, docker.ReferrerRequest{
		Image: image, SubjectDigest: digest, ArtifactType: attest.BundleContentType, Content: bundle,
		Annotations: annotations, SlotKeys: []string{annotationKind, annotationBundlePredicate},
	})
	if err != nil {
		return nil, fmt.Errorf("attach: %w", err)
	}

	out.ReferrerDigest, out.Created, out.Replaced = res.Digest, res.Created, res.Replaced

	return out, nil
}

// AttachSBOM attaches the stored SBOM report's document (JSON formats only).
func (a *AttestationAttacher) AttachSBOM(ctx context.Context, repo *models.Repository, report *models.SBOMReport, doc []byte) (*responses.AttestationAttachResponse, error) {
	predicate := attestPredicate(report.Format)
	if predicate == "" {
		return nil, errAttachNotJSON
	}

	return a.attach(ctx, repo, report.ArtifactID, attachKindSBOM, predicate, doc,
		attest.SHA256Subject("sbom."+sbomFileExtension(report.Format), report.BlobSHA256))
}

var errAttachNotJSON = errors.New("only JSON SBOMs can be attested")

// AttachVEX builds the VEX document of one artifact and attaches it.
func (a *AttestationAttacher) AttachVEX(ctx context.Context, repo *models.Repository, artifactID, format string) (*responses.AttestationAttachResponse, error) {
	metadata, err := storedArtifactMetadata(ctx, a.db, repo.ID, artifactID)
	if err != nil {
		return nil, fmt.Errorf("artifact properties: %w", err)
	}

	in, err := a.gen.VEXInput(ctx, repo.ID, repo.Format, artifactID, metadata)
	if err != nil {
		return nil, fmt.Errorf("vex input: %w", err)
	}

	var (
		result    *sbom.SBOMResult
		predicate = attest.PredicateOpenVEX
	)

	if format == sbom.VEXCycloneDX {
		predicate = attest.PredicateCycloneDX
		result, err = a.gen.GenerateCycloneDXVEX([]*sbom.Input{in}, "")
	} else {
		result, err = a.gen.GenerateOpenVEX([]*sbom.Input{in})
	}

	if err != nil {
		return nil, fmt.Errorf("generate VEX: %w", err)
	}

	return a.attach(ctx, repo, artifactID, attachKindVEX, predicate, result.Body)
}

// OnSBOMGenerated attaches a freshly generated SBOM when the repository opted in.
func (a *AttestationAttacher) OnSBOMGenerated(ctx context.Context, repo *models.Repository, report *models.SBOMReport, doc []byte) {
	if !a.Enabled(ctx, repo) || attestPredicate(report.Format) == "" {
		return
	}

	if _, err := a.AttachSBOM(ctx, repo, report, doc); err != nil {
		a.log.Warnw("attestation attach: SBOM not attached", "repository", repo.Name, "artifact", report.ArtifactID, "error", err)
	}
}

// OnScan attaches a CycloneDX SBOM (with the scan's findings) and the OpenVEX
// document of the scanned artifact when the repository opted in.
func (a *AttestationAttacher) OnScan(ctx context.Context, repoID, artifactID string) {
	ctx, cancel := context.WithTimeout(ctx, onScanTimeout)
	defer cancel()

	repo, err := a.db.GetRepositoryByID(ctx, repoID)
	if err != nil || repo == nil || !a.Enabled(ctx, repo) {
		return
	}

	metadata, err := storedArtifactMetadata(ctx, a.db, repo.ID, artifactID)
	if err != nil {
		a.log.Warnw("attestation attach: artifact properties unavailable", "repository", repo.Name, "artifact", artifactID, "error", err)
		return
	}

	result, err := a.gen.GenerateCycloneDX(ctx, repo.ID, artifactID, repo.Format, metadata, "")
	if err != nil {
		a.log.Warnw("attestation attach: SBOM generation failed", "repository", repo.Name, "artifact", artifactID, "error", err)
	} else if _, err := a.attach(ctx, repo, artifactID, attachKindSBOM, attest.PredicateCycloneDX, result.Body); err != nil {
		a.log.Warnw("attestation attach: SBOM not attached", "repository", repo.Name, "artifact", artifactID, "error", err)
	}

	if _, err := a.AttachVEX(ctx, repo, artifactID, sbom.VEXOpenVEX); err != nil {
		a.log.Warnw("attestation attach: VEX not attached", "repository", repo.Name, "artifact", artifactID, "error", err)
	}
}

// ---- HTTP ---------------------------------------------------------------

// AttestationAttachHandler serves the attach endpoints and the per-repository switch.
type AttestationAttachHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	attacher  *AttestationAttacher
	blobs     storage.BlobStore
}

// NewAttestationAttachHandler builds the handler.
func NewAttestationAttachHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator, attacher *AttestationAttacher, blobs storage.BlobStore) *AttestationAttachHandler {
	return &AttestationAttachHandler{db: db, log: log, evaluator: evaluator, attacher: attacher, blobs: blobs}
}

// RegisterAuthed registers the admin routes.
func (h *AttestationAttachHandler) RegisterAuthed(r chi.Router) {
	const repo = "/api/v1/repositories/{name}"

	r.Get(repo+"/attestation-attach", h.handleGetSetting)
	r.Put(repo+"/attestation-attach", h.handlePutSetting)
	r.Post(repo+"/sbom/{id}/attach", h.handleAttachSBOM)
	r.Post(repo+"/vex/attach", h.handleAttachVEX)
}

func (h *AttestationAttachHandler) adminRepo(w http.ResponseWriter, r *http.Request, op string) (*models.Repository, bool) {
	if !requireAdmin(w, r, h.evaluator) {
		return nil, false
	}

	repo, err := h.db.GetRepositoryByName(r.Context(), urlparams.Vars(r)["name"])
	if err != nil {
		h.log.Errorw("attestation attach "+op+": db error getting repo", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return nil, false
	}

	if repo == nil {
		writeError(w, http.StatusNotFound, "not_found", "repository not found")
		return nil, false
	}

	return repo, true
}

func (h *AttestationAttachHandler) handleGetSetting(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "get")
	if !ok {
		return
	}

	on, err := h.db.GetRepositoryAttestationAttach(r.Context(), repo.ID)
	if err != nil {
		h.log.Errorw("attestation attach get: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, responses.AttestationAttachSettingsResponse{Enabled: on})
}

func (h *AttestationAttachHandler) handlePutSetting(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "put")
	if !ok {
		return
	}

	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if body.Enabled && (repo.Format != "docker" || repo.RepoType != "hosted") {
		writeError(w, http.StatusBadRequest, "validation_failed", "attaching attestations needs a hosted docker repository")
		return
	}

	if h.attacher == nil || h.attacher.svc == nil {
		writeError(w, http.StatusNotImplemented, "not_implemented", "attestations are not enabled")
		return
	}

	if err := h.db.SetRepositoryAttestationAttach(r.Context(), repo.ID, body.Enabled); err != nil {
		h.log.Errorw("attestation attach put: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	writeJSON(w, http.StatusOK, responses.AttestationAttachSettingsResponse{Enabled: body.Enabled})
}

// attachError maps an attach failure to the response.
func (h *AttestationAttachHandler) attachError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errAttachNotDocker):
		writeError(w, http.StatusBadRequest, "validation_failed", "attaching attestations needs a hosted docker repository")
	case errors.Is(err, errAttachNotJSON):
		writeError(w, http.StatusBadRequest, "validation_failed", "only JSON SBOMs can be attested; generate cyclonedx, spdx-json or spdx3-json")
	case errors.Is(err, errAttachNoSubject), errors.Is(err, docker.ErrSubjectNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no image in the repository matches the artifact")
	case errors.Is(err, quota.ErrExceeded):
		writeError(w, http.StatusInsufficientStorage, "quota_exceeded", quota.Message)
	default:
		h.log.Errorw("attestation attach failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to attach the attestation")
	}
}

func (h *AttestationAttachHandler) handleAttachSBOM(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "sbom")
	if !ok {
		return
	}

	ctx := r.Context()

	id := urlparams.Vars(r)["id"]
	if !isUUIDString(id) {
		writeError(w, http.StatusNotFound, "not_found", "SBOM report not found")
		return
	}

	report, err := h.db.GetSBOMReportByID(ctx, repo.ID, id)
	if err != nil {
		h.log.Errorw("attestation attach sbom: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	if report == nil {
		writeError(w, http.StatusNotFound, "not_found", "SBOM report not found")
		return
	}

	rc, err := h.blobs.Get(ctx, report.BlobSHA256)
	if err != nil {
		h.log.Errorw("attestation attach sbom: blob fetch failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to read SBOM content")

		return
	}
	defer func() { _ = rc.Close() }()

	doc, err := readLimited(rc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to read SBOM content")
		return
	}

	out, err := h.attacher.AttachSBOM(ctx, repo, report, doc)
	if err != nil {
		h.attachError(w, err)
		return
	}

	writeJSON(w, attachStatus(out), out)
}

func (h *AttestationAttachHandler) handleAttachVEX(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.adminRepo(w, r, "vex")
	if !ok {
		return
	}

	var body struct {
		ArtifactID string `json:"artifact_id"`
		Format     string `json:"format"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	if msg := validateArtifactID(body.ArtifactID); msg != "" || body.ArtifactID == "" {
		writeError(w, http.StatusBadRequest, "validation_failed", "artifact_id is required")
		return
	}

	if body.Format == "" {
		body.Format = sbom.VEXOpenVEX
	}

	if body.Format != sbom.VEXOpenVEX && body.Format != sbom.VEXCycloneDX {
		writeError(w, http.StatusBadRequest, "validation_failed", "format must be 'openvex' or 'cyclonedx'")
		return
	}

	out, err := h.attacher.AttachVEX(r.Context(), repo, body.ArtifactID, body.Format)
	if err != nil {
		h.attachError(w, err)
		return
	}

	writeJSON(w, attachStatus(out), out)
}

func attachStatus(out *responses.AttestationAttachResponse) int {
	if out.Created {
		return http.StatusCreated
	}

	return http.StatusOK
}
