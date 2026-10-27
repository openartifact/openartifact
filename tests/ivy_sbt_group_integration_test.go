// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/ivy"
	"github.com/openartifact/openartifact/internal/format/sbt"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/storage"
)

// An ivy or sbt group over a real hosted repository and a stub proxy: a module
// the hosted member holds is served by it alone, in either spelling of its
// group, while a module only the proxy has comes from the proxy.
func TestIvySbtGroupHostedWinsAgainstPostgres(t *testing.T) {
	pg := freshPostgres(t, 5)

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	for _, tc := range []struct {
		format string
		reg    func() func(chi.Router)
		hosted string // a file the hosted member holds
		proxyV string // another version of the same module, only the proxy has it
		other  string // a module only the proxy has
	}{
		{
			"ivy",
			func() func(chi.Router) {
				return ivy.NewHTTPHandler(newTestLogger(t), pg, store, "http://localhost").RegisterRepoRoutes
			},
			"org.example/lib/1.0/jars/lib.jar", "org.example/lib/2.0/jars/lib.jar", "org.example/other/1.0/jars/other.jar",
		},
		{
			"sbt",
			func() func(chi.Router) {
				return sbt.NewHTTPHandler(newTestLogger(t), pg, store, "http://localhost").RegisterRepoRoutes
			},
			"com/example/lib_2.13/1.0/lib_2.13-1.0.jar", "com/example/lib_2.13/2.0/lib_2.13-2.0.jar",
			"com/example/other_2.13/1.0/other_2.13-1.0.jar",
		},
	} {
		t.Run(tc.format, func(t *testing.T) {
			systemOrg := "00000000-0000-0000-0000-000000000001"
			create := func(name, repoType string) *models.Repository {
				repo := &models.Repository{Name: name, Format: tc.format, RepoType: repoType, ProjectID: &systemOrg}
				if repoType == "proxy" {
					repo.UpstreamURL = "http://upstream.invalid"
				}

				if err := pg.CreateRepository(context.Background(), repo); err != nil {
					t.Fatalf("CreateRepository(%s): %v", name, err)
				}

				return repo
			}

			hosted := create(tc.format+"-hosted", "hosted")
			proxy := create(tc.format+"-remote", "proxy")
			grp := create(tc.format+"-all", "group")

			for i, m := range []*models.Repository{hosted, proxy} {
				if err := pg.AddGroupMember(context.Background(), grp.ID, m.ID, i); err != nil {
					t.Fatalf("AddGroupMember(%s): %v", m.Name, err)
				}
			}

			srv := newFormatServer(t, tc.reg())
			srv.must(http.MethodPut, "/repos/"+hosted.Name+"/"+tc.hosted, "hosted-jar", http.StatusCreated)

			proxyHolds := map[string]bool{tc.proxyV: true, tc.other: true, tc.hosted: true}
			h := group.NewHandler(newTestLogger(t), pg)

			get := func(relPath string) *httptest.ResponseRecorder {
				r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/repos/"+grp.Name+"/"+relPath, nil)
				w := httptest.NewRecorder()

				h.Serve(w, r, grp, relPath, group.Backend{
					Authorize: func(*http.Request, *models.Repository, string) bool { return true },
					ServeMember: func(w http.ResponseWriter, r *http.Request, m *models.Repository, rel string) {
						if m.RepoType != "proxy" {
							r = r.Clone(r.Context())
							r.URL.Path = "/repos/" + m.Name + "/" + rel
							srv.router.ServeHTTP(w, r)

							return
						}

						if !proxyHolds[rel] {
							http.NotFound(w, r)
							return
						}

						_, _ = io.WriteString(w, "proxy-jar")
					},
				})

				return w
			}

			if w := get(tc.hosted); w.Code != http.StatusOK || w.Body.String() != "hosted-jar" {
				t.Errorf("hosted file = %d %q, want the hosted member's", w.Code, w.Body.String())
			}

			if w := get(tc.proxyV); w.Code != http.StatusNotFound {
				t.Errorf("proxy's other version of a hosted module = %d %q, want 404 (dependency confusion)", w.Code, w.Body.String())
			}

			if w := get(tc.other); w.Code != http.StatusOK || w.Body.String() != "proxy-jar" {
				t.Errorf("module only the proxy has = %d %q", w.Code, w.Body.String())
			}

			// Deleting the hosted file releases the name to the proxy.
			srv.must(http.MethodDelete, "/repos/"+hosted.Name+"/"+tc.hosted, "", http.StatusNoContent)

			if w := get(tc.proxyV); w.Code != http.StatusOK || w.Body.String() != "proxy-jar" {
				t.Errorf("after the hosted module is gone = %d %q, want the proxy's", w.Code, w.Body.String())
			}
		})
	}
}
