// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vexsync

import (
	"context"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/federation"
	"github.com/openartifact/openartifact/internal/sbom"
)

const (
	artifact = "left-pad@1.3.0"
	vulnID   = "CVE-2024-1111"
)

// publisherDocument is the document a publishing peer serves for an npm
// repository holding a local decision, a feed's and another peer's.
func publisherDocument(t *testing.T) []byte {
	t.Helper()

	db := newMemStore()
	db.put(&models.VEXStatement{
		RepositoryID: "pub", ArtifactID: artifact, VulnID: vulnID, Status: models.VEXNotAffected,
		Justification: models.VEXComponentNotPresent, SourceKind: models.VEXSourceLocal, CreatedBy: "alice",
	})
	db.put(&models.VEXStatement{
		RepositoryID: "pub", ArtifactID: artifact, VulnID: "CVE-2024-3333", Status: models.VEXNotAffected,
		Justification: models.VEXComponentNotPresent, SourceKind: models.VEXSourceCSAF, SourceName: "acme", FeedID: feedID,
	})
	db.put(&models.VEXStatement{
		RepositoryID: "pub", ArtifactID: artifact, VulnID: "CVE-2024-4444", Status: models.VEXFixed,
		SourceKind: models.VEXSourcePeer, SourceName: "other", PeerID: "peer-x",
	})

	doc, err := newTestService(t, db, nil).PeerVEXDocument(context.Background(), "pub", "npm")
	if err != nil {
		t.Fatalf("PeerVEXDocument: %v", err)
	}

	return doc
}

func TestPeerVEXDocumentHoldsOnlyLocalDecisions(t *testing.T) {
	stmts, err := sbom.ParseOpenVEX(publisherDocument(t))
	if err != nil {
		t.Fatal(err)
	}

	if len(stmts) != 1 {
		t.Fatalf("statements = %+v, want the local decision only", stmts)
	}

	want, _ := sbom.NormalizePURL(sbom.ArtifactPURL("npm", artifact))
	got := stmts[0]

	if got.VulnID != vulnID || got.Status != models.VEXNotAffected || got.Justification != models.VEXComponentNotPresent ||
		len(got.PURLs) == 0 {
		t.Fatalf("statement = %+v", got)
	}

	if p, _ := sbom.NormalizePURL(got.PURLs[0]); p != want {
		t.Errorf("product purl = %s, want %s", got.PURLs[0], want)
	}
}

func pullingStore() *memStore {
	return newMemStore(models.VEXTarget{
		RepositoryID: "pull", RepositoryFormat: "npm", ArtifactID: artifact, Vulns: []models.Vulnerability{{ID: vulnID}},
	})
}

func testPeer(trust string) *federation.Peer {
	return &federation.Peer{ID: "peer-1", RepoID: "pull", Name: "upstream", BaseURL: "https://upstream.example", VEXTrust: trust}
}

func receive(t *testing.T, db *memStore, peer *federation.Peer, doc []byte) {
	t.Helper()

	if err := newTestService(t, db, nil).ReceivePeerVEX(context.Background(), "pull", "npm", peer, doc); err != nil {
		t.Fatalf("ReceivePeerVEX: %v", err)
	}
}

func TestReceivePeerVEXIgnoreDoesNothing(t *testing.T) {
	db := pullingStore()
	receive(t, db, testPeer(federation.VEXTrustIgnore), publisherDocument(t))

	if len(db.suggestions()) != 0 || db.statementCount() != 0 {
		t.Errorf("suggestions = %+v, statements = %d, want nothing", db.suggestions(), db.statementCount())
	}
}

func TestReceivePeerVEXSuggestKeepsPendingSuggestions(t *testing.T) {
	db := pullingStore()
	peer := testPeer(federation.VEXTrustSuggest)
	receive(t, db, peer, publisherDocument(t))

	sgs := db.suggestions()
	if len(sgs) != 1 {
		t.Fatalf("suggestions = %+v, want one", sgs)
	}

	sg := sgs[0]
	if sg.State != models.VEXSuggestionPending || sg.PeerID != peer.ID || sg.RepositoryID != "pull" ||
		sg.ArtifactID != artifact || sg.VulnID != vulnID || sg.Status != models.VEXNotAffected ||
		sg.Justification != models.VEXComponentNotPresent {
		t.Errorf("suggestion = %+v", sg)
	}

	if db.statementCount() != 0 {
		t.Error("suggest trust wrote a statement")
	}

	// A document without the decision deletes its suggestion.
	empty, err := sbom.NewGenerator(nil, testLogger(t)).PeerOpenVEX("npm", nil)
	if err != nil {
		t.Fatal(err)
	}

	receive(t, db, peer, empty.Body)

	if sgs := db.suggestions(); len(sgs) != 0 {
		t.Errorf("suggestions after an empty document = %+v, want none", sgs)
	}
}

func TestReceivePeerVEXApplyWritesFreeStatements(t *testing.T) {
	db := pullingStore()
	peer := testPeer(federation.VEXTrustApply)
	receive(t, db, peer, publisherDocument(t))

	st := db.statement("pull", artifact, vulnID)
	if st == nil {
		t.Fatal("apply trust wrote no statement")
	}

	if st.SourceKind != models.VEXSourcePeer || st.SourceName != peer.Name || st.SourceRef != peer.BaseURL ||
		st.PeerID != peer.ID || st.Status != models.VEXNotAffected || st.CreatedBy != "" {
		t.Errorf("applied statement = %+v", st)
	}

	sgs := db.suggestions()
	if len(sgs) != 1 || sgs[0].State != models.VEXSuggestionAccepted || sgs[0].DecidedBy != "" {
		t.Errorf("suggestions = %+v, want one accepted by nobody", sgs)
	}
}

func TestReceivePeerVEXApplyNeverOverridesALocalDecision(t *testing.T) {
	db := pullingStore()
	db.put(&models.VEXStatement{
		RepositoryID: "pull", ArtifactID: artifact, VulnID: vulnID, Status: models.VEXAffected,
		SourceKind: models.VEXSourceLocal, CreatedBy: "bob",
	})

	receive(t, db, testPeer(federation.VEXTrustApply), publisherDocument(t))

	if st := db.statement("pull", artifact, vulnID); st.SourceKind != models.VEXSourceLocal || st.Status != models.VEXAffected {
		t.Errorf("local decision replaced by %+v", st)
	}

	sgs := db.suggestions()
	if len(sgs) != 1 || sgs[0].State != models.VEXSuggestionPending {
		t.Errorf("suggestions = %+v, want one left pending", sgs)
	}
}
