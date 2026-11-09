// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vexsync

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/federation"
	"github.com/openartifact/openartifact/internal/sbom"
)

// PeerVEXDocument is the OpenVEX document a repository serves to its federated
// peers: its local decisions only, never those imported from a feed or a peer,
// so decisions do not echo between peers.
func (s *Service) PeerVEXDocument(ctx context.Context, repoID, format string) ([]byte, error) {
	stmts, err := s.db.ListVEXStatements(ctx, repoID, "")
	if err != nil {
		return nil, fmt.Errorf("list VEX statements: %w", err)
	}

	local := make([]*models.VEXStatement, 0, len(stmts))

	for _, st := range stmts {
		if st.SourceKind == "" || st.SourceKind == models.VEXSourceLocal {
			local = append(local, st)
		}
	}

	doc, err := s.gen.PeerOpenVEX(format, local)
	if err != nil {
		return nil, fmt.Errorf("build peer VEX document: %w", err)
	}

	return doc.Body, nil
}

// ReceivePeerVEX imports the VEX document a peer of the repository publishes,
// as the peer's trust says: ignore does nothing, suggest keeps each decision
// about a scanned artifact of the repository as a suggestion, apply also writes
// a pending suggestion to the artifact's statements unless a statement of
// another source is there (a local decision is never overridden), and marks it
// accepted. Suggestions the document no longer holds are deleted.
func (s *Service) ReceivePeerVEX(ctx context.Context, repoID, repoFormat string, peer *federation.Peer, doc []byte) error {
	if peer.VEXTrust != federation.VEXTrustSuggest && peer.VEXTrust != federation.VEXTrustApply {
		return nil
	}

	stmts, err := sbom.ParseOpenVEX(doc)
	if err != nil {
		return fmt.Errorf("parse peer VEX document: %w", err)
	}

	targets, err := s.db.ListVEXTargets(ctx, repoID)
	if err != nil {
		return fmt.Errorf("list VEX targets: %w", err)
	}

	keep := []string{}

	for _, sg := range peerSuggestions(repoID, repoFormat, peer, stmts, targets) {
		if err := s.receive(ctx, peer, sg); err != nil {
			return err
		}

		keep = append(keep, sg.ID)
	}

	if _, err := s.db.PruneVEXSuggestions(ctx, peer.ID, keep); err != nil {
		return fmt.Errorf("prune VEX suggestions: %w", err)
	}

	return nil
}

// receive stores one suggestion and, for a peer trusted with apply, applies it
// while it is pending.
func (s *Service) receive(ctx context.Context, peer *federation.Peer, sg *models.VEXSuggestion) error {
	if _, err := s.db.UpsertVEXSuggestion(ctx, sg); err != nil {
		return fmt.Errorf("store VEX suggestion: %w", err)
	}

	if peer.VEXTrust != federation.VEXTrustApply || sg.State != models.VEXSuggestionPending {
		return nil
	}

	written, err := s.db.ApplyPeerVEXStatement(ctx, &models.VEXStatement{
		RepositoryID:    sg.RepositoryID,
		ArtifactID:      sg.ArtifactID,
		VulnID:          sg.VulnID,
		Status:          sg.Status,
		Justification:   sg.Justification,
		ImpactStatement: sg.ImpactStatement,
		ActionStatement: sg.ActionStatement,
		SourceKind:      models.VEXSourcePeer,
		SourceName:      peer.Name,
		SourceRef:       peer.BaseURL,
		PeerID:          peer.ID,
	})
	if err != nil {
		return fmt.Errorf("apply peer VEX statement: %w", err)
	}

	if !written {
		return nil
	}

	if err := s.db.SetVEXSuggestionState(ctx, sg.RepositoryID, sg.ID, models.VEXSuggestionAccepted, ""); err != nil {
		return fmt.Errorf("accept applied VEX suggestion: %w", err)
	}

	sg.State = models.VEXSuggestionAccepted

	return nil
}

// peerSuggestions turns the peer's statements into suggestions about the
// repository's scanned artifacts, matched by the purl both sides give an
// artifact of the repository format, one per artifact and vulnerability (the
// first statement wins). A vulnerability the scan reports under another id
// takes the scan's id, so the decision applies to the finding.
func peerSuggestions(repoID, repoFormat string, peer *federation.Peer, stmts []sbom.OpenVEXStatement,
	targets []models.VEXTarget) []*models.VEXSuggestion {
	byPURL := make(map[string][]*scanned)

	for i := range targets {
		tg := &targets[i]
		if p, ok := sbom.NormalizePURL(sbom.ArtifactPURL(repoFormat, tg.ArtifactID)); ok {
			byPURL[p] = append(byPURL[p], &scanned{artifactID: tg.ArtifactID, vulns: scanVulns(tg.Vulns)})
		}
	}

	type key struct{ artifact, vuln string }

	seen := make(map[key]struct{})

	var out []*models.VEXSuggestion

	for i := range stmts {
		st := &stmts[i]

		for _, t := range purlTargets(byPURL, st.PURLs) {
			vulnID, ok := t.scanID(st.VulnID)
			if !ok {
				vulnID = st.VulnID
			}

			k := key{t.artifactID, foldID(vulnID)}
			if _, dup := seen[k]; dup {
				continue
			}

			seen[k] = struct{}{}

			out = append(out, &models.VEXSuggestion{
				RepositoryID:    repoID,
				PeerID:          peer.ID,
				ArtifactID:      t.artifactID,
				VulnID:          vulnID,
				Status:          st.Status,
				Justification:   st.Justification,
				ImpactStatement: st.ImpactStatement,
				ActionStatement: st.ActionStatement,
				PeerUpdatedAt:   st.Timestamp,
			})
		}
	}

	return out
}

// purlTargets are the artifacts any of purls names, each once.
func purlTargets(byPURL map[string][]*scanned, purls []string) []*scanned {
	var out []*scanned

	seen := make(map[*scanned]struct{})

	for _, p := range purls {
		norm, ok := sbom.NormalizePURL(p)
		if !ok {
			continue
		}

		for _, t := range byPURL[norm] {
			if _, dup := seen[t]; !dup {
				seen[t] = struct{}{}
				out = append(out, t)
			}
		}
	}

	return out
}
