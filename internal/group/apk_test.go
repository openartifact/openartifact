// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/apkkeys"
	"github.com/openartifact/openartifact/internal/database/models"
)

// fakeApkKeys answers the key of every repository it is asked about.
type fakeApkKeys struct {
	info  *apkkeys.Info
	err   error
	asked []string
}

func (k *fakeApkKeys) Info(_ context.Context, repo *models.Repository) (*apkkeys.Info, error) {
	k.asked = append(k.asked, repo.Name)

	return k.info, k.err
}

const (
	apkTestDir   = "v3.20/main/x86_64"
	apkTestIndex = apkTestDir + "/APKINDEX.tar.gz"
	apkTestFile  = apkTestDir + "/foo-1.0-r0.apk"
)

func newApk(t *testing.T, members ...*models.Repository) *debrpmFixture {
	t.Helper()

	f := newDebRPM(t, "apk", members...)
	f.h.WithApkKeys(&fakeApkKeys{info: &apkkeys.Info{KeyName: "grp-1a2b.rsa.pub", Fingerprint: "ff00", PublicKey: "PEM"}})

	return f
}

func TestApkGroupServesTheMergedIndex(t *testing.T) {
	f := newApk(t, member("a", "apk", "hosted"))
	now := time.Now()
	f.db.state = &models.GroupMetadataState{GeneratedAt: &now}
	f.db.put(f.blobs, apkTestIndex, "INDEX")

	w := f.get(http.MethodGet, apkTestIndex)
	if w.Code != http.StatusOK || w.Body.String() != "INDEX" || w.Header().Get("Content-Type") != "application/gzip" {
		t.Errorf("index = %d %q (%s)", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}

	if w.Header().Get("ETag") == "" {
		t.Error("the index has no ETag")
	}

	if len(f.gen.generated) != 0 || len(f.asked) != 0 {
		t.Errorf("generated %v, asked members %v for fresh metadata", f.gen.generated, f.asked)
	}

	if w := f.get(http.MethodHead, apkTestIndex); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("HEAD index = %d with %d body bytes", w.Code, w.Body.Len())
	}
}

func TestApkGroupFirstRequestOfADirectoryGeneratesIt(t *testing.T) {
	f := newApk(t, member("a", "apk", "hosted"))
	f.gen.published = map[string]string{apkTestIndex: "INDEX"}

	if w := f.get(http.MethodGet, apkTestIndex); w.Code != http.StatusOK || w.Body.String() != "INDEX" {
		t.Fatalf("first request = %d %q", w.Code, w.Body.String())
	}

	if !slices.Equal(f.gen.generated, []string{apkTestDir}) {
		t.Errorf("Generate calls = %v, want one naming the requested directory", f.gen.generated)
	}

	if w := f.get(http.MethodGet, apkTestIndex); w.Code != http.StatusOK || len(f.gen.generated) != 1 {
		t.Errorf("a published index triggered another generation: %d, %v", w.Code, f.gen.generated)
	}
}

func TestApkGroupUnknownDirectoryIs404(t *testing.T) {
	f := newApk(t, member("a", "apk", "hosted"))
	f.gen.err = ErrDistNotFound

	if w := f.get(http.MethodGet, "v9.9/main/x86_64/APKINDEX.tar.gz"); w.Code != http.StatusNotFound {
		t.Errorf("unknown directory = %d, want 404", w.Code)
	}
}

func TestApkGroupServesItsOwnKey(t *testing.T) {
	f := newApk(t, member("a", "apk", "hosted"))

	for _, path := range []string{"public.key", "grp-1a2b.rsa.pub"} {
		w := f.get(http.MethodGet, path)
		if w.Code != http.StatusOK || w.Body.String() != "PEM" || w.Header().Get("ETag") != `"ff00"` {
			t.Errorf("%s = %d %q etag %q", path, w.Code, w.Body.String(), w.Header().Get("ETag"))
		}
	}

	if w := f.get(http.MethodGet, "other.rsa.pub"); w.Code != http.StatusNotFound {
		t.Errorf("another key name = %d, want 404", w.Code)
	}

	if len(f.gen.generated) != 0 {
		t.Errorf("a key download generated metadata: %v", f.gen.generated)
	}
}

func TestApkGroupKeyUnavailableIs404(t *testing.T) {
	f := newDebRPM(t, "apk", member("a", "apk", "hosted"))

	if w := f.get(http.MethodGet, "public.key"); w.Code != http.StatusNotFound {
		t.Errorf("no key service = %d, want 404", w.Code)
	}

	f.h.WithApkKeys(&fakeApkKeys{err: apkkeys.ErrSecretKeyRequired})

	if w := f.get(http.MethodGet, "public.key"); w.Code != http.StatusNotFound {
		t.Errorf("no secret key = %d, want 404", w.Code)
	}
}

func TestApkGroupPackageFileComesFromTheOwningMember(t *testing.T) {
	hosted, mirror := member("hosted", "apk", "hosted"), member("mirror", "apk", "proxy")
	f := newApk(t, hosted, mirror)
	f.db.owners[apkTestFile] = "id-hosted"
	f.db.owners[apkTestDir+"/bar-2.0-r1.apk"] = "id-mirror"
	f.set("hosted", apkTestFile, 200, "FOO")
	f.set("mirror", apkTestDir+"/bar-2.0-r1.apk", 200, "BAR")

	if w := f.get(http.MethodGet, apkTestFile); w.Code != http.StatusOK || w.Body.String() != "FOO" {
		t.Errorf("hosted file = %d %q", w.Code, w.Body.String())
	}

	if w := f.get(http.MethodGet, apkTestDir+"/bar-2.0-r1.apk"); w.Code != http.StatusOK || w.Body.String() != "BAR" {
		t.Errorf("proxy file = %d %q", w.Code, w.Body.String())
	}

	if !slices.Equal(f.asked, []string{"hosted", "mirror"}) {
		t.Errorf("members asked = %v, want exactly the owners, in turn", f.asked)
	}
}

func TestApkGroupFileTheIndexDoesNotListIs404WithoutAskingMembers(t *testing.T) {
	f := newApk(t, member("a", "apk", "hosted"), member("mirror", "apk", "proxy"))
	f.set("mirror", apkTestFile, 200, "evil")

	if w := f.get(http.MethodGet, apkTestFile); w.Code != http.StatusNotFound {
		t.Errorf("unlisted file = %d %q, want 404", w.Code, w.Body.String())
	}

	if len(f.asked) != 0 {
		t.Errorf("members asked for an unlisted file: %v", f.asked)
	}
}

func TestApkGroupWithoutReadableMembersHasNothing(t *testing.T) {
	f := newApk(t, member("secret", "apk", "hosted"))
	now := time.Now()
	f.db.state = &models.GroupMetadataState{GeneratedAt: &now}
	f.db.put(f.blobs, apkTestIndex, "INDEX")
	f.denied["secret"] = true

	for _, path := range []string{apkTestIndex, "public.key", apkTestFile} {
		if w := f.get(http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("%s for a caller who reads no member = %d, want 404", path, w.Code)
		}
	}

	if len(f.gen.generated) != 0 {
		t.Errorf("generated for an unauthorised caller: %v", f.gen.generated)
	}
}

func TestApkGroupRejectsPathsItDoesNotMerge(t *testing.T) {
	f := newApk(t, member("a", "apk", "hosted"))
	now := time.Now()
	f.db.state = &models.GroupMetadataState{GeneratedAt: &now}

	for _, path := range []string{
		"",
		"v3.20/main/APKINDEX.tar.gz",
		"v3.20/main/x86_64/APKINDEX.tar.gz/extra",
		"v3.20/main/x86_64/notes.txt",
		"v3.20/main/x86_64/foo.apk",
		"../main/x86_64/APKINDEX.tar.gz",
		"v3.20/ma in/x86_64/APKINDEX.tar.gz",
		".oa-variant/abc/x86_64/APKINDEX.tar.gz",
	} {
		if w := f.get(http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("%q = %d, want 404", path, w.Code)
		}
	}

	if len(f.gen.generated) != 0 {
		t.Errorf("a malformed path generated metadata: %v", f.gen.generated)
	}
}

func TestApkGroupWithoutMetadataSupportIs501(t *testing.T) {
	f := newFixture(t, "apk", member("a", "apk", "hosted"))

	if w := f.get(http.MethodGet, apkTestIndex); w.Code != http.StatusNotImplemented {
		t.Errorf("= %d, want 501 when the handler has no generator", w.Code)
	}
}

func TestApkGroupServesACallerOnlyWhatTheirReadableMembersList(t *testing.T) {
	f := newApk(t, member("open", "apk", "hosted"), member("secret", "apk", "hosted"))
	now := time.Now()
	f.db.state = &models.GroupMetadataState{GeneratedAt: &now}
	f.db.put(f.blobs, apkTestIndex, "ALL")
	f.db.put(f.blobs, ".oa-variant/id-open/"+apkTestIndex, "OPEN-ONLY")
	f.denied["secret"] = true

	if w := f.get(http.MethodGet, apkTestIndex); w.Code != http.StatusOK || w.Body.String() != "OPEN-ONLY" {
		t.Errorf("restricted caller = %d %q, want the index of the members they read", w.Code, w.Body.String())
	}
}
