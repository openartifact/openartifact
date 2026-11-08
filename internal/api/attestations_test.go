// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/attestation"
	"github.com/openartifact/openartifact/internal/database/models"
)

type attRepoDB struct{ repo *models.Repository }

func (d attRepoDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	if d.repo != nil && d.repo.Name == name {
		return d.repo, nil
	}

	return nil, nil
}

// attIndexDB serves one indexed signature row and its payload blob; the rest of attestation.DB would panic.
type attIndexDB struct {
	attestation.DB

	row     *models.OCIAttestation
	payload []byte
}

func (d attIndexDB) ListOCIAttestations(_ context.Context, repoID, subject, image string) ([]*models.OCIAttestation, error) {
	if repoID == d.row.RepositoryID && subject == d.row.SubjectDigest && (image == "" || image == d.row.Image) {
		return []*models.OCIAttestation{d.row}, nil
	}

	return nil, nil
}

func (d attIndexDB) ListOCIAttestationsByDigest(_ context.Context, _, digest string) ([]*models.OCIAttestation, error) {
	if digest == d.row.AttestationDigest {
		return []*models.OCIAttestation{d.row}, nil
	}

	return nil, nil
}

func (d attIndexDB) GetDockerBlob(context.Context, string, string) (*models.DockerBlob, error) {
	return &models.DockerBlob{BlobSHA256: "k", Size: int64(len(d.payload))}, nil
}

func (d attIndexDB) Get(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(d.payload)), nil
}

func TestAttestationsReadEndpoints(t *testing.T) {
	subject := "sha256:" + strings.Repeat("a", 64)
	digest := "sha256:" + strings.Repeat("e", 64)
	repo := &models.Repository{ID: "repo-1", Name: "images", Format: "docker"}
	row := &models.OCIAttestation{
		RepositoryID: repo.ID, Image: "app", SubjectDigest: subject, AttestationDigest: digest, Kind: models.AttestationKindSignature,
		PredicateType: attestation.PredicateSimpleSigning, SignerIdentity: "key:abc", Verified: true, CreatedAt: time.Unix(1700000000, 0),
	}
	db := attIndexDB{row: row, payload: []byte(`{"critical":{}}`)}
	h := NewOCIAttestationsHandler(
		attRepoDB{repo: repo},
		newServerTestLogger(t),
		nil,
		attestation.NewService(newServerTestLogger(t), db, db),
	)

	r := chi.NewRouter()
	h.RegisterAuthed(r)

	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))

		return w
	}

	t.Run("list", func(t *testing.T) {
		w := get("/api/v1/repositories/images/attestations?subject=" + subject)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body)
		}

		var got struct {
			Attestations []map[string]any `json:"attestations"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Attestations) != 1 {
			t.Fatalf("body = %s (%v)", w.Body, err)
		}

		a := got.Attestations[0]
		if a["digest"] != digest || a["subject"] != subject || a["kind"] != "signature" || a["verified"] != true ||
			a["signer_identity"] != "key:abc" {
			t.Fatalf("attestation = %v", a)
		}

		for _, k := range []string{"predicate_type", "issuer", "verify_error", "created_at", "image"} {
			if _, ok := a[k]; !ok {
				t.Errorf("attestation has no %q", k)
			}
		}
	})

	t.Run("list of an image without attachments is empty", func(t *testing.T) {
		w := get("/api/v1/repositories/images/attestations?subject=sha256:" + strings.Repeat("0", 64))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"attestations":[]`) {
			t.Fatalf("status = %d, body = %s", w.Code, w.Body)
		}
	})

	t.Run("get answers the statement", func(t *testing.T) {
		w := get("/api/v1/repositories/images/attestations/" + digest)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body)
		}

		var st map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st["predicateType"] != attestation.PredicateSimpleSigning {
			t.Fatalf("body = %s (%v)", w.Body, err)
		}
	})

	for _, tc := range []struct {
		name, path string
		want       int
	}{
		{"list without subject", "/api/v1/repositories/images/attestations", http.StatusBadRequest},
		{"list with a malformed subject", "/api/v1/repositories/images/attestations?subject=latest", http.StatusBadRequest},
		{"get with a malformed digest", "/api/v1/repositories/images/attestations/nope", http.StatusBadRequest},
		{"get an unknown digest", "/api/v1/repositories/images/attestations/sha256:" + strings.Repeat("f", 64), http.StatusNotFound},
		{"unknown repository", "/api/v1/repositories/ghost/attestations?subject=" + subject, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := get(tc.path); w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body)
			}
		})
	}
}
