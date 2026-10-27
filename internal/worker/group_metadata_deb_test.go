// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/ulikunitz/xz"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/repotype"
)

// debMergeUpstream serves the files of fake proxy members and counts how often
// each is opened, so a test can bound the upstream work of a generation.
type debMergeUpstream struct {
	mu    sync.Mutex
	files map[string][]byte
	opens map[string]int
	// failing paths answer with an error instead of the file.
	failing map[string]bool
}

func newDebMergeUpstream() *debMergeUpstream {
	return &debMergeUpstream{files: map[string][]byte{}, opens: map[string]int{}, failing: map[string]bool{}}
}

func (u *debMergeUpstream) Open(_ context.Context, repo *models.Repository, relPath string) (io.ReadCloser, error) {
	key := repo.ID + "|" + relPath

	u.mu.Lock()
	u.opens[key]++
	data, ok := u.files[key]
	failing := u.failing[key]
	u.mu.Unlock()

	if failing {
		return nil, errors.New("connection reset")
	}

	if !ok {
		return nil, proxy.ErrNotFound
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

// debSynthIndex is one Packages index of a fake upstream.
type debSynthIndex struct {
	comp, arch string
	packages   []debSynthPackage
}

type debSynthPackage struct {
	name, version, file, sha string
}

// stanza renders the Packages entry of p for arch.
func (p debSynthPackage) stanza(arch string) string {
	return "Package: " + p.name + "\nVersion: " + p.version + "\nArchitecture: " + arch +
		"\nFilename: " + p.file + "\nSize: 5\nSHA256: " + p.sha + "\nDescription: synthetic " + p.name + "\n\n"
}

func synthPackage(comp, arch, name, version string) debSynthPackage {
	sum := sha256.Sum256([]byte(comp + arch + name + version))

	return debSynthPackage{
		name: name, version: version,
		file: "pool/" + comp + "/" + name[:1] + "/" + name + "/" + name + "_" + version + "_" + arch + ".deb",
		sha:  hex.EncodeToString(sum[:]),
	}
}

func gzipBytes(tb testing.TB, s string) []byte {
	tb.Helper()

	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		tb.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

func xzBytes(tb testing.TB, s string) []byte {
	tb.Helper()

	var buf bytes.Buffer

	xw, err := xz.NewWriter(&buf)
	if err != nil {
		tb.Fatal(err)
	}

	if _, err := xw.Write([]byte(s)); err != nil {
		tb.Fatal(err)
	}

	if err := xw.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

// addMember publishes the indexes under dists/<dist>/ of a fake proxy member and
// returns the member. withXZ also serves (and lists) the xz variant, which the
// merge must prefer over gzip.
func (u *debMergeUpstream) addMember(tb testing.TB, id, dist string, withXZ bool, indexes []debSynthIndex) *models.Repository {
	tb.Helper()

	comps, archs := map[string]bool{}, map[string]bool{}

	var sums strings.Builder

	sums.WriteString("SHA256:\n")

	for _, ix := range indexes {
		comps[ix.comp], archs[ix.arch] = true, true

		var plain strings.Builder
		for _, p := range ix.packages {
			plain.WriteString(p.stanza(ix.arch))
		}

		base := ix.comp + "/binary-" + ix.arch + "/Packages"
		u.files[id+"|dists/"+dist+"/"+base+".gz"] = gzipBytes(tb, plain.String())
		sums.WriteString(" " + strings.Repeat("0", 64) + " 1 " + base + ".gz\n")

		if withXZ {
			u.files[id+"|dists/"+dist+"/"+base+".xz"] = xzBytes(tb, plain.String())
			sums.WriteString(" " + strings.Repeat("0", 64) + " 1 " + base + ".xz\n")
		}
	}

	release := "Origin: up\nCodename: " + dist + "\nArchitectures: " + strings.Join(setKeys(archs), " ") +
		"\nComponents: " + strings.Join(setKeys(comps), " ") + "\n" + sums.String()
	u.files[id+"|dists/"+dist+"/Release"] = []byte(release)

	return &models.Repository{ID: id, Name: id, RepoType: repotype.Proxy, Format: "deb"}
}

func setKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}

func newDebMergeJob(tb testing.TB, opener MemberOpener, members ...*models.Repository) *groupJob {
	tb.Helper()

	w := &GroupMetadataWorker{opener: opener}
	if tt, ok := tb.(*testing.T); ok {
		w.log = testLogger(tt)
	}

	return &groupJob{
		w:       w,
		group:   &models.Repository{ID: "group", Name: "group"},
		members: members,
		blobs:   map[string][]byte{},
		owners:  map[string]groupOwner{},
	}
}

func gunzip(tb testing.TB, data []byte) string {
	tb.Helper()

	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		tb.Fatal(err)
	}

	out, err := io.ReadAll(zr)
	if err != nil {
		tb.Fatal(err)
	}

	return string(out)
}

func unxz(tb testing.TB, data []byte) string {
	tb.Helper()

	xr, err := xz.NewReader(bytes.NewReader(data))
	if err != nil {
		tb.Fatal(err)
	}

	out, err := io.ReadAll(xr)
	if err != nil {
		tb.Fatal(err)
	}

	return string(out)
}

// TestBuildDebDistMergesProxyMembers checks the merged indexes against a plain
// sequential merge, the checksums the Release lists, and that every upstream
// file is opened exactly once however many indexes the group merges.
func TestBuildDebDistMergesProxyMembers(t *testing.T) {
	up := newDebMergeUpstream()

	var first, second []debSynthIndex

	want := map[string]string{} // comp/arch -> expected plain index

	for _, comp := range []string{"main", "universe"} {
		for _, arch := range []string{"amd64", "arm64"} {
			a := debSynthIndex{comp: comp, arch: arch}
			b := debSynthIndex{comp: comp, arch: arch}

			var plain strings.Builder

			for i := range 40 {
				p := synthPackage(comp, arch, fmt.Sprintf("pkg%03d", i), "1.0")
				a.packages = append(a.packages, p)
				plain.WriteString(p.stanza(arch))
			}

			// The second member repeats the first 10 versions (hidden: the first
			// member wins) and adds 20 packages of its own.
			for i := range 30 {
				p := synthPackage(comp, arch, fmt.Sprintf("pkg%03d", i), "1.0")
				if i >= 10 {
					p = synthPackage(comp, arch, fmt.Sprintf("extra%03d", i), "2.0")
					plain.WriteString(p.stanza(arch))
				}

				b.packages = append(b.packages, p)
			}

			first, second = append(first, a), append(second, b)
			want[comp+"/"+arch] = plain.String()
		}
	}

	ma := up.addMember(t, "ma", "noble", true, first)
	mb := up.addMember(t, "mb", "noble", false, second)
	j := newDebMergeJob(t, up, ma, mb)

	found, err := j.buildDebDist(t.Context(), "noble", nil)
	if err != nil || !found {
		t.Fatalf("buildDebDist = %v, %v", found, err)
	}

	if j.partial {
		t.Fatal("generation is partial")
	}

	release := string(j.blobs["dists/noble/Release"])

	for key, plain := range want {
		base := "dists/noble/" + strings.Replace(key, "/", "/binary-", 1) + "/Packages"

		if got := gunzip(t, j.blobs[base+".gz"]); got != plain {
			t.Errorf("%s.gz differs from the sequential merge (%d bytes, want %d)", base, len(got), len(plain))
		}

		if got := unxz(t, j.blobs[base+".xz"]); got != plain {
			t.Errorf("%s.xz differs from the sequential merge (%d bytes, want %d)", base, len(got), len(plain))
		}

		sum := sha256.Sum256([]byte(plain))
		if line := fmt.Sprintf(
			" %s %10d %s",
			hex.EncodeToString(sum[:]),
			len(plain),
			strings.TrimPrefix(base, "dists/noble/"),
		); !strings.Contains(
			release,
			line,
		) {
			t.Errorf("Release lacks the plain index entry %q", line)
		}

		for _, ext := range []string{".gz", ".xz"} {
			data := j.blobs[base+ext]
			gsum := sha256.Sum256(data)

			if line := fmt.Sprintf(
				" %s %10d %s",
				hex.EncodeToString(gsum[:]),
				len(data),
				strings.TrimPrefix(base, "dists/noble/")+ext,
			); !strings.Contains(
				release,
				line,
			) {
				t.Errorf("Release lacks %q", line)
			}
		}
	}

	// Bounded upstream work: one Release per member and one read per index,
	// the best compression only (xz for the first member, gzip for the second).
	for key, n := range up.opens {
		if n != 1 {
			t.Errorf("%s opened %d times, want 1", key, n)
		}
	}

	if got, want := len(up.opens), 2+4+4; got != want {
		t.Errorf("%d distinct upstream files read, want %d", got, want)
	}

	if _, ok := up.opens["ma|dists/noble/main/binary-amd64/Packages.gz"]; ok {
		t.Error("gzip variant read although the Release lists xz")
	}
}

// TestBuildDebDistClaimOrderIsDeterministic: two members list the same pool
// path with different content in different indexes. The first index in
// component/architecture order owns the file, whatever the scheduling of the
// concurrent upstream reads.
func TestBuildDebDistClaimOrderIsDeterministic(t *testing.T) {
	for range 25 {
		up := newDebMergeUpstream()

		pa := synthPackage("main", "amd64", "shared", "1.0")
		pb := pa
		pb.sha = strings.Repeat("cd", 32) // same pool path, other content
		pb.version = "1.1"                // another version, so it is not a duplicate

		ma := up.addMember(
			t,
			"ma",
			"noble",
			false,
			[]debSynthIndex{{comp: "main", arch: "amd64", packages: []debSynthPackage{pa}}, {comp: "main", arch: "arm64"}},
		)
		mb := up.addMember(
			t,
			"mb",
			"noble",
			false,
			[]debSynthIndex{{comp: "main", arch: "amd64"}, {comp: "main", arch: "arm64", packages: []debSynthPackage{pb}}},
		)
		j := newDebMergeJob(t, up, ma, mb)

		if _, err := j.buildDebDist(t.Context(), "noble", nil); err != nil {
			t.Fatal(err)
		}

		if got := gunzip(t, j.blobs["dists/noble/main/binary-amd64/Packages.gz"]); got != pa.stanza("amd64") {
			t.Fatalf("amd64 index = %q, want the first member's entry", got)
		}

		if got := gunzip(t, j.blobs["dists/noble/main/binary-arm64/Packages.gz"]); got != "" {
			t.Fatalf("arm64 index = %q, want the conflicting entry left out", got)
		}

		if o := j.owners[pa.file]; o.member != "ma" || o.sha256 != pa.sha {
			t.Fatalf("owner of %s = %+v", pa.file, o)
		}
	}
}

// TestBuildDebDistUnreadableIndexMakesPartial: an index that cannot be read
// makes the generation partial but does not stop the others.
func TestBuildDebDistUnreadableIndexMakesPartial(t *testing.T) {
	up := newDebMergeUpstream()
	p := synthPackage("main", "amd64", "foo", "1.0")
	q := synthPackage("universe", "amd64", "bar", "1.0")
	m := up.addMember(t, "ma", "noble", false, []debSynthIndex{
		{comp: "main", arch: "amd64", packages: []debSynthPackage{p}},
		{comp: "universe", arch: "amd64", packages: []debSynthPackage{q}},
	})
	up.failing["ma|dists/noble/universe/binary-amd64/Packages.gz"] = true

	j := newDebMergeJob(t, up, m)

	if _, err := j.buildDebDist(t.Context(), "noble", nil); err != nil {
		t.Fatal(err)
	}

	if !j.partial {
		t.Error("generation is not partial")
	}

	if got := gunzip(t, j.blobs["dists/noble/main/binary-amd64/Packages.gz"]); got != p.stanza("amd64") {
		t.Errorf("main index = %q", got)
	}

	if got := gunzip(t, j.blobs["dists/noble/universe/binary-amd64/Packages.gz"]); got != "" {
		t.Errorf("universe index = %q, want empty", got)
	}
}

// BenchmarkBuildDebDistFullArchive merges one proxy member shaped like a full
// distribution archive: 78,000 stanzas over 4 components and 3 architectures
// (the per-index sizes follow a real suite: one big component, several small).
func BenchmarkBuildDebDistFullArchive(b *testing.B) {
	plan := []struct {
		comp string
		n    int
	}{{"main", 4800}, {"universe", 20000}, {"restricted", 150}, {"multiverse", 1050}}

	up := newDebMergeUpstream()

	var indexes []debSynthIndex

	total := 0

	for _, arch := range []string{"amd64", "arm64", "i386"} {
		for _, c := range plan {
			ix := debSynthIndex{comp: c.comp, arch: arch}
			for i := range c.n {
				ix.packages = append(ix.packages, synthPackage(c.comp, arch, fmt.Sprintf("lib%s-%06d", c.comp[:3], i), "1.2.3-1"))
			}

			total += c.n
			indexes = append(indexes, ix)
		}
	}

	m := up.addMember(b, "ma", "noble", false, indexes)

	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		j := newDebMergeJob(b, up, m)

		if _, err := j.buildDebDist(context.Background(), "noble", nil); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportMetric(float64(total), "stanzas")
}
