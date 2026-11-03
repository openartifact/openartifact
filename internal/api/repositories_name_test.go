// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
)

// A name that starts with a hyphen or underscore, a flag such as --help in
// particular, is refused on create; ordinary names, also with inner hyphens and
// underscores, still pass validation.
func TestRepositoriesCreateRejectsNameNotStartingAlphanumeric(t *testing.T) {
	for _, name := range []string{"--help", "-h", "-x", "_x", "-"} {
		log := newFixTestLogger(t)
		db := &repoCreateMockDB{targets: adminTargets()}
		h := NewRepositoriesHandler(db, log, auth.NewEvaluator(db, log, false))

		rw := httptest.NewRecorder()
		h.handleCreate(rw, adminRequest(db, http.MethodPost, "/api/v1/repositories", `{"name":"`+name+`","format":"npm"}`))

		if rw.Code != http.StatusBadRequest {
			t.Errorf("name %q: status = %d, want 400; body = %s", name, rw.Code, rw.Body.String())
		}
	}
}

func TestRepoAndProjectNameRegexps(t *testing.T) {
	valid := []string{"a", "0", "my-repo", "my_repo", "Repo-1", "a-"}
	invalid := []string{"", "-", "--help", "-h", "_a", "a b", "a/b", "a.b"}

	for _, n := range valid {
		if !repoNameRe().MatchString(n) || !projectNameRe().MatchString(n) {
			t.Errorf("%q should be valid", n)
		}
	}

	for _, n := range invalid {
		if repoNameRe().MatchString(n) || projectNameRe().MatchString(n) {
			t.Errorf("%q should be invalid", n)
		}
	}

	long := make([]byte, 101)
	for i := range long {
		long[i] = 'a'
	}

	if repoNameRe().Match(long) || !repoNameRe().Match(long[:100]) {
		t.Error("length limit must stay 100")
	}
}
