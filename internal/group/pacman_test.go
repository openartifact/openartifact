// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestClassifyPacman(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]struct {
		class    pathClass
		dist     string
		storedAs string
	}{
		"os/x86_64/grp.db":                        {classMetadata, "x86_64", "os/x86_64/db.tar.gz"},
		"os/x86_64/grp.db.tar.gz":                 {classMetadata, "x86_64", "os/x86_64/db.tar.gz"},
		"os/aarch64/grp.files":                    {classMetadata, "aarch64", "os/aarch64/files.tar.gz"},
		"os/x86_64/grp.files.tar.gz.sig":          {classMetadata, "x86_64", "os/x86_64/files.tar.gz.sig"},
		"os/x86_64/grp.db.sig":                    {classMetadata, "x86_64", "os/x86_64/db.tar.gz.sig"},
		"os/x86_64/tool-1:2-1-x86_64.pkg.tar.zst": {classFile, "", ""},
		"os/x86_64/tool-1-1-any.pkg.tar.zst.sig":  {classFile, "", ""},
		"os/x86_64/tool-1-1-x86_64.pkg.tar.bz2":   {classNone, "", ""},
		"os/../grp.db":                            {classNone, "", ""},
		"os/x86_64/sub/grp.db":                    {classNone, "", ""},
		"os/x86_64":                               {classNone, "", ""},
		"x86_64/grp.db":                           {classNone, "", ""},
		"dists/bookworm/Release":                  {classNone, "", ""},
	} {
		got := classifyPacman(path)
		if got.class != want.class || got.dist != want.dist || got.storedAs != want.storedAs {
			t.Errorf("classifyPacman(%q) = %+v, want %+v", path, got, want)
		}
	}
}

func TestPacmanGroupServesDatabasesAndAnswersIfModifiedSince(t *testing.T) {
	f := newDebRPM(t, "pacman", member("a", "pacman", "hosted"))
	generated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f.db.state = &models.GroupMetadataState{GeneratedAt: &generated}
	f.db.put(f.blobs, "os/x86_64/db.tar.gz", "DB")
	f.db.put(f.blobs, "os/x86_64/db.tar.gz.sig", "SIG")
	f.db.put(f.blobs, "public.key", "KEY")
	f.db.metadata["os/x86_64/db.tar.gz"].LastGenerated = generated

	for path, want := range map[string]struct{ body, ctype string }{
		"os/x86_64/mygroup.db":     {"DB", "application/gzip"},
		"os/x86_64/mygroup.db.sig": {"SIG", "application/pgp-signature"},
		"public.key":               {"KEY", "application/pgp-keys"},
	} {
		w := f.get(http.MethodGet, path)
		if w.Code != http.StatusOK || w.Body.String() != want.body || w.Header().Get("Content-Type") != want.ctype {
			t.Errorf("%s = %d %q (%s), want %q (%s)", path, w.Code, w.Body.String(), w.Header().Get("Content-Type"), want.body, want.ctype)
		}
	}

	w := f.get(http.MethodGet, "os/x86_64/mygroup.db")
	if got := w.Header().Get("Last-Modified"); got != generated.Format(http.TimeFormat) {
		t.Errorf("Last-Modified = %q", got)
	}

	// pacman asks again with the Last-Modified of the copy it holds.
	for since, want := range map[time.Time]int{generated: http.StatusNotModified, generated.Add(-time.Hour): http.StatusOK} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
		req.Header.Set("If-Modified-Since", since.Format(http.TimeFormat))

		rec := httptest.NewRecorder()
		f.h.Serve(rec, req, f.grp, "os/x86_64/mygroup.db", f.backend())

		if rec.Code != want {
			t.Errorf("If-Modified-Since %s = %d, want %d", since, rec.Code, want)
		}
	}

	if len(f.gen.generated) != 0 || len(f.asked) != 0 {
		t.Errorf("fresh metadata triggered generation %v / member requests %v", f.gen.generated, f.asked)
	}
}

func TestPacmanGroupFirstRequestGeneratesTheArchitecture(t *testing.T) {
	f := newDebRPM(t, "pacman", member("a", "pacman", "hosted"))
	f.gen.published = map[string]string{"os/aarch64/db.tar.gz": "DB"}

	if w := f.get(http.MethodGet, "os/aarch64/mygroup.db"); w.Code != http.StatusOK || w.Body.String() != "DB" {
		t.Fatalf("first request = %d %q", w.Code, w.Body.String())
	}

	if !slices.Equal(f.gen.generated, []string{"aarch64"}) {
		t.Errorf("Generate calls = %v, want one for the requested architecture", f.gen.generated)
	}
}

func TestPacmanGroupUnknownArchitectureIs404(t *testing.T) {
	f := newDebRPM(t, "pacman", member("a", "pacman", "hosted"))
	f.gen.err = ErrDistNotFound

	if w := f.get(http.MethodGet, "os/riscv64/mygroup.db"); w.Code != http.StatusNotFound {
		t.Errorf("unknown architecture = %d, want 404", w.Code)
	}

	// Not an architecture name: rejected before any generation.
	f.gen.generated = nil

	if w := f.get(http.MethodGet, "os/..%2fx/mygroup.db"); w.Code != http.StatusNotFound || len(f.gen.generated) != 0 {
		t.Errorf("invalid architecture = %d, generated %v", w.Code, f.gen.generated)
	}
}

func TestPacmanGroupPackageAndSignatureComeFromTheOwningMember(t *testing.T) {
	hosted, mirror := member("hosted", "pacman", "hosted"), member("mirror", "pacman", "proxy")
	f := newDebRPM(t, "pacman", hosted, mirror)
	f.db.owners["os/x86_64/tool-1:2-1-x86_64.pkg.tar.zst"] = "id-hosted"
	f.db.owners["os/x86_64/vim-9-1-x86_64.pkg.tar.zst"] = "id-mirror"
	f.set("hosted", "os/x86_64/tool-1:2-1-x86_64.pkg.tar.zst", 200, "TOOL")
	f.set("hosted", "os/x86_64/tool-1:2-1-x86_64.pkg.tar.zst.sig", 200, "TOOLSIG")
	f.set("mirror", "os/x86_64/vim-9-1-x86_64.pkg.tar.zst", 200, "VIM")

	for path, want := range map[string]string{
		"os/x86_64/tool-1:2-1-x86_64.pkg.tar.zst":     "TOOL",
		"os/x86_64/tool-1:2-1-x86_64.pkg.tar.zst.sig": "TOOLSIG",
		"os/x86_64/vim-9-1-x86_64.pkg.tar.zst":        "VIM",
	} {
		if w := f.get(http.MethodGet, path); w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("%s = %d %q, want %q", path, w.Code, w.Body.String(), want)
		}
	}

	// A package the merged database does not list is not served, whoever holds it.
	f.asked = nil
	f.set("mirror", "os/x86_64/evil-1-1-x86_64.pkg.tar.zst", 200, "evil")

	if w := f.get(http.MethodGet, "os/x86_64/evil-1-1-x86_64.pkg.tar.zst"); w.Code != http.StatusNotFound || len(f.asked) != 0 {
		t.Errorf("unlisted package = %d, members asked %v", w.Code, f.asked)
	}
}
