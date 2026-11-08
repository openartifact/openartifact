// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/ocipath"
)

// tagsGroup serves a docker group whose mirror member paginates two tags per
// page with a rel="next" Link, as a proxy forwarding a registry's default page
// does. brokenLink makes every page point at the same next page.
func tagsGroup(t *testing.T, brokenLink, noLast bool) *httptest.ResponseRecorder {
	t.Helper()

	f := newFixture(t, "docker", member("hosted", "docker", "hosted"), member("mirror", "docker", "proxy"))

	pages := map[string][]string{
		"":   {"a", "b"},
		"b":  {"c", "d"},
		"d":  {"e"},
		"zz": {"never"},
	}
	next := map[string]string{"": "b", "b": "d"}

	be := Backend{
		Authorize: func(*http.Request, *models.Repository, string) bool { return true },
		ServeMember: func(w http.ResponseWriter, r *http.Request, m *models.Repository, _ string) {
			w.Header().Set("Content-Type", "application/json")

			if m.Name == "hosted" {
				_ = json.NewEncoder(w).Encode(map[string]any{"name": "img", "tags": []string{"h1"}})
				return
			}

			last := r.URL.Query().Get("last")
			if n, ok := next[last]; ok {
				switch {
				case noLast:
					w.Header().Set("Link", `</v2/mirror/img/tags/list>; rel="next"`)
				case brokenLink:
					w.Header().Set("Link", `</v2/mirror/img/tags/list?last=b&n=2>; rel="next"`)
				default:
					w.Header().Set("Link", `</v2/mirror/img/tags/list?last=`+n+`&n=2>; rel="next"`)
				}
			}

			_ = json.NewEncoder(w).Encode(map[string]any{"name": "img", "tags": pages[last]})
		},
	}

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v2/all/img/tags/list", nil)
	w := httptest.NewRecorder()
	f.h.ServeOCI(w, r, f.grp, ocipath.Request{Kind: ocipath.KindTags, Name: "all/img"}, "img/tags/list", be)

	return w
}

func TestOCIGroup_TagsFollowMemberPagination(t *testing.T) {
	w := tagsGroup(t, false, false)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var doc struct {
		Tags []string `json:"tags"`
	}

	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}

	want := []string{"a", "b", "c", "d", "e", "h1"}
	if got := w.Header().Get("Cache-Control"); got == "no-store" {
		t.Errorf("Cache-Control = %q on a complete list", got)
	}

	if len(doc.Tags) != len(want) {
		t.Fatalf("tags = %v, want %v (every page of the paginating member)", doc.Tags, want)
	}

	for i := range want {
		if doc.Tags[i] != want[i] {
			t.Fatalf("tags = %v, want %v", doc.Tags, want)
		}
	}
}

// A member whose next page cannot be followed is an incomplete listing: the
// group must not let the truncated union be cached.
func TestOCIGroup_TagsUnfollowableLinkIsReportedPartial(t *testing.T) {
	for name, tc := range map[string]struct{ broken, noLast bool }{
		"link that does not advance": {true, false},
		"link without last":          {false, true},
	} {
		t.Run(name, func(t *testing.T) {
			w := tagsGroup(t, tc.broken, tc.noLast)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}

			if got := w.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store for a possibly incomplete list", got)
			}
		})
	}
}

// A member the caller may not read contributes nothing: not its tags, not its
// manifests, not its blobs.
func TestOCIGroup_DeniedMemberIsNeverServed(t *testing.T) {
	f := newFixture(t, "docker", member("hosted", "docker", "hosted"), member("mirror", "docker", "proxy"))
	denied := map[string]bool{"hosted": true}

	be := Backend{
		Authorize: func(_ *http.Request, m *models.Repository, _ string) bool { return !denied[m.Name] },
		ServeMember: func(w http.ResponseWriter, r *http.Request, m *models.Repository, _ string) {
			if m.Name == "mirror" && !strings.HasSuffix(r.URL.Path, "/tags/list") {
				http.NotFound(w, r)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "img", "tags": []string{m.Name + "-tag"}})
		},
	}

	serve := func(kind ocipath.Kind, rel, ref string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/"+rel, nil)
		w := httptest.NewRecorder()
		f.h.ServeOCI(w, r, f.grp, ocipath.Request{Kind: kind, Name: "all/img", Ref: ref}, rel, be)

		return w
	}

	w := serve(ocipath.KindTags, "img/tags/list", "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "hosted-tag") || !strings.Contains(w.Body.String(), "mirror-tag") {
		t.Errorf("tags = %d %s, want only the mirror's", w.Code, w.Body.String())
	}

	for _, c := range []struct {
		kind    ocipath.Kind
		rel     string
		ref     string
		wantMsg string
	}{
		{ocipath.KindManifest, "img/manifests/v1", "v1", "MANIFEST_UNKNOWN"},
		{ocipath.KindBlob, "img/blobs/sha256:abc", "sha256:abc", "BLOB_UNKNOWN"},
	} {
		w := serve(c.kind, c.rel, c.ref)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), c.wantMsg) {
			t.Errorf("%s held only by a denied member = %d %s, want 404 %s", c.rel, w.Code, w.Body.String(), c.wantMsg)
		}
	}
}
