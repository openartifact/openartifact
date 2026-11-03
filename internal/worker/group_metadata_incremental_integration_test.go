// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"

	"github.com/openartifact/openartifact/internal/database/models"
	formatdeb "github.com/openartifact/openartifact/internal/format/deb"
)

// debAdd adds a package to a hosted deb repository in the given distribution,
// component and architecture. It returns the file id, or "" when the file
// exists already.
func (f *groupFixture) debAdd(repo *models.Repository, dist, comp, arch, name, version string) string {
	f.t.Helper()

	ctx := context.Background()

	d, err := f.pg.GetOrCreateDebianDistribution(ctx, repo.ID, dist)
	if err != nil {
		f.t.Fatal(err)
	}

	c, err := f.pg.GetOrCreateDebianComponent(ctx, d.ID, comp)
	if err != nil {
		f.t.Fatal(err)
	}

	if _, err := f.pg.GetOrCreateDebianArchitecture(ctx, d.ID, arch); err != nil {
		f.t.Fatal(err)
	}

	pkg := &models.Component{
		RepositoryID: repo.ID, ArtifactID: f.blob(repo.Name + dist + comp + arch + name + version),
		Name: name, Version: version, Arch: arch, Filename: name + "_" + version + "_" + arch + ".deb",
	}
	if err := f.pg.CreateDebPackage(
		ctx,
		pkg,
		d.ID,
		c.ID,
		models.DebControl{Maintainer: "m <m@example.com>", Fields: map[string]string{"Description": "d0000"}},
	); err != nil {
		return ""
	}

	return pkg.ID
}

// owners returns the group's owner rows (path -> member).
func (f *groupFixture) owners(groupID string) map[string]string {
	f.t.Helper()

	rows, err := f.pg.GetDB().QueryContext(context.Background(),
		`SELECT path, member_repo_id::text FROM group_package_owners WHERE group_repo_id = $1`, groupID)
	if err != nil {
		f.t.Fatal(err)
	}

	defer func() { _ = rows.Close() }()

	out := map[string]string{}

	for rows.Next() {
		var path, member string
		if err := rows.Scan(&path, &member); err != nil {
			f.t.Fatal(err)
		}

		out[path] = member
	}

	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}

	return out
}

// ownerVersions returns the transaction that last wrote each owner row, so a
// test can tell which rows a generation rewrote.
func (f *groupFixture) ownerVersions(groupID string) map[string]string {
	f.t.Helper()

	rows, err := f.pg.GetDB().QueryContext(context.Background(),
		`SELECT path, xmin::text FROM group_package_owners WHERE group_repo_id = $1`, groupID)
	if err != nil {
		f.t.Fatal(err)
	}

	defer func() { _ = rows.Close() }()

	out := map[string]string{}

	for rows.Next() {
		var path, xmin string
		if err := rows.Scan(&path, &xmin); err != nil {
			f.t.Fatal(err)
		}

		out[path] = xmin
	}

	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}

	return out
}

// releaseHeader is a Release (or InRelease) without what two generations of
// the same content legitimately differ in: the date, and the checksums of the
// compressed indexes (the compressor's dictionary depends on the size of the
// whole group, not only on the index).
func releaseHeader(doc []byte) string {
	var out []string

	for line := range strings.SplitSeq(string(doc), "\n") {
		f := strings.Fields(line)
		if strings.HasPrefix(line, "Date:") || (len(f) == 3 && (strings.HasSuffix(f[2], ".gz") || strings.HasSuffix(f[2], ".xz"))) {
			continue
		}

		out = append(out, line)
	}

	return strings.Join(out, "\n")
}

// decompressed returns the content of a stored index: decompressed when it is
// compressed, as it is otherwise.
func decompressed(t *testing.T, path string, data []byte) []byte {
	t.Helper()

	var (
		r   io.Reader
		err error
	)

	switch {
	case strings.HasSuffix(path, ".gz"):
		r, err = gzip.NewReader(bytes.NewReader(data))
	case strings.HasSuffix(path, ".xz"):
		r, err = xz.NewReader(bytes.NewReader(data))
	default:
		return data
	}

	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}

	return out
}

// assertReleaseMatchesFiles checks what apt checks: every file a Release lists
// is stored with that size and checksum (the plain index is listed, not stored:
// it is checked against its compressed form).
func assertReleaseMatchesFiles(t *testing.T, md map[string][]byte, step string) {
	t.Helper()

	for path, doc := range md {
		if !strings.HasSuffix(path, "/Release") {
			continue
		}

		dir := strings.TrimSuffix(path, "Release")

		for name, fi := range formatdeb.ParseReleaseChecksums(doc) {
			data, ok := md[dir+name]
			if !ok {
				data, ok = md[dir+name+".gz"]
				if !ok {
					t.Fatalf("%s: %s lists %s, which the group does not publish", step, path, name)
				}

				data = decompressed(t, name+".gz", data)
			}

			if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != fi.SHA256 || int64(len(data)) != fi.Size {
				t.Fatalf("%s: %s lists %s as %s (%d bytes), but the file is %s (%d bytes)",
					step, path, name, fi.SHA256, fi.Size, hex.EncodeToString(sum[:]), len(data))
			}
		}
	}
}

func TestGroupDebIncrementalMatchesFullRebuild(t *testing.T) {
	for seed := range uint64(3) {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			runGroupDebSequence(t, seed)
		})
	}
}

func runGroupDebSequence(t *testing.T, seed uint64) {
	t.Helper()

	f := newGroupFixture(t)
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(seed, 7))

	a := f.repo("deb-a", "deb", "hosted")
	b := f.repo("deb-b", "deb", "hosted")
	up := f.repo("deb-up", "deb", "proxy")
	opener := upstreamDeb(t, up.ID, "alpha 9.0", "zeta 1.0", "gamma 2.0")
	members := []string{a.ID, up.ID, b.ID}
	hosted := []*models.Repository{a, b}
	inc := f.repo("deb-inc", "deb", "group", members...)

	w := NewGroupMetadataWorker(f.pg, f.blobs, opener, nil, fixedResolver{stubSigner{}}, testLogger(t))
	t.Cleanup(w.Stop)

	var (
		names = []string{"alpha", "beta", "gamma", "delta", "epsilon"}
		dists = []string{"bookworm", "trixie"}
		comps = []string{"main", "contrib"}
		archs = []string{"amd64", "arm64", "all"}
		added = map[string][]string{}
	)

	for step := range 24 {
		var desc string

		switch op := rng.IntN(10); {
		case op < 6:
			repo := hosted[rng.IntN(len(hosted))]
			dist, comp, arch := dists[rng.IntN(2)], comps[rng.IntN(2)], archs[rng.IntN(3)]
			name, version := names[rng.IntN(len(names))], fmt.Sprintf("%d.0", 1+rng.IntN(3))

			id := f.debAdd(repo, dist, comp, arch, name, version)
			if id != "" {
				added[repo.ID] = append(added[repo.ID], id)
			}

			desc = fmt.Sprintf("add %s %s/%s/%s %s %s", repo.Name, dist, comp, arch, name, version)
		case op < 7:
			// The control fields of a file change without changing its size:
			// the index must follow, not be reused.
			repo := hosted[rng.IntN(len(hosted))]
			if ids := added[repo.ID]; len(ids) > 0 {
				if _, err := f.pg.GetDB().ExecContext(ctx,
					`UPDATE deb_files SET control_fields = control_fields || jsonb_build_object('Description', $2::text) WHERE id = $1`,
					ids[rng.IntN(len(ids))], fmt.Sprintf("d%04d", 1+rng.IntN(9000))); err != nil {
					t.Fatal(err)
				}

				desc = "rewrite the controls of a file of " + repo.Name
			}
		case op < 8:
			repo := hosted[rng.IntN(len(hosted))]
			if ids := added[repo.ID]; len(ids) > 0 {
				i := rng.IntN(len(ids))
				if err := f.pg.DeleteComponent(ctx, ids[i]); err != nil {
					t.Fatal(err)
				}

				added[repo.ID] = append(ids[:i], ids[i+1:]...)
				desc = "delete from " + repo.Name
			}
		default:
			desc = "no change"
		}

		if err := w.Generate(ctx, inc.ID, "", nil); err != nil {
			t.Fatalf("step %d (%s): Generate: %v", step, desc, err)
		}

		fresh := NewGroupMetadataWorker(f.pg, f.blobs, opener, nil, fixedResolver{stubSigner{}}, testLogger(t))
		ref := f.repo(fmt.Sprintf("deb-ref-%d", step), "deb", "group", members...)

		if err := fresh.Generate(ctx, ref.ID, "", nil); err != nil {
			t.Fatalf("step %d (%s): from-scratch Generate: %v", step, desc, err)
		}

		fresh.Stop()

		got, want := f.metadata(inc.ID), f.metadata(ref.ID)
		if g, w := keys(got), keys(want); !equalStrings(g, w) {
			t.Fatalf("step %d (%s): published files = %v, want %v", step, desc, g, w)
		}

		for path, data := range want {
			same := string(decompressed(t, path, got[path])) == string(decompressed(t, path, data))
			if strings.HasSuffix(path, "Release") {
				same = releaseHeader(got[path]) == releaseHeader(data)
			}

			if !same {
				t.Fatalf("step %d (%s): %s differs from a rebuild from scratch", step, desc, path)
			}
		}

		assertReleaseMatchesFiles(t, got, fmt.Sprintf("step %d (%s)", step, desc))

		if g, w := f.owners(inc.ID), f.owners(ref.ID); !maps.Equal(g, w) {
			t.Fatalf("step %d (%s): owners differ from a rebuild from scratch: got %d rows, want %d", step, desc, len(g), len(w))
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// A generation that changes nothing writes no owner row, and one upload writes
// one: the owners table is not rebuilt, so it does not fill with dead rows.
func TestGroupOwnersAreUpdatedInPlace(t *testing.T) {
	f := newGroupFixture(t)
	ctx := context.Background()

	a := f.repo("deb-a", "deb", "hosted")
	b := f.repo("deb-b", "deb", "hosted")
	grp := f.repo("deb-group", "deb", "group", a.ID, b.ID)

	var pkg0ID string

	for i := range 20 {
		id := f.debAdd(a, "bookworm", "main", "amd64", fmt.Sprintf("pkg%d", i), "1.0")
		if i == 0 {
			pkg0ID = id
		}
	}

	f.debAdd(b, "bookworm", "main", "amd64", "pkg0", "1.0")

	w := NewGroupMetadataWorker(f.pg, f.blobs, fakeUpstream{}, nil, fixedResolver{stubSigner{}}, testLogger(t))
	t.Cleanup(w.Stop)

	generate := func() {
		t.Helper()

		if err := w.Generate(ctx, grp.ID, "", nil); err != nil {
			t.Fatal(err)
		}
	}

	generate()

	before := f.ownerVersions(grp.ID)
	if len(before) != 20 {
		t.Fatalf("owner rows = %d, want 20 (b's pkg0 is the same path as a's: the first member owns it)", len(before))
	}

	generate()

	if after := f.ownerVersions(grp.ID); !maps.Equal(before, after) {
		t.Errorf("a generation that changed nothing rewrote owner rows")
	}

	f.debAdd(b, "bookworm", "main", "amd64", "extra", "1.0")
	generate()

	after := f.ownerVersions(grp.ID)
	if len(after) != 21 {
		t.Fatalf("owner rows = %d, want 21", len(after))
	}

	changed := 0

	for path, v := range after {
		if before[path] != v {
			changed++
		}
	}

	if changed != 1 {
		t.Errorf("one upload rewrote %d owner rows, want 1", changed)
	}

	// The member that owns a path changes when its file goes away.
	pkg0 := func() (string, string) {
		for p, m := range f.owners(grp.ID) {
			if strings.Contains(p, "/pkg0_") {
				return p, m
			}
		}

		t.Fatal("pkg0 has no owner")

		return "", ""
	}

	path, member := pkg0()
	if member != a.ID {
		t.Fatalf("pkg0 owner = %s, want the first member", member)
	}

	if err := f.pg.DeleteComponent(ctx, pkg0ID); err != nil {
		t.Fatal(err)
	}

	generate()

	if p, m := pkg0(); p != path || m != b.ID {
		t.Errorf("after the first member lost pkg0, owner of %s = %s (%s), want the second member", path, m, p)
	}
}
