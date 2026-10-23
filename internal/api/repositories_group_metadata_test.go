// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
)

// recordingGroupMetadata records the invalidations the handlers request.
type recordingGroupMetadata struct {
	stale, members []string
}

func (r *recordingGroupMetadata) NotifyStale(groupID string) { r.stale = append(r.stale, groupID) }
func (r *recordingGroupMetadata) NotifyMemberChanged(repoID string) {
	r.members = append(r.members, repoID)
}

// groupsOfMemberDB reports a fixed set of deb/rpm groups for every member.
type groupsOfMemberDB struct {
	repoLifecycleDB

	groups []string
}

func (db *groupsOfMemberDB) ListMetadataGroupsOfMember(context.Context, string) ([]string, error) {
	return db.groups, nil
}

func newGroupMetadataHandler(t *testing.T, db *groupsOfMemberDB) (*RepositoriesHandler, *recordingGroupMetadata) {
	t.Helper()

	db.targets = adminTargets()
	rec := &recordingGroupMetadata{}
	log := newFixTestLogger(t)

	return NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false)).WithGroupMetadata(rec), rec
}

func TestCreatingDebAndRPMReposInvalidatesGroupMetadata(t *testing.T) {
	for name, tc := range map[string]struct {
		body        string
		wantStale   bool
		wantMembers bool
	}{
		"deb group":    {`{"name":"g","format":"deb","repo_type":"group"}`, true, false},
		"rpm group":    {`{"name":"g","format":"rpm","repo_type":"group"}`, true, false},
		"hosted deb":   {`{"name":"h","format":"deb"}`, false, true},
		"hosted rpm":   {`{"name":"h","format":"rpm"}`, false, true},
		"other format": {`{"name":"h","format":"raw"}`, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			h, rec := newGroupMetadataHandler(t, &groupsOfMemberDB{})

			rw := httptest.NewRecorder()
			h.handleCreate(rw, adminRequest(nil, http.MethodPost, "/api/v1/repositories", tc.body))

			if rw.Code != http.StatusCreated {
				t.Fatalf("status %d: %s", rw.Code, rw.Body.String())
			}

			if got := slices.Equal(rec.stale, []string{"repo-new"}); got != tc.wantStale || len(rec.stale) > 1 {
				t.Errorf("stale notifications = %v, want one: %v", rec.stale, tc.wantStale)
			}

			if got := slices.Equal(rec.members, []string{"repo-new"}); got != tc.wantMembers || len(rec.members) > 1 {
				t.Errorf("member notifications = %v, want one: %v", rec.members, tc.wantMembers)
			}
		})
	}
}

func TestDeletingAMemberRegeneratesItsGroups(t *testing.T) {
	db := &groupsOfMemberDB{groups: []string{"group-1", "group-2"}}
	h, rec := newGroupMetadataHandler(t, db)

	rw := httptest.NewRecorder()
	h.handleCreate(rw, adminRequest(nil, http.MethodPost, "/api/v1/repositories", `{"name":"debs","format":"deb"}`))

	if rw.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rw.Code, rw.Body.String())
	}

	rec.stale, rec.members = nil, nil

	router := chi.NewRouter()
	router.Delete("/api/v1/repositories/{name}", h.handleDelete)

	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, adminRequest(nil, http.MethodDelete, "/api/v1/repositories/debs?confirm=debs", ""))

	if rw.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rw.Code, rw.Body.String())
	}

	h.WaitBackground()

	if !slices.Equal(rec.stale, []string{"group-1", "group-2"}) {
		t.Errorf("groups notified = %v, want both groups the member was in", rec.stale)
	}
}

func TestDeletingANonDebRepositoryDoesNotTouchGroupMetadata(t *testing.T) {
	db := &groupsOfMemberDB{groups: []string{"group-1"}}
	h, rec := newGroupMetadataHandler(t, db)

	rw := httptest.NewRecorder()
	h.handleCreate(rw, adminRequest(nil, http.MethodPost, "/api/v1/repositories", `{"name":"files","format":"raw"}`))

	router := chi.NewRouter()
	router.Delete("/api/v1/repositories/{name}", h.handleDelete)

	rw = httptest.NewRecorder()
	router.ServeHTTP(rw, adminRequest(nil, http.MethodDelete, "/api/v1/repositories/files?confirm=files", ""))
	h.WaitBackground()

	if len(rec.stale) != 0 || len(rec.members) != 0 {
		t.Errorf("notified %v %v for a raw repository", rec.stale, rec.members)
	}
}

func TestCreatingDebRPMGroupsIsAccepted(t *testing.T) {
	for _, format := range []string{"deb", "rpm"} {
		db := newProxyGroupDB()

		rw := createRepo(t, db, `{"name":"g","format":"`+format+`","repo_type":"group"}`)
		if rw.Code != http.StatusCreated || db.created == nil || db.created.RepoType != "group" {
			t.Errorf("%s group: status %d body %s", format, rw.Code, rw.Body.String())
		}
	}
}
