// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// PathReadFilter must answer like Evaluate does for the same (repository,
// path): a target limited to part of a repository grants exactly that part, a
// project role grants all of it.
func TestPathReadFilterMatchesEvaluate(t *testing.T) {
	db := newProjDB(g(projA, models.ProjectRoleViewer))
	db.targets = []*models.PermissionTarget{{
		ID: "t1", Name: "team-images", RepositoryFilter: "^in-b$", PathPattern: "^team/.*", Actions: []string{"read"},
	}}
	e := NewEvaluator(db, nil, false)
	user := projUser()

	filter, err := e.PathReadFilter(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		repo, path string
		want       bool
	}{
		{"in-b", "team/api/tags/list", true},
		{"in-b", "other/tags/list", false},
		{"in-b", "tags/list", false},
		{"in-a", "anything/tags/list", true}, // project viewer reads the whole repository
		{"in-sys", "team/api/tags/list", false},
	}

	for _, tt := range tests {
		got, err := filter(db.repos[tt.repo], tt.path)
		if err != nil {
			t.Fatal(err)
		}

		if got != tt.want {
			t.Errorf("filter(%s, %s) = %v, want %v", tt.repo, tt.path, got, tt.want)
		}

		viaEvaluate, err := e.Evaluate(context.Background(), user, tt.repo, tt.path, "read")
		if err != nil {
			t.Fatal(err)
		}

		if viaEvaluate != got {
			t.Errorf("Evaluate(%s, %s) = %v but the filter said %v", tt.repo, tt.path, viaEvaluate, got)
		}
	}
}
