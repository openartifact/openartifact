// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
