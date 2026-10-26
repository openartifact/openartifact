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
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/ocipath"
)

const groupProxyManifest = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","annotations":{"from":"proxy"}}`

// dockerGroupFixture is a docker group of [hosted, proxy] over a real
// Postgres-backed hosted registry; the proxy member is a stub that holds
// proxied/app:1 and team/app:2.
type dockerGroupFixture struct {
	t   *testing.T
	reg *dockerRegistry
	grp *models.Repository
	h   *group.Handler
}

func newDockerGroupFixture(t *testing.T) *dockerGroupFixture {
	t.Helper()

	reg := newDockerRegistry(t)
	systemOrg := "00000000-0000-0000-0000-000000000001"

	create := func(name, repoType string) *models.Repository {
		repo := &models.Repository{Name: name, Format: "docker", RepoType: repoType, ProjectID: &systemOrg}
		if repoType == "proxy" {
			repo.UpstreamURL = "http://upstream.invalid"
		}

		if err := reg.pg.CreateRepository(context.Background(), repo); err != nil {
			t.Fatalf("CreateRepository(%s): %v", name, err)
		}

		return repo
	}

	hosted := create("dhosted", "hosted")
	proxy := create("dremote", "proxy")
	grp := create("dgroup", "group")

	for i, m := range []*models.Repository{hosted, proxy} {
		if err := reg.pg.AddGroupMember(context.Background(), grp.ID, m.ID, i); err != nil {
			t.Fatalf("AddGroupMember(%s): %v", m.Name, err)
		}
	}

	return &dockerGroupFixture{t: t, reg: reg, grp: grp, h: group.NewHandler(newTestLogger(t), reg.pg)}
}

// proxyMember answers like a docker proxy repository whose upstream holds
// proxied/app:1 and team/app:2.
func proxyMember(w http.ResponseWriter, relPath string) {
	switch relPath {
	case "proxied/app/manifests/1", "team/app/manifests/2":
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		_, _ = io.WriteString(w, groupProxyManifest)
	case "proxied/app/tags/list":
		_, _ = io.WriteString(w, `{"name":"proxied/app","tags":["1"]}`)
	case "team/app/tags/list":
		_, _ = io.WriteString(w, `{"name":"team/app","tags":["2"]}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"errors":[{"code":"NAME_UNKNOWN"}]}`)
	}
}

func (f *dockerGroupFixture) get(path string) *httptest.ResponseRecorder {
	f.t.Helper()

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v2/dgroup/"+path, nil)
	w := httptest.NewRecorder()

	f.h.ServeOCI(w, r, f.grp, ocipath.Parse("dgroup/"+path), path, group.Backend{
		Authorize: func(*http.Request, *models.Repository, string) bool { return true },
		ServeMember: func(w http.ResponseWriter, r *http.Request, m *models.Repository, relPath string) {
			if m.RepoType == "proxy" {
				proxyMember(w, relPath)
				return
			}

			// The hosted registry handler, as a direct request would reach it.
			r = r.Clone(r.Context())
			r.URL.Path = "/v2/" + m.Name + "/" + relPath
			f.reg.router.ServeHTTP(w, r)
		},
	})

	return w
}

// An empty hosted member holds no image, so it must not shadow the proxy
// members (issue 233); an image a hosted member holds stays hosted-only unless
// the group merges proxy versions.
func TestDockerGroup_EmptyHostedDoesNotShadowProxy(t *testing.T) {
	f := newDockerGroupFixture(t)

	for p, want := range map[string]string{
		"proxied/app/manifests/1": groupProxyManifest,
		"proxied/app/tags/list":   `{"name":"dgroup/proxied/app","tags":["1"]}`,
		"team/app/manifests/2":    groupProxyManifest,
	} {
		if w := f.get(p); w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("empty hosted member: %s = %d %s, want the proxy's answer %s", p, w.Code, w.Body.String(), want)
		}
	}

	// The hosted member now holds team/app: its tags are the only ones.
	hostedManifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","annotations":{"from":"hosted"}}`
	if rec := f.reg.putManifest("dhosted/team/app", "1", hostedManifest); rec.Code != http.StatusCreated {
		t.Fatalf("push to hosted = %d; %s", rec.Code, rec.Body.String())
	}

	if w := f.get("team/app/manifests/1"); w.Code != http.StatusOK || w.Body.String() != hostedManifest {
		t.Errorf("hosted tag = %d %s, want the hosted manifest", w.Code, w.Body.String())
	}

	if w := f.get("team/app/manifests/2"); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "proxy") {
		t.Errorf("proxy tag of a hosted image = %d %s, want 404 (dependency confusion)", w.Code, w.Body.String())
	}

	if w := f.get("team/app/tags/list"); w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"2"`) {
		t.Errorf("tags of a hosted image = %d %s, want the hosted tags only", w.Code, w.Body.String())
	}

	// An image only the proxy holds is still served.
	if w := f.get("proxied/app/manifests/1"); w.Code != http.StatusOK || w.Body.String() != groupProxyManifest {
		t.Errorf("proxy-only image = %d %s", w.Code, w.Body.String())
	}

	f.grp.MergeProxyVersions = true

	if w := f.get("team/app/manifests/2"); w.Code != http.StatusOK || w.Body.String() != groupProxyManifest {
		t.Errorf("merge_proxy_versions: proxy tag = %d %s, want the proxy manifest", w.Code, w.Body.String())
	}
}
