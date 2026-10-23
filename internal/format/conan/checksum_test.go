// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"net/http"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// deploy sends what the conan client sends before uploading a file: an empty
// PUT carrying the file's SHA-1.
func (e *env) deploy(target, sha1 string) int {
	e.t.Helper()

	req := formattest.Request(http.MethodPut, target, strings.NewReader(""))
	req.Header.Set("X-Checksum-Deploy", "true")

	if sha1 != "" {
		req.Header.Set("X-Checksum-Sha1", sha1)
	}

	return formattest.Do(e.h.RegisterRepoRoutes, req).Code
}

func (e *env) file(rrev, name string) *models.ConanPackage {
	e.t.Helper()

	row, err := e.db.GetConanPackage(e.t.Context(), "repo-1", models.ConanFileKey{
		Name: "zlib", Version: "1.3", UserChannel: "_/_", RecipeRevision: rrev, Filename: name,
	})
	if err != nil {
		e.t.Fatal(err)
	}

	return row
}

func TestChecksumDeployStoresKnownContentWithoutABody(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)

	body := "conanfile.py@" + rrev1
	if code := e.deploy(recipeFileURL(rrev2, "conanfile.py"), sha1Hex(body)); code != http.StatusCreated {
		t.Fatalf("deploy = %d, want 201", code)
	}

	if got := e.mustDo(http.MethodGet, recipeFileURL(rrev2, "conanfile.py"), "", http.StatusOK).Body.String(); got != body {
		t.Errorf("deployed file = %q, want %q", got, body)
	}

	deployed, source := e.file(rrev2, "conanfile.py"), e.file(rrev1, "conanfile.py")
	if deployed.SHA256 != source.SHA256 || deployed.BlobKey != source.BlobKey || deployed.SHA1 != sha1Hex(body) || deployed.MD5 != md5Hex(body) {
		t.Errorf("deployed row = %+v, want the source's blob and digests", deployed)
	}

	if len(e.bs.Blobs) != 4 {
		t.Errorf("blobs = %d, want 4: a deploy must not copy bytes", len(e.bs.Blobs))
	}

	if e.db.used != recipeBytes(rrev1)+int64(len(body)) || e.db.artifacts != 5 {
		t.Errorf("quota used=%d artifacts=%d, want the deployed file counted", e.db.used, e.db.artifacts)
	}

	events := e.pub.Events()
	if last := events[len(events)-1]; last.Artifact()["path"] != "zlib/1.3/_/_/conanfile.py" {
		t.Errorf("last event = %+v, want an upload event for the deployed file", last)
	}
}

func TestChecksumDeployWithUnknownContentAsksForTheBytes(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)

	if code := e.deploy(recipeFileURL(rrev2, "conanfile.py"), sha1Hex("never uploaded")); code != http.StatusNotFound {
		t.Errorf("deploy of unknown content = %d, want 404", code)
	}

	if e.file(rrev2, "conanfile.py") != nil {
		t.Error("a missed deploy stored a file")
	}

	if e.db.artifacts != 4 {
		t.Errorf("artifacts = %d, a missed deploy must not count", e.db.artifacts)
	}

	// The client then uploads the bytes in the same request shape without the header.
	e.mustDo(http.MethodPut, recipeFileURL(rrev2, "conanfile.py"), "never uploaded", http.StatusCreated)
}

func TestChecksumDeployRejectsAMalformedChecksum(t *testing.T) {
	e := newEnv(t)

	for _, sum := range []string{"", "xyz", strings.Repeat("g", 40), strings.Repeat("a", 39)} {
		if code := e.deploy(recipeFileURL(rrev1, "conanfile.py"), sum); code != http.StatusBadRequest {
			t.Errorf("deploy with checksum %q = %d, want 400", sum, code)
		}
	}

	if e.db.count() != 0 {
		t.Error("a rejected deploy stored a file")
	}
}

func TestChecksumDeployAcceptsUppercaseHex(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)

	if code := e.deploy(recipeFileURL(rrev2, "conanfile.py"), strings.ToUpper(sha1Hex("conanfile.py@"+rrev1))); code != http.StatusCreated {
		t.Errorf("deploy with an uppercase checksum = %d, want 201", code)
	}
}

func TestChecksumDeployOfAPackageNeedsItsRecipe(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)
	e.publishPackage(rrev1, pid, prev1)

	known := sha1Hex("conaninfo.txt@" + prev1)

	if code := e.deploy(packageFileURL(rrev2, pid, prev1, "conaninfo.txt"), known); code != http.StatusNotFound {
		t.Errorf("deploy into an unpublished recipe revision = %d, want 404", code)
	}

	if code := e.deploy(packageFileURL(rrev1, strings.Repeat("f", 40), prev1, "conaninfo.txt"), known); code != http.StatusCreated {
		t.Errorf("deploy into a published recipe revision = %d, want 201", code)
	}
}

func TestChecksumDeployIsIdempotent(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)

	if code := e.deploy(recipeFileURL(rrev1, "conanfile.py"), sha1Hex("conanfile.py@"+rrev1)); code != http.StatusCreated {
		t.Errorf("deploy onto the same content = %d, want 201", code)
	}

	if e.db.artifacts != 4 || e.db.used != recipeBytes(rrev1) {
		t.Errorf("quota used=%d artifacts=%d, an identical deploy must not count twice", e.db.used, e.db.artifacts)
	}
}

func TestChecksumDeployHonoursImmutableReleases(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)
	e.mustDo(http.MethodPut, recipeFileURL(rrev2, "conanfile.py"), "other content", http.StatusCreated)

	e.db.Repo.ImmutableReleases = true

	// Replacing different content with a deploy is an overwrite like any other.
	if code := e.deploy(recipeFileURL(rrev2, "conanfile.py"), sha1Hex("conanfile.py@"+rrev1)); code != http.StatusConflict {
		t.Errorf("deploy over different content in an immutable repository = %d, want 409", code)
	}

	if got := e.file(rrev2, "conanfile.py"); got.SHA1 != sha1Hex("other content") {
		t.Errorf("the refused deploy changed the file: %+v", got)
	}
}

func TestChecksumDeployHonoursTheQuota(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)
	e.db.limit = e.db.used

	if code := e.deploy(recipeFileURL(rrev2, "conanfile.py"), sha1Hex("conanfile.py@"+rrev1)); code != http.StatusInsufficientStorage {
		t.Errorf("deploy over quota = %d, want 507", code)
	}

	if e.file(rrev2, "conanfile.py") != nil || e.db.artifacts != 4 {
		t.Error("a refused deploy stored a file or kept its reservation")
	}
}

func TestChecksumDeployNeverReusesAQuarantinedFile(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)
	e.db.Keys["zlib/1.3/_/_/conanfile.py"] = true

	if code := e.deploy(recipeFileURL(rrev2, "conanfile.py"), sha1Hex("conanfile.py@"+rrev1)); code != http.StatusNotFound {
		t.Errorf("deploy of quarantined content = %d, want 404 so the bytes are uploaded and scanned", code)
	}

	if e.file(rrev2, "conanfile.py") != nil {
		t.Error("quarantined content was reused")
	}
}

func TestChecksumDeployIgnoresFilesWithoutDigests(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)

	// A file stored before digests were tracked is not a deploy source.
	if err := e.db.SetConanPackageDigests(t.Context(), e.file(rrev1, "conanfile.py").ID, "", ""); err != nil {
		t.Fatal(err)
	}

	if code := e.deploy(recipeFileURL(rrev2, "conanfile.py"), sha1Hex("conanfile.py@"+rrev1)); code != http.StatusNotFound {
		t.Errorf("deploy from a file without digests = %d, want 404", code)
	}
}

func TestSameBytesReuploadRecordsMissingDigests(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)

	row := e.file(rrev1, "conanfile.py")
	if err := e.db.SetConanPackageDigests(t.Context(), row.ID, "", ""); err != nil {
		t.Fatal(err)
	}

	e.mustDo(http.MethodPut, recipeFileURL(rrev1, "conanfile.py"), "conanfile.py@"+rrev1, http.StatusCreated)

	if got := e.file(rrev1, "conanfile.py"); got.SHA1 != sha1Hex("conanfile.py@"+rrev1) || got.MD5 == "" {
		t.Errorf("digests after an identical re-upload = %+v, want them recorded", got)
	}
}
