// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// fakeLicenses is a LicenseSource answering from a table by file id and
// recording which files it was asked about.
type fakeLicenses struct {
	mu     sync.Mutex
	byFile map[string][]string
	err    error
	asked  []string
}

func (f *fakeLicenses) Licenses(_ context.Context, _, artifactID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.asked = append(f.asked, artifactID)

	return f.byFile[artifactID], f.err
}

// blockGPL gives repo a blocking policy that denies GPL-3.0.
func blockGPL(t *testing.T, h *harness, repo *models.Repository) {
	t.Helper()

	policy := &models.LicensePolicy{
		RepositoryID: &repo.ID, Name: "no gpl", Action: "block", DeniedLicenses: []string{"GPL-3.0"}, Enabled: true,
	}
	if err := h.pg.CreateLicensePolicy(context.Background(), policy); err != nil {
		t.Fatalf("CreateLicensePolicy: %v", err)
	}
}

// promoteBundleOf seals the single item sha of src and promotes it to dst with
// a license source.
func promoteBundleOf(t *testing.T, h *harness, src, dst *models.Repository, source LicenseSource, sha, path string) error {
	t.Helper()

	env := &bundleEnv{h: h, engine: NewEngine(h.pg, h.log, nil, source), src: src, dst: dst}
	env.seal([]string{sha}, src.Format, path)

	_, err := env.engine.Promote(context.Background(), env.params())

	return err
}

// TestPromoteJudgesJarsByThePOMBesideThem: a maven jar declares no license, the
// .pom of its version does, so promoting the jar alone is judged on the pom.
func TestPromoteJudgesJarsByThePOMBesideThem(t *testing.T) {
	h := newHarness(t)
	src, dst := h.repo("maven", false), h.repo("maven", false)
	blockGPL(t, h, dst)

	jarBlob, jarSHA := h.blob("jar")
	pomBlob, _ := h.blob("pom")
	h.file(src.ID, jarBlob, fileSeed{pkg: "com.acme:shop", version: "1.0", path: "com/acme/shop/1.0/shop-1.0.jar"})
	pomID := h.file(src.ID, pomBlob, fileSeed{pkg: "com.acme:shop", version: "1.0", path: "com/acme/shop/1.0/shop-1.0.pom"})

	source := &fakeLicenses{byFile: map[string][]string{pomID: {"GPL-3.0"}}}

	err := promoteBundleOf(t, h, src, dst, source, jarSHA, "com/acme/shop/1.0/shop-1.0.jar")

	var blocked *LicenseBlockedError
	if !errors.As(err, &blocked) || !slices.Equal(blocked.Licenses, []string{"GPL-3.0"}) {
		t.Fatalf("err = %v, want a LicenseBlockedError for GPL-3.0", err)
	}

	if !slices.Equal(source.asked, []string{pomID}) {
		t.Errorf("licenses read from %v, want only the pom %s", source.asked, pomID)
	}

	if n := h.count(`SELECT COUNT(*) FROM package_files WHERE repository_id = $1`, dst.ID); n != 0 {
		t.Errorf("target holds %d rows after a blocked promotion", n)
	}
}

// TestPromoteAdmitsFilesThatDeclareAllowedLicenses: nuget and helm packages are
// read from their file and admitted when the license passes.
func TestPromoteAdmitsFilesThatDeclareAllowedLicenses(t *testing.T) {
	for _, tc := range []struct{ format, path string }{{"nuget", "shop.1.0.nupkg"}, {"helm", "shop-1.0.tgz"}} {
		t.Run(tc.format, func(t *testing.T) {
			h := newHarness(t)
			src, dst := h.repo(tc.format, false), h.repo(tc.format, false)
			blockGPL(t, h, dst)

			blob, sha := h.blob("package " + tc.format)
			id := h.file(src.ID, blob, fileSeed{pkg: "shop", version: "1.0", path: tc.path})
			source := &fakeLicenses{byFile: map[string][]string{id: {"MIT"}}}

			if err := promoteBundleOf(t, h, src, dst, source, sha, tc.path); err != nil {
				t.Fatalf("promote: %v", err)
			}

			if !slices.Equal(source.asked, []string{id}) {
				t.Errorf("licenses read from %v, want %s", source.asked, id)
			}

			if n := h.count(`SELECT COUNT(*) FROM package_files WHERE repository_id = $1`, dst.ID); n != 1 {
				t.Errorf("target holds %d files, want 1", n)
			}
		})
	}
}

// TestPromoteRefusesFilesWhoseLicenseCannotBeRead: a source that cannot read
// the package file under a blocking policy refuses the promotion and the
// target stays untouched.
func TestPromoteRefusesFilesWhoseLicenseCannotBeRead(t *testing.T) {
	h := newHarness(t)
	src, dst := h.repo("nuget", false), h.repo("nuget", false)

	blob, sha := h.blob("broken nupkg")
	h.file(src.ID, blob, fileSeed{pkg: "shop", version: "1.0", path: "shop.1.0.nupkg"})

	err := promoteBundleOf(t, h, src, dst, &fakeLicenses{err: ErrLicenseUnreadable}, sha, "shop.1.0.nupkg")
	if !errors.Is(err, ErrLicenseUnreadable) {
		t.Fatalf("err = %v, want ErrLicenseUnreadable", err)
	}

	if n := h.count(`SELECT COUNT(*) FROM package_files WHERE repository_id = $1`, dst.ID); n != 0 {
		t.Errorf("target holds %d rows after a refused promotion", n)
	}
}

// TestPromoteWithoutLicenseSourceFailsClosed: an engine with no source cannot
// judge a format that keeps its license in the package file, and must not
// silently skip the policy.
func TestPromoteWithoutLicenseSourceFailsClosed(t *testing.T) {
	h := newHarness(t)
	src, dst := h.repo("nuget", false), h.repo("nuget", false)

	blob, sha := h.blob("nupkg")
	h.file(src.ID, blob, fileSeed{pkg: "shop", version: "1.0", path: "shop.1.0.nupkg"})

	if err := promoteBundleOf(t, h, src, dst, nil, sha, "shop.1.0.nupkg"); !errors.Is(err, ErrNoLicenseSource) {
		t.Fatalf("err = %v, want ErrNoLicenseSource", err)
	}
}
