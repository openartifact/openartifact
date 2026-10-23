// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/quota"
)

// bundleEnv is a sealed maven bundle in a "dev" -> "prod" pipeline whose prod
// stage is the target repository.
type bundleEnv struct {
	h      *harness
	engine *Engine
	src    *models.Repository
	dst    *models.Repository
	bundle *models.ReleaseBundle
	items  []*models.ReleaseBundleItem
	blobs  []string // blob ids of the items
	shas   []string
	regen  *fakeRegen
}

// fakeRegen records the refreshes the engine enqueues.
type fakeRegen struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeRegen) add(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, s)
}

func (f *fakeRegen) NotifyDebStale(repoID string) { f.add("deb:" + repoID) }
func (f *fakeRegen) NotifyRPMStale(repoID string) { f.add("rpm:" + repoID) }
func (f *fakeRegen) NotifyNPMStale(pkgID string)  { f.add("npm:" + pkgID) }
func (f *fakeRegen) NotifyMavenStale(repoID, group, artifact string) {
	f.add("maven:" + repoID + ":" + group + ":" + artifact)
}

// newBundleEnv seeds n maven artifacts in a source repository and seals a
// bundle with one item each.
func newBundleEnv(t *testing.T, n int) *bundleEnv {
	t.Helper()

	h := newHarness(t)
	ctx := context.Background()
	regen := &fakeRegen{}
	env := &bundleEnv{h: h, engine: NewEngine(h.pg, h.log, regen), src: h.repo("maven", false), dst: h.repo("maven", true), regen: regen}

	pipeline := &models.PromotionPipeline{Name: "pipe", Stages: []models.PipelineStage{{Name: "dev"}, {Name: "prod", RepositoryID: env.dst.ID}}}
	if err := h.pg.CreatePromotionPipeline(ctx, pipeline); err != nil {
		t.Fatalf("CreatePromotionPipeline: %v", err)
	}

	env.bundle = &models.ReleaseBundle{Name: "shop", Version: "1.0.0", PipelineID: &pipeline.ID}
	if err := h.pg.CreateReleaseBundle(ctx, env.bundle); err != nil {
		t.Fatalf("CreateReleaseBundle: %v", err)
	}

	for i := range n {
		blob, sha := h.blob(fmt.Sprintf("artifact %d", i))
		path := fmt.Sprintf("com/acme/shop/1.0.0/shop%d-1.0.0.jar", i)
		h.row("maven_artifacts", env.src.ID, "blob_id", blob, mavenCols(i, path))

		item := &models.ReleaseBundleItem{
			ID: uuid.NewString(), BundleID: env.bundle.ID, SourceRepoID: env.src.ID,
			SHA256: sha, ArtifactPath: path, Format: "maven", SizeBytes: int64(len(fmt.Sprintf("artifact %d", i))), AddedAt: time.Now(),
		}
		if err := h.pg.AddBundleItem(ctx, item); err != nil {
			t.Fatalf("AddBundleItem: %v", err)
		}

		env.items = append(env.items, item)
		env.blobs = append(env.blobs, blob)
		env.shas = append(env.shas, sha)
	}

	if ok, err := h.pg.SealReleaseBundle(ctx, env.bundle.ID, "admin", time.Now(), nil); err != nil || !ok {
		t.Fatalf("SealReleaseBundle = %v, %v", ok, err)
	}

	env.reload()

	return env
}

func mavenCols(i int, path string) map[string]any {
	return map[string]any{
		"group_id": "com.acme", "artifact_id_coord": fmt.Sprintf("shop%d", i), "version": "1.0.0",
		"extension": "jar", "repo_path": path,
	}
}

func (e *bundleEnv) reload() {
	e.h.t.Helper()

	b, err := e.h.pg.GetReleaseBundleByID(context.Background(), e.bundle.ID)
	if err != nil || b == nil {
		e.h.t.Fatalf("GetReleaseBundleByID: %v, %v", b, err)
	}

	e.bundle = b
}

func (e *bundleEnv) params() Params {
	return Params{Bundle: e.bundle, Items: e.items, Target: e.dst, ToStage: "prod", Notes: "release", Promoter: "alice"}
}

// assertUntouched fails unless the failed promotion left no trace.
func (e *bundleEnv) assertUntouched() {
	e.h.t.Helper()

	if n := e.h.count(`SELECT COUNT(*) FROM maven_artifacts WHERE repository_id = $1`, e.dst.ID); n != 0 {
		e.h.t.Errorf("target holds %d rows after a failed promotion", n)
	}

	if n := e.h.count(`SELECT COUNT(*) FROM bundle_promotions WHERE bundle_id = $1`, e.bundle.ID); n != 0 {
		e.h.t.Errorf("%d promotion rows recorded for a failed promotion", n)
	}

	if stage := e.h.str(`SELECT current_stage FROM release_bundles WHERE id = $1`, e.bundle.ID); stage != "" {
		e.h.t.Errorf("current_stage = %q after a failed promotion", stage)
	}

	q, err := e.h.pg.GetOrCreateStorageQuota(context.Background(), e.dst.ID)
	if err != nil {
		e.h.t.Fatalf("quota: %v", err)
	}

	if q.CurrentSizeBytes != 0 || q.CurrentArtifactCount != 0 {
		e.h.t.Errorf("quota charged %d bytes / %d artifacts by a failed promotion", q.CurrentSizeBytes, q.CurrentArtifactCount)
	}
}

func TestEnginePromoteRecordsAndCharges(t *testing.T) {
	env := newBundleEnv(t, 2)
	blobsBefore := env.h.count(`SELECT COUNT(*) FROM blobs`)

	report, err := env.engine.Promote(context.Background(), env.params())
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}

	if report.Copied != 2 || report.Existing != 0 || len(report.Items) != 2 {
		t.Fatalf("report = copied %d existing %d items %d, want 2/0/2", report.Copied, report.Existing, len(report.Items))
	}

	pr := report.Promotion
	if pr.Status != models.PromotionStatusSuccess || pr.ItemCount != 2 || pr.ToStage != "prod" || pr.FromStage != nil || pr.CompletedAt == nil {
		t.Errorf("promotion = %+v", pr)
	}

	if stage := env.h.str(`SELECT current_stage FROM release_bundles WHERE id = $1`, env.bundle.ID); stage != "prod" {
		t.Errorf("current_stage = %q, want prod", stage)
	}

	listed, err := env.h.pg.ListBundlePromotions(context.Background(), env.bundle.ID)
	if err != nil || len(listed) != 1 || listed[0].Notes != "release" {
		t.Errorf("ListBundlePromotions = %+v, %v", listed, err)
	}

	if got := env.h.count(`SELECT COUNT(*) FROM blobs`); got != blobsBefore {
		t.Errorf("blobs rows %d -> %d: bytes must be shared, not duplicated", blobsBefore, got)
	}

	shared := env.h.count(`SELECT COUNT(*) FROM maven_artifacts s JOIN maven_artifacts d ON d.blob_id = s.blob_id
		WHERE s.repository_id = $1 AND d.repository_id = $2`, env.src.ID, env.dst.ID)
	if shared != 2 {
		t.Errorf("%d target rows reference the source's blobs, want 2", shared)
	}

	q, err := env.h.pg.GetOrCreateStorageQuota(context.Background(), env.dst.ID)
	if err != nil {
		t.Fatalf("quota: %v", err)
	}

	if q.CurrentArtifactCount != 2 || q.CurrentSizeBytes != int64(len("artifact 0")+len("artifact 1")) {
		t.Errorf("quota = %d bytes / %d artifacts, want %d / 2", q.CurrentSizeBytes, q.CurrentArtifactCount, len("artifact 0")+len("artifact 1"))
	}
}

func TestEnginePromoteIsAtomic(t *testing.T) {
	env := newBundleEnv(t, 2)

	// The second item's coordinates are taken in the target by other content.
	other, _ := env.h.blob("someone else's jar")
	env.h.row("maven_artifacts", env.dst.ID, "blob_id", other, mavenCols(1, "com/acme/shop/1.0.0/shop1-1.0.0.jar"))

	_, err := env.engine.Promote(context.Background(), env.params())

	var conflict *ConflictError
	if !errors.As(err, &conflict) || !conflict.Immutable {
		t.Fatalf("err = %v, want an immutable-release ConflictError", err)
	}

	var item *ItemError
	if !errors.As(err, &item) || item.SHA256 != env.shas[1] {
		t.Errorf("err = %v, want it to name item %s", err, env.shas[1])
	}

	if n := env.h.count(`SELECT COUNT(*) FROM maven_artifacts WHERE repository_id = $1`, env.dst.ID); n != 1 {
		t.Errorf("target has %d rows, want only the pre-existing one: the first item's copy must roll back", n)
	}

	if n := env.h.count(`SELECT COUNT(*) FROM bundle_promotions`); n != 0 {
		t.Errorf("%d promotion rows after a failed promotion", n)
	}

	if stage := env.h.str(`SELECT current_stage FROM release_bundles WHERE id = $1`, env.bundle.ID); stage != "" {
		t.Errorf("current_stage = %q after a failed promotion", stage)
	}
}

func TestEnginePromoteRetryIsIdempotent(t *testing.T) {
	env := newBundleEnv(t, 2)

	// A previous attempt copied the first item but never recorded the promotion.
	req := Request{Src: env.src, Dst: env.dst, SHA256: env.shas[0], Path: env.items[0].ArtifactPath}
	if _, err := env.h.promoteOnce(promoterFor(t, "maven"), req); err != nil {
		t.Fatalf("pre-copy: %v", err)
	}

	report, err := env.engine.Promote(context.Background(), env.params())
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}

	if report.Copied != 1 || report.Existing != 1 {
		t.Errorf("copied %d existing %d, want 1 and 1", report.Copied, report.Existing)
	}
}

func TestEnginePromoteQuota(t *testing.T) {
	env := newBundleEnv(t, 2)

	q, err := env.h.pg.GetOrCreateStorageQuota(context.Background(), env.dst.ID)
	if err != nil {
		t.Fatalf("quota: %v", err)
	}

	q.MaxArtifactCount = 1
	if err := env.h.pg.UpdateStorageQuota(context.Background(), q); err != nil {
		t.Fatalf("UpdateStorageQuota: %v", err)
	}

	_, err = env.engine.Promote(context.Background(), env.params())
	if !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("err = %v, want quota.ErrExceeded", err)
	}

	env.assertUntouched()
}

func TestEnginePromoteLicensePolicy(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	src, dst := h.repo("pypi", false), h.repo("pypi", false)

	blob, sha := h.blob("gpl wheel")
	h.row("pypi_packages", src.ID, "blob_id", blob, map[string]any{
		"name": "shop", "version": "1.0", "filename": "shop-1.0.whl", "package_type": "bdist_wheel", "license": "GPL-3.0",
	})

	policy := &models.LicensePolicy{RepositoryID: &dst.ID, Name: "no gpl", Action: "block", DeniedLicenses: []string{"GPL-3.0"}, Enabled: true}
	if err := h.pg.CreateLicensePolicy(ctx, policy); err != nil {
		t.Fatalf("CreateLicensePolicy: %v", err)
	}

	env := &bundleEnv{h: h, engine: NewEngine(h.pg, h.log, nil), src: src, dst: dst}
	env.seal([]string{sha}, "pypi", "shop-1.0.whl")

	_, err := env.engine.Promote(ctx, env.params())

	var blocked *LicenseBlockedError
	if !errors.As(err, &blocked) || len(blocked.Violations) == 0 {
		t.Fatalf("err = %v, want a LicenseBlockedError", err)
	}

	if n := h.count(`SELECT COUNT(*) FROM pypi_packages WHERE repository_id = $1`, dst.ID); n != 0 {
		t.Errorf("target holds %d rows after a blocked promotion", n)
	}
}

// seal builds a pipeline and a sealed bundle whose items are the shas of src.
func (e *bundleEnv) seal(shas []string, format string, paths ...string) {
	e.h.t.Helper()

	ctx := context.Background()
	pipeline := &models.PromotionPipeline{Name: "pipe", Stages: []models.PipelineStage{{Name: "dev"}, {Name: "prod", RepositoryID: e.dst.ID}}}

	if err := e.h.pg.CreatePromotionPipeline(ctx, pipeline); err != nil {
		e.h.t.Fatalf("CreatePromotionPipeline: %v", err)
	}

	e.bundle = &models.ReleaseBundle{Name: "b", Version: "1", PipelineID: &pipeline.ID}
	if err := e.h.pg.CreateReleaseBundle(ctx, e.bundle); err != nil {
		e.h.t.Fatalf("CreateReleaseBundle: %v", err)
	}

	for i, sha := range shas {
		item := &models.ReleaseBundleItem{
			ID: uuid.NewString(), BundleID: e.bundle.ID, SourceRepoID: e.src.ID, SHA256: sha,
			ArtifactPath: paths[i], Format: format, AddedAt: time.Now(),
		}
		if err := e.h.pg.AddBundleItem(ctx, item); err != nil {
			e.h.t.Fatalf("AddBundleItem: %v", err)
		}

		e.items = append(e.items, item)
	}

	if ok, err := e.h.pg.SealReleaseBundle(ctx, e.bundle.ID, "admin", time.Now(), nil); err != nil || !ok {
		e.h.t.Fatalf("SealReleaseBundle = %v, %v", ok, err)
	}

	e.reload()
}

func TestEnginePromoteRefusesQuarantinedSource(t *testing.T) {
	env := newBundleEnv(t, 1)

	// A scan of the source artifact quarantined it.
	var jobID string
	if err := env.h.pg.GetDB().QueryRowContext(context.Background(),
		`INSERT INTO scan_jobs (artifact_ref_id, scan_type)
		 SELECT id, 'secret' FROM artifact_refs WHERE repository_id = $1 AND artifact_path = $2 RETURNING id`,
		env.src.ID, env.items[0].ArtifactPath).Scan(&jobID); err != nil {
		t.Fatalf("scan job: %v", err)
	}

	env.h.exec(`INSERT INTO secret_results (scan_job_id, quarantined) VALUES ($1, true)`, jobID)

	_, err := env.engine.Promote(context.Background(), env.params())
	if !errors.Is(err, ErrQuarantined) {
		t.Fatalf("err = %v, want ErrQuarantined", err)
	}

	env.assertUntouched()
}

func TestEnginePromoteRevalidatesBundleUnderLock(t *testing.T) {
	t.Run("stage moved concurrently", func(t *testing.T) {
		env := newBundleEnv(t, 1)
		env.h.exec(`UPDATE release_bundles SET current_stage = 'dev' WHERE id = $1`, env.bundle.ID)

		_, err := env.engine.Promote(context.Background(), env.params())
		if !errors.Is(err, ErrStageChanged) {
			t.Fatalf("err = %v, want ErrStageChanged", err)
		}

		env.h.exec(`UPDATE release_bundles SET current_stage = NULL WHERE id = $1`, env.bundle.ID)
		env.assertUntouched()
	})

	t.Run("archived", func(t *testing.T) {
		env := newBundleEnv(t, 1)
		env.h.exec(`UPDATE release_bundles SET state = 'archived' WHERE id = $1`, env.bundle.ID)

		if _, err := env.engine.Promote(context.Background(), env.params()); !errors.Is(err, ErrBundleState) {
			t.Fatalf("err = %v, want ErrBundleState", err)
		}
	})
}

func TestEnginePromoteRejectsBadRepositories(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name   string
		mutate func(env *bundleEnv, p *Params)
		want   error
	}{
		{"same repository", func(env *bundleEnv, p *Params) { p.Target = env.src }, ErrSameRepository},
		{"format mismatch", func(env *bundleEnv, p *Params) { p.Target = env.h.repo("pypi", false) }, ErrFormatMismatch},
		{"proxy target", func(_ *bundleEnv, p *Params) { proxy := *p.Target; proxy.RepoType = "proxy"; p.Target = &proxy }, ErrNotHosted},
		{"no items", func(_ *bundleEnv, p *Params) { p.Items = nil }, ErrEmpty},
		{"item format differs from its repository", func(_ *bundleEnv, p *Params) {
			it := *p.Items[0]
			it.Format = "swift"
			p.Items = []*models.ReleaseBundleItem{&it}
		}, ErrFormatMismatch},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newBundleEnv(t, 1)
			p := env.params()
			tt.mutate(env, &p)

			if _, err := env.engine.Promote(ctx, p); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestEngineNotifyDispatchesByKind(t *testing.T) {
	regen := &fakeRegen{}
	e := &Engine{regen: regen}
	e.notify([]Regen{
		{Kind: RegenDeb, RepoID: "r1"},
		{Kind: RegenRPM, RepoID: "r2"},
		{Kind: RegenMaven, RepoID: "r3", GroupID: "g", ArtifactID: "a"},
		{Kind: RegenNPM, PackageID: "p1"},
	})

	want := []string{"deb:r1", "rpm:r2", "maven:r3:g:a", "npm:p1"}
	if fmt.Sprint(regen.calls) != fmt.Sprint(want) {
		t.Errorf("calls = %v, want %v", regen.calls, want)
	}
}

func TestEngineConcurrentPromotionsAreSerialized(t *testing.T) {
	env := newBundleEnv(t, 2)

	const racers = 4

	errs := make([]error, racers)

	var wg sync.WaitGroup

	for i := range racers {
		wg.Go(func() {
			_, errs[i] = env.engine.Promote(context.Background(), env.params())
		})
	}

	wg.Wait()

	won := 0

	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, ErrStageChanged):
			t.Errorf("loser failed with %v, want ErrStageChanged", err)
		}
	}

	if won != 1 {
		t.Fatalf("%d promotions succeeded, want exactly 1", won)
	}

	if n := env.h.count(`SELECT COUNT(*) FROM bundle_promotions WHERE bundle_id = $1`, env.bundle.ID); n != 1 {
		t.Errorf("%d promotion rows, want 1", n)
	}

	q, err := env.h.pg.GetOrCreateStorageQuota(context.Background(), env.dst.ID)
	if err != nil || q.CurrentArtifactCount != 2 {
		t.Errorf("quota = %+v, %v: charged more than once", q, err)
	}
}
