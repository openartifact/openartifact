// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/openartifact/openartifact/internal/httpx"
)

// VEX trust of a peer: what happens to the VEX decisions it publishes.
const (
	// VEXTrustIgnore fetches nothing.
	VEXTrustIgnore = "ignore"
	// VEXTrustSuggest keeps the decisions as pending suggestions.
	VEXTrustSuggest = "suggest"
	// VEXTrustApply applies the decisions where no other statement exists.
	VEXTrustApply = "apply"
)

const (
	// vexSyncInterval is how often the VEX decisions of a peer are fetched.
	vexSyncInterval = time.Hour
	// vexSyncTimeout bounds one fetch and import of a peer's VEX decisions.
	vexSyncTimeout = 2 * time.Minute
	// maxVEXDocument bounds the VEX document read from a peer.
	maxVEXDocument = 16 << 20
	// vexNotPublished is the vex_error of a peer that serves no VEX document.
	vexNotPublished = "the peer does not publish VEX decisions"
)

var (
	errVEXTrust = fmt.Errorf("%w: vex_trust must be one of %s, %s or %s", ErrInvalid,
		VEXTrustIgnore, VEXTrustSuggest, VEXTrustApply)
	errVEXPanic = errors.New("federation: VEX import panicked")
)

// ValidVEXTrust reports whether s is a VEX trust level.
func ValidVEXTrust(s string) bool {
	switch s {
	case VEXTrustIgnore, VEXTrustSuggest, VEXTrustApply:
		return true
	}

	return false
}

// VEXPublisher builds the VEX document a federated repository serves to its
// peers: its local decisions only.
type VEXPublisher interface {
	PeerVEXDocument(ctx context.Context, repoID, format string) ([]byte, error)
}

// VEXReceiver imports the VEX document fetched from a peer of a repository.
type VEXReceiver interface {
	ReceivePeerVEX(ctx context.Context, repoID, repoFormat string, peer *Peer, doc []byte) error
}

// SetVEXPublish chooses whether a federated repository serves its VEX
// decisions to its peers.
func (s *Store) SetVEXPublish(ctx context.Context, repoID string, on bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE federation_repos SET vex_publish = $2 WHERE repo_id = $1::uuid`, repoID, on)
	if err != nil {
		return fmt.Errorf("set vex_publish: %w", err)
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFederated
	}

	return nil
}

// MarkVEXSync records a fetch of the peer's VEX decisions. Only a client-safe
// summary of syncErr is stored (see Describe); nil clears the error.
func (s *Store) MarkVEXSync(ctx context.Context, peerID string, syncErr error) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE federation_peers SET vex_synced_at = now(), vex_error = $2 WHERE id = $1::uuid`, peerID, vexErrorText(syncErr))
	if err != nil {
		return fmt.Errorf("mark VEX sync: %w", err)
	}

	return nil
}

func vexErrorText(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNotFound):
		return vexNotPublished
	}

	return Describe(err)
}

// WithVEX makes the server answer GET /federation/v1/vex with the documents p
// builds, for the repositories that publish their VEX decisions.
func (s *Server) WithVEX(p VEXPublisher) *Server {
	s.vex = p

	return s
}

func (s *Server) vexDocument(w http.ResponseWriter, r *http.Request) {
	repo := repoFrom(r)

	st, err := s.store.RepoState(r.Context(), repo.ID)
	if err != nil && !errors.Is(err, ErrNotFederated) {
		s.fail(w, "read federation state", err)

		return
	}

	if err != nil || !st.VEXPublish || s.vex == nil {
		httpx.WriteError(w, http.StatusNotFound, codeNotFound, "repository does not publish VEX decisions")

		return
	}

	doc, err := s.vex.PeerVEXDocument(r.Context(), repo.ID, repo.Format)
	if err != nil {
		s.fail(w, "build VEX document", err)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc)
}

// VEX fetches the VEX document the peer publishes for the repository;
// ErrNotFound when it publishes none.
func (c *Client) VEX(ctx context.Context) ([]byte, error) {
	resp, err := c.get(ctx, "vex", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	doc, err := io.ReadAll(io.LimitReader(resp.Body, maxVEXDocument+1))
	if err != nil {
		return nil, fmt.Errorf("read VEX document: %w", err)
	}

	if len(doc) > maxVEXDocument {
		return nil, fmt.Errorf("%w: VEX document exceeds %d bytes", errPeerResponse, maxVEXDocument)
	}

	return doc, nil
}

// syncVEX imports the peer's VEX decisions after a successful pull, when the
// peer is trusted with them and they were not fetched in the last hour. The
// outcome is recorded on the peer; it never changes the pull's.
func (e *Engine) syncVEX(ctx context.Context, peer *Peer, repo *RepoInfo) {
	if e.VEX == nil || peer.VEXTrust == VEXTrustIgnore ||
		(peer.VEXSyncedAt != nil && time.Since(*peer.VEXSyncedAt) < vexSyncInterval) {
		return
	}

	fctx, cancel := context.WithTimeout(ctx, vexSyncTimeout)
	defer cancel()

	syncErr := e.fetchVEX(fctx, peer, repo)
	if ctx.Err() != nil {
		// Shutting down: the next pull fetches them again.
		return
	}

	if syncErr != nil {
		e.Log.Warnw("federation: VEX import failed", "repo", repo.Name, "peer", peer.Name, "error", syncErr)
	}

	mctx, mcancel := context.WithTimeout(context.WithoutCancel(ctx), stateWriteTimeout)
	defer mcancel()

	if err := e.Store.MarkVEXSync(mctx, peer.ID, syncErr); err != nil {
		e.Log.Warnw("federation: record VEX import", "repo", repo.Name, "peer", peer.Name, "error", err)
	}
}

func (e *Engine) fetchVEX(ctx context.Context, peer *Peer, repo *RepoInfo) (err error) {
	defer func() {
		if r := recover(); r != nil {
			e.logPanic("VEX import", r, "repo", repo.Name, "peer", peer.Name)

			err = fmt.Errorf("%w: %v", errVEXPanic, r)
		}
	}()

	client, err := e.peerClient(ctx, peer)
	if err != nil {
		return err
	}

	doc, err := client.VEX(ctx)
	if err != nil {
		return err
	}

	return e.VEX.ReceivePeerVEX(ctx, repo.ID, repo.Format, peer, doc)
}
