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
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	formatdeb "github.com/openartifact/openartifact/internal/format/deb"
	"github.com/openartifact/openartifact/internal/format/formattest"
	formatrpm "github.com/openartifact/openartifact/internal/format/rpm"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/pkg/gpg"
	pkgrpm "github.com/openartifact/openartifact/pkg/rpm"
)

// fakeUpstream serves proxy members' files: key "<repo id>|<path>".
type fakeUpstream map[string][]byte

func (u fakeUpstream) Open(_ context.Context, repo *models.Repository, relPath string) (io.ReadCloser, error) {
	data, ok := u[repo.ID+"|"+relPath]
	if !ok {
		return nil, proxy.ErrNotFound
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

// downUpstream fails every read the way an unreachable upstream does.
type downUpstream struct{}

func (downUpstream) Open(context.Context, *models.Repository, string) (io.ReadCloser, error) {
	return nil, errors.New("connection refused")
}

type fakeRPMLoader map[string][]pkgrpm.Package

func (l fakeRPMLoader) PackagesForMetadata(_ context.Context, repoID string) ([]pkgrpm.Package, error) {
	return l[repoID], nil
}

// stubSigner signs with fixed bytes: the tests check what is published, not
// the cryptography (the signer is the same one hosted repositories use).
type stubSigner struct{}

func (stubSigner) CleartextSign(r io.Reader) (io.Reader, error) {
	data, _ := io.ReadAll(r)

	return strings.NewReader("-----BEGIN PGP SIGNED MESSAGE-----\n\n" + string(data) + "-----BEGIN PGP SIGNATURE-----\n"), nil
}
func (stubSigner) DetachedSign(io.Reader) ([]byte, error) { return []byte("detached"), nil }
func (stubSigner) PublicKey() ([]byte, error)             { return []byte("public"), nil }
func (stubSigner) KeyFingerprint() string                 { return "FP" }

var _ gpg.Signer = stubSigner{}

type groupFixture struct {
	t     *testing.T
	pg    *database.PostgresDB
	blobs *formattest.BlobStore
}

func newGroupFixture(t *testing.T) *groupFixture {
	t.Helper()

	return &groupFixture{t: t, pg: newTrashPurgeTestDB(t), blobs: formattest.NewBlobStore()}
}

func (f *groupFixture) repo(name, format, repoType string, members ...string) *models.Repository {
	f.t.Helper()

	org := trashTestOrgID
	repo := &models.Repository{Name: name, Format: format, RepoType: repoType, ProjectID: &org, CacheTTLSeconds: 3600}

	if repoType == "proxy" {
		repo.UpstreamURL = "https://upstream.example/" + name
	}

	if err := f.pg.CreateRepositoryWithMembers(context.Background(), repo, members); err != nil {
		f.t.Fatalf("create %s: %v", name, err)
	}

	return repo
}

func (f *groupFixture) blob(content string) string {
	f.t.Helper()

	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])

	var id string

	err := f.pg.GetDB().QueryRowContext(context.Background(),
		`INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES ($1, $2, $3)
		 ON CONFLICT (sha256) DO UPDATE SET size_bytes = EXCLUDED.size_bytes RETURNING id`,
		sha, len(content), sha).Scan(&id)
	if err != nil {
		f.t.Fatalf("insert blob: %v", err)
	}

	return id
}

// debPackage adds a bookworm/main/amd64 package to the hosted deb repository.
func (f *groupFixture) debPackage(repo *models.Repository, name, version string) {
	f.t.Helper()

	ctx := context.Background()

	dist, err := f.pg.GetOrCreateDebianDistribution(ctx, repo.ID, "bookworm")
	if err != nil {
		f.t.Fatal(err)
	}

	comp, err := f.pg.GetOrCreateDebianComponent(ctx, dist.ID, "main")
	if err != nil {
		f.t.Fatal(err)
	}

	if _, err := f.pg.GetOrCreateDebianArchitecture(ctx, dist.ID, "amd64"); err != nil {
		f.t.Fatal(err)
	}

	pkg := &models.Component{
		RepositoryID: repo.ID, ArtifactID: f.blob(repo.Name + name + version),
		Name: name, Version: version, Arch: "amd64", Filename: name + "_" + version + "_amd64.deb",
	}
	if err := f.pg.CreateDebPackage(ctx, pkg, dist.ID, comp.ID); err != nil {
		f.t.Fatalf("CreateDebPackage: %v", err)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}

func gz(t *testing.T, s string) []byte {
	t.Helper()

	var buf bytes.Buffer

	w := gzip.NewWriter(&buf)
	_, _ = w.Write([]byte(s))
	_ = w.Close()

	return buf.Bytes()
}

// upstreamDeb returns the files of a bookworm/main/amd64 upstream listing pkgs
// ("name version").
func upstreamDeb(t *testing.T, repoID string, pkgs ...string) fakeUpstream {
	t.Helper()

	var packages strings.Builder

	for _, p := range pkgs {
		name, version, _ := strings.Cut(p, " ")
		packages.WriteString("Package: " + name + "\nVersion: " + version + "\nArchitecture: amd64\nFilename: pool/main/" + name[:1] + "/" + name + "/" + name + "_" + version + "_amd64.deb\nSize: 5\nSHA256: " + strings.Repeat("ab", 32) + "\n\n")
	}

	sum := sha256.Sum256(gz(t, packages.String()))
	release := "Origin: up\nCodename: bookworm\nArchitectures: amd64\nComponents: main\nSHA256:\n " +
		hex.EncodeToString(sum[:]) + " 100 main/binary-amd64/Packages.gz\n"

	return fakeUpstream{
		repoID + "|dists/bookworm/Release":                       []byte(release),
		repoID + "|dists/bookworm/main/binary-amd64/Packages.gz": gz(t, packages.String()),
	}
}

func (f *groupFixture) metadata(groupID string) map[string][]byte {
	f.t.Helper()

	out := map[string][]byte{}

	for path, sha := range metadataSHAs(f.t, f.pg, groupID) {
		rc, err := f.blobs.Get(context.Background(), sha)
		if err != nil {
			f.t.Fatalf("metadata %s: %v", path, err)
		}

		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		out[path] = data
	}

	return out
}

func (f *groupFixture) owner(groupID, path string) string {
	f.t.Helper()

	id, ok, err := f.pg.GetGroupPackageOwner(context.Background(), groupID, path)
	if err != nil || !ok {
		f.t.Fatalf("owner of %s: %q, %v, %v", path, id, ok, err)
	}

	return id
}

type packageRow struct{ name, version, filename string }

func debPlainIndex(t *testing.T, md map[string][]byte) []byte {
	t.Helper()

	zr, err := gzip.NewReader(bytes.NewReader(md["dists/bookworm/main/binary-amd64/Packages.gz"]))
	if err != nil {
		t.Fatalf("Packages.gz: %v", err)
	}

	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}

	return plain
}

func debListing(t *testing.T, md map[string][]byte) []packageRow {
	t.Helper()

	zr, err := gzip.NewReader(bytes.NewReader(md["dists/bookworm/main/binary-amd64/Packages.gz"]))
	if err != nil {
		t.Fatalf("Packages.gz: %v", err)
	}

	var rows []packageRow

	err = formatdeb.ScanPackages(zr, func(s formatdeb.Stanza) error {
		rows = append(rows, packageRow{s.Package, s.Version, s.Filename})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return rows
}

func TestGroupDebMergesHostedAndProxyInMemberOrder(t *testing.T) {
	f := newGroupFixture(t)
	ctx := context.Background()

	hostedA := f.repo("deb-a", "deb", "hosted")
	hostedB := f.repo("deb-b", "deb", "hosted")
	up := f.repo("deb-up", "deb", "proxy")
	grp := f.repo("deb-group", "deb", "group", hostedA.ID, hostedB.ID, up.ID)

	f.debPackage(hostedA, "foo", "1.0")
	f.debPackage(hostedB, "foo", "1.0") // same version in a later member: A wins
	f.debPackage(hostedB, "bar", "2.0")

	opener := upstreamDeb(t, up.ID, "foo 1.0", "baz 3.0")

	w := NewGroupMetadataWorker(f.pg, f.blobs, opener, nil, fixedResolver{stubSigner{}}, testLogger(t))
	t.Cleanup(w.Stop)

	if err := w.Generate(ctx, grp.ID, "bookworm", nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	md := f.metadata(grp.ID)

	for _, path := range []string{"dists/bookworm/Release", "dists/bookworm/InRelease", "dists/bookworm/Release.gpg",
		"dists/bookworm/main/binary-amd64/Packages.gz", "dists/bookworm/main/binary-amd64/Packages.xz", "public.key"} {
		if _, ok := md[path]; !ok {
			t.Errorf("missing %s; have %v", path, keys(md))
		}
	}

	rows := debListing(t, md)

	want := map[string]string{"foo": "1.0", "bar": "2.0", "baz": "3.0"}
	if len(rows) != len(want) {
		t.Fatalf("listing = %+v, want one entry per package", rows)
	}

	for _, r := range rows {
		if want[r.name] != r.version {
			t.Errorf("unexpected entry %+v", r)
		}
	}

	owners := map[string]string{}

	for _, r := range rows {
		owners[r.name] = f.owner(grp.ID, r.filename)
	}

	if owners["foo"] != hostedA.ID || owners["bar"] != hostedB.ID || owners["baz"] != up.ID {
		t.Errorf("owners = %v, want foo→A, bar→B, baz→proxy (member order decides duplicates)", owners)
	}

	release := formatdeb.ParseRelease(md["dists/bookworm/Release"])
	if len(release.Files) == 0 || !release.HasIndex("main", "amd64") {
		t.Errorf("merged Release = %q", md["dists/bookworm/Release"])
	}

	// apt skips an index whose uncompressed entry is missing from Release.
	plain := debPlainIndex(t, md)
	sum := sha256.Sum256(plain)

	plainLine := hex.EncodeToString(sum[:]) + " " + strconv.Itoa(len(plain)) + " main/binary-amd64/Packages"
	if !strings.Contains(strings.Join(strings.Fields(string(md["dists/bookworm/Release"])), " ")+" ", plainLine+" ") {
		t.Errorf("Release does not list the uncompressed Packages as %q:\n%s", plainLine, md["dists/bookworm/Release"])
	}

	st, err := f.pg.GetGroupMetadataState(ctx, grp.ID)
	if err != nil || st.Stale || !slices.Contains(st.Dists, "bookworm") {
		t.Errorf("state after generation = %+v, %v; want fresh with the dist remembered", st, err)
	}
}

func TestGroupDebMemberOrderDecidesDuplicateVersion(t *testing.T) {
	f := newGroupFixture(t)
	ctx := context.Background()

	hostedA := f.repo("deb-a", "deb", "hosted")
	hostedB := f.repo("deb-b", "deb", "hosted")
	grp := f.repo("deb-group", "deb", "group", hostedB.ID, hostedA.ID)

	f.debPackage(hostedA, "foo", "1.0")
	f.debPackage(hostedB, "foo", "1.0")

	w := NewGroupMetadataWorker(f.pg, f.blobs, fakeUpstream{}, nil, fixedResolver{}, testLogger(t))
	t.Cleanup(w.Stop)

	if err := w.Generate(ctx, grp.ID, "bookworm", nil); err != nil {
		t.Fatal(err)
	}

	md := f.metadata(grp.ID)
	rows := debListing(t, md)

	if len(rows) != 1 || f.owner(grp.ID, rows[0].filename) != hostedB.ID {
		t.Errorf("listing %+v owner %s, want the first member (B) to win", rows, f.owner(grp.ID, rows[0].filename))
	}

	if _, ok := md["dists/bookworm/InRelease"]; ok {
		t.Error("an unsigned group must not publish InRelease")
	}

	if err := f.pg.ReplaceGroupMembers(ctx, grp.ID, []string{hostedA.ID, hostedB.ID}); err != nil {
		t.Fatal(err)
	}

	if err := w.Generate(ctx, grp.ID, "bookworm", nil); err != nil {
		t.Fatal(err)
	}

	rows = debListing(t, f.metadata(grp.ID))
	if len(rows) != 1 || f.owner(grp.ID, rows[0].filename) != hostedA.ID {
		t.Errorf("after reordering the owner must be A, listing %+v", rows)
	}
}

func TestGroupDebUnknownDistribution(t *testing.T) {
	f := newGroupFixture(t)

	hosted := f.repo("deb-a", "deb", "hosted")
	grp := f.repo("deb-group", "deb", "group", hosted.ID)
	f.debPackage(hosted, "foo", "1.0")

	w := NewGroupMetadataWorker(f.pg, f.blobs, fakeUpstream{}, nil, fixedResolver{}, testLogger(t))
	t.Cleanup(w.Stop)

	if err := w.Generate(context.Background(), grp.ID, "no-such-dist", nil); !errors.Is(err, group.ErrDistNotFound) {
		t.Errorf("Generate(unknown dist) = %v, want ErrDistNotFound", err)
	}

	if err := w.Generate(context.Background(), grp.ID, "../etc", nil); !errors.Is(err, group.ErrDistNotFound) {
		t.Errorf("Generate(invalid dist name) = %v, want ErrDistNotFound", err)
	}
}

func TestGroupDebUnreachableProxyIsPartialAndRetriesSoon(t *testing.T) {
	f := newGroupFixture(t)
	ctx := context.Background()

	hosted := f.repo("deb-a", "deb", "hosted")
	up := f.repo("deb-up", "deb", "proxy")
	grp := f.repo("deb-group", "deb", "group", hosted.ID, up.ID)
	f.debPackage(hosted, "foo", "1.0")

	w := NewGroupMetadataWorker(f.pg, f.blobs, downUpstream{}, nil, fixedResolver{}, testLogger(t))
	t.Cleanup(w.Stop)

	if err := w.Generate(ctx, grp.ID, "bookworm", nil); err != nil {
		t.Fatalf("an unreachable proxy member must not fail the group: %v", err)
	}

	rows := debListing(t, f.metadata(grp.ID))
	if len(rows) != 1 || rows[0].name != "foo" {
		t.Errorf("listing = %+v, want the hosted member's package", rows)
	}

	st, err := f.pg.GetGroupMetadataState(ctx, grp.ID)
	if err != nil || st.RefreshAfter == nil || st.GeneratedAt == nil || st.RefreshAfter.Sub(*st.GeneratedAt) > 2*time.Minute {
		t.Errorf("state = %+v, %v; a partial generation must schedule a retry within about a minute", st, err)
	}
}

func TestGroupDebProxyWithoutTheDistributionIsNotPartial(t *testing.T) {
	f := newGroupFixture(t)
	ctx := context.Background()

	hosted := f.repo("deb-a", "deb", "hosted")
	up := f.repo("deb-up", "deb", "proxy")
	grp := f.repo("deb-group", "deb", "group", hosted.ID, up.ID)
	f.debPackage(hosted, "foo", "1.0")

	w := NewGroupMetadataWorker(f.pg, f.blobs, fakeUpstream{}, nil, fixedResolver{}, testLogger(t))
	t.Cleanup(w.Stop)

	if err := w.Generate(ctx, grp.ID, "bookworm", nil); err != nil {
		t.Fatal(err)
	}

	st, err := f.pg.GetGroupMetadataState(ctx, grp.ID)
	if err != nil || st.RefreshAfter == nil || st.GeneratedAt == nil || st.RefreshAfter.Sub(*st.GeneratedAt) < 30*time.Minute {
		t.Errorf("state = %+v, %v; an upstream that answers 404 is a complete answer: refresh after the proxy's cache lifetime", st, err)
	}
}

func TestGroupDebHostedNameHidesProxyVersionsUnlessMerging(t *testing.T) {
	f := newGroupFixture(t)
	ctx := context.Background()

	hosted := f.repo("deb-a", "deb", "hosted")
	up := f.repo("deb-up", "deb", "proxy")
	grp := f.repo("deb-group", "deb", "group", hosted.ID, up.ID)
	f.debPackage(hosted, "foo", "1.0")

	opener := upstreamDeb(t, up.ID, "foo 9.9", "qux 1.0")

	w := NewGroupMetadataWorker(f.pg, f.blobs, opener, nil, fixedResolver{}, testLogger(t))
	t.Cleanup(w.Stop)

	versions := func() map[string]string {
		t.Helper()

		if err := w.Generate(ctx, grp.ID, "bookworm", nil); err != nil {
			t.Fatal(err)
		}

		out := map[string]string{}
		for _, r := range debListing(t, f.metadata(grp.ID)) {
			out[r.name] += r.version + " "
		}

		return out
	}

	got := versions()
	if got["foo"] != "1.0 " || got["qux"] != "1.0 " {
		t.Errorf("hosted-wins: listing = %v, want foo only as the hosted 1.0 (dependency confusion) and the proxy-only qux", got)
	}

	if _, err := f.pg.GetDB().ExecContext(ctx, `UPDATE repositories SET merge_proxy_versions = true WHERE id = $1`, grp.ID); err != nil {
		t.Fatal(err)
	}

	got = versions()
	if !strings.Contains(got["foo"], "1.0") || !strings.Contains(got["foo"], "9.9") {
		t.Errorf("merge_proxy_versions: foo = %q, want both versions", got["foo"])
	}
}

func TestGroupStaleTracking(t *testing.T) {
	f := newGroupFixture(t)
	ctx := context.Background()

	hosted := f.repo("deb-a", "deb", "hosted")
	other := f.repo("deb-b", "deb", "hosted")
	grp := f.repo("deb-group", "deb", "group", hosted.ID)
	f.debPackage(hosted, "foo", "1.0")

	w := NewGroupMetadataWorker(f.pg, f.blobs, fakeUpstream{}, nil, fixedResolver{}, testLogger(t))
	t.Cleanup(w.Stop)

	groups, err := f.pg.ListMetadataGroupsOfMember(ctx, hosted.ID)
	if err != nil || !slices.Equal(groups, []string{grp.ID}) {
		t.Errorf("groups of the member = %v, %v", groups, err)
	}

	if groups, _ := f.pg.ListMetadataGroupsOfMember(ctx, other.ID); len(groups) != 0 {
		t.Errorf("a repository outside the group lists groups %v", groups)
	}

	if err := w.Generate(ctx, grp.ID, "bookworm", nil); err != nil {
		t.Fatal(err)
	}

	if stale, _ := f.pg.ListStaleMetadataGroups(ctx); slices.Contains(stale, grp.ID) {
		t.Error("a generated group is not stale")
	}

	if err := f.pg.MarkGroupMetadataStale(ctx, grp.ID); err != nil {
		t.Fatal(err)
	}

	if stale, _ := f.pg.ListStaleMetadataGroups(ctx); !slices.Contains(stale, grp.ID) {
		t.Error("a marked group must be listed as stale")
	}

	if err := w.Generate(ctx, grp.ID, "", nil); err != nil {
		t.Fatal(err)
	}

	if stale, _ := f.pg.ListStaleMetadataGroups(ctx); slices.Contains(stale, grp.ID) {
		t.Error("regenerating clears the stale flag")
	}
}

func rpmPackage(name, version, sum string) pkgrpm.Package {
	return pkgrpm.Package{
		Name: name, Version: version, Release: "1", Arch: "x86_64", SHA256: sum,
		PackageSize: 10, Files: []pkgrpm.FileEntry{{Path: "/usr/bin/" + name}},
	}
}

func upstreamRPM(t *testing.T, repoID string, pkgs ...pkgrpm.Package) fakeUpstream {
	t.Helper()

	var primary, filelists, other bytes.Buffer

	for _, w := range []struct {
		buf   *bytes.Buffer
		write func(io.Writer, []pkgrpm.Package) error
	}{{&primary, formatrpm.WritePrimaryXML}, {&filelists, formatrpm.WriteFilelistsXML}, {&other, formatrpm.WriteOtherXML}} {
		if err := w.write(w.buf, pkgs); err != nil {
			t.Fatal(err)
		}
	}

	repomd := `<?xml version="1.0"?><repomd xmlns="http://linux.duke.edu/metadata/repo">` +
		`<data type="primary"><location href="repodata/p-primary.xml.gz"/></data>` +
		`<data type="filelists"><location href="repodata/f-filelists.xml.gz"/></data>` +
		`<data type="other"><location href="repodata/o-other.xml.gz"/></data></repomd>`

	return fakeUpstream{
		repoID + "|repodata/repomd.xml":         []byte(repomd),
		repoID + "|repodata/p-primary.xml.gz":   gz(t, primary.String()),
		repoID + "|repodata/f-filelists.xml.gz": gz(t, filelists.String()),
		repoID + "|repodata/o-other.xml.gz":     gz(t, other.String()),
	}
}

func rpmListing(t *testing.T, md map[string][]byte) []formatrpm.PackageInfo {
	t.Helper()

	repomd, ok := md["repodata/repomd.xml"]
	if !ok {
		t.Fatalf("no repomd.xml in %v", keys(md))
	}

	locs, err := formatrpm.ParseRepomd(bytes.NewReader(repomd))
	if err != nil {
		t.Fatal(err)
	}

	data, ok := md[locs["primary"]]
	if !ok {
		t.Fatalf("primary %q not published; have %v", locs["primary"], keys(md))
	}

	rc, err := formatrpm.OpenMetadata(io.NopCloser(bytes.NewReader(data)), locs["primary"])
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = rc.Close() }()

	var out []formatrpm.PackageInfo

	err = formatrpm.ScanPackages(rc, false, func(i formatrpm.PackageInfo, _ []byte) error {
		out = append(out, i)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func TestGroupRPMMergesHostedAndProxyInMemberOrder(t *testing.T) {
	f := newGroupFixture(t)
	ctx := context.Background()

	hostedA := f.repo("rpm-a", "rpm", "hosted")
	hostedB := f.repo("rpm-b", "rpm", "hosted")
	up := f.repo("rpm-up", "rpm", "proxy")
	grp := f.repo("rpm-group", "rpm", "group", hostedA.ID, hostedB.ID, up.ID)

	loader := fakeRPMLoader{
		hostedA.ID: {rpmPackage("foo", "1.0", "aaaa")},
		hostedB.ID: {rpmPackage("foo", "1.0", "bbbb"), rpmPackage("bar", "2.0", "cccc")},
	}
	opener := upstreamRPM(t, up.ID, rpmPackage("foo", "1.0", "dddd"), rpmPackage("baz", "3.0", "eeee"))

	w := NewGroupMetadataWorker(f.pg, f.blobs, opener, loader, fixedResolver{stubSigner{}}, testLogger(t))
	t.Cleanup(w.Stop)

	if err := w.Generate(ctx, grp.ID, "", nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	md := f.metadata(grp.ID)

	for _, path := range []string{"repodata/repomd.xml", "repodata/repomd.xml.asc", "public.key"} {
		if _, ok := md[path]; !ok {
			t.Errorf("missing %s; have %v", path, keys(md))
		}
	}

	owners := map[string]string{}
	pkgids := map[string]string{}

	rows := rpmListing(t, md)
	if len(rows) != 3 {
		t.Fatalf("listing = %+v, want foo, bar, baz once each", rows)
	}

	for _, r := range rows {
		href, ok := formatrpm.SafeLocation(r.Href)
		if !ok {
			t.Fatalf("unsafe href %q", r.Href)
		}

		owners[r.Name] = f.owner(grp.ID, href)
		pkgids[r.Name] = r.PkgID
	}

	if owners["foo"] != hostedA.ID || pkgids["foo"] != "aaaa" || owners["bar"] != hostedB.ID || owners["baz"] != up.ID {
		t.Errorf("owners %v pkgids %v: member order must decide the duplicate foo", owners, pkgids)
	}

	for _, kind := range []string{"filelists", "other"} {
		locs, err := formatrpm.ParseRepomd(bytes.NewReader(md["repodata/repomd.xml"]))
		if err != nil || locs[kind] == "" {
			t.Errorf("repomd lacks %s: %v, %v", kind, locs, err)
		}
	}
}

func TestGroupRPMProxyOnlyAndEmptyHosted(t *testing.T) {
	f := newGroupFixture(t)
	ctx := context.Background()

	empty := f.repo("rpm-empty", "rpm", "hosted")
	up := f.repo("rpm-up", "rpm", "proxy")
	grp := f.repo("rpm-group", "rpm", "group", empty.ID, up.ID)

	opener := upstreamRPM(t, up.ID, rpmPackage("baz", "3.0", "eeee"))

	w := NewGroupMetadataWorker(f.pg, f.blobs, opener, fakeRPMLoader{}, fixedResolver{}, testLogger(t))
	t.Cleanup(w.Stop)

	if err := w.Generate(ctx, grp.ID, "", nil); err != nil {
		t.Fatal(err)
	}

	md := f.metadata(grp.ID)

	if rows := rpmListing(t, md); len(rows) != 1 || rows[0].Name != "baz" {
		t.Errorf("listing = %+v", rows)
	}

	if _, ok := md["repodata/repomd.xml.asc"]; ok {
		t.Error("an unsigned group must not publish repomd.xml.asc")
	}
}

// fixedResolver signs every repository with one signer (nil: unsigned).
type fixedResolver struct{ signer gpg.Signer }

func (r fixedResolver) SignerForRepo(context.Context, string) (gpg.Signer, error) {
	return r.signer, nil
}

func TestGroupDebCallerWhoCannotReadAMemberGetsAVariantWithoutIt(t *testing.T) {
	f := newGroupFixture(t)
	ctx := context.Background()

	open := f.repo("deb-open", "deb", "hosted")
	secret := f.repo("deb-secret", "deb", "hosted")
	grp := f.repo("deb-group", "deb", "group", open.ID, secret.ID)

	f.debPackage(open, "foo", "1.0")
	f.debPackage(secret, "classified", "1.0")

	w := NewGroupMetadataWorker(f.pg, f.blobs, fakeUpstream{}, nil, fixedResolver{stubSigner{}}, testLogger(t))
	t.Cleanup(w.Stop)

	if err := w.Generate(ctx, grp.ID, "bookworm", nil); err != nil {
		t.Fatal(err)
	}

	if scope, ok, err := w.Scope(ctx, grp.ID, []string{secret.ID, open.ID}); err != nil || !ok || scope != "" {
		t.Fatalf("Scope(all members) = %q, %v, %v; want the shared index", scope, ok, err)
	}

	scope, ok, err := w.Scope(ctx, grp.ID, []string{open.ID})
	if err != nil || !ok || !strings.HasPrefix(scope, variantPrefix) {
		t.Fatalf("Scope(open) = %q, %v, %v; want a variant", scope, ok, err)
	}

	if _, ok, _ := w.Scope(ctx, grp.ID, []string{}); ok {
		t.Error("a caller who can read no member must have no metadata")
	}

	if err := w.Generate(ctx, grp.ID, "bookworm", []string{open.ID}); err != nil {
		t.Fatal(err)
	}

	md := f.metadata(grp.ID)
	variant := map[string][]byte{}

	for path, data := range md {
		if rest, ok := strings.CutPrefix(path, scope); ok {
			variant[rest] = data
		}
	}

	rows := debListing(t, variant)
	if len(rows) != 1 || rows[0].name != "foo" {
		t.Fatalf("variant lists %+v, want foo only", rows)
	}

	if f.owner(grp.ID, scope+rows[0].filename) != open.ID {
		t.Error("the variant must map its files to the readable member")
	}

	for _, p := range []string{"dists/bookworm/Release", "dists/bookworm/InRelease", "dists/bookworm/Release.gpg"} {
		if _, ok := variant[p]; !ok {
			t.Errorf("variant has no signed %s", p)
		}
	}

	shared := debListing(t, md)
	if len(shared) != 2 {
		t.Errorf("shared index lists %+v, want both members' packages", shared)
	}

	f.assertVariantsDroppedByRegeneration(t, w, grp.ID, scope+rows[0].filename)
}

// assertVariantsDroppedByRegeneration regenerates the shared index of the group
// and checks that no variant row or owner (ownerPath) survives.
func (f *groupFixture) assertVariantsDroppedByRegeneration(t *testing.T, w *GroupMetadataWorker, groupID, ownerPath string) {
	t.Helper()

	ctx := context.Background()

	if err := w.Generate(ctx, groupID, "bookworm", nil); err != nil {
		t.Fatal(err)
	}

	for path := range f.metadata(groupID) {
		if strings.HasPrefix(path, variantPrefix) {
			t.Errorf("variant row %s survived a regeneration of the shared index", path)
		}
	}

	if _, ok, _ := f.pg.GetGroupPackageOwner(ctx, groupID, ownerPath); ok {
		t.Error("variant owners survived a regeneration of the shared index")
	}
}
