// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/ocipath"
)

// A merge that left a failed member out is incomplete and may be missing
// versions the member has: it must not be given an ETag or be cacheable, or a
// client would keep and revalidate the incomplete answer after the member is
// back.
func TestGroupMerge_PartialResultIsNotCacheable(t *testing.T) {
	cases := []struct {
		name string
		make func(t *testing.T) *fixture
		// setup fills the members; it is called once with the failing mirror,
		// once with a healthy one.
		setup func(t *testing.T, f *fixture, mirrorOK bool)
		path  string
	}{
		{
			"npm", npmGroup,
			func(t *testing.T, f *fixture, ok bool) {
				t.Helper()

				f.set(
					"hosted",
					"lib",
					200,
					packument(t, "hosted", "lib", map[string]string{"1.0.0": "AAAA"}, map[string]string{"latest": "1.0.0"}),
				)

				if ok {
					f.set(
						"mirror",
						"lib",
						200,
						packument(t, "mirror", "lib", map[string]string{"2.0.0": "BBBB"}, map[string]string{"next": "2.0.0"}),
					)
				} else {
					f.set("mirror", "lib", 502, "down")
				}
			},
			"lib",
		},
		{
			"go", goGroup,
			func(t *testing.T, f *fixture, ok bool) {
				t.Helper()

				f.set("hosted", goList1, 200, "v1.0.0")

				if ok {
					f.set("mirror", goList1, 200, "v1.1.0")
				} else {
					f.set("mirror", goList1, 503, "down")
				}
			},
			goList1,
		},
		{
			"helm", helmGroup,
			func(t *testing.T, f *fixture, ok bool) {
				t.Helper()

				f.set("hosted", "index.yaml", 200, memberIndex("hosted", map[string][]string{"app": {"1.0.0"}}))

				if ok {
					f.set("mirror", "index.yaml", 200, memberIndex("mirror", map[string][]string{"app": {"2.0.0"}}))
				} else {
					f.set("mirror", "index.yaml", 502, "down")
				}
			},
			"index.yaml",
		},
		{
			"cargo", cargoGroup,
			func(t *testing.T, f *fixture, ok bool) {
				t.Helper()

				f.set("hosted", "index/de/mo/demo", 200, cargoLine("hosted", "1.0.0"))

				if ok {
					f.set("mirror", "index/de/mo/demo", 200, cargoLine("mirror", "2.0.0"))
				} else {
					f.set("mirror", "index/de/mo/demo", 500, "boom")
				}
			},
			"index/de/mo/demo",
		},
		{
			"nuget versions", nugetGroup,
			func(t *testing.T, f *fixture, ok bool) {
				t.Helper()

				f.set("hosted", "v3/package/lib.a/index.json", 200, `{"versions":["1.0.0"]}`)

				if ok {
					f.set("mirror", "v3/package/lib.a/index.json", 200, `{"versions":["2.0.0"]}`)
				} else {
					f.set("mirror", "v3/package/lib.a/index.json", 500, "boom")
				}
			},
			"v3/package/lib.a/index.json",
		},
		{
			"nuget registration", nugetGroup,
			func(t *testing.T, f *fixture, ok bool) {
				t.Helper()

				f.set("hosted", "v3/registration/lib/index.json", 200, nugetInlineIndex("hosted", "lib", "1.0.0"))

				if ok {
					f.set("mirror", "v3/registration/lib/index.json", 200, nugetInlineIndex("mirror", "lib", "2.0.0"))
				} else {
					f.set("mirror", "v3/registration/lib/index.json", 500, "boom")
				}
			},
			"v3/registration/lib/index.json",
		},
	}

	for _, c := range cases {
		t.Run(c.name+"/member failed", func(t *testing.T) {
			f := c.make(t)
			c.setup(t, f, false)

			w := f.get(http.MethodGet, c.path)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d, want the healthy member's merge: %s", w.Code, w.Body.String())
			}

			if got := w.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}

			if got := w.Header().Get("ETag"); got != "" {
				t.Errorf("ETag = %q, want none for a partial merge", got)
			}
		})

		t.Run(c.name+"/all members answered", func(t *testing.T) {
			f := c.make(t)
			c.setup(t, f, true)

			w := f.get(http.MethodGet, c.path)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}

			if got := w.Header().Get("Cache-Control"); got == "no-store" {
				t.Errorf("Cache-Control = %q, a complete merge is cacheable", got)
			}

			if w.Header().Get("ETag") == "" {
				t.Error("a complete merge carries an ETag")
			}
		})
	}
}

// HEAD /v2/<group>/<image>/tags/list is answered from the members' tag lists.
// Members answer a HEAD without a body, so the group must ask them with GET.
func TestOCIGroup_HeadTagsListAsksMembersWithGet(t *testing.T) {
	f := newFixture(t, "docker", member("hosted", "docker", "hosted"), member("mirror", "docker", "proxy"))

	var methods []string

	be := Backend{
		Authorize: func(*http.Request, *models.Repository, string) bool { return true },
		ServeMember: func(w http.ResponseWriter, r *http.Request, m *models.Repository, _ string) {
			methods = append(methods, m.Name+" "+r.Method)

			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusBadGateway)
				return
			}

			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, fmt.Sprintf(`{"name":"img","tags":["%s"]}`, m.Name))
		},
	}

	req := ocipath.Request{Kind: ocipath.KindTags, Name: "all/img"}

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r := httptest.NewRequestWithContext(context.Background(), method, "/v2/all/img/tags/list", nil)
		w := httptest.NewRecorder()
		f.h.ServeOCI(w, r, f.grp, req, "img/tags/list", be)

		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", method, w.Code)
		}

		if method == http.MethodHead && w.Body.Len() != 0 {
			t.Errorf("HEAD carried a body: %q", w.Body.String())
		}

		if method == http.MethodHead && w.Header().Get("Content-Length") == "" {
			t.Error("HEAD without Content-Length")
		}

		if method == http.MethodGet && !strings.Contains(w.Body.String(), `"hosted","mirror"`) {
			t.Errorf("GET body = %s, want the union", w.Body.String())
		}
	}

	for _, m := range methods {
		if !strings.HasSuffix(m, " GET") {
			t.Errorf("member asked with %q, want GET", m)
		}
	}
}
