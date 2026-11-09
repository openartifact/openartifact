// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// newVEXPeer federates repoID (once) and adds a peer to it; it returns the peer id.
func newVEXPeer(t *testing.T, pg *PostgresDB, repoID, name string) string {
	t.Helper()

	ctx := context.Background()
	if _, err := pg.GetDB().ExecContext(ctx,
		`INSERT INTO federation_repos (repo_id) VALUES ($1) ON CONFLICT DO NOTHING`, repoID); err != nil {
		t.Fatalf("federate repository: %v", err)
	}

	var id string
	if err := pg.GetDB().QueryRowContext(ctx,
		`INSERT INTO federation_peers (repo_id, name, base_url, token_enc) VALUES ($1, $2, $3, 'x') RETURNING id`,
		repoID, name, "https://"+name+".example").Scan(&id); err != nil {
		t.Fatalf("create peer: %v", err)
	}

	return id
}

func newVEXFeed(t *testing.T, pg *PostgresDB, name string) *models.VEXFeed {
	t.Helper()

	f := &models.VEXFeed{
		Name:             name,
		URL:              "https://" + name + ".example/provider-metadata.json",
		Enabled:          true,
		RequireSignature: true,
		IntervalHours:    24,
	}
	if err := pg.CreateVEXFeed(context.Background(), f); err != nil {
		t.Fatalf("CreateVEXFeed: %v", err)
	}

	return f
}

// vexByTarget indexes the statements of a repository by artifact/vulnerability.
func vexByTarget(t *testing.T, pg *PostgresDB, repoID string) map[string]*models.VEXStatement {
	t.Helper()

	all, err := pg.ListVEXStatements(context.Background(), repoID, "")
	if err != nil {
		t.Fatal(err)
	}

	out := make(map[string]*models.VEXStatement, len(all))
	for _, s := range all {
		out[s.ArtifactID+"/"+s.VulnID] = s
	}

	return out
}

func csafStmt(repo, artifact, vuln, doc string) *models.VEXStatement {
	return &models.VEXStatement{
		RepositoryID: repo, ArtifactID: artifact, VulnID: vuln, Status: models.VEXNotAffected,
		ImpactStatement: "vendor says so", SourceName: "vendor", SourceRef: doc,
	}
}

func TestVEXFeedLifecycle(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)

	f := newVEXFeed(t, pg, "vendor")
	if f.ID == "" || f.CreatedAt.IsZero() || !f.Enabled || f.IntervalHours != 24 {
		t.Fatalf("created feed = %+v", f)
	}

	if err := pg.CreateVEXFeed(
		ctx,
		&models.VEXFeed{Name: "vendor", URL: "https://other.example", IntervalHours: 1},
	); !errors.Is(
		err,
		ErrVEXFeedConflict,
	) {
		t.Errorf("duplicate name = %v, want ErrVEXFeedConflict", err)
	}

	other := newVEXFeed(t, pg, "other")

	for _, id := range []string{"not-a-uuid", "00000000-0000-0000-0000-00000000dead"} {
		if _, err := pg.GetVEXFeed(ctx, id); !errors.Is(err, ErrVEXFeedNotFound) {
			t.Errorf("GetVEXFeed(%q) = %v, want ErrVEXFeedNotFound", id, err)
		}

		if err := pg.UpdateVEXFeed(
			ctx,
			&models.VEXFeed{ID: id, Name: "x", URL: "https://x", IntervalHours: 1},
		); !errors.Is(
			err,
			ErrVEXFeedNotFound,
		) {
			t.Errorf("UpdateVEXFeed(%q) = %v, want ErrVEXFeedNotFound", id, err)
		}

		if err := pg.DeleteVEXFeed(ctx, id); !errors.Is(err, ErrVEXFeedNotFound) {
			t.Errorf("DeleteVEXFeed(%q) = %v, want ErrVEXFeedNotFound", id, err)
		}
	}

	rename := *other
	rename.Name = "vendor"

	if err := pg.UpdateVEXFeed(ctx, &rename); !errors.Is(err, ErrVEXFeedConflict) {
		t.Errorf("rename onto a taken name = %v, want ErrVEXFeedConflict", err)
	}

	checkVEXFeedSync(t, pg, f)
	checkVEXFeedDocuments(t, pg, f)

	feeds, err := pg.ListVEXFeeds(ctx)
	if err != nil || len(feeds) != 2 || feeds[0].Name != "other" || feeds[1].Name != "vendor" {
		t.Fatalf("ListVEXFeeds = %+v, %v; want other, vendor", feeds, err)
	}

	if err := pg.DeleteVEXFeed(ctx, f.ID); err != nil {
		t.Fatal(err)
	}

	if stmts, err := pg.ListVEXFeedStatements(ctx, f.ID); err != nil || len(stmts) != 0 {
		t.Errorf("statements after delete = %d, %v; the delete cascades", len(stmts), err)
	}

	if err := pg.DeleteVEXFeed(ctx, f.ID); !errors.Is(err, ErrVEXFeedNotFound) {
		t.Errorf("second delete = %v, want ErrVEXFeedNotFound", err)
	}
}

// checkVEXFeedSync: a successful sync moves the cursor and the totals, a failed
// one only the attempt and the error, and a new URL resets the cursor.
func checkVEXFeedSync(t *testing.T, pg *PostgresDB, f *models.VEXFeed) {
	t.Helper()

	ctx := context.Background()
	cursor := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	if err := pg.RecordVEXFeedSync(
		ctx,
		f.ID,
		models.VEXFeedSyncResult{Success: true, CursorAt: &cursor, Documents: 3, Rejected: 1},
	); err != nil {
		t.Fatal(err)
	}

	if err := pg.RecordVEXFeedSync(
		ctx,
		f.ID,
		models.VEXFeedSyncResult{Error: "provider unreachable", Documents: 9, Rejected: 9},
	); err != nil {
		t.Fatal(err)
	}

	got, err := pg.GetVEXFeed(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}

	if got.CursorAt == nil || !got.CursorAt.Equal(cursor) || got.LastSuccessAt == nil || got.LastAttemptAt == nil ||
		got.LastError != "provider unreachable" || got.DocumentsTotal != 3 || got.RejectedTotal != 1 {
		t.Errorf("after a good and a failed sync: %+v", got)
	}

	// Same URL keeps the cursor; a new one starts over.
	got.Enabled, got.IntervalHours = false, 6
	if err := pg.UpdateVEXFeed(ctx, got); err != nil || got.CursorAt == nil || got.Enabled || got.IntervalHours != 6 {
		t.Fatalf("update keeping the URL = %+v, %v", got, err)
	}

	got.URL = "https://moved.example/feed.json"
	if err := pg.UpdateVEXFeed(ctx, got); err != nil || got.CursorAt != nil || got.URL != "https://moved.example/feed.json" {
		t.Errorf("update with a new URL = %+v, %v; want the cursor reset", got, err)
	}

	if err := pg.RecordVEXFeedSync(
		ctx,
		"00000000-0000-0000-0000-00000000dead",
		models.VEXFeedSyncResult{},
	); !errors.Is(
		err,
		ErrVEXFeedNotFound,
	) {
		t.Errorf("sync of a missing feed = %v, want ErrVEXFeedNotFound", err)
	}
}

// checkVEXFeedDocuments: a re-fetched document replaces its rows only.
func checkVEXFeedDocuments(t *testing.T, pg *PostgresDB, f *models.VEXFeed) {
	t.Helper()

	ctx := context.Background()
	released := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	st := func(doc, vuln string) models.VEXFeedStatement {
		return models.VEXFeedStatement{
			DocumentURL: doc, TrackingID: "T-" + vuln, VulnID: vuln, PURL: "pkg:npm/lib@1.0.0",
			Status: models.VEXFixed, ReleasedAt: &released,
		}
	}

	if err := pg.ReplaceVEXFeedDocument(
		ctx,
		f.ID,
		"https://d/1",
		[]models.VEXFeedStatement{st("https://d/1", "CVE-1"), st("https://d/1", "CVE-2")},
	); err != nil {
		t.Fatal(err)
	}

	cpeOnly := models.VEXFeedStatement{
		DocumentURL: "https://d/2",
		VulnID:      "CVE-3",
		CPE:         "cpe:2.3:a:acme:lib:1.0:*:*:*:*:*:*:*",
		Status:      models.VEXFixed,
	}
	if err := pg.ReplaceVEXFeedDocument(ctx, f.ID, "https://d/2", []models.VEXFeedStatement{cpeOnly}); err != nil {
		t.Fatal(err)
	}

	if err := pg.ReplaceVEXFeedDocument(ctx, f.ID, "https://d/1", []models.VEXFeedStatement{st("https://d/1", "CVE-9")}); err != nil {
		t.Fatal(err)
	}

	stmts, err := pg.ListVEXFeedStatements(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}

	vulns := make([]string, len(stmts))
	for i, s := range stmts {
		vulns[i] = s.VulnID
	}

	if !slices.Equal(vulns, []string{"CVE-3", "CVE-9"}) {
		t.Fatalf("statements = %v, want CVE-3 (other document) and CVE-9 (replacement)", vulns)
	}

	if s := stmts[1]; s.ReleasedAt == nil || !s.ReleasedAt.Equal(released) || s.TrackingID != "T-CVE-9" || s.PURL != "pkg:npm/lib@1.0.0" {
		t.Errorf("stored statement = %+v", s)
	}

	if stmts[0].ReleasedAt != nil || stmts[0].CPE == "" {
		t.Errorf("cpe-only statement = %+v; want no release date", stmts[0])
	}

	if got, _ := pg.GetVEXFeed(ctx, f.ID); got == nil || got.StatementCount != 2 {
		t.Errorf("StatementCount = %+v, want 2", got)
	}
}

func TestReplaceFeedVEXStatements(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	repo := newQuotaRepo(t, pg, "vex-feed", 0, 0)
	feed := newVEXFeed(t, pg, "vendor")
	other := newVEXFeed(t, pg, "other-vendor")

	local := &models.VEXStatement{RepositoryID: repo, ArtifactID: "lib@1", VulnID: "CVE-1", Status: models.VEXAffected, CreatedBy: "alice"}
	if err := pg.UpsertVEXStatement(ctx, local); err != nil || local.SourceKind != models.VEXSourceLocal {
		t.Fatalf("local upsert = %+v, %v; want source local", local, err)
	}

	n, err := pg.ReplaceFeedVEXStatements(ctx, feed.ID, []*models.VEXStatement{
		csafStmt(repo, "lib@1", "CVE-1", "https://d/1"),
		csafStmt(repo, "lib@1", "CVE-2", "https://d/1"),
		csafStmt(repo, "lib@2", "CVE-3", "https://d/2"),
	})
	if err != nil || n != 2 {
		t.Fatalf("first materialization = %d, %v; want 2 (the local decision stays)", n, err)
	}

	byTarget := vexByTarget(t, pg, repo)
	if s := byTarget["lib@1/CVE-1"]; s.SourceKind != models.VEXSourceLocal || s.Status != models.VEXAffected || s.FeedID != "" {
		t.Errorf("local decision replaced: %+v", s)
	}

	if s := byTarget["lib@1/CVE-2"]; s.SourceKind != models.VEXSourceCSAF || s.FeedID != feed.ID || s.SourceName != "vendor" ||
		s.SourceRef != "https://d/1" {
		t.Errorf("csaf statement = %+v", s)
	}

	// Another feed never replaces the first feed's statement.
	if n, err := pg.ReplaceFeedVEXStatements(
		ctx,
		other.ID,
		[]*models.VEXStatement{csafStmt(repo, "lib@1", "CVE-2", "https://o/1")},
	); err != nil ||
		n != 0 {
		t.Errorf("other feed = %d, %v; want 0", n, err)
	}

	// The next materialization drops the statements the feed no longer makes.
	n, err = pg.ReplaceFeedVEXStatements(ctx, feed.ID, []*models.VEXStatement{csafStmt(repo, "lib@1", "CVE-2", "https://d/1")})
	if err != nil || n != 1 {
		t.Fatalf("second materialization = %d, %v; want 1", n, err)
	}

	byTarget = vexByTarget(t, pg, repo)
	if _, stale := byTarget["lib@2/CVE-3"]; stale || len(byTarget) != 2 {
		t.Errorf("statements after the second run = %v; want the stale lib@2/CVE-3 removed", byTarget)
	}

	if got, _ := pg.GetVEXFeed(ctx, feed.ID); got == nil || got.AppliedCount != 1 {
		t.Errorf("AppliedCount = %+v, want 1", got)
	}

	checkVEXOverrideAndFeedDelete(t, pg, repo, feed.ID)
}

// checkVEXOverrideAndFeedDelete: an operator decision replaces a csaf
// statement and detaches it from the feed, so deleting the feed keeps it.
func checkVEXOverrideAndFeedDelete(t *testing.T, pg *PostgresDB, repo, feedID string) {
	t.Helper()

	ctx := context.Background()

	op := &models.VEXStatement{RepositoryID: repo, ArtifactID: "lib@1", VulnID: "CVE-2", Status: models.VEXAffected, CreatedBy: "bob"}
	if err := pg.UpsertVEXStatement(
		ctx,
		op,
	); err != nil || op.SourceKind != models.VEXSourceLocal || op.FeedID != "" ||
		op.SourceRef != "" {
		t.Errorf("operator override = %+v, %v", op, err)
	}

	if err := pg.DeleteVEXFeed(ctx, feedID); err != nil {
		t.Fatal(err)
	}

	if got := vexByTarget(t, pg, repo); len(got) != 2 {
		t.Errorf("deleting the feed removed local decisions: %v", got)
	}
}

func TestApplyPeerVEXStatement(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	repo := newQuotaRepo(t, pg, "vex-peer", 0, 0)
	peerA := newVEXPeer(t, pg, repo, "peer-a")
	peerB := newVEXPeer(t, pg, repo, "peer-b")
	feed := newVEXFeed(t, pg, "vendor")

	if err := pg.UpsertVEXStatement(
		ctx,
		&models.VEXStatement{RepositoryID: repo, ArtifactID: "a", VulnID: "CVE-1", Status: models.VEXAffected},
	); err != nil {
		t.Fatal(err)
	}

	if _, err := pg.ReplaceFeedVEXStatements(
		ctx,
		feed.ID,
		[]*models.VEXStatement{csafStmt(repo, "a", "CVE-2", "https://d/1")},
	); err != nil {
		t.Fatal(err)
	}

	peer := func(peerID, vuln, status string) *models.VEXStatement {
		return &models.VEXStatement{
			RepositoryID: repo, ArtifactID: "a", VulnID: vuln, Status: status,
			Justification: models.VEXComponentNotPresent, PeerID: peerID, SourceName: "peer", SourceRef: "https://peer.example",
		}
	}

	for _, tc := range []struct {
		name  string
		stmt  *models.VEXStatement
		wrote bool
	}{
		{"a local decision stays", peer(peerA, "CVE-1", models.VEXNotAffected), false},
		{"a csaf statement stays", peer(peerA, "CVE-2", models.VEXNotAffected), false},
		{"a free target is written", peer(peerA, "CVE-3", models.VEXNotAffected), true},
		{"the same peer updates its own statement", peer(peerA, "CVE-3", models.VEXFixed), true},
		{"another peer does not replace it", peer(peerB, "CVE-3", models.VEXNotAffected), false},
	} {
		wrote, err := pg.ApplyPeerVEXStatement(ctx, tc.stmt)
		if err != nil || wrote != tc.wrote {
			t.Errorf("%s: wrote = %v, %v; want %v", tc.name, wrote, err, tc.wrote)
		}
	}

	byTarget := vexByTarget(t, pg, repo)
	if s := byTarget["a/CVE-1"]; s.SourceKind != models.VEXSourceLocal || s.Status != models.VEXAffected {
		t.Errorf("local = %+v", s)
	}

	if s := byTarget["a/CVE-2"]; s.SourceKind != models.VEXSourceCSAF {
		t.Errorf("csaf = %+v", s)
	}

	if s := byTarget["a/CVE-3"]; s.SourceKind != models.VEXSourcePeer || s.PeerID != peerA || s.Status != models.VEXFixed ||
		s.SourceRef != "https://peer.example" {
		t.Errorf("peer = %+v", s)
	}
}

func TestVEXSuggestions(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	repo := newQuotaRepo(t, pg, "vex-sug", 0, 0)
	otherRepo := newQuotaRepo(t, pg, "vex-sug-other", 0, 0)
	peer := newVEXPeer(t, pg, repo, "peer-a")

	sug := func(vuln, status, just string) *models.VEXSuggestion {
		return &models.VEXSuggestion{
			RepositoryID:  repo,
			PeerID:        peer,
			ArtifactID:    "lib@1",
			VulnID:        vuln,
			Status:        status,
			Justification: just,
		}
	}

	first := sug("CVE-1", models.VEXNotAffected, models.VEXComponentNotPresent)
	if changed, err := pg.UpsertVEXSuggestion(
		ctx,
		first,
	); err != nil || !changed || first.ID == "" ||
		first.State != models.VEXSuggestionPending {
		t.Fatalf("new suggestion = %+v, changed %v, %v", first, changed, err)
	}

	same := sug("CVE-1", models.VEXNotAffected, models.VEXComponentNotPresent)
	if changed, err := pg.UpsertVEXSuggestion(ctx, same); err != nil || changed || same.ID != first.ID {
		t.Errorf("unchanged suggestion = %+v, changed %v, %v", same, changed, err)
	}

	if err := pg.SetVEXSuggestionState(ctx, repo, first.ID, models.VEXSuggestionDismissed, "alice"); err != nil {
		t.Fatal(err)
	}

	if err := pg.SetVEXSuggestionState(
		ctx,
		otherRepo,
		first.ID,
		models.VEXSuggestionDismissed,
		"alice",
	); !errors.Is(
		err,
		ErrVEXSuggestionNotFound,
	) {
		t.Errorf("state change through another repository = %v, want ErrVEXSuggestionNotFound", err)
	}

	if changed, _ := pg.UpsertVEXSuggestion(ctx, same); changed || same.State != models.VEXSuggestionDismissed {
		t.Errorf("unchanged content reopened the decision: %+v", same)
	}

	// The peer changes its mind: back to pending, decision cleared.
	moved := sug("CVE-1", models.VEXFixed, "")
	if changed, err := pg.UpsertVEXSuggestion(
		ctx,
		moved,
	); err != nil || !changed || moved.ID != first.ID ||
		moved.State != models.VEXSuggestionPending {
		t.Fatalf("changed suggestion = %+v, changed %v, %v", moved, changed, err)
	}

	if got, err := pg.GetVEXSuggestion(
		ctx,
		repo,
		first.ID,
	); err != nil || got.DecidedBy != "" || got.DecidedAt != nil ||
		got.Status != models.VEXFixed {
		t.Errorf("after the change = %+v, %v; want the decision cleared", got, err)
	}

	checkVEXSuggestionAccept(t, pg, repo, otherRepo, first.ID)
	checkVEXSuggestionPrune(t, pg, repo, peer)
}

// checkVEXSuggestionAccept: accepting replaces even a local decision and
// records the peer as the source.
func checkVEXSuggestionAccept(t *testing.T, pg *PostgresDB, repo, otherRepo, id string) {
	t.Helper()

	ctx := context.Background()

	if err := pg.UpsertVEXStatement(
		ctx,
		&models.VEXStatement{RepositoryID: repo, ArtifactID: "lib@1", VulnID: "CVE-1", Status: models.VEXAffected},
	); err != nil {
		t.Fatal(err)
	}

	if got, _ := pg.GetVEXSuggestion(
		ctx,
		repo,
		id,
	); got == nil || got.LocalStatus != models.VEXAffected ||
		got.LocalSourceKind != models.VEXSourceLocal {
		t.Errorf("suggestion before accept = %+v; want the local statement shown", got)
	}

	if _, err := pg.AcceptVEXSuggestion(ctx, otherRepo, id, "bob"); !errors.Is(err, ErrVEXSuggestionNotFound) {
		t.Errorf("accept through another repository = %v, want ErrVEXSuggestionNotFound", err)
	}

	stmt, err := pg.AcceptVEXSuggestion(ctx, repo, id, "bob")
	if err != nil {
		t.Fatal(err)
	}

	if stmt.SourceKind != models.VEXSourcePeer || stmt.SourceName != "peer-a" || stmt.SourceRef != "https://peer-a.example" ||
		stmt.CreatedBy != "bob" || stmt.Status != models.VEXFixed || stmt.PeerID == "" {
		t.Errorf("accepted statement = %+v", stmt)
	}

	if s := vexByTarget(t, pg, repo)["lib@1/CVE-1"]; s == nil || s.ID != stmt.ID || s.SourceKind != models.VEXSourcePeer {
		t.Errorf("stored statement = %+v", s)
	}

	checkVEXSuggestionAccepted(t, pg, repo, id)
}

func checkVEXSuggestionAccepted(t *testing.T, pg *PostgresDB, repo, id string) {
	t.Helper()

	got, err := pg.GetVEXSuggestion(context.Background(), repo, id)
	if err != nil || got.State != models.VEXSuggestionAccepted || got.DecidedBy != "bob" || got.DecidedAt == nil ||
		got.LocalSourceKind != models.VEXSourcePeer || got.PeerName != "peer-a" {
		t.Errorf("accepted suggestion = %+v, %v", got, err)
	}
}

// checkVEXSuggestionPrune: listing filters by state; pruning keeps the given ids.
func checkVEXSuggestionPrune(t *testing.T, pg *PostgresDB, repo, peer string) {
	t.Helper()

	ctx := context.Background()

	second := &models.VEXSuggestion{RepositoryID: repo, PeerID: peer, ArtifactID: "lib@2", VulnID: "CVE-2", Status: models.VEXAffected}
	if _, err := pg.UpsertVEXSuggestion(ctx, second); err != nil {
		t.Fatal(err)
	}

	if all, err := pg.ListVEXSuggestions(ctx, repo, ""); err != nil || len(all) != 2 {
		t.Errorf("all suggestions = %d, %v; want 2", len(all), err)
	}

	pending, err := pg.ListVEXSuggestions(ctx, repo, models.VEXSuggestionPending)
	if err != nil || len(pending) != 1 || pending[0].ID != second.ID {
		t.Errorf("pending = %+v, %v; want the second only", pending, err)
	}

	if n, err := pg.PruneVEXSuggestions(ctx, peer, []string{second.ID}); err != nil || n != 1 {
		t.Errorf("prune = %d, %v; want 1", n, err)
	}

	if n, err := pg.DeleteVEXSuggestionsOfPeer(ctx, peer); err != nil || n != 1 {
		t.Errorf("delete of the peer's suggestions = %d, %v; want 1", n, err)
	}

	if _, err := pg.GetVEXSuggestion(ctx, repo, second.ID); !errors.Is(err, ErrVEXSuggestionNotFound) {
		t.Errorf("deleted suggestion = %v, want ErrVEXSuggestionNotFound", err)
	}
}

func TestListVEXTargets(t *testing.T) {
	ctx := context.Background()
	pg := newQuotaTestDB(t, 4)
	repoA := seedRepo(t, pg.GetDB(), "vex-targets-a", "npm")
	repoB := seedRepo(t, pg.GetDB(), "vex-targets-b", "pypi")

	scan := func(repo, artifact, status string, vulns ...models.Vulnerability) {
		t.Helper()

		if err := pg.CreateVulnScanResult(ctx, &models.VulnScanResult{
			RepositoryID: repo, ArtifactID: artifact, Scanner: "osv", ScanStatus: status,
			Vulnerabilities: vulns, VulnCount: len(vulns),
		}); err != nil {
			t.Fatal(err)
		}
	}

	scan(repoA, "lib@1.0.0", "completed", models.Vulnerability{ID: "GHSA-xxxx", Aliases: []string{"CVE-2026-1"}})
	scan(repoA, "clean@1.0.0", "completed")
	scan(repoA, "broken@1.0.0", "failed", models.Vulnerability{ID: "CVE-2026-2"})
	scan(repoB, "requests@2.0.0", "partial", models.Vulnerability{ID: "CVE-2026-3"})

	all, err := pg.ListVEXTargets(ctx, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("ListVEXTargets(all) = %+v, %v; want lib and requests", all, err)
	}

	only, err := pg.ListVEXTargets(ctx, repoA)
	if err != nil || len(only) != 1 {
		t.Fatalf("ListVEXTargets(repoA) = %+v, %v", only, err)
	}

	got := only[0]
	if got.RepositoryID != repoA || got.RepositoryFormat != "npm" || got.ArtifactID != "lib@1.0.0" ||
		len(got.Vulns) != 1 || got.Vulns[0].ID != "GHSA-xxxx" || !slices.Equal(got.Vulns[0].Aliases, []string{"CVE-2026-1"}) {
		t.Errorf("target = %+v", got)
	}
}
