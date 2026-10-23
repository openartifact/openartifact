// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	conanV1Recipe = "/repos/conan/v1/conans/zlib/1.3/_/_"
	conanV1Files  = "/repos/conan/v1/files/zlib/1.3/_/_/"
)

func sha1OfString(s string) string {
	sum := sha1.Sum([]byte(s))

	return hex.EncodeToString(sum[:])
}

// deploy sends the empty PUT with X-Checksum-Deploy a conan client tries first.
func (s *formatServer) deploy(target, sha1 string) int {
	s.t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, target, strings.NewReader(""))
	if err != nil {
		s.t.Fatal(err)
	}

	req.Header.Set("X-Checksum-Deploy", "true")
	req.Header.Set("X-Checksum-Sha1", sha1)

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	return w.Code
}

// A Conan 1 client against the real schema: files are stored with their digests,
// a checksum deploy reuses the blob of the same repository only, and recording
// the digests of an older file leaves its revision time alone.
func TestConanV1AgainstPostgres(t *testing.T) {
	pg := freshPostgres(t, 5)
	repoID := newRepo(t, pg, "conan", "conan")
	otherID := newRepo(t, pg, "conan-other", "conan")
	srv := newConanServer(t, pg)
	ctx := context.Background()

	for _, f := range []string{"conan_export.tgz", "conanfile.py", "conanmanifest.txt"} {
		srv.must(http.MethodPut, conanV1Files+"0/export/"+f, "v1:"+f, http.StatusCreated)
	}

	// Digests are stored and a Conan 2 client sees revision 0.
	row, err := pg.GetConanPackage(ctx, repoID, models.ConanFileKey{
		Name: "zlib", Version: "1.3", UserChannel: "_/_", RecipeRevision: "0", Filename: "conanfile.py",
	})
	if err != nil || row == nil {
		t.Fatalf("stored file = %v, %v", row, err)
	}

	if row.SHA1 != sha1OfString("v1:conanfile.py") || len(row.MD5) != 32 {
		t.Errorf("digests = %s/%s", row.MD5, row.SHA1)
	}

	srv.must(http.MethodGet, conanBase+"/latest", "", http.StatusOK)

	// Listing and downloading through the v1 URLs.
	var urls map[string]string
	mustUnmarshal(t, srv.must(http.MethodGet, conanV1Recipe+"/download_urls", "", http.StatusOK).Body.Bytes(), &urls)

	if want := "http://localhost/repos/conan/v1/files/zlib/1.3/_/_/0/export/conanfile.py"; urls["conanfile.py"] != want {
		t.Errorf("download url = %q, want %q", urls["conanfile.py"], want)
	}

	srv.must(http.MethodGet, conanV1Files+"0/export/conanfile.py", "", http.StatusOK)

	// Checksum deploy: same repository, new revision, no bytes.
	target := conanV1Files + "1/export/conanfile.py"
	if code := srv.deploy(target, sha1OfString("v1:conanfile.py")); code != http.StatusCreated {
		t.Fatalf("deploy = %d, want 201", code)
	}

	if got := srv.must(http.MethodGet, target, "", http.StatusOK).Body.String(); got != "v1:conanfile.py" {
		t.Errorf("deployed content = %q", got)
	}

	// Another repository cannot reach that content by checksum.
	if code := srv.deploy("/repos/conan-other/v1/files/zlib/1.3/_/_/0/export/conanfile.py", sha1OfString("v1:conanfile.py")); code != http.StatusNotFound {
		t.Errorf("deploy across repositories = %d, want 404", code)
	}

	if rows, _ := pg.ListConanRecipeFiles(ctx, otherID, "zlib", "1.3", "_/_"); len(rows) != 0 {
		t.Errorf("rows in the other repository = %d, want 0", len(rows))
	}

	// A file stored before the digests were tracked gets them without its time moving.
	old, _ := pg.GetConanPackage(ctx, repoID, models.ConanFileKey{
		Name: "zlib", Version: "1.3", UserChannel: "_/_", RecipeRevision: "0", Filename: "conanmanifest.txt",
	})

	if err := pg.SetConanPackageDigests(ctx, old.ID, "", ""); err != nil {
		t.Fatal(err)
	}

	var snapshot map[string]string
	mustUnmarshal(t, srv.must(http.MethodGet, conanV1Recipe, "", http.StatusOK).Body.Bytes(), &snapshot)

	if len(snapshot["conanmanifest.txt"]) != 32 {
		t.Errorf("snapshot md5 of an older file = %q, want it computed", snapshot["conanmanifest.txt"])
	}

	again, _ := pg.GetConanPackage(ctx, repoID, models.ConanFileKey{
		Name: "zlib", Version: "1.3", UserChannel: "_/_", RecipeRevision: "0", Filename: "conanmanifest.txt",
	})
	if again.SHA1 != sha1OfString("v1:conanmanifest.txt") || !again.UpdatedAt.Equal(old.UpdatedAt) {
		t.Errorf("after digest backfill: sha1=%s updated_at %v -> %v", again.SHA1, old.UpdatedAt, again.UpdatedAt)
	}
}

func mustUnmarshal(t *testing.T, data []byte, v any) {
	t.Helper()

	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
}
