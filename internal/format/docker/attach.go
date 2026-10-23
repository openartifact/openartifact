// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/ocimanifest"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
)

const (
	mediaTypeOCIManifest = "application/vnd.oci.image.manifest.v1+json"
	mediaTypeOCIEmpty    = "application/vnd.oci.empty.v1+json"
	// emptyConfigDigest is the digest of the two bytes "{}", the OCI 1.1 empty descriptor.
	emptyConfigDigest = "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"
)

// ErrSubjectNotFound is returned by AttachReferrer when the image holds no manifest with the subject digest.
var ErrSubjectNotFound = errors.New("subject manifest not found in the image")

// ReferrerRequest describes one artifact to attach to a manifest of a hosted
// docker repository as an OCI 1.1 referrer: an image manifest with `subject`,
// the given artifactType, an empty config and one layer holding Content.
type ReferrerRequest struct {
	// Image is the image path inside the repository ("" is the default image).
	Image string
	// SubjectDigest is the digest ("sha256:...") of the manifest to attach to.
	SubjectDigest string
	// ArtifactType is the referrer's artifactType and the layer's media type.
	ArtifactType string
	// Content is the layer.
	Content []byte
	// Annotations are set on the referrer manifest.
	Annotations map[string]string
	// SlotKeys name the annotations that, with the subject and artifact type,
	// identify the slot the referrer occupies: an earlier referrer of the same
	// slot is replaced instead of kept next to it.
	SlotKeys []string
}

// ReferrerResult reports what AttachReferrer did.
type ReferrerResult struct {
	// Digest is the referrer manifest's digest.
	Digest string
	// Created is false when an identical referrer was already attached.
	Created bool
	// Replaced is the digest of the earlier referrer this one replaced ("" if none).
	Replaced string
}

// AttachReferrer stores req as a referrer of a manifest the repository holds.
// It goes through the registry's own accounting: the layer, config and
// manifest are quota-metered, tracked for registry GC and visible to the
// Referrers API like a client push. It is idempotent per slot (subject, artifact
// type and the SlotKeys annotations): an identical referrer is left alone
// (Created false) and one with other content replaces the older (its layer blob
// is left to registry GC).
func (h *HTTPHandler) AttachReferrer(ctx context.Context, repo *models.Repository, req ReferrerRequest) (*ReferrerResult, error) {
	subject, err := h.db.GetDockerManifest(ctx, repo.ID, req.Image, req.SubjectDigest)
	if err != nil {
		return nil, fmt.Errorf("look up subject: %w", err)
	}

	if subject == nil {
		return nil, ErrSubjectNotFound
	}

	layerSum := sha256.Sum256(req.Content)
	layerDigest := "sha256:" + hex.EncodeToString(layerSum[:])

	body, err := referrerManifest(subject, layerDigest, req)
	if err != nil {
		return nil, err
	}

	sum := sha256.Sum256(body)
	manifestHex := hex.EncodeToString(sum[:])
	digest := "sha256:" + manifestHex

	// One writer per slot, across replicas.
	slot := sha256.Sum256([]byte("referrer\x00" + repo.ID + "\x00" + req.Image + "\x00" + req.SubjectDigest + "\x00" + req.ArtifactType + "\x00" + slotValue(req.Annotations, req.SlotKeys)))

	var res *ReferrerResult

	err = storage.WithBlobLock(ctx, h.db, hex.EncodeToString(slot[:]), func(ctx context.Context) error {
		var inner error

		res, inner = h.attachLocked(ctx, repo, req, body, digest, manifestHex, layerDigest)

		return inner
	})
	if err != nil {
		return nil, err
	}

	return res, nil
}

func (h *HTTPHandler) attachLocked(ctx context.Context, repo *models.Repository, req ReferrerRequest, body []byte,
	digest, manifestHex, layerDigest string,
) (*ReferrerResult, error) {
	existing, err := h.db.ListDockerManifestsBySubject(ctx, repo.ID, req.Image, req.SubjectDigest)
	if err != nil {
		return nil, fmt.Errorf("list referrers: %w", err)
	}

	var replace *models.DockerManifest

	for _, m := range existing {
		if m.Digest == digest {
			return &ReferrerResult{Digest: digest}, nil
		}

		if replace == nil && h.sameSlot(ctx, m, req) {
			replace = m
		}
	}

	if err := h.ensureBlob(ctx, repo, req.Image, layerDigest, req.ArtifactType, req.Content); err != nil {
		return nil, err
	}

	if err := h.ensureBlob(ctx, repo, req.Image, emptyConfigDigest, mediaTypeOCIEmpty, []byte("{}")); err != nil {
		return nil, err
	}

	if err := h.storeReferrerManifest(ctx, repo, req, body, digest, manifestHex); err != nil {
		return nil, err
	}

	res := &ReferrerResult{Digest: digest, Created: true}

	if replace != nil {
		if err := h.hardDeleteManifest(ctx, repo.ID, req.Image, replace.Digest, replace.Size); err != nil {
			return nil, fmt.Errorf("remove replaced referrer: %w", err)
		}

		res.Replaced = replace.Digest
	}

	return res, nil
}

// sameSlot reports whether the stored manifest m occupies the slot req targets.
func (h *HTTPHandler) sameSlot(ctx context.Context, m *models.DockerManifest, req ReferrerRequest) bool {
	rc, err := h.blobStore.Get(ctx, manifestBlobKey(m.Digest))
	if err != nil {
		return false
	}

	content, readErr := io.ReadAll(io.LimitReader(rc, maxManifestBytes))
	_ = rc.Close()

	if readErr != nil {
		return false
	}

	meta := parseOCIManifestMeta(content)

	return meta.ArtifactType == req.ArtifactType && slotValue(meta.Annotations, req.SlotKeys) == slotValue(req.Annotations, req.SlotKeys)
}

func slotValue(annotations map[string]string, keys []string) string {
	var b strings.Builder

	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(annotations[k])
		b.WriteByte(0)
	}

	return b.String()
}

// FindReferrer returns the digest of the referrer of subjectDigest that has the
// artifact type and carries every given annotation ("" when there is none), so a
// caller can skip rebuilding what is already attached.
func (h *HTTPHandler) FindReferrer(ctx context.Context, repo *models.Repository, image, subjectDigest, artifactType string, annotations map[string]string) (string, error) {
	existing, err := h.db.ListDockerManifestsBySubject(ctx, repo.ID, image, subjectDigest)
	if err != nil {
		return "", fmt.Errorf("list referrers: %w", err)
	}

	for _, m := range existing {
		rc, err := h.blobStore.Get(ctx, manifestBlobKey(m.Digest))
		if err != nil {
			continue
		}

		content, readErr := io.ReadAll(io.LimitReader(rc, maxManifestBytes))
		_ = rc.Close()

		if readErr != nil {
			continue
		}

		meta := parseOCIManifestMeta(content)
		if meta.ArtifactType != artifactType {
			continue
		}

		match := true

		for k, v := range annotations {
			if meta.Annotations[k] != v {
				match = false
				break
			}
		}

		if match {
			return m.Digest, nil
		}
	}

	return "", nil
}

// ensureBlob makes the repository hold the blob (quota-metered, linked to the image).
func (h *HTTPHandler) ensureBlob(ctx context.Context, repo *models.Repository, image, digest, mediaType string, content []byte) error {
	shaHex := manifestBlobKey(digest)

	return storage.WithBlobLock(ctx, h.db, shaHex, func(ctx context.Context) error {
		held, err := h.blobAlreadyHeld(ctx, repo.ID, image, digest)
		if err != nil {
			return err
		}

		if held {
			return nil
		}

		blob := &models.DockerBlob{
			ID:           uuid.New().String(),
			RepositoryID: repo.ID,
			Digest:       digest,
			MediaType:    mediaType,
			Size:         int64(len(content)),
			Image:        &image,
			CreatedAt:    time.Now(),
		}

		return h.storeNewBlob(ctx, bytes.NewReader(content), blob, shaHex)
	})
}

func (h *HTTPHandler) storeReferrerManifest(ctx context.Context, repo *models.Repository, req ReferrerRequest, body []byte, digest, manifestHex string) error {
	size := int64(len(body))

	existing, err := h.db.GetDockerManifest(ctx, repo.ID, req.Image, digest)
	if err != nil {
		return fmt.Errorf("look up referrer: %w", err)
	}

	reserved := false

	if existing == nil {
		if err := quota.EnforceN(ctx, h.db, repo.ID, size, 1); err != nil {
			return fmt.Errorf("reserve manifest quota: %w", err)
		}

		reserved = true
	}

	refs := ocimanifest.ParseRefs(body)
	pushed := ocimanifest.PushedBlobs(body)
	now := time.Now()

	manifest := &models.DockerManifest{
		ID:            uuid.New().String(),
		RepositoryID:  repo.ID,
		Image:         req.Image,
		Digest:        digest,
		MediaType:     mediaTypeOCIManifest,
		Size:          size,
		SubjectDigest: req.SubjectDigest,
		Refs:          &refs,
		RequiredBlobs: pushed,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := h.storeManifest(ctx, manifest, manifestHex, mediaTypeOCIManifest, body); err != nil {
		if reserved {
			h.releaseQuota(ctx, repo.ID, size, 1)
		}

		if errors.Is(err, database.ErrDockerBlobsMissing) {
			return fmt.Errorf("referrer blobs missing: %w", err)
		}

		return fmt.Errorf("store referrer manifest: %w", err)
	}

	h.settleQuota(ctx, repo.ID, size, 1, reserved, manifest.Inserted)

	return nil
}

type ociDescriptor struct {
	MediaType    string            `json:"mediaType"` //nolint:tagliatelle // OCI image-spec wire format is camelCase
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	ArtifactType string            `json:"artifactType,omitempty"` //nolint:tagliatelle // OCI image-spec wire format is camelCase
}

// referrerManifest renders the deterministic OCI 1.1 manifest (no timestamps, so
// the same content always has the same digest).
func referrerManifest(subject *models.DockerManifest, layerDigest string, req ReferrerRequest) ([]byte, error) {
	doc := struct {
		SchemaVersion int               `json:"schemaVersion"` //nolint:tagliatelle // OCI image-spec wire format is camelCase
		MediaType     string            `json:"mediaType"`     //nolint:tagliatelle // OCI image-spec wire format is camelCase
		ArtifactType  string            `json:"artifactType"`  //nolint:tagliatelle // OCI image-spec wire format is camelCase
		Config        ociDescriptor     `json:"config"`
		Layers        []ociDescriptor   `json:"layers"`
		Subject       ociDescriptor     `json:"subject"`
		Annotations   map[string]string `json:"annotations,omitempty"`
	}{
		SchemaVersion: 2,
		MediaType:     mediaTypeOCIManifest,
		ArtifactType:  req.ArtifactType,
		Config:        ociDescriptor{MediaType: mediaTypeOCIEmpty, Digest: emptyConfigDigest, Size: 2},
		Layers:        []ociDescriptor{{MediaType: req.ArtifactType, Digest: layerDigest, Size: int64(len(req.Content))}},
		Subject:       ociDescriptor{MediaType: subject.MediaType, Digest: subject.Digest, Size: subject.Size},
		Annotations:   req.Annotations,
	}

	b, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode referrer manifest: %w", err)
	}

	return b, nil
}
