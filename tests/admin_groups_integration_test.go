// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

func groupMemberNames(t *testing.T, pg *database.PostgresDB, groupID string) []string {
	t.Helper()

	users, err := pg.ListUsersByGroup(context.Background(), groupID)
	if err != nil {
		t.Fatalf("ListUsersByGroup: %v", err)
	}

	names := make([]string, 0, len(users))
	for _, u := range users {
		names = append(names, u.Username)
	}

	slices.Sort(names)

	return names
}

// PUT /groups/{id}/members replaces the membership as one change: a member list
// naming a user that does not exist is refused with 400 and leaves the current
// members in place, instead of removing them first.
func TestSetGroupMembersIsAllOrNothing(t *testing.T) {
	pg := freshPostgres(t, 5)
	ctx := context.Background()

	var users []*models.User

	for _, name := range []string{"alice", "bob", "carol"} {
		u, err := pg.CreateUser(ctx, &models.User{Username: name})
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", name, err)
		}

		users = append(users, u)
	}

	alice, bob, carol := users[0], users[1], users[2]

	grp, err := pg.CreateGroup(ctx, &models.Group{Name: "team"})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	for _, u := range []*models.User{alice, bob} {
		if err := pg.AddUserToGroup(ctx, u.ID, grp.ID); err != nil {
			t.Fatalf("AddUserToGroup(%s): %v", u.Username, err)
		}
	}

	log := newTestLogger(t)
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(middleware.WithPrincipal(r.Context(), &auth.Principal{Username: "admin", IsLegacy: true})))
		})
	})
	api.NewAdminGroupsHandler(pg, log, auth.NewEvaluator(pg, log, false)).Register(router)

	put := func(members ...string) *httptest.ResponseRecorder {
		quoted := make([]string, len(members))
		for i, m := range members {
			quoted[i] = `"` + m + `"`
		}

		req := httptest.NewRequestWithContext(ctx, http.MethodPut, "/api/v1/groups/"+grp.ID+"/members",
			strings.NewReader(`{"members":[`+strings.Join(quoted, ",")+`]}`))
		req.Header.Set("Content-Type", "application/json")

		rw := httptest.NewRecorder()
		router.ServeHTTP(rw, req)

		return rw
	}

	// Alice stays, Bob would be removed, a user that does not exist would be added.
	if rw := put(alice.ID, uuid.NewString()); rw.Code != http.StatusBadRequest {
		t.Fatalf("PUT with an unknown user = %d, want 400: %s", rw.Code, rw.Body)
	}

	if got := groupMemberNames(t, pg, grp.ID); !slices.Equal(got, []string{"alice", "bob"}) {
		t.Fatalf("members after the rejected PUT = %v, want [alice bob] unchanged", got)
	}

	// A valid replacement removes Bob and adds Carol.
	if rw := put(alice.ID, carol.ID); rw.Code != http.StatusNoContent {
		t.Fatalf("PUT = %d, want 204: %s", rw.Code, rw.Body)
	}

	if got := groupMemberNames(t, pg, grp.ID); !slices.Equal(got, []string{"alice", "carol"}) {
		t.Fatalf("members after the PUT = %v, want [alice carol]", got)
	}

	// An empty list clears the group.
	if rw := put(); rw.Code != http.StatusNoContent {
		t.Fatalf("PUT [] = %d, want 204: %s", rw.Code, rw.Body)
	}

	if got := groupMemberNames(t, pg, grp.ID); len(got) != 0 {
		t.Fatalf("members after PUT [] = %v, want none", got)
	}
}
