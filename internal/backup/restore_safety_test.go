// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"context"
	"errors"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// blockingDB parks the first CreateUser until released, so a test can observe
// a restore that is in flight.
type blockingDB struct {
	*fakeDB

	started chan struct{}
	release chan struct{}
}

func (d *blockingDB) CreateUser(ctx context.Context, u *models.User) (*models.User, error) {
	d.started <- struct{}{}
	<-d.release

	return d.fakeDB.CreateUser(ctx, u)
}

// Two restores (or a restore and a backup) must never interleave: the second
// is refused while the first runs, and a backup fails its job instead of
// capturing a half-restored state.
func TestRestore_SerializedAgainstRestoresAndBackups(t *testing.T) {
	ctx := context.Background()
	db := &blockingDB{fakeDB: newFakeDB("dst"), started: make(chan struct{}, 2), release: make(chan struct{})}
	eng, _ := newTestEngine(t, db)

	archive := writeLegacyArchive(t, []legacyEntry{{"users.jsonl", []map[string]any{{"ID": "u1", "Username": "alice"}}}})
	done := make(chan error, 1)

	go func() { done <- eng.RunRestore(ctx, archive) }()

	<-db.started

	if !eng.RestoreInProgress() {
		t.Error("RestoreInProgress = false while a restore is running")
	}

	if err := eng.RunRestore(ctx, archive); !errors.Is(err, ErrRestoreInProgress) {
		t.Errorf("second RunRestore = %v, want ErrRestoreInProgress", err)
	}

	job := &models.BackupJob{ID: "job1", Status: "pending", Type: "metadata-only"}
	_ = db.CreateBackupJob(ctx, job)

	if err := eng.RunBackup(ctx, job.ID, "metadata-only"); !errors.Is(err, ErrRestoreInProgress) || db.jobs[job.ID].Status != "failed" {
		t.Errorf("RunBackup during restore = %v, job status %q; want ErrRestoreInProgress and a failed job", err, db.jobs[job.ID].Status)
	}

	close(db.release)

	if err := <-done; err != nil {
		t.Fatalf("first restore: %v", err)
	}

	if eng.RestoreInProgress() {
		t.Error("RestoreInProgress = true after the restore finished")
	}

	if err := eng.RunRestore(ctx, archive); err != nil {
		t.Errorf("restore after the first finished: %v", err)
	}
}

// A failing record of one restore must not be reported by (or absorbed into)
// another run: the counter starts at zero for every restore.
func TestRestore_FailureCountIsPerRun(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB("dst")
	eng, _ := newTestEngine(t, db)

	bad := writeLegacyArchive(t, []legacyEntry{{"repositories.jsonl", []map[string]any{{"ID": "r1", "Name": "raw", "Format": "raw", "RepoType": "hosted"}}}})
	good := writeLegacyArchive(t, []legacyEntry{{"users.jsonl", []map[string]any{{"ID": "u1", "Username": "alice"}}}})

	if err := eng.RunRestore(ctx, bad); !errors.Is(err, ErrRestoreIncomplete) {
		t.Fatalf("restore with a reserved repository name = %v, want ErrRestoreIncomplete", err)
	}

	if len(db.repos) != 0 {
		t.Fatalf("repository with a reserved name was restored: %v", db.repos)
	}

	if err := eng.RunRestore(ctx, good); err != nil {
		t.Fatalf("clean restore after a failed one = %v, want success", err)
	}
}

// A null element in a table must be skipped, not dereferenced.
func TestRestore_SkipsNullElements(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB("dst")
	eng, _ := newTestEngine(t, db)

	archive := writeLegacyArchive(t, []legacyEntry{
		{"users.jsonl", []any{nil, map[string]any{"ID": "u1", "Username": "alice"}}},
		{"repositories.jsonl", []any{nil}},
		{"groups.jsonl", []any{nil}},
		{"permission_targets.jsonl", []any{nil}},
		{"user_groups.jsonl", []any{nil}},
		{"storage_quotas.jsonl", []any{nil}},
	})

	if err := eng.RunRestore(ctx, archive); err != nil {
		t.Fatalf("RunRestore with null elements: %v", err)
	}

	if len(db.users) != 1 {
		t.Fatalf("users after restore = %d, want the one real user", len(db.users))
	}
}
