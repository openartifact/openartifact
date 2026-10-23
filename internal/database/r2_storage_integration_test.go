// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// API-14: the pending-upload cap was a count followed by a separate insert, so
// parallel creates all passed the check.
func TestCreatePresignedUploadCappedIsAtomic(t *testing.T) {
	pg := newQuotaTestDB(t, 20)
	s := newQuotaSeeder(t, pg, "cap-raw", "raw")

	const (
		maxPending = 3
		workers    = 16
	)

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		created  int
		refused  int
		failures []error
	)

	for i := range workers {
		wg.Go(func() {
			err := pg.CreatePresignedUploadCapped(context.Background(), &models.PresignedUpload{
				RepositoryID: s.repo, Path: "p" + string(rune('a'+i)), SizeBytes: 1,
				SHA256: strings.Repeat("a", 64), CreatedBy: "alice", ExpiresAt: time.Now().Add(time.Hour),
			}, maxPending)

			mu.Lock()
			defer mu.Unlock()

			switch {
			case err == nil:
				created++
			case errors.Is(err, ErrPresignedUploadCap):
				refused++
			default:
				failures = append(failures, err)
			}
		})
	}

	wg.Wait()

	if len(failures) > 0 {
		t.Fatalf("unexpected errors: %v", failures)
	}

	if created != maxPending || refused != workers-maxPending {
		t.Fatalf("created %d, refused %d; want exactly %d created", created, refused, maxPending)
	}
}

// API-11: the repository row and its group members change together or not at all.
func TestUpdateRepositoryAndMembersRollsBack(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	group := newQuotaSeeder(t, pg, "grp-atomic", "raw")

	repo, err := pg.GetRepositoryByID(ctx, group.repo)
	if err != nil || repo == nil {
		t.Fatalf("GetRepositoryByID = %v, %v", repo, err)
	}

	repo.Description = "changed"

	// A member id that is not a repository violates the foreign key.
	err = pg.UpdateRepositoryAndMembers(ctx, repo, []string{"00000000-0000-0000-0000-000000000001"})
	if err == nil {
		t.Fatal("UpdateRepositoryAndMembers accepted a member that does not exist")
	}

	after, err := pg.GetRepositoryByID(ctx, group.repo)
	if err != nil || after == nil {
		t.Fatalf("GetRepositoryByID = %v, %v", after, err)
	}

	if after.Description == "changed" {
		t.Error("the repository update was applied although the member replacement failed")
	}
}

// API-10 / STOR-9: members of equal priority must come back in a stable order.
func TestListGroupMembersOrderIsStable(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	ctx := context.Background()
	group := newQuotaSeeder(t, pg, "grp-order", "raw")

	var ids []string

	for _, n := range []string{"m-a", "m-b", "m-c", "m-d"} {
		m := newQuotaSeeder(t, pg, n, "raw")

		if err := pg.AddGroupMember(ctx, group.repo, m.repo, 0); err != nil {
			t.Fatal(err)
		}

		ids = append(ids, m.repo)
	}

	first, err := pg.ListGroupMembers(ctx, group.repo)
	if err != nil || len(first) != len(ids) {
		t.Fatalf("ListGroupMembers = %d, %v", len(first), err)
	}

	for range 20 {
		again, err := pg.ListGroupMembers(ctx, group.repo)
		if err != nil {
			t.Fatal(err)
		}

		for i := range again {
			if again[i].MemberRepoID != first[i].MemberRepoID {
				t.Fatalf("order changed between calls: %v vs %v", again[i].MemberRepoID, first[i].MemberRepoID)
			}
		}
	}

	for i := range ids {
		if first[i].MemberRepoID != ids[i] {
			t.Fatalf("equal-priority members not in insertion order: got %s at %d, want %s", first[i].MemberRepoID, i, ids[i])
		}
	}
}

// A multipart upload keeps its plan and backend handle in the row, and every
// parts call can move the deadline out but never back in.
func TestPresignedMultipartPlanAndExpiry(t *testing.T) {
	pg := newQuotaTestDB(t, 4)
	s := newQuotaSeeder(t, pg, "mp-raw", "raw")
	ctx := context.Background()
	soon := time.Now().Add(time.Hour).Truncate(time.Second)

	up := &models.PresignedUpload{
		RepositoryID: s.repo, Path: "big.bin", SizeBytes: 6 << 30, PartSize: 64 << 20,
		SHA256: strings.Repeat("b", 64), CreatedBy: "alice", ExpiresAt: soon,
	}
	if err := pg.CreatePresignedUpload(ctx, up); err != nil {
		t.Fatalf("CreatePresignedUpload: %v", err)
	}

	if err := pg.SetPresignedUploadMultipartID(ctx, up.ID, "upload-handle"); err != nil {
		t.Fatalf("SetPresignedUploadMultipartID: %v", err)
	}

	later := soon.Add(5 * time.Hour)
	if err := pg.ExtendPresignedUploadExpiry(ctx, up.ID, later); err != nil {
		t.Fatalf("ExtendPresignedUploadExpiry: %v", err)
	}

	if err := pg.ExtendPresignedUploadExpiry(ctx, up.ID, soon); err != nil {
		t.Fatalf("ExtendPresignedUploadExpiry (earlier): %v", err)
	}

	got, err := pg.GetPresignedUpload(ctx, s.repo, up.ID)
	if err != nil || got == nil {
		t.Fatalf("GetPresignedUpload = %v, %v", got, err)
	}

	if got.PartSize != 64<<20 || got.MultipartID != "upload-handle" || !got.ExpiresAt.Equal(later) {
		t.Errorf("row = part size %d, handle %q, expires %v; want 64 MiB, upload-handle, %v", got.PartSize, got.MultipartID, got.ExpiresAt, later)
	}
}
