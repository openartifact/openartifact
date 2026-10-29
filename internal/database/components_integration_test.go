// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// packageSeed is one file written through writePackage, the way a format store
// writes it (the shapes are those of /docs/design/format-plugins.md and the
// per-format specs).
type packageSeed struct {
	repo, format, name, version, filename, arch string
	// path defaults to filename; versionKey to version.
	path, versionKey string
	// blobSeed derives the blob digest ("" = a file without a blob).
	blobSeed string
	blobSize int64
	// size is the declared size_bytes that wins over the blob's (0 = none).
	size int64
	// attrs, meta are the file attrs and the version metadata.
	attrs, meta map[string]any
	unlisted    bool
	role        string
	created     time.Time
}

// seedPackageFile writes seed and returns the id of its file.
func seedPackageFile(t *testing.T, pg *PostgresDB, s packageSeed) string {
	t.Helper()

	if s.path == "" {
		s.path = s.filename
	}

	if s.versionKey == "" {
		s.versionKey = s.version
	}

	f := FileWrite{Path: s.path, Filename: s.filename, Arch: s.arch, Attrs: s.attrs, Unlisted: s.unlisted, Role: s.role}

	if s.blobSeed != "" {
		sum := sha256.Sum256([]byte(s.blobSeed))
		sha := hex.EncodeToString(sum[:])
		f.Blob = BlobWrite{SHA256: sha, Key: sha, MediaType: "application/octet-stream", Size: s.blobSize}
	}

	if s.size != 0 {
		f.SizeBytes = &s.size
	}

	out, err := pg.writePackage(context.Background(), &PackageWrite{
		RepoID: s.repo, Format: s.format, Name: s.name, NameKey: s.name, Version: s.version, VersionKey: s.versionKey,
		VersionMeta: s.meta, CreatedAt: s.created, Files: []FileWrite{f},
	})
	if err != nil {
		t.Fatalf("write %s %s@%s: %v", s.format, s.name, s.version, err)
	}

	return out.Files[0].ID
}

// listed is one expected row of the listing.
type listed struct {
	name, version, format, arch, filename string
	size                                  int64
}

func listComponentsOf(t *testing.T, pg *PostgresDB, repo string, opts ComponentListOptions) ([]*models.Component, int) {
	t.Helper()

	opts.Page, opts.Limit = 1, 100

	rows, total, err := pg.SearchComponents(context.Background(), repo, opts)
	if err != nil {
		t.Fatalf("SearchComponents(%+v): %v", opts, err)
	}

	return rows, total
}

// TestComponentListingShapes writes the shapes of the formats whose listing
// reads something other than the plain name/version/filename and checks the
// listing, the by-name order, the filters, the download lookup and the name
// resolution against the expected rows.
func TestComponentListingShapes(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	db := pg.GetDB()
	ctx := context.Background()
	repo := seedRepo(t, db, "shapes", "raw")
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)

	seeds := []struct {
		seed packageSeed
		want *listed
	}{
		{packageSeed{format: "maven", name: "org.acme:lib", version: "5.0", arch: "sources", filename: "org/acme/lib/5.0/lib-5.0-sources.jar", blobSize: 10},
			&listed{"org.acme:lib", "5.0", "maven", "sources", "org/acme/lib/5.0/lib-5.0-sources.jar", 10}},
		{packageSeed{format: "npm", name: "@acme/widget", version: "1.0.0", filename: "widget-1.0.0.tgz", blobSize: 20},
			&listed{"@acme/widget", "1.0.0", "npm", "", "widget-1.0.0.tgz", 20}},
		{packageSeed{format: "apk", name: "v3.20/main/musl", version: "1.2.5-r0", arch: "x86_64", filename: "musl-1.2.5-r0.apk", blobSize: 30,
			attrs: map[string]any{"build_time": 1700000000, "installed_size": 99}},
			&listed{"v3.20/main/musl", "1.2.5-r0", "apk", "x86_64", "musl-1.2.5-r0.apk", 30}},
		{packageSeed{format: "terraform", name: "providers/null", version: "16.0.0", arch: "linux_arm64", filename: "terraform-provider-null_16.0.0_linux_arm64.zip", blobSize: 40},
			&listed{"providers/null", "16.0.0", "terraform", "linux_arm64", "terraform-provider-null_16.0.0_linux_arm64.zip", 40}},
		{packageSeed{format: "terraform", name: "modules/vpc/aws", version: "1.0.0", filename: "vpc-aws-1.0.0.zip", blobSize: 50},
			&listed{"modules/vpc/aws", "1.0.0", "terraform", "", "vpc-aws-1.0.0.zip", 50}},
		{packageSeed{format: "go", name: "example.com/mod", version: "v1.0.0", path: "example.com/mod/@v/v1.0.0.zip", filename: "v1.0.0.zip", blobSize: 60},
			&listed{"example.com/mod", "v1.0.0", "go", "", "v1.0.0.zip", 60}},
		// The go .mod is a second file of the same version and stays out.
		{packageSeed{format: "go", name: "example.com/mod", version: "v1.0.0", path: "example.com/mod/@v/v1.0.0.mod", filename: "v1.0.0.mod", blobSize: 5,
			unlisted: true, role: "mod"}, nil},
		// A conan recipe is listed through its manifest by its full reference.
		{packageSeed{format: "conan", name: "zlib", version: "1.3", path: "zlib/1.3/_/_/r1/export/conanmanifest.txt", filename: "zlib/1.3@_/_#r1",
			attrs: map[string]any{"release": "r1"}},
			&listed{"zlib", "1.3", "conan", "", "zlib/1.3@_/_#r1", 0}},
		{packageSeed{format: "conan", name: "zlib", version: "1.3", path: "zlib/1.3/_/_/r1/export/conan_export.tgz", filename: "conan_export.tgz", blobSize: 7,
			unlisted: true}, nil},
		{packageSeed{format: "raw", name: "dir/a.txt", filename: "dir/a.txt", blobSize: 70},
			&listed{"dir/a.txt", "", "raw", "", "dir/a.txt", 70}},
		{packageSeed{format: "rubygems", name: "rack", version: "3.0", arch: "java", filename: "rack-3.0-java.gem", blobSize: 80, size: 4096},
			&listed{"rack", "3.0", "rubygems", "java", "rack-3.0-java.gem", 4096}},
		{packageSeed{format: "rpm", name: "bash", version: "5.2", arch: "x86_64", filename: "bash-5.2-1.x86_64.rpm", blobSize: 90,
			attrs: map[string]any{"release": "1", "epoch": 1, "build_time": 1700000001, "archive_size": 3, "installed_size": 4}},
			&listed{"bash", "5.2", "rpm", "x86_64", "bash-5.2-1.x86_64.rpm", 90}},
	}

	ids := map[string]string{}

	for i, s := range seeds {
		s.seed.repo, s.seed.created = repo, base.Add(time.Duration(i)*time.Minute)
		s.seed.blobSeed = s.seed.filename

		if s.seed.blobSize == 0 {
			s.seed.blobSeed = ""
		}

		id := seedPackageFile(t, pg, s.seed)
		if s.want != nil {
			ids[s.want.name+"@"+s.want.version+"/"+s.want.arch] = id
		}
	}

	var want []listed

	for _, s := range seeds {
		if s.want != nil {
			want = append(want, *s.want)
		}
	}

	got, total := listComponentsOf(t, pg, repo, ComponentListOptions{})
	if total != len(want) || len(got) != len(want) {
		t.Fatalf("listing total %d len %d, want %d", total, len(got), len(want))
	}

	// Newest first: the seeds were written oldest first.
	for i, c := range got {
		w := want[len(want)-1-i]
		if c.Name != w.name || c.Version != w.version || c.Format != w.format || c.Arch != w.arch || c.Filename != w.filename || c.PackageSize != w.size {
			t.Errorf("row %d = %s %s %s %q %s %d, want %+v", i, c.Name, c.Version, c.Format, c.Arch, c.Filename, c.PackageSize, w)
		}
	}

	t.Run("attrs", func(t *testing.T) {
		rpm, _ := listComponentsOf(t, pg, repo, ComponentListOptions{Format: "rpm"})
		if len(rpm) != 1 || rpm[0].Release != "1" || rpm[0].Epoch != 1 || rpm[0].BuildTime != 1700000001 || rpm[0].ArchiveSize != 3 || rpm[0].InstalledSize != 4 {
			t.Fatalf("rpm attrs %+v", rpm)
		}

		apk, _ := listComponentsOf(t, pg, repo, ComponentListOptions{Format: "apk"})
		if len(apk) != 1 || apk[0].BuildTime != 1700000000 || apk[0].InstalledSize != 99 {
			t.Fatalf("apk attrs %+v", apk)
		}

		conan, _ := listComponentsOf(t, pg, repo, ComponentListOptions{Format: "conan"})
		if len(conan) != 1 || conan[0].Release != "r1" || conan[0].SHA256 != "" {
			t.Fatalf("conan %+v", conan)
		}
	})

	t.Run("by name", func(t *testing.T) {
		got, total := listComponentsOf(t, pg, repo, ComponentListOptions{SortByName: true})
		if total != len(want) || len(got) != len(want) {
			t.Fatalf("total %d len %d", total, len(got))
		}

		for i := 1; i < len(got); i++ {
			if got[i-1].Name > got[i].Name {
				t.Errorf("not sorted by name: %q before %q", got[i-1].Name, got[i].Name)
			}
		}
	})

	t.Run("filters", func(t *testing.T) {
		for _, tc := range []struct {
			opts ComponentListOptions
			want int
		}{
			{ComponentListOptions{Format: "terraform"}, 2},
			{ComponentListOptions{Format: "go"}, 1},
			{ComponentListOptions{Format: "docker"}, 0},
			{ComponentListOptions{Name: "providers/null"}, 1},
			{ComponentListOptions{Name: "providers"}, 0},
			{ComponentListOptions{Query: "VPC"}, 1},
			{ComponentListOptions{Query: "5.2"}, 1},
			{ComponentListOptions{Query: "a"}, 8},
			{ComponentListOptions{Format: "maven", Name: "org.acme:lib", Query: "5"}, 1},
			{ComponentListOptions{Format: "npm", Query: "zzz"}, 0},
		} {
			for _, byName := range []bool{false, true} {
				tc.opts.SortByName = byName

				if rows, total := listComponentsOf(t, pg, repo, tc.opts); total != tc.want || len(rows) != tc.want {
					t.Errorf("%+v: total %d len %d, want %d", tc.opts, total, len(rows), tc.want)
				}
			}
		}
	})

	t.Run("pages", func(t *testing.T) {
		rows, total, err := pg.SearchComponents(ctx, repo, ComponentListOptions{Page: 2, Limit: 3})
		if err != nil || total != len(want) || len(rows) != 3 || rows[0].Name != want[len(want)-4].name {
			t.Fatalf("page 2: total %d len %d err %v rows %+v", total, len(rows), err, rows)
		}
	})

	t.Run("download lookup and names", func(t *testing.T) {
		all := make([]string, 0, len(ids))

		for key, id := range ids {
			all = append(all, id)

			blob, err := pg.GetRepoComponentBlob(ctx, repo, id)
			if err != nil || blob == nil {
				t.Fatalf("%s: blob %+v err %v", key, blob, err)
			}
		}

		rows, err := pg.q.ResolveComponentNames(ctx, all)
		if err != nil || len(rows) != len(all) {
			t.Fatalf("resolve: %d rows, err %v, want %d", len(rows), err, len(all))
		}
	})

	t.Run("usage", func(t *testing.T) {
		usage, err := pg.GetRepositoryUsage(ctx, repo)
		if err != nil || usage.VersionCount != int64(len(want)) {
			t.Fatalf("usage %+v err %v", usage, err)
		}

		// terraform providers/null and modules/vpc/aws are two packages; conan
		// zlib, go mod, ... one each; the unlisted files add none.
		if usage.PackageCount != 10 {
			t.Errorf("package count %d, want 10", usage.PackageCount)
		}
	})

	t.Run("go mod is not addressable", func(t *testing.T) {
		var modID string
		if err := db.QueryRowContext(ctx, `SELECT id FROM package_files WHERE repository_id = $1 AND filename = 'v1.0.0.mod'`, repo).Scan(&modID); err != nil {
			t.Fatal(err)
		}

		if blob, err := pg.GetRepoComponentBlob(ctx, repo, modID); err != nil || blob != nil {
			t.Fatalf("unlisted file resolved: %+v %v", blob, err)
		}
	})
}

// TestComponentStoreOnGenericTables round-trips a deb and an rpm component
// through the specs' mapping and the ComponentStore reads.
func TestComponentStoreOnGenericTables(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	repo := seedRepo(t, pg.GetDB(), "components", "rpm")
	store := &ComponentStore{core: pg.core}

	rpmID := seedPackageFile(t, pg, packageSeed{
		repo: repo, format: "rpm", name: "bash", version: "5.2", arch: "x86_64", filename: "bash-5.2-1.x86_64.rpm",
		blobSeed: "bash", blobSize: 100, size: 123,
		attrs: map[string]any{"release": "1", "epoch": 2, "build_time": 1700000001, "archive_size": 3, "installed_size": 4},
		meta:  map[string]any{"summary": "shell", "description": "d", "license": "GPL", "url": "u", "vendor": "v", "group_name": "g", "packager": "p", "source_rpm": "s.src.rpm", "build_host": "h"},
	})
	debID := seedPackageFile(t, pg, packageSeed{
		repo: repo, format: "deb", name: "dpkg", version: "1.0", arch: "amd64", filename: "dpkg_1.0_amd64.deb", blobSeed: "dpkg", blobSize: 50,
		attrs: map[string]any{"installed_size": 7}, meta: map[string]any{"summary": "tool", "license": "GPL"},
	})
	seedPackageFile(t, pg, packageSeed{repo: repo, format: "raw", name: "x", filename: "x", blobSeed: "x", blobSize: 1})

	c, err := store.GetComponentByID(ctx, rpmID)
	if err != nil || c == nil {
		t.Fatalf("rpm by id: %v %v", c, err)
	}

	if c.Format != "rpm" || c.Name != "bash" || c.Version != "5.2" || c.Release != "1" || c.Epoch != 2 || c.Arch != "x86_64" ||
		c.Summary != "shell" || c.Description != "d" || c.License != "GPL" || c.URL != "u" || c.Vendor != "v" || c.Group != "g" ||
		c.Packager != "p" || c.SourceRPM != "s.src.rpm" || c.BuildHost != "h" || c.BuildTime != 1700000001 ||
		c.PackageSize != 123 || c.InstalledSize != 4 || c.ArchiveSize != 3 || c.Filename != "bash-5.2-1.x86_64.rpm" || len(c.SHA256) != 64 {
		t.Fatalf("rpm component %+v", c)
	}

	d, err := store.GetComponentByFilename(ctx, repo, "dpkg_1.0_amd64.deb")
	if err != nil || d == nil || d.ID != debID || d.Format != "deb" || d.PackageSize != 50 || d.InstalledSize != 7 || d.Summary != "tool" || d.Release != "" {
		t.Fatalf("deb by filename: %+v %v", d, err)
	}

	if bySHA, err := store.GetComponentBySHA256(ctx, repo, d.SHA256); err != nil || bySHA == nil || bySHA.ID != debID {
		t.Fatalf("by sha: %+v %v", bySHA, err)
	}

	if none, err := store.GetComponentByID(ctx, "00000000-0000-0000-0000-000000000000"); err != nil || none != nil {
		t.Fatalf("missing id: %+v %v", none, err)
	}

	rpms, err := store.GetComponentsByRepo(ctx, repo)
	if err != nil || len(rpms) != 1 || rpms[0].ID != rpmID {
		t.Fatalf("by repo: %+v %v", rpms, err)
	}

	if err := store.DeleteComponent(ctx, rpmID); err != nil {
		t.Fatal(err)
	}

	if gone, err := store.GetComponentByID(ctx, rpmID); err != nil || gone != nil {
		t.Fatalf("deleted component still found: %+v %v", gone, err)
	}

	var left int
	if err := pg.GetDB().QueryRowContext(ctx, `SELECT count(*) FROM packages WHERE repository_id = $1 AND name = 'bash'`, repo).Scan(&left); err != nil || left != 0 {
		t.Fatalf("empty package not pruned: %d %v", left, err)
	}
}
