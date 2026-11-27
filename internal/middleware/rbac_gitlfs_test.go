// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/gitlfs"
	"github.com/openartifact/openartifact/pkg/logger"
)

const lfsOID = "4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393"

// noGrantsDB is benchRBACDB for requests that fall through to the project
// grants (a denied one): the user has none.
type noGrantsDB struct{ *benchRBACDB }

func (noGrantsDB) ListProjectGrantsByUser(context.Context, string) ([]*models.ProjectGrant, error) {
	return nil, nil
}

// readOnlyRBAC serves RBACMiddleware over a user that may only read repository
// "abc", and the bearer token of that user. next answers 200 with the request
// body it received.
func readOnlyRBAC(t *testing.T) (http.Handler, string) {
	t.Helper()

	log, err := logger.New("error", "json", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	db := &benchRBACDB{
		targets:  []*models.PermissionTarget{{ID: "ro", RepositoryFilter: "abc", PathPattern: ".*", Actions: []string{"read"}}},
		repos:    map[string]*models.Repository{"abc": {Name: "abc", Format: "gitlfs"}},
		tokens:   map[string]*models.Token{},
		users:    map[string]*models.User{"user-1": {ID: "user-1", Username: "jane"}},
		sessions: map[string]*models.Session{},
	}

	raw, tok, err := auth.CreateToken(t.Context(), db, auth.TokenKindAccess, "ci", "user-1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	tok.LastUsedAt = &now
	db.tokens[tok.TokenHash] = tok

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write(body)
	})

	return RBACMiddleware(RBACConfig{}, noGrantsDB{db}, auth.NewEvaluator(noGrantsDB{db}, log, false), log)(next), raw
}

// A batch is a POST, but a "download" batch only asks where objects can be
// fetched, so a read-only principal may send it; every other batch is a write,
// whatever the body looks like.
func TestRBACGitLFSBatchNeedsWriteUnlessDownload(t *testing.T) {
	handler, token := readOnlyRBAC(t)

	const batch = "/repos/gitlfs/abc/objects/batch"

	object := `"objects":[{"oid":"` + lfsOID + `","size":3}]`
	pad := strings.Repeat(" ", gitlfs.MaxBatchBytes)

	tests := []struct {
		name string
		path string
		body string
		want int
	}{
		{"download", batch, `{"operation":"download",` + object + `}`, http.StatusOK},
		{"download, key order swapped", batch, `{` + object + `,"operation":"download"}`, http.StatusOK},
		{"upload", batch, `{"operation":"upload",` + object + `}`, http.StatusForbidden},
		{"unknown operation", batch, `{"operation":"delete",` + object + `}`, http.StatusForbidden},
		{"no operation", batch, `{` + object + `}`, http.StatusForbidden},
		{"not JSON", batch, `download`, http.StatusForbidden},
		{"empty body", batch, ``, http.StatusForbidden},
		{"download then upload (the last duplicate wins)", batch, `{"operation":"download","operation":"upload"}`, http.StatusForbidden},
		{"upload then download", batch, `{"operation":"upload","operation":"download"}`, http.StatusOK},
		{"download, trailing garbage", batch, `{"operation":"download"} x`, http.StatusForbidden},
		{"download padded past the cap", batch, `{"operation":"download"}` + pad, http.StatusForbidden},
		{"download inside a nested object", batch, `{"objects":[{"operation":"download"}]}`, http.StatusForbidden},
		{"upload object", "/repos/gitlfs/abc/objects/" + lfsOID, `x`, http.StatusForbidden},
		{"verify", "/repos/gitlfs/abc/verify", `{"oid":"` + lfsOID + `","size":3}`, http.StatusForbidden},
		{"lock", "/repos/gitlfs/abc/locks", `{"path":"a.bin"}`, http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+token)

			rw := httptest.NewRecorder()
			handler.ServeHTTP(rw, req)

			if rw.Code != tc.want {
				t.Fatalf("status = %d, want %d", rw.Code, tc.want)
			}

			// The handler behind an authorized batch reads the whole body.
			if tc.want == http.StatusOK && rw.Body.String() != tc.body {
				t.Errorf("handler saw %d bytes of body, sent %d", rw.Body.Len(), len(tc.body))
			}
		})
	}
}

// Reading objects stays a read, and a read-only principal cannot upload one.
func TestRBACGitLFSObjectMethods(t *testing.T) {
	handler, token := readOnlyRBAC(t)

	for method, want := range map[string]int{
		http.MethodGet:  http.StatusOK,
		http.MethodHead: http.StatusOK,
		http.MethodPut:  http.StatusForbidden,
	} {
		req := httptest.NewRequestWithContext(t.Context(), method, "/repos/gitlfs/abc/objects/"+lfsOID, http.NoBody)
		req.Header.Set("Authorization", "Bearer "+token)

		rw := httptest.NewRecorder()
		handler.ServeHTTP(rw, req)

		if rw.Code != want {
			t.Errorf("%s status = %d, want %d", method, rw.Code, want)
		}
	}
}

// An anonymous request to a repository that is not public is challenged with
// the headers git-lfs reads, and the challenge is for LFS repositories only.
func TestRBACGitLFSChallenge(t *testing.T) {
	handler, _ := readOnlyRBAC(t)

	for path, want := range map[string]string{
		"/repos/gitlfs/abc/objects/batch": `Basic realm="` + AuthRealm + `"`,
		"/repos/abc/x":                    "",
	} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(`{"operation":"download"}`))

		rw := httptest.NewRecorder()
		handler.ServeHTTP(rw, req)

		if rw.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want 401", path, rw.Code)
		}

		if got := rw.Header().Get("Lfs-Authenticate"); got != want {
			t.Errorf("%s LFS-Authenticate = %q, want %q", path, got, want)
		}

		if rw.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s lost its WWW-Authenticate challenge", path)
		}
	}
}
