// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestChecksumOf_PicksTheAlgorithmOfTheSuffix(t *testing.T) {
	body := []byte("<metadata/>")

	tests := []struct {
		suffix string
		h      hash.Hash
	}{
		{".md5", md5.New()},
		{".sha1", sha1.New()},
		{".sha256", sha256.New()},
		{".sha512", sha512.New()},
	}

	for _, tt := range tests {
		t.Run(tt.suffix, func(t *testing.T) {
			tt.h.Write(body)

			got, err := checksumOf(body, tt.suffix)
			if err != nil || got != hex.EncodeToString(tt.h.Sum(nil)) {
				t.Errorf("checksumOf(%s) = %q, %v", tt.suffix, got, err)
			}
		})
	}
}

func TestServe_MavenMetadataChecksumSuffixes(t *testing.T) {
	for _, suffix := range []string{".md5", ".sha1", ".sha256", ".sha512"} {
		t.Run(suffix, func(t *testing.T) {
			f := newFixture(t, "maven", member("hosted", "maven", "hosted"), member("central", "maven", "proxy"))
			f.set("hosted", metaPath, 200, versionsDoc("1", "1.0"))
			f.set("central", metaPath, 200, versionsDoc("2", "2.0"))

			merged := f.get(http.MethodGet, metaPath).Body.String()

			w := f.get(http.MethodGet, metaPath+suffix)
			want, _ := checksumOf([]byte(merged), suffix)

			if got := strings.Fields(w.Body.String()); w.Code != http.StatusOK || len(got) != 2 || got[0] != want || got[1] != "maven-metadata.xml" {
				t.Errorf("%s = %d %q, want %s of the merged bytes", suffix, w.Code, w.Body.String(), want)
			}

			if h := f.get(http.MethodHead, metaPath+suffix); h.Code != http.StatusOK || h.Body.Len() != 0 {
				t.Errorf("HEAD %s = %d with %d body bytes", suffix, h.Code, h.Body.Len())
			}
		})
	}
}

func TestServe_MavenMetadataMemberFailureMapping(t *testing.T) {
	tests := []struct {
		name  string
		reply memberReply
		want  int
	}{
		{"5xx", memberReply{http.StatusInternalServerError, "secret upstream trace"}, http.StatusBadGateway},
		{"4xx", memberReply{http.StatusForbidden, "denied"}, http.StatusBadGateway},
		{"404", memberReply{http.StatusNotFound, ""}, http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, "maven", member("hosted", "maven", "hosted"), member("central", "maven", "proxy"))
			f.set("hosted", metaPath, http.StatusNotFound, "")
			f.set("central", metaPath, tt.reply.status, tt.reply.body)

			w := f.get(http.MethodGet, metaPath)
			if w.Code != tt.want {
				t.Errorf("status = %d, want %d", w.Code, tt.want)
			}

			if strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "maintenance") {
				t.Errorf("member text leaked: %q", w.Body.String())
			}
		})
	}
}

func TestServe_MavenMetadataAllMembersUnparsableIs502(t *testing.T) {
	f := newFixture(t, "maven", member("hosted", "maven", "hosted"), member("central", "maven", "proxy"))
	f.set("hosted", metaPath, http.StatusOK, "<html>maintenance")
	f.set("central", metaPath, http.StatusOK, "<html>maintenance")

	w := f.get(http.MethodGet, metaPath)
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "maintenance") {
		t.Errorf("= %d %q, want 502 without member text", w.Code, w.Body.String())
	}
}

func TestServe_MavenMetadataOversizedMemberDocumentIsAFailure(t *testing.T) {
	f := newFixture(t, "maven", member("hosted", "maven", "hosted"), member("central", "maven", "proxy"))
	be := f.backend()
	chunk := []byte(strings.Repeat("x", 1<<20))

	be.ServeMember = func(w http.ResponseWriter, _ *http.Request, m *models.Repository, _ string) {
		if m.Name == "central" {
			for range maxMetadataBytes>>20 + 2 {
				_, _ = w.Write(chunk)
			}

			return
		}

		http.NotFound(w, nil)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	f.h.Serve(w, req, f.grp, metaPath, be)

	if w.Code != http.StatusBadGateway {
		t.Errorf("oversized member document = %d, want 502", w.Code)
	}
}

func TestNuGetGroup_ServiceIndexRevalidates(t *testing.T) {
	f := nugetGroup(t)

	first := f.get(http.MethodGet, "v3/index.json")

	etag := first.Header().Get("ETag")
	if first.Code != http.StatusOK || etag == "" {
		t.Fatalf("service index = %d etag %q", first.Code, etag)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	w := httptest.NewRecorder()
	f.h.Serve(w, req, f.grp, "v3/index.json", f.backend())

	if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Errorf("conditional service index = %d with %d bytes, want 304", w.Code, w.Body.Len())
	}

	if h := f.get(http.MethodHead, "v3/index.json"); h.Code != http.StatusOK || h.Body.Len() != 0 {
		t.Errorf("HEAD = %d with %d bytes", h.Code, h.Body.Len())
	}
}

func TestNuGetGroup_MemberDocumentFailureMapping(t *testing.T) {
	const doc = "v3/registration/lib/index.json"

	tests := []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"5xx", http.StatusInternalServerError, "internal stack trace", http.StatusBadGateway},
		{"redirect", http.StatusFound, "", http.StatusBadGateway},
		{"forbidden", http.StatusForbidden, "nope", http.StatusBadGateway},
		{"truncated json", http.StatusOK, `{"items":[{"@id":`, http.StatusBadGateway},
		{"not found", http.StatusNotFound, "", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := nugetGroup(t)
			f.set("hosted", doc, http.StatusNotFound, "")
			f.set("mirror", doc, tt.status, tt.body)

			w := f.get(http.MethodGet, doc)
			if w.Code != tt.want {
				t.Errorf("status = %d, want %d", w.Code, tt.want)
			}

			if strings.Contains(w.Body.String(), "stack trace") || strings.Contains(w.Body.String(), "nope") {
				t.Errorf("member text leaked: %q", w.Body.String())
			}
		})
	}
}

func TestNuGetGroup_OversizedMemberDocumentIsAFailure(t *testing.T) {
	f := nugetGroup(t)
	chunk := []byte(strings.Repeat(" ", 1<<20))
	be := f.backend()

	be.ServeMember = func(w http.ResponseWriter, _ *http.Request, m *models.Repository, _ string) {
		if m.Name != "mirror" {
			http.NotFound(w, nil)
			return
		}

		for range maxNuGetDocBytes>>20 + 2 {
			_, _ = w.Write(chunk)
		}
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	f.h.Serve(w, req, f.grp, "v3/registration/lib/index.json", be)

	if w.Code != http.StatusBadGateway {
		t.Errorf("oversized member document = %d, want 502", w.Code)
	}
}

func TestNuGetGroup_VersionListFailureMapping(t *testing.T) {
	const doc = "v3/package/lib/index.json"

	f := nugetGroup(t)
	f.set("hosted", doc, http.StatusNotFound, "")
	f.set("mirror", doc, http.StatusOK, "<html>not a version list</html>")

	if w := f.get(http.MethodGet, doc); w.Code != http.StatusBadGateway {
		t.Errorf("only an unreadable list = %d, want 502", w.Code)
	}

	f.set("mirror", doc, http.StatusServiceUnavailable, "down")

	if w := f.get(http.MethodGet, doc); w.Code != http.StatusBadGateway {
		t.Errorf("failing member = %d, want 502", w.Code)
	}

	// A readable list from one member survives the failure of another, but is
	// not validated as complete.
	f.set("hosted", doc, http.StatusOK, fmt.Sprintf(`{"versions":[%q]}`, "1.0.0"))

	w := f.get(http.MethodGet, doc)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "1.0.0") {
		t.Errorf("partial list = %d %q", w.Code, w.Body.String())
	}

	if w.Header().Get("ETag") != "" {
		t.Errorf("a list that skipped a failed member carries validator %q", w.Header().Get("ETag"))
	}
}
