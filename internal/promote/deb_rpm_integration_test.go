// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// debPoolPath is where the fixture package is served from in component comp.
func debPoolPath(comp string) string {
	return "pool/" + comp + "/libs/libshop6/" + debTestFilename
}

func wantConflict(t *testing.T, err error) {
	t.Helper()

	var conflict *ConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want a ConflictError", err)
	}
}

func TestDebSameFilenameInOtherComponentConflicts(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "deb")

	src, dst := h.repo("deb", false), h.repo("deb", false)
	blobA, shaA := h.blob("deb A")
	blobB, _ := h.blob("deb B")

	seedDebPackage(h, src.ID, blobA, "bookworm", "main", debTestFilename)
	// Other component, so no coordinates collide, but the pool serves one file
	// per name: the same name with other content cannot be promoted.
	seedDebPackage(h, dst.ID, blobB, "bookworm", "contrib", debTestFilename)

	_, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: shaA})
	wantConflict(t, err)

	if got := h.count(`SELECT COUNT(*) FROM deb_packages WHERE repository_id = $1`, dst.ID); got != 1 {
		t.Errorf("target has %d packages after a refused promotion, want its own 1", got)
	}
}

func TestDebSameContentInOtherComponentIsCopied(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "deb")

	src, dst := h.repo("deb", false), h.repo("deb", false)
	blobA, shaA := h.blob("deb A")

	seedDebPackage(h, src.ID, blobA, "bookworm", "main", debTestFilename)
	seedDebPackage(h, dst.ID, blobA, "bookworm", "contrib", debTestFilename)

	res, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: shaA})
	if err != nil {
		t.Fatalf("promotion: %v", err)
	}

	if len(res.Copied) != 1 || len(res.Existing) != 0 {
		t.Fatalf("copied %d existing %d, want 1 and 0: %+v", len(res.Copied), len(res.Existing), res)
	}

	// The existing distribution is reused; only the component is new.
	if got := h.count(`SELECT COUNT(*) FROM debian_distributions WHERE repository_id = $1`, dst.ID); got != 1 {
		t.Errorf("target has %d distributions, want the existing one reused", got)
	}

	if got := h.count(`SELECT COUNT(*) FROM debian_components c JOIN debian_distributions d ON d.id = c.distribution_id WHERE d.repository_id = $1`, dst.ID); got != 2 {
		t.Errorf("target has %d components, want contrib and main", got)
	}

	if got := h.count(`SELECT COUNT(*) FROM deb_packages WHERE repository_id = $1 AND deleted_at IS NULL`, dst.ID); got != 2 {
		t.Errorf("target has %d packages, want 2", got)
	}
}

func TestDebNarrowsByPoolPath(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "deb")

	src := h.repo("deb", false)
	blobA, shaA := h.blob("deb A")

	seedDebPackage(h, src.ID, blobA, "bookworm", "main", debTestFilename)
	seedDebPackage(h, src.ID, blobA, "bookworm", "contrib", debTestFilename)

	for _, tc := range []struct {
		path  string
		comps []string
	}{
		{path: "", comps: []string{"contrib", "main"}},
		{path: debTestFilename, comps: []string{"contrib", "main"}},
		{path: "/" + debPoolPath("contrib"), comps: []string{"contrib"}},
		{path: debPoolPath("main"), comps: []string{"main"}},
		{path: "unrelated.deb", comps: []string{"contrib", "main"}},
	} {
		dst := h.repo("deb", false)

		res, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: shaA, Path: tc.path})
		if err != nil {
			t.Fatalf("path %q: %v", tc.path, err)
		}

		if len(res.Copied) != len(tc.comps) {
			t.Errorf("path %q: copied %d, want %d", tc.path, len(res.Copied), len(tc.comps))
		}

		got := queryStrings(t, h, `SELECT c.name FROM deb_packages p JOIN debian_components c ON c.id = p.component_id
			  WHERE p.repository_id = $1 ORDER BY c.name`, dst.ID)

		if !reflect.DeepEqual(got, tc.comps) {
			t.Errorf("path %q: target components %v, want %v", tc.path, got, tc.comps)
		}
	}
}

func TestDebKeepsExistingParents(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "deb")

	src, dst := h.repo("deb", false), h.repo("deb", false)
	blobA, shaA := h.blob("deb A")

	seedDebPackage(h, src.ID, blobA, "bookworm", "main", debTestFilename)
	h.exec(`INSERT INTO debian_distributions (repository_id, name, codename, description) VALUES ($1, 'bookworm', 'Custom', 'custom')`, dst.ID)

	if _, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: shaA}); err != nil {
		t.Fatalf("promotion: %v", err)
	}

	if got := h.str(`SELECT codename || '/' || description FROM debian_distributions WHERE repository_id = $1`, dst.ID); got != "Custom/custom" {
		t.Errorf("target distribution is %q, want its own fields kept", got)
	}
}

func TestDebReportsWhatItCopied(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "deb")

	src, dst := h.repo("deb", false), h.repo("deb", false)
	blobA, shaA := h.blob("deb A")
	rowID := seedDebPackage(h, src.ID, blobA, "bookworm", "main", debTestFilename)
	req := Request{Src: src, Dst: dst, SHA256: shaA, Path: debTestFilename}

	res, err := h.promoteOnce(p, req)
	if err != nil {
		t.Fatalf("promotion: %v", err)
	}

	wantArtifact := Artifact{Name: "libshop6", Version: "1:2.4.0-1", Path: debTestFilename, SHA256: shaA, Size: int64(len("deb A"))}
	if !reflect.DeepEqual(res.Copied, []Artifact{wantArtifact}) {
		t.Errorf("copied = %+v, want %+v", res.Copied, wantArtifact)
	}

	if res.Usage != (Usage{Bytes: int64(len("deb A")), Artifacts: 1}) {
		t.Errorf("usage = %+v, want the blob's bytes and one artifact", res.Usage)
	}

	if !reflect.DeepEqual(res.Licenses, []string{"MIT"}) {
		t.Errorf("licenses = %v, want [MIT]", res.Licenses)
	}

	for _, key := range []string{rowID, debTestFilename, debPoolPath("main")} {
		if !slices.Contains(res.QuarantineKeys, key) {
			t.Errorf("quarantine keys %v lack %q", res.QuarantineKeys, key)
		}
	}

	if want := []Regen{{Kind: RegenDeb, RepoID: dst.ID}}; !reflect.DeepEqual(res.Regen, want) {
		t.Errorf("regen = %+v, want %+v", res.Regen, want)
	}

	again, err := h.promoteOnce(p, req)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}

	if len(again.Regen) != 0 || len(again.Copied) != 0 || len(again.Existing) != 1 {
		t.Errorf("retry = %+v, want an existing artifact and no refresh", again)
	}
}

func TestRPMEpochCollisionConflicts(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "rpm")

	src, dst := h.repo("rpm", false), h.repo("rpm", false)
	blobA, shaA := h.blob("rpm A")
	blobB, _ := h.blob("rpm B")

	seedRPMPackage(h, src.ID, blobA, 2, rpmTestFilename)
	// Another epoch of the same N-V-R.A has another NEVRA but the same file
	// name, which carries no epoch: the download could not tell them apart.
	seedRPMPackage(h, dst.ID, blobB, 0, rpmTestFilename)

	_, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: shaA})
	wantConflict(t, err)

	if got := h.count(`SELECT COUNT(*) FROM rpm_packages WHERE repository_id = $1`, dst.ID); got != 1 {
		t.Errorf("target has %d packages after a refused promotion, want its own 1", got)
	}
}

func TestRPMReportsWhatItCopied(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "rpm")

	src, dst := h.repo("rpm", false), h.repo("rpm", false)
	blobA, shaA := h.blob("rpm A")
	rowID := seedRPMPackage(h, src.ID, blobA, 2, rpmTestFilename)
	req := Request{Src: src, Dst: dst, SHA256: shaA}

	res, err := h.promoteOnce(p, req)
	if err != nil {
		t.Fatalf("promotion: %v", err)
	}

	wantArtifact := Artifact{Name: "shop", Version: "2:2.4.0-3.el9", Path: rpmTestFilename, SHA256: shaA, Size: int64(len("rpm A"))}
	if !reflect.DeepEqual(res.Copied, []Artifact{wantArtifact}) {
		t.Errorf("copied = %+v, want %+v", res.Copied, wantArtifact)
	}

	if res.Usage != (Usage{Bytes: int64(len("rpm A")), Artifacts: 1}) {
		t.Errorf("usage = %+v, want the blob's bytes and one artifact", res.Usage)
	}

	if !reflect.DeepEqual(res.Licenses, []string{"MIT"}) {
		t.Errorf("licenses = %v, want [MIT]", res.Licenses)
	}

	for _, key := range []string{rowID, rpmTestFilename, "Packages/" + rpmTestFilename} {
		if !slices.Contains(res.QuarantineKeys, key) {
			t.Errorf("quarantine keys %v lack %q", res.QuarantineKeys, key)
		}
	}

	if want := []Regen{{Kind: RegenRPM, RepoID: dst.ID}}; !reflect.DeepEqual(res.Regen, want) {
		t.Errorf("regen = %+v, want %+v", res.Regen, want)
	}

	again, err := h.promoteOnce(p, req)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}

	if len(again.Regen) != 0 || len(again.Copied) != 0 || len(again.Existing) != 1 {
		t.Errorf("retry = %+v, want an existing artifact and no refresh", again)
	}
}

func TestRPMKeepsTheTargetRepodataRow(t *testing.T) {
	h := newHarness(t)
	p := promoterFor(t, "rpm")

	src, dst := h.repo("rpm", false), h.repo("rpm", false)
	blobA, shaA := h.blob("rpm A")

	seedRPMPackage(h, src.ID, blobA, 2, rpmTestFilename)
	h.exec(`INSERT INTO rpm_repos (repository_id, base_arch, metadata_stale, signing_key_id) VALUES ($1, 'aarch64', false, 'DSTKEY')`, dst.ID)

	if _, err := h.promoteOnce(p, Request{Src: src, Dst: dst, SHA256: shaA}); err != nil {
		t.Fatalf("promotion: %v", err)
	}

	if got := h.count(`SELECT COUNT(*) FROM rpm_repos WHERE repository_id = $1`, dst.ID); got != 1 {
		t.Errorf("target has %d rpm_repos rows, want its own one reused", got)
	}

	if got := h.str(`SELECT base_arch || '/' || metadata_stale::text || '/' || signing_key_id FROM rpm_repos WHERE repository_id = $1`, dst.ID); got != "aarch64/true/DSTKEY" {
		t.Errorf("target rpm_repos row is %q, want it flagged stale with its own key kept", got)
	}
}

// TestDebRPMCopyQueriesCoverEveryColumn fails when a migration adds a column to
// a table these promoters copy without the copy learning about it, so a
// promotion can never silently drop metadata.
func TestDebRPMCopyQueriesCoverEveryColumn(t *testing.T) {
	h := newHarness(t)

	insertColumns := regexp.MustCompile(`(?s)INSERT INTO (\w+)\s*\(([^)]*)\)`)
	timestamps := []string{"id", "created_at", "updated_at"}

	for _, tc := range []struct {
		query   string
		skipped []string
	}{
		{debCopyQuery, append([]string{"deleted_at", "deleted_by"}, timestamps...)},
		{rpmCopyQuery, append([]string{"deleted_at", "deleted_by"}, timestamps...)},
		{debEnsureDistribution, timestamps},
		{debEnsureComponent, timestamps},
		{debEnsureArchitecture, timestamps},
	} {
		m := insertColumns.FindStringSubmatch(tc.query)
		if m == nil {
			t.Fatalf("no INSERT column list in %q", tc.query)
		}

		table := m[1]
		copied := map[string]bool{}

		for c := range strings.SplitSeq(m[2], ",") {
			copied[strings.TrimSpace(c)] = true
		}

		have := map[string]bool{}
		for _, c := range queryStrings(t, h, `SELECT column_name FROM information_schema.columns
			  WHERE table_schema = 'public' AND table_name = $1 AND is_generated = 'NEVER'`, table) {
			have[c] = true
		}

		for c := range copied {
			if !have[c] {
				t.Errorf("%s: the copy writes %s, which the table does not have", table, c)
			}
		}

		for c := range have {
			if !copied[c] && !slices.Contains(tc.skipped, c) {
				t.Errorf("%s.%s is neither copied nor deliberately skipped", table, c)
			}
		}
	}
}

// queryStrings runs a single-column query and returns its rows.
func queryStrings(t *testing.T, h *harness, query string, args ...any) []string {
	t.Helper()

	rows, err := h.pg.GetDB().QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	defer func() { _ = rows.Close() }()

	var out []string

	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}

		out = append(out, v)
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("read rows: %v", err)
	}

	return out
}
