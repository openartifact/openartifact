// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import (
	"context"
	"errors"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// copyEnv is a maven source with the two files of shop 1.0.0 (one carrying a
// property) and an empty maven target.
type copyEnv struct {
	h        *harness
	engine   *Engine
	src, dst *models.Repository
	jar, pom string // file ids in the source
	shaJar   string
}

const (
	jarPath = "com/acme/shop/1.0.0/shop-1.0.0.jar"
	pomPath = "com/acme/shop/1.0.0/shop-1.0.0.pom"
)

func newCopyEnv(t *testing.T) *copyEnv {
	t.Helper()

	h := newHarness(t)
	env := &copyEnv{
		h:      h,
		engine: NewEngine(h.pg, h.log, (&fakeRegen{}).workers(), &fakeLicenses{byFile: map[string][]string{}}),
		src:    h.repo("maven", false),
		dst:    h.repo("maven", false),
	}

	blobJar, sha := h.blob("jar bytes")
	blobPom, _ := h.blob("pom bytes")
	env.shaJar = sha
	env.jar = h.file(env.src.ID, blobJar, fileSeed{pkg: "com.acme:shop", version: "1.0.0", path: jarPath, unitKey: "com.acme:shop:1.0.0"})
	env.pom = h.file(
		env.src.ID,
		blobPom,
		fileSeed{pkg: "com.acme:shop", version: "1.0.0", path: pomPath, role: "metadata", unitKey: "com.acme:shop:1.0.0"},
	)
	h.exec(`INSERT INTO maven_files (id, classifier, extension, md5) VALUES ($1, '', 'jar', 'm5'), ($2, '', 'pom', 'm6')`, env.jar, env.pom)

	ref := h.str(`INSERT INTO artifact_refs (repository_id, artifact_path, format, sha256) VALUES ($1, $2, 'maven', $3) RETURNING id::text`,
		env.src.ID, env.jar, sha)
	h.exec(
		`INSERT INTO artifact_properties (repository_id, key, value, artifact_ref_id) VALUES ($1, 'build.name', 'shop', $2)`,
		env.src.ID,
		ref,
	)

	return env
}

func (e *copyEnv) copy(p CopyParams) (*CopyReport, error) {
	e.h.t.Helper()

	p.Source, p.Target, p.Actor = e.src, e.dst, "alice"
	if len(p.Items) == 0 {
		p.Items = []CopyItem{{Package: "com.acme:shop", Version: "1.0.0"}}
	}

	return e.engine.Copy(context.Background(), p)
}

func (e *copyEnv) live(repoID string) int {
	e.h.t.Helper()

	return e.h.count(`SELECT COUNT(*) FROM package_files WHERE repository_id = $1 AND deleted_at IS NULL`, repoID)
}

func TestCopyVersionCarriesFilesAndProperties(t *testing.T) {
	env := newCopyEnv(t)

	rep, err := env.copy(CopyParams{})
	if err != nil || rep.Copied != 2 || len(rep.Uploaded) != 2 || len(rep.Moved) != 0 {
		t.Fatalf("copy = %+v, %v", rep, err)
	}

	if env.live(env.src.ID) != 2 || env.live(env.dst.ID) != 2 {
		t.Errorf("source %d / target %d live files, want 2 / 2", env.live(env.src.ID), env.live(env.dst.ID))
	}

	if n := env.h.count(`SELECT COUNT(*) FROM artifact_properties p JOIN artifact_refs r ON r.id = p.artifact_ref_id
		JOIN package_files f ON f.id::text = r.artifact_path
		WHERE p.repository_id = $1 AND r.repository_id = $1 AND f.repository_id = $1 AND f.path = $2 AND p.key = 'build.name' AND p.value = 'shop'`,
		env.dst.ID, jarPath); n != 1 {
		t.Errorf("target has %d copied properties on the jar, want 1", n)
	}

	if n := env.h.count(
		`SELECT COUNT(*) FROM package_files WHERE repository_id = $1 AND created_at < now() - interval '1 minute'`,
		env.dst.ID,
	); n != 0 {
		t.Errorf("%d copies carry an old created_at", n)
	}

	// The second copy finds everything.
	again, err := env.copy(CopyParams{})
	if err != nil || again.Copied != 0 || again.Existing != 2 {
		t.Fatalf("second copy = %+v, %v", again, err)
	}
}

func TestCopyBySHAAndPath(t *testing.T) {
	env := newCopyEnv(t)

	rep, err := env.copy(CopyParams{Items: []CopyItem{{SHA256: env.shaJar, Path: jarPath}}})
	if err != nil || rep.Copied != 1 || env.live(env.dst.ID) != 1 {
		t.Fatalf("copy = %+v, %v", rep, err)
	}
}

func TestCopyDryRunWritesNothing(t *testing.T) {
	env := newCopyEnv(t)

	rep, err := env.copy(CopyParams{DryRun: true, Move: true})
	if err != nil || rep.Copied != 2 || len(rep.Moved) != 2 {
		t.Fatalf("dry run = %+v, %v", rep, err)
	}

	if env.live(env.dst.ID) != 0 || env.live(env.src.ID) != 2 {
		t.Errorf("dry run left source %d / target %d live files, want 2 / 0", env.live(env.src.ID), env.live(env.dst.ID))
	}

	if n := env.h.count(`SELECT COUNT(*) FROM artifact_refs WHERE repository_id = $1`, env.dst.ID); n != 0 {
		t.Errorf("dry run wrote %d property refs", n)
	}
}

func TestCopyConflictAndForce(t *testing.T) {
	env := newCopyEnv(t)
	blob, _ := env.h.blob("other jar")
	env.h.file(env.dst.ID, blob, fileSeed{pkg: "com.acme:shop", version: "1.0.0", path: jarPath, unitKey: "com.acme:shop:1.0.0"})

	if _, err := env.copy(CopyParams{}); !errors.As(err, new(*ConflictError)) {
		t.Fatalf("err = %v, want a ConflictError", err)
	}

	if env.live(env.dst.ID) != 1 {
		t.Fatalf("a refused copy changed the target: %d live files", env.live(env.dst.ID))
	}

	rep, err := env.copy(CopyParams{Force: true})
	if err != nil || rep.Replaced != 1 {
		t.Fatalf("forced copy = %+v, %v", rep, err)
	}

	if n := env.h.count(
		`SELECT COUNT(*) FROM package_files WHERE repository_id = $1 AND blob_id = $2 AND deleted_at IS NOT NULL`,
		env.dst.ID,
		blob,
	); n != 1 {
		t.Errorf("%d replaced files in the target's trash, want 1", n)
	}
}

func TestMoveTrashesTheSourceAndRestoreKeepsProperties(t *testing.T) {
	env := newCopyEnv(t)

	rep, err := env.copy(CopyParams{Move: true})
	if err != nil || rep.Copied != 2 || len(rep.Moved) != 2 {
		t.Fatalf("move = %+v, %v", rep, err)
	}

	if env.live(env.src.ID) != 0 || env.live(env.dst.ID) != 2 {
		t.Fatalf("source %d / target %d live files, want 0 / 2", env.live(env.src.ID), env.live(env.dst.ID))
	}

	if n := env.h.count(`SELECT COUNT(*) FROM package_files WHERE repository_id = $1 AND deleted_at IS NOT NULL`, env.src.ID); n != 2 {
		t.Errorf("%d source files in the trash, want 2", n)
	}

	if n := env.h.count(`SELECT COUNT(*) FROM artifact_properties WHERE repository_id = $1`, env.src.ID); n != 1 {
		t.Errorf("source lost its property on the move: %d left", n)
	}
}

func TestMoveRefusedBySealedBundleAndImmutableRelease(t *testing.T) {
	env := newBundleEnv(t, 1)
	other := env.h.repo("maven", false)

	_, err := env.engine.Copy(context.Background(), CopyParams{
		Source: env.src,
		Target: other,
		Move:   true,
		Actor:  "alice",
		Items:  []CopyItem{{SHA256: env.shas[0], Path: env.items[0].ArtifactPath}},
	})
	if !errors.Is(err, ErrSourceHeld) {
		t.Fatalf("err = %v, want ErrSourceHeld", err)
	}

	if n := env.h.count(`SELECT COUNT(*) FROM package_files WHERE repository_id = $1 AND deleted_at IS NOT NULL`, env.src.ID); n != 0 {
		t.Errorf("%d source files trashed by a refused move", n)
	}

	// A copy of the same item is fine: the bundle keeps its content.
	if _, err := env.engine.Copy(context.Background(), CopyParams{
		Source: env.src, Target: other, Actor: "alice", Items: []CopyItem{{SHA256: env.shas[0], Path: env.items[0].ArtifactPath}},
	}); err != nil {
		t.Fatalf("copy of a held item: %v", err)
	}

	// Immutable releases: the release version cannot leave.
	imm := newCopyEnv(t)
	imm.h.exec(`UPDATE repositories SET immutable_releases = true WHERE id = $1`, imm.src.ID)
	imm.src.ImmutableReleases = true

	if _, err := imm.copy(CopyParams{Move: true}); !errors.Is(err, ErrSourceImmutable) {
		t.Fatalf("err = %v, want ErrSourceImmutable", err)
	}

	if imm.live(imm.src.ID) != 2 || imm.live(imm.dst.ID) != 0 {
		t.Errorf("a refused move changed rows: source %d / target %d", imm.live(imm.src.ID), imm.live(imm.dst.ID))
	}

	if rep, err := imm.copy(CopyParams{Move: true, Force: true}); err != nil || len(rep.Moved) != 2 {
		t.Fatalf("forced move = %+v, %v", rep, err)
	}
}

func TestCopyRefusesWhatIsNotHostedSameFormat(t *testing.T) {
	env := newCopyEnv(t)
	npm := env.h.repo("npm", false)

	if _, err := env.engine.Copy(
		context.Background(),
		CopyParams{Source: env.src, Target: npm, Items: []CopyItem{{Package: "p", Version: "1"}}},
	); !errors.Is(
		err,
		ErrFormatMismatch,
	) {
		t.Errorf("cross-format err = %v, want ErrFormatMismatch", err)
	}

	proxy := env.h.repo("maven", false)
	proxy.RepoType = "proxy"

	if _, err := env.engine.Copy(
		context.Background(),
		CopyParams{Source: proxy, Target: env.dst, Items: []CopyItem{{Package: "p", Version: "1"}}},
	); !errors.Is(
		err,
		ErrNotHosted,
	) {
		t.Errorf("proxy source err = %v, want ErrNotHosted", err)
	}

	if _, err := env.copy(CopyParams{Items: []CopyItem{{Package: "p"}}}); !errors.Is(err, ErrBadItem) {
		t.Errorf("half item err = %v, want ErrBadItem", err)
	}

	if _, err := env.engine.Copy(
		context.Background(),
		CopyParams{Source: env.src, Target: env.src, Items: []CopyItem{{Package: "p", Version: "1"}}},
	); !errors.Is(
		err,
		ErrSameRepository,
	) {
		t.Errorf("same repo err = %v, want ErrSameRepository", err)
	}
}

func TestCopyAuthorizeDenyChangesNothing(t *testing.T) {
	env := newCopyEnv(t)

	_, err := env.copy(CopyParams{Move: true, Authorize: func(_ context.Context, a Artifact) error {
		if a.Path == pomPath {
			return ErrNotPermitted
		}

		return nil
	}})
	if !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("err = %v, want ErrNotPermitted", err)
	}

	if env.live(env.dst.ID) != 0 || env.live(env.src.ID) != 2 {
		t.Errorf("a refused move changed rows: source %d / target %d", env.live(env.src.ID), env.live(env.dst.ID))
	}
}

func TestCopyHonoursTheTargetLicensePolicy(t *testing.T) {
	env := newCopyEnv(t)
	env.engine = NewEngine(env.h.pg, env.h.log, nil, &fakeLicenses{byFile: map[string][]string{env.pom: {"GPL-3.0"}, env.jar: {"GPL-3.0"}}})
	blockGPL(t, env.h, env.dst)

	if _, err := env.copy(CopyParams{}); !errors.As(err, new(*LicenseBlockedError)) {
		t.Fatalf("err = %v, want a LicenseBlockedError", err)
	}

	if env.live(env.dst.ID) != 0 {
		t.Errorf("a blocked copy wrote %d files", env.live(env.dst.ID))
	}
}
