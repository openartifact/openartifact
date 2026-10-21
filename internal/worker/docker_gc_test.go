// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestSelectUnreferencedManifests(t *testing.T) {
	// m builds a manifest of image "app"; digest doubles as row id.
	m := func(digest string, mutate func(*models.DockerGCManifest)) *models.DockerGCManifest {
		g := &models.DockerGCManifest{ID: digest, Image: "app", Digest: digest}
		if mutate != nil {
			mutate(g)
		}

		return g
	}
	tagged := func(g *models.DockerGCManifest) { g.Tagged = true }
	young := func(g *models.DockerGCManifest) { g.Young = true }
	children := func(c ...string) func(*models.DockerGCManifest) {
		return func(g *models.DockerGCManifest) { g.Children = c }
	}
	subject := func(s string) func(*models.DockerGCManifest) {
		return func(g *models.DockerGCManifest) { g.SubjectDigest = s }
	}

	tests := []struct {
		name      string
		manifests []*models.DockerGCManifest
		want      []string
	}{
		{
			name:      "untagged old manifest is collected, tagged one kept",
			manifests: []*models.DockerGCManifest{m("a", tagged), m("b", nil)},
			want:      []string{"b"},
		},
		{
			name:      "young manifest is kept",
			manifests: []*models.DockerGCManifest{m("a", young)},
		},
		{
			name: "children of a tagged index are kept",
			manifests: []*models.DockerGCManifest{
				m("idx", func(g *models.DockerGCManifest) { tagged(g); children("c1", "c2")(g) }),
				m("c1", nil), m("c2", nil), m("other", nil),
			},
			want: []string{"other"},
		},
		{
			name: "children of an untagged old index are collected with it, index first",
			manifests: []*models.DockerGCManifest{
				m("c1", nil), m("idx", children("c1")),
			},
			want: []string{"idx", "c1"},
		},
		{
			name: "nested index keeps grandchildren",
			manifests: []*models.DockerGCManifest{
				m("top", func(g *models.DockerGCManifest) { tagged(g); children("mid")(g) }),
				m("mid", children("leaf")), m("leaf", nil),
			},
		},
		{
			name: "subject of a tagged referrer is kept",
			manifests: []*models.DockerGCManifest{
				m("sig", func(g *models.DockerGCManifest) { tagged(g); subject("img")(g) }),
				m("img", nil),
			},
		},
		{
			name: "referrer of a tagged subject is kept",
			manifests: []*models.DockerGCManifest{
				m("img", tagged), m("sbom", subject("img")), m("lonely", nil),
			},
			want: []string{"lonely"},
		},
		{
			name: "untagged subject with untagged referrer are both collected",
			manifests: []*models.DockerGCManifest{
				m("img", nil), m("sbom", subject("img")),
			},
			want: []string{"img", "sbom"},
		},
		{
			name: "a young referrer keeps its old subject and that subject's other referrers",
			manifests: []*models.DockerGCManifest{
				m("img", nil), m("sbom", subject("img")), m("new", func(g *models.DockerGCManifest) { young(g); subject("img")(g) }),
			},
		},
		{
			name: "same digest in another image is independent",
			manifests: []*models.DockerGCManifest{
				{ID: "1", Image: "app", Digest: "d", Tagged: true},
				{ID: "2", Image: "other", Digest: "d"},
			},
			want: []string{"2"},
		},
		{
			name: "index child lookup stays inside the image",
			manifests: []*models.DockerGCManifest{
				{ID: "1", Image: "app", Digest: "idx", Tagged: true, Children: []string{"c"}},
				{ID: "2", Image: "other", Digest: "c"},
			},
			want: []string{"2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, d := range selectUnreferencedManifests(tt.manifests) {
				got = append(got, d.ID)
			}

			if !slices.Equal(got, tt.want) {
				t.Errorf("collected %v, want %v", got, tt.want)
			}
		})
	}
}
