// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/quota"
)

func TestEngineRefundsAndSkipsNotifyWhenRecordingFails(t *testing.T) {
	env := newBundleEnv(t, 2)

	env.h.exec(`CREATE FUNCTION oa_refuse_promotion() RETURNS trigger LANGUAGE plpgsql AS
		$$ BEGIN RAISE EXCEPTION 'promotion refused'; END $$`)
	env.h.exec(`CREATE TRIGGER oa_refuse BEFORE INSERT ON bundle_promotions
		FOR EACH ROW EXECUTE FUNCTION oa_refuse_promotion()`)

	if _, err := env.engine.Promote(context.Background(), env.params()); err == nil {
		t.Fatal("Promote succeeded although the promotion row was refused")
	}

	env.assertUntouched()

	if len(env.regen.calls) != 0 {
		t.Errorf("refreshes %v enqueued for a failed promotion", env.regen.calls)
	}
}

// TestEngineForcedPromotionReportsReplaced: a forced bundle promotion replaces
// conflicting content and says so, where the same promotion unforced fails.
func TestEngineForcedPromotionReportsReplaced(t *testing.T) {
	env := newBundleEnv(t, 2)

	other, _ := env.h.blob("someone else's jar")
	env.h.file(env.dst.ID, other, mavenSeed(1, "com/acme/shop/1.0.0/shop1-1.0.0.jar"))

	p := env.params()
	if _, err := env.engine.Promote(context.Background(), p); !errors.Is(err, ErrConflict) {
		t.Fatalf("unforced err = %v, want ErrConflict", err)
	}

	p.Force = true

	report, err := env.engine.Promote(context.Background(), p)
	if err != nil {
		t.Fatalf("forced Promote: %v", err)
	}

	if report.Copied != 2 || report.Replaced != 1 {
		t.Errorf("copied %d replaced %d, want 2 and 1", report.Copied, report.Replaced)
	}

	if n := env.h.count(`SELECT COUNT(*) FROM package_files WHERE repository_id = $1 AND deleted_at IS NULL`, env.dst.ID); n != 2 {
		t.Errorf("target has %d live rows, want 2", n)
	}

	if n := env.h.count(
		`SELECT COUNT(*) FROM package_files WHERE repository_id = $1 AND deleted_at IS NOT NULL AND deleted_by = 'alice'`,
		env.dst.ID,
	); n != 1 {
		t.Errorf("%d rows trashed by the promoter, want 1", n)
	}
}

// TestEngineRejectsUnusableRepositories covers sources and targets that are
// gone or of an unsupported kind.
func TestEngineRejectsUnusableRepositories(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name   string
		mutate func(env *bundleEnv, p *Params)
		want   error
	}{
		{"deleted source", func(env *bundleEnv, _ *Params) {
			env.h.exec(`UPDATE repositories SET deleted_at = now() WHERE id = $1`, env.src.ID)
		}, ErrItemNotFound},
		{"deleted target", func(_ *bundleEnv, p *Params) {
			gone := *p.Target
			now := time.Now()
			gone.DeletedAt = &now
			p.Target = &gone
		}, ErrNotHosted},
		{"proxy source", func(env *bundleEnv, _ *Params) {
			env.h.exec(`UPDATE repositories SET repo_type = 'proxy' WHERE id = $1`, env.src.ID)
		}, ErrNotHosted},
		{"unknown source", func(_ *bundleEnv, p *Params) {
			it := *p.Items[0]
			it.SourceRepoID = "00000000-0000-0000-0000-0000000000ff"
			p.Items = []*models.ReleaseBundleItem{&it}
		}, ErrItemNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newBundleEnv(t, 1)
			p := env.params()
			tt.mutate(env, &p)

			_, err := env.engine.Promote(ctx, p)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}

			env.assertUntouched()
		})
	}
}

// TestEngineQuotaByteLimit: the byte quota of the target stops a promotion
// that fits the artifact count, and nothing is charged.
func TestEngineQuotaByteLimit(t *testing.T) {
	env := newBundleEnv(t, 2)

	q, err := env.h.pg.GetOrCreateStorageQuota(context.Background(), env.dst.ID)
	if err != nil {
		t.Fatalf("quota: %v", err)
	}

	q.MaxSizeBytes = int64(len("artifact 0")) // the second item does not fit
	if err := env.h.pg.UpdateStorageQuota(context.Background(), q); err != nil {
		t.Fatalf("UpdateStorageQuota: %v", err)
	}

	if _, err := env.engine.Promote(context.Background(), env.params()); !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("err = %v, want quota.ErrExceeded", err)
	}

	env.assertUntouched()
}
