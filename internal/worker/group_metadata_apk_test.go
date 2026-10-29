// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/repotype"
	pkgapk "github.com/openartifact/openartifact/pkg/apk"
	"github.com/openartifact/openartifact/pkg/apk/apktest"
	"github.com/openartifact/openartifact/pkg/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// apkSum is a well-formed index checksum (Q1 + base64 of a SHA-1) derived from tag.
func apkSum(tag string) string {
	sum := sha1.Sum([]byte(tag))

	return "Q1" + base64.StdEncoding.EncodeToString(sum[:])
}

func apkEntry(name, version, arch, sum string) pkgapk.Package {
	return pkgapk.Package{Name: name, Version: version, Arch: arch, Checksum: sum, Size: 100}
}

func apkMember(id string) *models.Repository { return &models.Repository{ID: id, Name: id} }

// selectedFiles renders a merge result as "<file> <member>" lines.
func selectedFiles(sel []apkSelected) []string {
	out := make([]string, len(sel))
	for i, s := range sel {
		out[i] = s.pkg.FileName() + " " + s.member.ID
	}

	return out
}

func TestMergeApkDir(t *testing.T) {
	t.Parallel()

	hosted, other, remote, mirror := apkMember("hosted"), apkMember("other"), apkMember("remote"), apkMember("mirror")

	tests := []struct {
		name    string
		sources []apkDirSource
		hidden  map[string]struct{}
		want    []string
	}{
		{name: "no sources", want: []string{}},
		{
			name:    "empty member lists",
			sources: []apkDirSource{{member: hosted}, {member: remote, proxy: true}},
			want:    []string{},
		},
		{
			name: "union in member order",
			sources: []apkDirSource{
				{member: hosted, entries: []pkgapk.Package{apkEntry("alpha", "1.0-r0", "x86_64", apkSum("a"))}},
				{member: remote, proxy: true, entries: []pkgapk.Package{
					apkEntry("beta", "2.0-r0", "x86_64", apkSum("b")), apkEntry("gamma", "3.0-r1", "x86_64", apkSum("c")),
				}},
			},
			want: []string{"alpha-1.0-r0.apk hosted", "beta-2.0-r0.apk remote", "gamma-3.0-r1.apk remote"},
		},
		{
			name: "first member wins on the same release",
			sources: []apkDirSource{
				{member: hosted, entries: []pkgapk.Package{apkEntry("alpha", "1.0-r0", "x86_64", apkSum("hosted"))}},
				{member: other, entries: []pkgapk.Package{apkEntry("alpha", "1.0-r0", "x86_64", apkSum("other"))}},
				{member: remote, proxy: true, entries: []pkgapk.Package{apkEntry("alpha", "1.0-r0", "x86_64", apkSum("remote"))}},
			},
			hidden: nil,
			want:   []string{"alpha-1.0-r0.apk hosted"},
		},
		{
			name: "a proxy listed before a hosted member wins the same release when versions merge",
			sources: []apkDirSource{
				{member: remote, proxy: true, entries: []pkgapk.Package{apkEntry("alpha", "1.0-r0", "x86_64", apkSum("remote"))}},
				{member: hosted, entries: []pkgapk.Package{apkEntry("alpha", "1.0-r0", "x86_64", apkSum("hosted"))}},
			},
			want: []string{"alpha-1.0-r0.apk remote"},
		},
		{
			name: "different releases of a name all stay when versions merge",
			sources: []apkDirSource{
				{member: hosted, entries: []pkgapk.Package{apkEntry("alpha", "1.0-r0", "x86_64", apkSum("a"))}},
				{member: remote, proxy: true, entries: []pkgapk.Package{apkEntry("alpha", "9.0-r0", "x86_64", apkSum("b"))}},
			},
			want: []string{"alpha-1.0-r0.apk hosted", "alpha-9.0-r0.apk remote"},
		},
		{
			name: "hosted names hide every proxy release of the name",
			sources: []apkDirSource{
				{member: hosted, entries: []pkgapk.Package{apkEntry("alpha", "1.0-r0", "x86_64", apkSum("a"))}},
				{member: remote, proxy: true, entries: []pkgapk.Package{
					apkEntry("alpha", "9.0-r0", "x86_64", apkSum("b")), apkEntry("beta", "2.0-r0", "x86_64", apkSum("c")),
				}},
				{member: mirror, proxy: true, entries: []pkgapk.Package{apkEntry("alpha", "1.0-r0", "x86_64", apkSum("d"))}},
			},
			hidden: map[string]struct{}{"alpha": {}},
			want:   []string{"alpha-1.0-r0.apk hosted", "beta-2.0-r0.apk remote"},
		},
		{
			name: "hosted names never hide another hosted member",
			sources: []apkDirSource{
				{member: hosted, entries: []pkgapk.Package{apkEntry("alpha", "1.0-r0", "x86_64", apkSum("a"))}},
				{member: other, entries: []pkgapk.Package{apkEntry("alpha", "2.0-r0", "x86_64", apkSum("b"))}},
			},
			hidden: map[string]struct{}{"alpha": {}},
			want:   []string{"alpha-1.0-r0.apk hosted", "alpha-2.0-r0.apk other"},
		},
		{
			name: "noarch entries are kept as listed and deduplicated by file name",
			sources: []apkDirSource{
				{member: hosted, entries: []pkgapk.Package{apkEntry("docs", "1.0-r0", "noarch", apkSum("a"))}},
				{member: remote, proxy: true, entries: []pkgapk.Package{
					apkEntry("docs", "1.0-r0", "noarch", apkSum("b")), apkEntry("fonts", "1.0-r0", "noarch", apkSum("c")),
				}},
			},
			want: []string{"docs-1.0-r0.apk hosted", "fonts-1.0-r0.apk remote"},
		},
		{
			name: "a repeated entry of one member counts once",
			sources: []apkDirSource{
				{member: remote, proxy: true, entries: []pkgapk.Package{
					apkEntry("alpha", "1.0-r0", "x86_64", apkSum("a")), apkEntry("alpha", "1.0-r0", "x86_64", apkSum("b")),
				}},
			},
			want: []string{"alpha-1.0-r0.apk remote"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := selectedFiles(mergeApkDir(tt.sources, tt.hidden))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMergeApkDirKeepsTheChosenEntry(t *testing.T) {
	t.Parallel()

	hosted, remote := apkMember("hosted"), apkMember("remote")

	sel := mergeApkDir([]apkDirSource{
		{member: hosted, entries: []pkgapk.Package{apkEntry("alpha", "1.0-r0", "x86_64", apkSum("hosted"))}},
		{member: remote, proxy: true, entries: []pkgapk.Package{apkEntry("alpha", "1.0-r0", "x86_64", apkSum("remote"))}},
	}, nil)

	require.Len(t, sel, 1)
	assert.Equal(t, apkSum("hosted"), sel[0].pkg.Checksum, "the entry carries the winner's checksum, which the owner serves")
	assert.Same(t, hosted, sel[0].member)
}

func TestValidApkDirSegments(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"v3.20/main/x86_64":    true,
		"edge/community/armv7": true,
		"":                     false,
		"v3.20/main":           false,
		"v3.20/main/x86_64/x":  false,
		"v3.20/../x86_64":      false,
		"v3.20//x86_64":        false,
		"v3.20/main/x 86":      false,
		".oa-variant/x/y":      false,
	}

	for dir, want := range tests {
		assert.Equal(t, want, validApkDir(dir), dir)
	}
}

func TestLimitedReaderFailsInsteadOfTruncating(t *testing.T) {
	t.Parallel()

	data := strings.Repeat("x", 100)

	var within bytes.Buffer

	_, err := within.ReadFrom(&limitedReader{r: strings.NewReader(data), left: 100})
	require.NoError(t, err)
	assert.Equal(t, data, within.String())

	var over bytes.Buffer

	_, err = over.ReadFrom(&limitedReader{r: strings.NewReader(data), left: 99})
	require.Error(t, err)
	assert.ErrorIs(t, err, errApkIndexTooLarge)
}

func TestGroupAnchor(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "dists/bookworm/Release", groupAnchor("deb", "bookworm"))
	assert.Equal(t, "repodata/repomd.xml", groupAnchor("rpm", ""))
	assert.Equal(t, "v3.20/main/x86_64/APKINDEX.tar.gz", groupAnchor("apk", "v3.20/main/x86_64"))
	assert.Empty(t, groupAnchor("apk", ""), "an apk group without a requested directory has no anchor")
}

// apkGroupDB serves the packages of hosted members to a generation.
type apkGroupDB struct {
	GroupMetadataStore

	rows map[string][]*models.ApkPackage
}

func (d *apkGroupDB) ListApkPackages(_ context.Context, repoID string) ([]*models.ApkPackage, error) {
	return d.rows[repoID], nil
}

func hostedRow(branch, repo, arch, name, version, sum string) *models.ApkPackage {
	return &models.ApkPackage{
		Branch: branch, Repo: repo, Arch: arch, PkgArch: arch, Name: name, Version: version, Checksum: sum, SizeBytes: 10,
	}
}

// apkUpstream serves fake proxy members' indexes by member ID and path.
type apkUpstream struct {
	files   map[string][]byte
	failing map[string]bool
}

func (u *apkUpstream) Open(_ context.Context, repo *models.Repository, relPath string) (io.ReadCloser, error) {
	key := repo.ID + "|" + relPath
	if u.failing[key] {
		return nil, errors.New("connection reset")
	}

	data, ok := u.files[key]
	if !ok {
		return nil, proxy.ErrNotFound
	}

	return io.NopCloser(bytes.NewReader(data)), nil
}

func upstreamIndex(t *testing.T, pkgs ...pkgapk.Package) []byte {
	t.Helper()

	data, err := pkgapk.BuildIndex(pkgs, "upstream", nil)
	require.NoError(t, err)

	return data
}

// newApkJob builds a generation of a group signing with a fresh key, whose
// public half it returns.
func newApkJob(
	t *testing.T,
	db *apkGroupDB,
	up *apkUpstream,
	mergeProxy bool,
	members ...*models.Repository,
) (*groupJob, *rsa.PublicKey) {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	require.NoError(t, err)

	key, err := pkgapk.GenerateKey(2048)
	require.NoError(t, err)

	signer, err := pkgapk.NewSigner("openartifact-grp.rsa.pub", key)
	require.NoError(t, err)

	w := &GroupMetadataWorker{db: db, opener: up, log: log}

	return &groupJob{
		w:         w,
		group:     &models.Repository{ID: "grp", Name: "grp", Format: "apk", RepoType: repotype.Group, MergeProxyVersions: mergeProxy},
		members:   members,
		apkSigner: signer,
		blobs:     make(map[string][]byte),
		owners:    make(map[string]groupOwner),
		keep:      make(map[string]struct{}),
	}, &key.PublicKey
}

// indexNames parses a generated index and returns "<file> <checksum>" lines.
func indexNames(t *testing.T, j *groupJob, path string) []string {
	t.Helper()

	data, ok := j.blobs[path]
	require.True(t, ok, "no index at %s (have %v)", path, slices.Collect(maps.Keys(j.blobs)))

	pkgs, err := pkgapk.ParseIndex(bytes.NewReader(data))
	require.NoError(t, err)

	out := make([]string, len(pkgs))
	for i := range pkgs {
		out[i] = pkgs[i].FileName() + " " + pkgs[i].Checksum
	}

	return out
}

func TestBuildApkMergesHostedAndProxyAndSigns(t *testing.T) {
	t.Parallel()

	const dir = "v3.20/main/x86_64"

	hosted := &models.Repository{ID: "h", Name: "h", Format: "apk", RepoType: repotype.Hosted}
	remote := &models.Repository{ID: "p", Name: "p", Format: "apk", RepoType: repotype.Proxy}

	db := &apkGroupDB{rows: map[string][]*models.ApkPackage{
		"h": {
			hostedRow("v3.20", "main", "x86_64", "alpha", "1.0-r0", apkSum("ha")),
			hostedRow("v3.20", "main", "x86_64", "mine", "1.0-r0", apkSum("hm")),
		},
	}}
	up := &apkUpstream{files: map[string][]byte{
		"p|" + dir + "/APKINDEX.tar.gz": upstreamIndex(t,
			apkEntry("alpha", "1.0-r0", "x86_64", apkSum("pa")), apkEntry("alpha", "2.0-r0", "x86_64", apkSum("pa2")),
			apkEntry("beta", "3.0-r0", "x86_64", apkSum("pb"))),
		"p|v3.20/community/x86_64/APKINDEX.tar.gz": upstreamIndex(t, apkEntry("gamma", "1.0-r0", "x86_64", apkSum("pg"))),
	}}

	j, pub := newApkJob(t, db, up, false, hosted, remote)
	require.NoError(t, j.buildApk(context.Background(), []string{"v3.20/community/x86_64", "v3.20/missing/x86_64"}))

	assert.False(t, j.partial)
	assert.Equal(t, []string{"v3.20/community/x86_64", dir}, j.dists, "directories found, the unknown one is dropped")

	// Hosted wins: alpha is held by the hosted member, so the proxy's alpha 2.0 is hidden;
	// beta, which only the proxy has, is served.
	assert.Equal(t, []string{"alpha-1.0-r0.apk " + apkSum("ha"), "beta-3.0-r0.apk " + apkSum("pb"), "mine-1.0-r0.apk " + apkSum("hm")},
		indexNames(t, j, dir+"/APKINDEX.tar.gz"))
	assert.Equal(t, []string{"gamma-1.0-r0.apk " + apkSum("pg")}, indexNames(t, j, "v3.20/community/x86_64/APKINDEX.tar.gz"))

	assert.Equal(t, map[string]groupOwner{
		dir + "/alpha-1.0-r0.apk":                 {member: "h", sha256: apkSum("ha")},
		dir + "/mine-1.0-r0.apk":                  {member: "h", sha256: apkSum("hm")},
		dir + "/beta-3.0-r0.apk":                  {member: "p", sha256: apkSum("pb")},
		"v3.20/community/x86_64/gamma-1.0-r0.apk": {member: "p", sha256: apkSum("pg")},
	}, j.owners)

	// The merged index is signed with the group's key.
	name, err := apktest.VerifyIndex(j.blobs[dir+"/APKINDEX.tar.gz"], map[string]*rsa.PublicKey{"openartifact-grp.rsa.pub": pub})
	require.NoError(t, err)
	assert.Equal(t, "openartifact-grp.rsa.pub", name)
}

func TestBuildApkMergeProxyVersionsKeepsProxyReleases(t *testing.T) {
	t.Parallel()

	const dir = "v3.20/main/x86_64"

	hosted := &models.Repository{ID: "h", Name: "h", Format: "apk", RepoType: repotype.Hosted}
	remote := &models.Repository{ID: "p", Name: "p", Format: "apk", RepoType: repotype.Proxy}
	db := &apkGroupDB{rows: map[string][]*models.ApkPackage{"h": {hostedRow("v3.20", "main", "x86_64", "alpha", "1.0-r0", apkSum("ha"))}}}
	up := &apkUpstream{files: map[string][]byte{
		"p|" + dir + "/APKINDEX.tar.gz": upstreamIndex(t, apkEntry("alpha", "2.0-r0", "x86_64", apkSum("pa"))),
	}}

	j, _ := newApkJob(t, db, up, true, hosted, remote)
	require.NoError(t, j.buildApk(context.Background(), nil))

	assert.Equal(
		t,
		[]string{"alpha-1.0-r0.apk " + apkSum("ha"), "alpha-2.0-r0.apk " + apkSum("pa")},
		indexNames(t, j, dir+"/APKINDEX.tar.gz"),
	)
}

func TestBuildApkUnreadableProxyIsPartial(t *testing.T) {
	t.Parallel()

	const (
		hostedDir = "v3.20/main/x86_64"
		proxyDir  = "v3.20/community/x86_64"
	)

	hosted := &models.Repository{ID: "h", Name: "h", Format: "apk", RepoType: repotype.Hosted}
	remote := &models.Repository{ID: "p", Name: "p", Format: "apk", RepoType: repotype.Proxy}
	db := &apkGroupDB{rows: map[string][]*models.ApkPackage{"h": {hostedRow("v3.20", "main", "x86_64", "alpha", "1.0-r0", apkSum("ha"))}}}
	up := &apkUpstream{
		files: map[string][]byte{},
		failing: map[string]bool{
			"p|" + hostedDir + "/APKINDEX.tar.gz": true,
			"p|" + proxyDir + "/APKINDEX.tar.gz":  true,
		},
	}

	j, _ := newApkJob(t, db, up, false, hosted, remote)
	require.NoError(t, j.buildApk(context.Background(), []string{proxyDir}))

	assert.True(t, j.partial, "an unreadable proxy member makes the generation partial, to be retried")

	// The directory the hosted member has is published without the proxy's entries.
	assert.Equal(t, []string{"alpha-1.0-r0.apk " + apkSum("ha")}, indexNames(t, j, hostedDir+"/APKINDEX.tar.gz"))

	// The proxy-only directory is neither rendered nor forgotten: its previous
	// index stays and the retry finds it again.
	assert.NotContains(t, j.blobs, proxyDir+"/APKINDEX.tar.gz")
	assert.Contains(t, j.keep, proxyDir+"/APKINDEX.tar.gz")
	assert.Contains(t, j.dists, proxyDir)
	assert.Equal(t, []string{proxyDir + "/"}, j.keptPrefixes())
}

func TestAddEmptyApkIndexes(t *testing.T) {
	t.Parallel()

	db := &apkGroupDB{}
	j, pub := newApkJob(t, db, &apkUpstream{}, false)
	j.scope = variantPrefix + "abc/"
	j.blobs["v3.20/main/x86_64/APKINDEX.tar.gz"] = []byte("current")
	j.keep["v3.20/kept/x86_64/APKINDEX.tar.gz"] = struct{}{}

	old := map[string]string{
		j.scope + "v3.20/main/x86_64/APKINDEX.tar.gz": "sha-current",
		j.scope + "v3.20/gone/x86_64/APKINDEX.tar.gz": "sha-gone",
		j.scope + "v3.20/kept/x86_64/APKINDEX.tar.gz": "sha-kept",
		j.scope + "v3.20/main/x86_64/notes.txt":       "sha-other",
	}

	require.NoError(t, j.addEmptyApkIndexes(old))

	assert.Equal(t, []byte("current"), j.blobs["v3.20/main/x86_64/APKINDEX.tar.gz"])
	assert.NotContains(t, j.blobs, "v3.20/kept/x86_64/APKINDEX.tar.gz", "a directory kept for a retry is not blanked")
	assert.Len(t, j.blobs, 2, "only the vanished directory gets an empty index")

	empty, ok := j.blobs["v3.20/gone/x86_64/APKINDEX.tar.gz"]
	require.True(t, ok)

	name, err := apktest.VerifyIndex(empty, map[string]*rsa.PublicKey{"openartifact-grp.rsa.pub": pub})
	require.NoError(t, err)
	assert.Equal(t, "openartifact-grp.rsa.pub", name)

	pkgs, err := pkgapk.ParseIndex(bytes.NewReader(empty))
	require.NoError(t, err)
	assert.Empty(t, pkgs)
}

func TestCheckApkDir(t *testing.T) {
	t.Parallel()

	const dir = "v3.20/main/x86_64"

	hosted := &models.Repository{ID: "h", Name: "h", Format: "apk", RepoType: repotype.Hosted}
	remote := &models.Repository{ID: "p", Name: "p", Format: "apk", RepoType: repotype.Proxy}
	db := &apkGroupDB{rows: map[string][]*models.ApkPackage{"h": {hostedRow("v3.20", "main", "x86_64", "alpha", "1.0-r0", apkSum("ha"))}}}
	up := &apkUpstream{
		files:   map[string][]byte{"p|v3.21/main/aarch64/APKINDEX.tar.gz": upstreamIndex(t)},
		failing: map[string]bool{"p|v3.19/main/x86_64/APKINDEX.tar.gz": true},
	}

	j, _ := newApkJob(t, db, up, false, hosted, remote)
	members := []*models.Repository{hosted, remote}

	require.NoError(t, j.w.checkApkDir(context.Background(), dir, members), "a hosted member has it")
	require.NoError(t, j.w.checkApkDir(context.Background(), "v3.21/main/aarch64", members), "a proxy member has it, even empty")
	require.ErrorIs(t, j.w.checkApkDir(context.Background(), "v3.22/main/x86_64", members), group.ErrDistNotFound)
	require.ErrorIs(t, j.w.checkApkDir(context.Background(), "../etc/x", members), group.ErrDistNotFound)

	err := j.w.checkApkDir(context.Background(), "v3.19/main/x86_64", members)
	require.Error(t, err)
	assert.NotErrorIs(t, err, group.ErrDistNotFound, "an unreadable proxy is not a missing directory")
}
