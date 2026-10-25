// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"testing"

	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
)

type publicRepoMockDB struct {
	databasetest.BaseMockDB

	repos  map[string]*models.Repository
	target *models.PermissionTarget
}

func (m *publicRepoMockDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return m.repos[name], nil
}

func (m *publicRepoMockDB) ListPublicRepositoryNames(_ context.Context) ([]string, error) {
	var names []string

	for n, r := range m.repos {
		if r.Public {
			names = append(names, n)
		}
	}

	return names, nil
}

func (m *publicRepoMockDB) GetPermissionTargetByName(_ context.Context, _ string) (*models.PermissionTarget, error) {
	return m.target, nil
}

func newPublicRepoDB() *publicRepoMockDB {
	return &publicRepoMockDB{repos: map[string]*models.Repository{
		"pub":     {Name: "pub", Public: true},
		"priv":    {Name: "priv"},
		"pubgrp":  {Name: "pubgrp", Public: true, RepoType: "group"},
		"blocked": {Name: "blocked", Public: true, Blocked: true},
	}}
}

func TestEvaluateAnonymousPublicRepo(t *testing.T) {
	anon := Principal{IsAnonymous: true}
	ctx := context.Background()

	tests := []struct {
		name       string
		globalFlag bool
		repo       string
		action     string
		want       bool
	}{
		{"read public, flag off", false, "pub", "read", true},
		{"read public, flag on", true, "pub", "read", true},
		{"read private, flag off", false, "priv", "read", false},
		{"read unknown, flag off", false, "nope", "read", false},
		{"write public", false, "pub", "write", false},
		{"delete public", true, "pub", "delete", false},
		{"manage public", true, "pub", "manage", false},
		{"read public group, flag off", false, "pubgrp", "read", true},
		{"read private, flag on, no target", true, "priv", "read", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewEvaluator(newPublicRepoDB(), nil, tt.globalFlag)

			got, err := e.Evaluate(ctx, anon, tt.repo, "x", tt.action)
			if err != nil {
				t.Fatal(err)
			}

			if got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestEvaluateAnonymousReadTargetStillWorksForPrivate(t *testing.T) {
	db := newPublicRepoDB()
	db.target = &models.PermissionTarget{Name: "anonymous-read", RepositoryFilter: "priv", PathPattern: "*", Actions: []string{"read"}}
	e := NewEvaluator(db, nil, true)

	ok, err := e.Evaluate(context.Background(), Principal{IsAnonymous: true}, "priv", "x", "read")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestEvaluateAuthenticatedUnaffectedByPublic(t *testing.T) {
	// An authenticated principal without targets gets nothing from the
	// public flag; mock returns no targets (BaseMockDB errors), so use a
	// legacy principal check plus a user lookup that yields no targets.
	db := &userTargetsDB{publicRepoMockDB: *newPublicRepoDB()}
	e := NewEvaluator(db, nil, false)
	uid := "u1"

	ok, err := e.Evaluate(context.Background(), Principal{UserID: &uid}, "pub", "x", "read")
	if err != nil {
		t.Fatal(err)
	}

	if ok {
		t.Fatal("authenticated user without targets must not gain access from the public flag")
	}
}

type userTargetsDB struct{ publicRepoMockDB }

func (m *userTargetsDB) GetPermissionTargetsForPrincipal(_ context.Context, _ string, _ []string) ([]*models.PermissionTarget, error) {
	return nil, nil
}

func TestRepoReadFilterAnonymousIncludesPublic(t *testing.T) {
	for _, flag := range []bool{false, true} {
		e := NewEvaluator(newPublicRepoDB(), nil, flag)

		f, err := e.RepoReadFilter(context.Background(), Principal{IsAnonymous: true})
		if err != nil {
			t.Fatal(err)
		}

		for name, want := range map[string]bool{"pub": true, "pubgrp": true, "priv": false, "nope": false} {
			got, err := f(&models.Repository{Name: name})
			if err != nil {
				t.Fatal(err)
			}

			if got != want {
				t.Fatalf("flag=%v %s: got %v want %v", flag, name, got, want)
			}
		}
	}
}
