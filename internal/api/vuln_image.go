// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/exploit"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/internal/vuln"
)

// imageDigestRe is a manifest digest, the id of the scan of an image.
var imageDigestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// errScanSlots is returned when all background-scan slots are taken.
var errScanSlots = errors.New("too many vulnerability scans in progress")

// defaultImageScanners are the scanners that scan an image started by an SBOM
// ingest: osv answers the language ecosystems from the local mirror and the
// operating system packages by name and version.
func defaultImageScanners() []string { return []string{"osv"} }

// ImageSBOMs reads the SBOMs ingested for container images.
type ImageSBOMs interface {
	// HasSBOM reports whether the image has a stored SBOM.
	HasSBOM(ctx context.Context, repoID, digest string) (bool, error)
	// Components lists the packages of the image's SBOMs.
	Components(ctx context.Context, repoID, digest string) ([]sbom.Component, error)
}

// AttestationSummary counts the attestations an image has.
type AttestationSummary struct {
	// Total is the number of attestations and signatures indexed for the image.
	Total int
	// Verified is how many of them verified against a trusted identity.
	Verified int
	// Failed is how many were checked and did not verify.
	Failed int
}

// AttestationLookup answers the attestation presence check of the digest gate.
type AttestationLookup interface {
	AttestationSummary(ctx context.Context, repoID, subjectDigest string) (AttestationSummary, error)
}

// WithImages sets the reader of the SBOMs of container images, which enables
// the vulnerability scan and the license evaluation of an image.
func (h *VulnHandler) WithImages(images ImageSBOMs) *VulnHandler {
	h.images = images
	return h
}

// WithAttestations adds the attestation check to the digest gate.
func (h *VulnHandler) WithAttestations(a AttestationLookup) *VulnHandler {
	h.attestations = a
	return h
}

// ScanImage starts the vulnerability scan of an image from its stored SBOM, as
// a scan started by an SBOM ingest. The scan ends like any other: stored, then
// announced to the scan hook.
func (h *VulnHandler) ScanImage(ctx context.Context, repoID, digest string) error {
	_, err := h.startImageScan(ctx, repoID, digest, defaultImageScanners())
	return err
}

// startImageScan stores a "scanning" result for the image and runs the scan.
func (h *VulnHandler) startImageScan(ctx context.Context, repoID, digest string, scanners []string) (*models.VulnScanResult, error) {
	if !h.reserveScanSlot() {
		return nil, errScanSlots
	}

	now := time.Now()
	scan := &models.VulnScanResult{
		RepositoryID:    repoID,
		ArtifactID:      digest,
		PackageName:     digest,
		Ecosystem:       vuln.ImageEcosystem,
		Scanner:         strings.Join(scanners, ","),
		ScanStatus:      "scanning",
		Vulnerabilities: []models.Vulnerability{},
		FailedScanners:  []string{},
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if err := h.db.CreateVulnScanResult(ctx, scan); err != nil {
		h.releaseScanSlot()
		return nil, fmt.Errorf("store scan: %w", err)
	}

	if err := h.queueScan(ctx, repoID, scan, vuln.Package{Name: digest, Ecosystem: vuln.ImageEcosystem}); err != nil {
		h.releaseScanSlot()
		return nil, fmt.Errorf("queue scan: %w", err)
	}

	return scan, nil
}

// handleImageScan serves POST .../vuln/scan for the docker ecosystem: the scan
// of the packages of an image's SBOM. artifact_id is the manifest digest.
func (h *VulnHandler) handleImageScan(w http.ResponseWriter, r *http.Request, repo *models.Repository, digest string, scanners []string) {
	switch {
	case repo.Format != "docker":
		writeError(w, http.StatusBadRequest, "validation_failed", "the docker ecosystem needs a docker repository")
		return
	case !imageDigestRe.MatchString(digest):
		writeError(w, http.StatusBadRequest, "validation_failed", "artifact_id must be the manifest digest (sha256:<hex>) of the image")
		return
	}

	ctx := r.Context()

	has := false

	if h.images != nil {
		var err error
		if has, err = h.images.HasSBOM(ctx, repo.ID, digest); err != nil {
			h.dbError(w, "scan image: reading the SBOM", err)
			return
		}
	}

	if !has {
		writeError(w, http.StatusUnprocessableEntity, "no_sbom",
			"the image has no SBOM; attach one with cosign (attach sbom or attest) or push it as an OCI referrer")

		return
	}

	scan, err := h.startImageScan(ctx, repo.ID, digest, scanners)
	if errors.Is(err, errScanSlots) {
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusTooManyRequests, "too_many_scans", err.Error())

		return
	}

	if err != nil {
		h.dbError(w, "scan image: starting the scan", err)
		return
	}

	resp, err := h.scanResponse(ctx, scan)
	if err != nil {
		h.dbError(w, "scan image: exploit signals", err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// imageOutcome scans every package of the image's SBOM. note is the client-safe
// text for the stored scan error: the components that were not scanned, or why
// the scan could not run.
func (h *VulnHandler) imageOutcome(ctx context.Context, scan *models.VulnScanResult, scanners []string) (vuln.Outcome, string) {
	failed := vuln.Outcome{Status: vuln.StatusFailed, Vulnerabilities: []models.Vulnerability{}}

	if h.images == nil {
		return failed, "image SBOMs are not available on this server"
	}

	comps, err := h.images.Components(ctx, scan.RepositoryID, scan.ArtifactID)
	if err != nil {
		h.log.Errorw("image scan: reading the SBOM", "repository_id", scan.RepositoryID, "digest", scan.ArtifactID, "error", err)
		return failed, "the SBOM of the image could not be read"
	}

	purls := make([]string, len(comps))
	for i, c := range comps {
		purls[i] = c.PURL
	}

	out := vuln.RunComponents(ctx, h.log, h.newBackend, scanners, purls)
	note := out.SkippedNote()

	if out.Scanned == 0 {
		out.Status, out.Vulnerabilities = vuln.StatusFailed, []models.Vulnerability{}
		note = joinScanErrors("the SBOM names no package a scanner covers", note)
	}

	return out.Outcome, note
}

// readableRepo authorises a repository-scoped read of {name} (anonymous callers
// of a public repository included) and loads the repository: 403 without the
// read permission, 404 for an unknown repository.
func (h *VulnHandler) readableRepo(w http.ResponseWriter, r *http.Request, op string) (*models.Repository, bool) {
	p := currentPrincipal(r)
	if p == nil {
		writeError(w, http.StatusForbidden, "forbidden", "access denied")
		return nil, false
	}

	allowed, err := h.evaluator.Evaluate(r.Context(), *p, urlparams.Vars(r)["name"], "", "read")
	if err != nil {
		h.log.Errorw(op+": auth evaluation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")

		return nil, false
	}

	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "access denied")
		return nil, false
	}

	return h.loadRepo(w, r, op)
}

// handleGateDigest serves GET /repositories/{name}/gate?digest=sha256:...: it
// aggregates, for one image, the vulnerability gate of its latest scan, the
// license policies over its SBOM, the quarantine flag and the attestations. The
// image passes only when every check passes; a missing scan is a failure.
func (h *VulnHandler) handleGateDigest(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.readableRepo(w, r, "gate image")
	if !ok {
		return
	}

	digest := r.URL.Query().Get("digest")
	if !imageDigestRe.MatchString(digest) {
		writeError(w, http.StatusBadRequest, "validation_failed", "digest must be a manifest digest (sha256:<hex>)")
		return
	}

	if repo.Format != "docker" {
		writeError(w, http.StatusBadRequest, "validation_failed", "the digest gate needs a docker repository")
		return
	}

	criteria, ok := gateCriteria(w, r, true)
	if !ok {
		return
	}

	ctx := r.Context()
	resp := responses.GateResponse{Digest: digest, ScanStatus: "none", Checks: []responses.GateCheck{}}

	steps := []func(context.Context, *models.Repository, string, exploit.Criteria, *responses.GateResponse) error{
		h.gateVulnerabilities, h.gateLicenses, h.gateQuarantine, h.gateAttestations,
	}

	for _, step := range steps {
		if err := step(ctx, repo, digest, criteria, &resp); err != nil {
			h.dbError(w, "gate image", err)
			return
		}
	}

	resp.Pass = true

	for _, c := range resp.Checks {
		if !c.Pass {
			resp.Pass, resp.Reason = false, c.Name+": "+c.Detail

			break
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *VulnHandler) gateVulnerabilities(
	ctx context.Context, repo *models.Repository, digest string, criteria exploit.Criteria, resp *responses.GateResponse,
) error {
	scan, err := h.db.GetVulnScanResult(ctx, repo.ID, digest)
	if err != nil {
		return fmt.Errorf("latest scan: %w", err)
	}

	if scan == nil {
		resp.Checks = append(resp.Checks, responses.GateCheck{Name: gateCheckVuln, Detail: "the image has not been scanned"})
		return nil
	}

	verdict, err := h.scanVerdict(ctx, criteria, scan)
	if err != nil {
		return err
	}

	resp.Scan, resp.ScanStatus = scan.ID, scan.ScanStatus
	resp.Checks = append(resp.Checks, verdict.Checks...)

	return nil
}

func (h *VulnHandler) gateLicenses(
	ctx context.Context, repo *models.Repository, digest string, _ exploit.Criteria, resp *responses.GateResponse,
) error {
	check := responses.GateCheck{Name: "license"}

	blocking, err := licensepolicy.HasBlocking(ctx, h.db, repo.ID)
	if err != nil {
		return fmt.Errorf("license policies: %w", err)
	}

	if !blocking {
		check.Pass, check.Detail = true, "no blocking license policy applies"
		resp.Checks = append(resp.Checks, check)

		return nil
	}

	has := false

	if h.images != nil {
		if has, err = h.images.HasSBOM(ctx, repo.ID, digest); err != nil {
			return fmt.Errorf("SBOM: %w", err)
		}
	}

	if !has {
		check.Detail = "a blocking license policy applies and the image has no SBOM to evaluate"
		resp.Checks = append(resp.Checks, check)

		return nil
	}

	comps, err := h.images.Components(ctx, repo.ID, digest)
	if err != nil {
		return fmt.Errorf("SBOM components: %w", err)
	}

	var licenses []string
	for _, c := range comps {
		licenses = append(licenses, c.Licenses...)
	}

	violations, err := licensepolicy.Evaluate(ctx, h.db, h.log, repo.ID, licenses)
	if err != nil {
		return fmt.Errorf("license evaluation: %w", err)
	}

	if blocked := licensepolicy.Blocked(violations); len(blocked) > 0 {
		check.Detail = fmt.Sprintf("%d blocking license violation(s), first: %s", len(blocked), blocked[0].Violation)
	} else {
		check.Pass, check.Detail = true, fmt.Sprintf("%d package(s) evaluated, no blocking violation", len(comps))
	}

	resp.Checks = append(resp.Checks, check)

	return nil
}

func (h *VulnHandler) gateQuarantine(
	ctx context.Context, repo *models.Repository, digest string, _ exploit.Criteria, resp *responses.GateResponse,
) error {
	quarantined, err := h.db.IsArtifactQuarantined(ctx, repo.ID, []string{digest})
	if err != nil {
		return fmt.Errorf("quarantine: %w", err)
	}

	check := responses.GateCheck{Name: "quarantine", Pass: !quarantined, Detail: "the image is not quarantined"}
	if quarantined {
		check.Detail = "the image is quarantined"
	}

	resp.Checks = append(resp.Checks, check)

	return nil
}

func (h *VulnHandler) gateAttestations(
	ctx context.Context, repo *models.Repository, digest string, _ exploit.Criteria, resp *responses.GateResponse,
) error {
	if h.attestations == nil {
		return nil
	}

	sum, err := h.attestations.AttestationSummary(ctx, repo.ID, digest)
	if err != nil {
		return fmt.Errorf("attestations: %w", err)
	}

	check := responses.GateCheck{Name: "attestations"}

	switch {
	case sum.Total == 0:
		check.Detail = "the image has no signature or attestation"
	case sum.Failed > 0:
		check.Detail = fmt.Sprintf("%d of %d signature(s) and attestation(s) failed verification", sum.Failed, sum.Total)
	default:
		check.Pass = true
		check.Detail = fmt.Sprintf("%d signature(s) and attestation(s), %d verified", sum.Total, sum.Verified)
	}

	resp.Checks = append(resp.Checks, check)

	return nil
}
