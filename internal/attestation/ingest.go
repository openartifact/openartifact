// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package attestation

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/attest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/ociref"
)

// Event is a manifest that was pushed to a hosted docker repository.
type Event struct {
	RepositoryID string
	// Image is the image inside the repository ("" for the default image).
	Image string
	// Tag is the reference the manifest was pushed under; empty for a push by digest.
	Tag string
	// Digest is the manifest's digest ("sha256:<hex>").
	Digest string
	// Raw is the manifest body.
	Raw []byte
}

// attachment says how a manifest relates to its subject.
type attachment int

const (
	attachSignature attachment = iota // cosign ".sig" tag
	attachAttestation
	attachSBOM
	attachReferrer // OCI referrer holding a Sigstore bundle or a DSSE envelope
)

// Artifact types of referrers that carry a signed envelope.
func isEnvelopeArtifact(t string) bool {
	switch t {
	case mediaTypeBundle, mediaTypeDSSE, mediaTypeInToto:
		return true
	}

	return false
}

// classify finds out whether the pushed manifest is an attachment and of which subject.
func classify(ev Event, m *manifestDoc) (string, attachment, bool) {
	if subject, kind, ok := ociref.ParseCosignTag(ev.Tag); ok {
		switch kind {
		case ociref.KindSignature:
			return subject, attachSignature, true
		case ociref.KindAttestation:
			return subject, attachAttestation, true
		case ociref.KindSBOM:
			return subject, attachSBOM, true
		case ociref.KindReferrersIndex, ociref.KindSource:
			return "", 0, false
		}
	}

	if m.Subject != nil && m.Subject.Digest != "" && isEnvelopeArtifact(m.artifactType()) {
		return m.Subject.Digest, attachReferrer, true
	}

	return "", 0, false
}

// Ingest indexes and verifies the attachments of a pushed manifest. A manifest
// that is no attachment is ignored. Every layer is judged on its own; the first
// error is returned after the rest were processed.
func (s *Service) Ingest(ctx context.Context, ev Event) error {
	m, ok := parseManifest(ev.Raw)
	if !ok {
		return nil
	}

	subject, att, ok := classify(ev, &m)
	if !ok {
		return nil
	}

	return s.ingestManifest(ctx, ev, &m, subject, att)
}

func (s *Service) ingestManifest(ctx context.Context, ev Event, m *manifestDoc, subject string, att attachment) error {
	trust, err := s.trust(ctx, ev.RepositoryID)
	if err != nil {
		return err
	}

	row, err := s.db.GetDockerManifest(ctx, ev.RepositoryID, ev.Image, ev.Digest)
	if err != nil {
		return fmt.Errorf("look up manifest %s: %w", ev.Digest, err)
	}

	if row == nil {
		return fmt.Errorf("%w: %s", errManifestMissing, ev.Digest)
	}

	layers := m.Layers
	if len(layers) > maxManifestLayers {
		layers = layers[:maxManifestLayers]
	}

	var errs []error

	for _, l := range layers {
		rec, err := s.judgeLayer(ctx, ev, subject, att, l, trust)
		if err != nil {
			errs = append(errs, fmt.Errorf("layer %s: %w", l.Digest, err))
			continue
		}

		if rec == nil {
			continue
		}

		rec.RepositoryID, rec.Image, rec.SubjectDigest, rec.ManifestID = ev.RepositoryID, ev.Image, subject, row.ID
		rec.AttestationDigest = l.Digest
		rec.PayloadBlob = strings.TrimPrefix(l.Digest, "sha256:")

		if err := s.db.UpsertOCIAttestation(ctx, rec); err != nil {
			errs = append(errs, fmt.Errorf("store attestation %s: %w", l.Digest, err))
		}
	}

	return errors.Join(errs...)
}

// judgeLayer decides what one layer is and whether it verifies. It returns nil
// for a layer that holds no attachment.
func (s *Service) judgeLayer(
	ctx context.Context, ev Event, subject string, att attachment, l descriptor, trust *Trust,
) (*models.OCIAttestation, error) {
	if att == attachSBOM {
		return sbomRecord(l), nil
	}

	if !wantsLayer(att, l.MediaType) {
		return nil, nil //nolint:nilnil // not an attachment layer
	}

	at, err := s.verificationTime(ctx, ev, subject, l.Digest)
	if err != nil {
		return nil, err
	}

	blob, err := s.readStored(ctx, ev.RepositoryID, l.Digest)
	if errors.Is(err, errTooLarge) {
		return &models.OCIAttestation{Kind: kindOf(att), VerifyError: errTooLarge.Error()}, nil
	}

	if err != nil {
		return nil, err
	}

	if att == attachSignature {
		return s.judgeSignature(blob, subject, l, trust, at), nil
	}

	return s.judgeEnvelope(blob, subject, l, trust, at), nil
}

func kindOf(att attachment) string {
	switch att {
	case attachSignature:
		return models.AttestationKindSignature
	case attachSBOM:
		return models.AttestationKindSBOM
	default:
		return models.AttestationKindAttestation
	}
}

// wantsLayer reports whether a layer of the media type can hold the attachment.
func wantsLayer(att attachment, mediaType string) bool {
	if att == attachSignature {
		return mediaType == mediaTypeSimpleSign
	}

	return isEnvelopeArtifact(mediaType)
}

// verificationTime is the time a signature is judged at: when the envelope was
// first seen, so that re-pushing a manifest (cosign appends to its ".att" tag)
// or re-verifying later does not fail short-lived certificates, and now for a
// new one.
func (s *Service) verificationTime(ctx context.Context, ev Event, subject, digest string) (time.Time, error) {
	seen, ok, err := s.db.GetOCIAttestationFirstSeen(ctx, ev.RepositoryID, ev.Image, subject, digest)
	if err != nil {
		return time.Time{}, fmt.Errorf("look up first seen time: %w", err)
	}

	if ok {
		return seen, nil
	}

	return s.now(), nil
}

// sbomRecord indexes a "cosign attach sbom" layer. These SBOMs carry no signature.
func sbomRecord(l descriptor) *models.OCIAttestation {
	var predicate string

	switch mt := strings.ToLower(l.MediaType); {
	case strings.Contains(mt, "spdx"):
		predicate = attest.PredicateSPDX
	case strings.Contains(mt, "cyclonedx"):
		predicate = attest.PredicateCycloneDX
	default:
		return nil
	}

	return &models.OCIAttestation{
		Kind:          models.AttestationKindSBOM,
		PredicateType: predicate,
		VerifyError:   "unsigned SBOM (cosign attach sbom)",
	}
}

// judgeSignature verifies a cosign signature layer: the simple signing payload
// must be about the subject and the annotated signature must verify over it.
func (s *Service) judgeSignature(payload []byte, subject string, l descriptor, trust *Trust, at time.Time) *models.OCIAttestation {
	rec := &models.OCIAttestation{Kind: models.AttestationKindSignature, PredicateType: PredicateSimpleSigning}

	var ss simpleSigning
	if err := json.Unmarshal(payload, &ss); err != nil {
		rec.VerifyError = "the signature payload is not a cosign simple signing document"
		return rec
	}

	sig, err := attest.DecodeBase64(l.Annotations[annotationSignature])
	if err != nil {
		rec.VerifyError = "the layer has no " + annotationSignature + " annotation"
		return rec
	}

	out := s.verifier.VerifyBlob(trust, payload, sig, annotationMaterial(l.Annotations), at)

	if out.Verified && ss.Critical.Image.Digest != subject {
		out.Verified, out.Reason = false, errSubjectDig.Error()
	}

	apply(rec, out)

	return rec
}

// judgeEnvelope verifies a DSSE envelope or a Sigstore bundle layer.
func (s *Service) judgeEnvelope(blob []byte, subject string, l descriptor, trust *Trust, at time.Time) *models.OCIAttestation {
	dec := decodeAttestationBlob(blob, l.MediaType, l.Annotations)
	rec := &models.OCIAttestation{Kind: dec.kind(), PredicateType: dec.predicate(l.Annotations)}

	if dec.env == nil || dec.st == nil {
		rec.VerifyError = dec.problem
		return rec
	}

	out := s.verifier.VerifyEnvelope(trust, dec.env, dec.mat, at)

	if out.Verified && !covers(dec.st, subject) {
		out.Verified, out.Reason = false, errSubjectDig.Error()
	}

	apply(rec, out)

	return rec
}

func apply(rec *models.OCIAttestation, out Outcome) {
	rec.Verified, rec.SignerIdentity, rec.Issuer, rec.VerifyError = out.Verified, out.Signer, out.Issuer, out.Reason
}

// covers reports whether the statement names the sha256 digest subject.
func covers(st *attest.Statement, subject string) bool {
	want, ok := strings.CutPrefix(subject, "sha256:")
	if !ok {
		return false
	}

	for _, sub := range st.Subject {
		if strings.EqualFold(sub.Digest["sha256"], want) {
			return true
		}
	}

	return false
}

// decoded is a layer read as an envelope.
type decoded struct {
	env *attest.Envelope // nil for an unsigned statement or a bundle without envelope
	st  *attest.Statement
	mat Material
	sig bool // a bundle with a message signature: a signature, not an attestation
	// problem says why the layer cannot be verified.
	problem string
}

func (d *decoded) kind() string {
	if d.sig {
		return models.AttestationKindSignature
	}

	return models.AttestationKindAttestation
}

func (d *decoded) predicate(ann map[string]string) string {
	switch {
	case d.st != nil && d.st.PredicateType != "":
		return d.st.PredicateType
	case d.sig:
		return PredicateSimpleSigning
	default:
		return ann[annotationPredicate]
	}
}

// decodeAttestationBlob reads an envelope layer: a Sigstore bundle, a DSSE
// envelope (whose certificate, if any, rides in the layer annotations), or an
// unsigned in-toto statement.
func decodeAttestationBlob(blob []byte, mediaType string, ann map[string]string) *decoded {
	d := &decoded{}

	var b bundleDoc
	if json.Unmarshal(blob, &b) == nil && (mediaType == mediaTypeBundle || b.DSSEEnvelope != nil || len(b.MessageSignature) > 0) {
		return d.fromBundle(&b)
	}

	var env attest.Envelope
	if err := json.Unmarshal(blob, &env); err != nil {
		d.problem = "the layer is not a DSSE envelope"
		return d
	}

	if env.Payload == "" {
		return d.fromStatement(blob)
	}

	d.env, d.mat = &env, annotationMaterial(ann)
	d.decodeStatement()

	return d
}

func (d *decoded) fromBundle(b *bundleDoc) *decoded {
	if b.DSSEEnvelope == nil {
		d.sig = true
		d.problem = "message signature bundles are not verified"

		return d
	}

	d.env, d.mat = b.DSSEEnvelope, b.material()
	d.decodeStatement()

	return d
}

func (d *decoded) fromStatement(blob []byte) *decoded {
	var st attest.Statement
	if json.Unmarshal(blob, &st) != nil || st.PredicateType == "" {
		d.problem = "the layer is neither a DSSE envelope nor an in-toto statement"
		return d
	}

	d.st = &st
	d.problem = "unsigned in-toto statement"

	return d
}

// decodeStatement reads the in-toto statement out of the envelope.
func (d *decoded) decodeStatement() {
	if d.env.PayloadType != attest.PayloadType {
		d.problem = "the envelope payload type is " + d.env.PayloadType + ", not an in-toto statement"
		return
	}

	payload, err := attest.DecodeBase64(d.env.Payload)
	if err != nil {
		d.problem = "the envelope payload is not base64"
		return
	}

	var st attest.Statement
	if err := json.Unmarshal(payload, &st); err != nil {
		d.problem = "the envelope payload is not an in-toto statement"
		return
	}

	d.st = &st
}

// IngestSubject re-reads the attachments of a subject digest of an image (its
// cosign tags and its referrers) and indexes and verifies them again. It
// rebuilds the index after the manifests were restored and applies a changed
// trust configuration. It returns how many manifests it processed.
func (s *Service) IngestSubject(ctx context.Context, repoID, image, subject string) (int, error) {
	var (
		n    int
		errs []error
	)

	process := func(digest, tag string) {
		raw, err := s.manifestBody(ctx, repoID, digest)
		if err != nil {
			errs = append(errs, err)
			return
		}

		n++

		if err := s.Ingest(ctx, Event{RepositoryID: repoID, Image: image, Tag: tag, Digest: digest, Raw: raw}); err != nil {
			errs = append(errs, err)
		}
	}

	for _, kind := range []ociref.Kind{ociref.KindSignature, ociref.KindAttestation, ociref.KindSBOM} {
		tag := ociref.CosignTag(subject, kind)
		if tag == "" {
			return 0, fmt.Errorf("%w: %q is not a sha256 digest", ErrInvalidSubject, subject)
		}

		t, err := s.db.GetDockerTag(ctx, repoID, image, tag)
		if err != nil {
			errs = append(errs, fmt.Errorf("look up tag %s: %w", tag, err))
			continue
		}

		if t != nil {
			process(t.ManifestDigest, tag)
		}
	}

	refs, err := s.db.ListDockerManifestsBySubject(ctx, repoID, image, subject)
	if err != nil {
		errs = append(errs, fmt.Errorf("list referrers: %w", err))
	}

	for _, r := range refs {
		process(r.Digest, "")
	}

	return n, errors.Join(errs...)
}

// manifestBody reads a stored manifest: manifests are kept under the hex of their digest.
func (s *Service) manifestBody(ctx context.Context, repoID, digest string) ([]byte, error) {
	key := strings.TrimPrefix(digest, "sha256:")
	if _, err := hex.DecodeString(key); err != nil {
		return nil, fmt.Errorf("manifest digest %q: %w", digest, err)
	}

	body, err := s.readBlobKey(ctx, key, 0)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s of %s: %w", digest, repoID, err)
	}

	return body, nil
}

// Reverify judges the indexed attachments again against the current trust
// configuration: of one subject digest, or, with an empty subject, of the whole
// repository. image narrows a subject to one image and is how a subject whose
// rows are gone is re-indexed. It returns the number of manifests processed.
func (s *Service) Reverify(ctx context.Context, repoID, subject, image string) (int, error) {
	if subject != "" {
		return s.reverifySubject(ctx, repoID, subject, image)
	}

	var (
		total int
		after = "00000000-0000-0000-0000-000000000000"
		seen  = map[[2]string]bool{}
	)

	for {
		rows, err := s.db.ListOCIAttestationsPage(ctx, repoID, after, reverifyPage)
		if err != nil {
			return total, fmt.Errorf("list attestations: %w", err)
		}

		for _, r := range rows {
			key := [2]string{r.Image, r.SubjectDigest}
			if seen[key] {
				continue
			}

			seen[key] = true

			n, err := s.IngestSubject(ctx, repoID, r.Image, r.SubjectDigest)
			total += n

			if err != nil {
				s.log.Warnw("attestation: reverify subject", "repo_id", repoID, "subject", r.SubjectDigest, "error", err)
			}
		}

		if len(rows) < reverifyPage {
			return total, nil
		}

		after = rows[len(rows)-1].ID
	}
}

func (s *Service) reverifySubject(ctx context.Context, repoID, subject, image string) (int, error) {
	images := map[string]bool{image: true}

	if image == "" {
		rows, err := s.List(ctx, repoID, subject, "")
		if err != nil {
			return 0, err
		}

		for _, r := range rows {
			images[r.Image] = true
		}
	}

	var (
		total int
		errs  []error
	)

	for img := range images {
		n, err := s.IngestSubject(ctx, repoID, img, subject)
		total += n

		if err != nil {
			errs = append(errs, err)
		}
	}

	return total, errors.Join(errs...)
}
