// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// debrpmDB adds the tables a deb or rpm group reads to the group fixture's DB.
type debrpmDB struct {
	*groupDB

	metadata map[string]*models.RepoMetadata // path -> row of the group
	owners   map[string]string               // path -> member id
	state    *models.GroupMetadataState
}

func (d *debrpmDB) GetRepoMetadataByPath(_ context.Context, _, path string) (*models.RepoMetadata, error) {
	return d.metadata[path], nil
}

func (d *debrpmDB) GetGroupMetadataState(context.Context, string) (*models.GroupMetadataState, error) {
	return d.state, nil
}

func (d *debrpmDB) GetGroupPackageOwner(_ context.Context, _, path string) (string, bool, error) {
	id, ok := d.owners[path]

	return id, ok, nil
}

// fakeGenerator records calls and, on Generate, publishes what a worker would.
type fakeGenerator struct {
	db        *debrpmDB
	blobs     *formattest.BlobStore
	err       error
	published map[string]string // path -> content written by a successful Generate

	generated []string   // dist of every Generate call
	readable  [][]string // readable members of every Generate call
	refreshed int
	members   int // members of the group; fewer readable ones get a variant scope
}

// Scope mirrors the worker: all members readable is the shared index.
func (g *fakeGenerator) Scope(_ context.Context, _ string, readable []string) (string, bool, error) {
	if len(readable) == 0 {
		return "", false, nil
	}

	if len(readable) == g.members {
		return "", true, nil
	}

	return ".oa-variant/" + strings.Join(readable, "+") + "/", true, nil
}

func (g *fakeGenerator) Generate(_ context.Context, _, dist string, readable []string) error {
	g.generated = append(g.generated, dist)
	g.readable = append(g.readable, readable)

	if g.err != nil {
		return g.err
	}

	now := time.Now()
	g.db.state = &models.GroupMetadataState{GeneratedAt: &now}

	for path, content := range g.published {
		g.db.put(g.blobs, path, content)
	}

	return nil
}

func (g *fakeGenerator) Refresh(string) { g.refreshed++ }

func (d *debrpmDB) put(blobs *formattest.BlobStore, path, content string) {
	sum := "sha-" + path
	blobs.Blobs[sum] = []byte(content)
	d.metadata[path] = &models.RepoMetadata{Path: path, BlobSHA256: sum, BlobSize: int64(len(content))}
}

type debrpmFixture struct {
	*fixture

	db    *debrpmDB
	blobs *formattest.BlobStore
	gen   *fakeGenerator
}

// newDebRPM builds a group of format with the members (priority = order).
func newDebRPM(t *testing.T, format string, members ...*models.Repository) *debrpmFixture {
	t.Helper()

	f := newFixture(t, format, members...)
	db := &debrpmDB{groupDB: f.h.db.(*groupDB), metadata: map[string]*models.RepoMetadata{}, owners: map[string]string{}}
	blobs := formattest.NewBlobStore()
	gen := &fakeGenerator{db: db, blobs: blobs, members: len(members)}

	f.h = NewHandler(f.h.log, db).WithMetadata(blobs, gen)

	return &debrpmFixture{fixture: f, db: db, blobs: blobs, gen: gen}
}

func TestDebGroupServesGeneratedMetadataWithoutGenerating(t *testing.T) {
	f := newDebRPM(t, "deb", member("a", "deb", "hosted"))
	now := time.Now()
	f.db.state = &models.GroupMetadataState{GeneratedAt: &now}
	f.db.put(f.blobs, "dists/bookworm/Release", "Codename: bookworm\n")
	f.db.put(f.blobs, "dists/bookworm/main/binary-amd64/Packages.gz", "gzdata")
	f.db.put(f.blobs, "public.key", "KEY")

	for path, want := range map[string]struct{ body, ctype string }{
		"dists/bookworm/Release":                       {"Codename: bookworm\n", "text/plain"},
		"dists/bookworm/main/binary-amd64/Packages.gz": {"gzdata", "application/gzip"},
		"public.key": {"KEY", "application/pgp-keys"},
	} {
		w := f.get(http.MethodGet, path)
		if w.Code != http.StatusOK || w.Body.String() != want.body || w.Header().Get("Content-Type") != want.ctype {
			t.Errorf("%s = %d %q (%s), want %q (%s)", path, w.Code, w.Body.String(), w.Header().Get("Content-Type"), want.body, want.ctype)
		}

		if w.Header().Get("ETag") == "" {
			t.Errorf("%s has no ETag", path)
		}
	}

	if len(f.gen.generated) != 0 || f.gen.refreshed != 0 {
		t.Errorf("generated %v refreshed %d for fresh metadata", f.gen.generated, f.gen.refreshed)
	}

	if len(f.asked) != 0 {
		t.Errorf("metadata must come from the group's own rows, members were asked: %v", f.asked)
	}
}

func TestRPMGroupServesRepodata(t *testing.T) {
	f := newDebRPM(t, "rpm", member("a", "rpm", "hosted"))
	now := time.Now()
	f.db.state = &models.GroupMetadataState{GeneratedAt: &now}
	f.db.put(f.blobs, "repodata/repomd.xml", "<repomd/>")
	f.db.put(f.blobs, "repodata/abc-primary.xml.gz", "gz")
	f.db.put(f.blobs, "repodata/abc-primary.xml.zst", "zst")

	if w := f.get(http.MethodGet, "repodata/repomd.xml"); w.Code != http.StatusOK || w.Body.String() != "<repomd/>" || w.Header().Get("Content-Type") != "application/xml" {
		t.Errorf("repomd = %d %q %s", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}

	if w := f.get(http.MethodGet, "repodata/abc-primary.xml.gz"); w.Code != http.StatusOK || w.Body.String() != "gz" {
		t.Errorf("primary = %d %q", w.Code, w.Body.String())
	}

	// Only the formats the generator writes are served.
	if w := f.get(http.MethodGet, "repodata/abc-primary.xml.zst"); w.Code != http.StatusNotFound {
		t.Errorf("zst = %d, want 404", w.Code)
	}
}

func TestDebGroupFirstRequestGeneratesTheDistribution(t *testing.T) {
	f := newDebRPM(t, "deb", member("a", "deb", "hosted"))
	f.gen.published = map[string]string{"dists/bookworm/Release": "R", "dists/bookworm/InRelease": "IR"}

	w := f.get(http.MethodGet, "dists/bookworm/InRelease")
	if w.Code != http.StatusOK || w.Body.String() != "IR" {
		t.Fatalf("first request = %d %q", w.Code, w.Body.String())
	}

	if !slices.Equal(f.gen.generated, []string{"bookworm"}) {
		t.Errorf("Generate calls = %v, want one for the requested distribution", f.gen.generated)
	}

	// Published now: no further generation.
	if w := f.get(http.MethodGet, "dists/bookworm/Release"); w.Code != http.StatusOK {
		t.Errorf("second request = %d", w.Code)
	}

	if len(f.gen.generated) != 1 {
		t.Errorf("a published row triggered another generation: %v", f.gen.generated)
	}
}

func TestDebGroupUnknownDistributionIs404(t *testing.T) {
	f := newDebRPM(t, "deb", member("a", "deb", "hosted"))
	f.gen.err = ErrDistNotFound

	if w := f.get(http.MethodGet, "dists/nope/Release"); w.Code != http.StatusNotFound {
		t.Errorf("unknown distribution = %d, want 404", w.Code)
	}

	if f.gen.refreshed != 0 {
		t.Error("an unknown distribution must not schedule background work")
	}
}

func TestGroupGenerationFailureIs503AndRetriesInBackground(t *testing.T) {
	f := newDebRPM(t, "rpm", member("a", "rpm", "hosted"))
	f.gen.err = errors.New("upstream down")

	w := f.get(http.MethodGet, "repodata/repomd.xml")
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("failed generation = %d (Retry-After %q, Cache-Control %q), want 503 with retry hints", w.Code, w.Header().Get("Retry-After"), w.Header().Get("Cache-Control"))
	}

	if f.gen.refreshed != 1 {
		t.Errorf("refreshes = %d, want the work continued in the background", f.gen.refreshed)
	}
}

func TestDebGroupMissAfterFirstGenerationDoesNotRegenerate(t *testing.T) {
	f := newDebRPM(t, "deb", member("a", "deb", "hosted"))
	now := time.Now()
	f.db.state = &models.GroupMetadataState{GeneratedAt: &now}

	// A file of a published distribution that does not exist (a Packages index
	// for an architecture, say) is a plain 404: every such probe regenerating
	// the group would let clients drive the work.
	if w := f.get(http.MethodGet, "dists/bookworm/main/binary-riscv64/Packages.gz"); w.Code != http.StatusNotFound {
		t.Errorf("missing index = %d, want 404", w.Code)
	}

	if len(f.gen.generated) != 0 {
		t.Errorf("generated %v for a missing index", f.gen.generated)
	}

	// A Release of a distribution the group has not served yet is probed.
	f.gen.err = ErrDistNotFound

	if w := f.get(http.MethodGet, "dists/trixie/Release"); w.Code != http.StatusNotFound {
		t.Errorf("unknown release = %d, want 404", w.Code)
	}

	if !slices.Equal(f.gen.generated, []string{"trixie"}) {
		t.Errorf("Generate calls = %v, want the new distribution to be probed", f.gen.generated)
	}
}

func TestGroupMetadataRefreshesWhenStaleOrExpired(t *testing.T) {
	past, future, now := time.Now().Add(-time.Minute), time.Now().Add(time.Hour), time.Now()

	for name, tc := range map[string]struct {
		state *models.GroupMetadataState
		want  int
	}{
		"fresh":   {&models.GroupMetadataState{GeneratedAt: &now, RefreshAfter: &future}, 0},
		"no ttl":  {&models.GroupMetadataState{GeneratedAt: &now}, 0},
		"stale":   {&models.GroupMetadataState{GeneratedAt: &now, Stale: true}, 1},
		"expired": {&models.GroupMetadataState{GeneratedAt: &now, RefreshAfter: &past}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			f := newDebRPM(t, "rpm", member("a", "rpm", "hosted"))
			f.db.state = tc.state
			f.db.put(f.blobs, "repodata/repomd.xml", "<repomd/>")

			if w := f.get(http.MethodGet, "repodata/repomd.xml"); w.Code != http.StatusOK || w.Body.String() != "<repomd/>" {
				t.Fatalf("stale metadata must still be served: %d %q", w.Code, w.Body.String())
			}

			if f.gen.refreshed != tc.want {
				t.Errorf("refreshes = %d, want %d", f.gen.refreshed, tc.want)
			}
		})
	}
}

func TestGroupPackageFileComesFromTheOwningMember(t *testing.T) {
	hosted, mirror := member("hosted", "deb", "hosted"), member("mirror", "deb", "proxy")
	f := newDebRPM(t, "deb", hosted, mirror)
	f.db.owners["pool/main/f/foo/foo_1.0 b1_amd64.deb"] = "id-hosted"
	f.db.owners["pool/main/b/baz/baz_3_amd64.deb"] = "id-mirror"
	f.set("hosted", "pool/main/f/foo/foo_1.0%20b1_amd64.deb", 200, "FOO")
	f.set("mirror", "pool/main/b/baz/baz_3_amd64.deb", 200, "BAZ")

	// A hosted router unescapes the file name itself, so it is asked the
	// escaped form; the proxy gets the path as is.
	if w := f.get(http.MethodGet, "pool/main/f/foo/foo_1.0 b1_amd64.deb"); w.Code != http.StatusOK || w.Body.String() != "FOO" {
		t.Errorf("hosted file = %d %q", w.Code, w.Body.String())
	}

	if w := f.get(http.MethodGet, "pool/main/b/baz/baz_3_amd64.deb"); w.Code != http.StatusOK || w.Body.String() != "BAZ" {
		t.Errorf("proxy file = %d %q", w.Code, w.Body.String())
	}

	if !slices.Equal(f.asked, []string{"hosted", "mirror"}) {
		t.Errorf("members asked = %v, want exactly the owners, in turn", f.asked)
	}
}

func TestGroupPackageFileNotInTheIndexIs404WithoutAskingMembers(t *testing.T) {
	f := newDebRPM(t, "rpm", member("a", "rpm", "hosted"), member("mirror", "rpm", "proxy"))
	f.set("mirror", "Packages/evil-1.rpm", 200, "evil")

	if w := f.get(http.MethodGet, "Packages/evil-1.rpm"); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "evil") {
		t.Errorf("unlisted file = %d %q, want 404", w.Code, w.Body.String())
	}

	if len(f.asked) != 0 {
		t.Errorf("members asked for an unlisted file: %v", f.asked)
	}
}

func TestGroupPackageFileOfAnUnreadableOwnerIs404(t *testing.T) {
	f := newDebRPM(t, "rpm", member("open", "rpm", "hosted"), member("secret", "rpm", "hosted"))
	f.db.owners["Packages/s-1.rpm"] = "id-secret"
	f.db.owners["Packages/o-1.rpm"] = "id-open"
	// The variant of the caller who can read only "open" has owners of its own.
	f.db.owners[".oa-variant/id-open/Packages/o-1.rpm"] = "id-open"
	f.set("secret", "Packages/s-1.rpm", 200, "SECRET")
	f.set("open", "Packages/o-1.rpm", 200, "OPEN")
	f.denied["secret"] = true

	if w := f.get(http.MethodGet, "Packages/s-1.rpm"); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "SECRET") {
		t.Errorf("file of a member the caller cannot read = %d %q, want 404", w.Code, w.Body.String())
	}

	if w := f.get(http.MethodGet, "Packages/o-1.rpm"); w.Code != http.StatusOK || w.Body.String() != "OPEN" {
		t.Errorf("file of a readable member = %d %q", w.Code, w.Body.String())
	}
}

func TestGroupWithoutReadableMembersHasNoMetadata(t *testing.T) {
	f := newDebRPM(t, "deb", member("secret", "deb", "hosted"))
	now := time.Now()
	f.db.state = &models.GroupMetadataState{GeneratedAt: &now}
	f.db.put(f.blobs, "dists/bookworm/Release", "R")
	f.denied["secret"] = true

	if w := f.get(http.MethodGet, "dists/bookworm/Release"); w.Code != http.StatusNotFound {
		t.Errorf("metadata for a caller who reads no member = %d, want 404", w.Code)
	}

	if len(f.gen.generated) != 0 {
		t.Errorf("generated for an unauthorised caller: %v", f.gen.generated)
	}
}

func TestDebRPMGroupRejectsPathsItDoesNotMerge(t *testing.T) {
	for name, tc := range map[string]struct {
		format string
		path   string
	}{
		"plain Packages":  {"deb", "dists/bookworm/main/binary-amd64/Packages"},
		"by-hash":         {"deb", "dists/bookworm/main/binary-amd64/by-hash/SHA256/abc"},
		"Sources":         {"deb", "dists/bookworm/main/source/Sources.gz"},
		"dist traversal":  {"deb", "dists/bookworm/../../etc/passwd"},
		"flat dists":      {"deb", "dists/bookworm"},
		"top level":       {"deb", "index.html"},
		"comps":           {"rpm", "repodata/comps.xml"},
		"nested repodata": {"rpm", "repodata/x/abc-primary.xml.gz"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newDebRPM(t, tc.format, member("a", tc.format, "hosted"))
			now := time.Now()
			f.db.state = &models.GroupMetadataState{GeneratedAt: &now}

			if w := f.get(http.MethodGet, tc.path); w.Code != http.StatusNotFound {
				t.Errorf("%s = %d, want 404", tc.path, w.Code)
			}

			if len(f.asked) != 0 || len(f.gen.generated) != 0 {
				t.Errorf("asked %v generated %v", f.asked, f.gen.generated)
			}
		})
	}
}

func TestDebRPMGroupWithoutMetadataSupportIs501(t *testing.T) {
	f := newFixture(t, "deb", member("a", "deb", "hosted"))

	if w := f.get(http.MethodGet, "dists/bookworm/Release"); w.Code != http.StatusNotImplemented {
		t.Errorf("= %d, want 501 when the handler has no generator", w.Code)
	}
}

func TestDebGroupServesPlainPackagesFromTheGzip(t *testing.T) {
	f := newDebRPM(t, "deb", member("a", "deb", "hosted"))
	now := time.Now()
	f.db.state = &models.GroupMetadataState{GeneratedAt: &now}

	var gz bytes.Buffer

	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("Package: foo\n\n"))
	_ = zw.Close()

	f.db.put(f.blobs, "dists/bookworm/main/binary-amd64/Packages.gz", gz.String())

	w := f.get(http.MethodGet, "dists/bookworm/main/binary-amd64/Packages")
	if w.Code != http.StatusOK || w.Body.String() != "Package: foo\n\n" {
		t.Errorf("plain Packages = %d %q, want the decompressed index", w.Code, w.Body.String())
	}
}

func TestDebRPMGroupServesACallerOnlyWhatTheirReadableMembersList(t *testing.T) {
	a, b := member("a", "deb", "hosted"), member("b", "deb", "hosted")
	f := newDebRPM(t, "deb", a, b)
	now := time.Now()
	f.db.state = &models.GroupMetadataState{GeneratedAt: &now}
	f.db.put(f.blobs, "dists/bookworm/Release", "everything")
	f.db.put(f.blobs, "public.key", "KEY")
	f.db.owners["pool/main/s/secret/secret_1_amd64.deb"] = b.ID // the shared index only

	// The caller can read a only: the shared index is not theirs, and the
	// variant of {a} does not exist yet, so it is generated from a alone.
	f.denied = map[string]bool{"b": true}
	variant := ".oa-variant/" + a.ID + "/"
	f.gen.published = map[string]string{variant + "dists/bookworm/Release": "only a"}

	w := f.get(http.MethodGet, "dists/bookworm/Release")
	if w.Code != http.StatusOK || w.Body.String() != "only a" {
		t.Errorf("restricted Release = %d %q, want the variant listing a only", w.Code, w.Body.String())
	}

	if len(f.gen.readable) != 1 || len(f.gen.readable[0]) != 1 || f.gen.readable[0][0] != a.ID {
		t.Errorf("generated for %v, want only member a", f.gen.readable)
	}

	if w := f.get(http.MethodGet, "public.key"); w.Body.String() != "KEY" {
		t.Errorf("public.key = %q, want the shared key", w.Body.String())
	}

	// A file b owns in the shared index is not visible through the variant.
	if w := f.get(http.MethodGet, "pool/main/s/secret/secret_1_amd64.deb"); w.Code != http.StatusNotFound {
		t.Errorf("file of an unreadable member = %d, want 404", w.Code)
	}

	// Everyone who can read both members gets the shared index.
	f.denied = nil

	if w := f.get(http.MethodGet, "dists/bookworm/Release"); w.Body.String() != "everything" {
		t.Errorf("unrestricted Release = %q, want the shared index", w.Body.String())
	}
}
