// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package federation

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"
)

const engVEXPath = "/federation/v1/vex"

// fakeVEXPublisher serves a fixed document and records what it was asked for.
type fakeVEXPublisher struct {
	doc []byte
	err error

	mu    sync.Mutex
	calls []string // repoID/format
}

func (p *fakeVEXPublisher) PeerVEXDocument(_ context.Context, repoID, format string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.calls = append(p.calls, repoID+"/"+format)

	return p.doc, p.err
}

func (p *fakeVEXPublisher) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.err = err
}

func (p *fakeVEXPublisher) asked() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return slices.Clone(p.calls)
}

type vexReceipt struct {
	repoID, format, peerID string
	doc                    string
}

// fakeVEXReceiver records the documents handed to it and answers err.
type fakeVEXReceiver struct {
	mu       sync.Mutex
	err      error
	receipts []vexReceipt
}

func (r *fakeVEXReceiver) ReceivePeerVEX(_ context.Context, repoID, repoFormat string, peer *Peer, doc []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.receipts = append(r.receipts, vexReceipt{repoID: repoID, format: repoFormat, peerID: peer.ID, doc: string(doc)})

	return r.err
}

func (r *fakeVEXReceiver) count() int {
	return len(r.all())
}

func (r *fakeVEXReceiver) all() []vexReceipt {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.receipts)
}

func (r *fakeVEXReceiver) setErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.err = err
}

func TestVEXEndpointServesOnlyPublishingRepositories(t *testing.T) {
	a := newEngSite(t, "site-a", "raw")
	ctx := context.Background()
	auth := engBearer(a.token)

	status, _, body := engGet(t, a.srv.URL, engVEXPath, auth)
	if status != http.StatusNotFound || engErrCode(t, body) != codeNotFound {
		t.Fatalf("no publisher: %d %s", status, body)
	}

	pub := &fakeVEXPublisher{doc: []byte(`{"@context":"https://openvex.dev/ns/v0.2.0"}`)}
	a.fed.WithVEX(pub)

	status, _, body = engGet(t, a.srv.URL, engVEXPath, auth)
	if status != http.StatusNotFound || engErrCode(t, body) != codeNotFound {
		t.Fatalf("not published: %d %s", status, body)
	}

	if err := a.store.SetVEXPublish(ctx, a.repoID, true); err != nil {
		t.Fatalf("SetVEXPublish: %v", err)
	}

	if !a.state(t).VEXPublish {
		t.Fatal("RepoState.VEXPublish is false after SetVEXPublish(true)")
	}

	status, hdr, body := engGet(t, a.srv.URL, engVEXPath, auth)
	if status != http.StatusOK || string(body) != string(pub.doc) || hdr.Get("Content-Type") != "application/json" {
		t.Fatalf("published: %d %q %q", status, hdr.Get("Content-Type"), body)
	}

	engEqual(t, "publisher calls", pub.asked(), []string{a.repoID + "/raw"})

	if status, _, _ := engGet(t, a.srv.URL, engVEXPath, ""); status != http.StatusUnauthorized {
		t.Fatalf("without token: %d", status)
	}

	pub.setErr(errors.New("database down"))

	status, _, body = engGet(t, a.srv.URL, engVEXPath, auth)
	if status != http.StatusInternalServerError || engErrCode(t, body) != codeInternal {
		t.Fatalf("publisher error: %d %s", status, body)
	}

	if err := a.store.SetVEXPublish(ctx, a.repoID, false); err != nil {
		t.Fatalf("SetVEXPublish(false): %v", err)
	}

	status, _, _ = engGet(t, a.srv.URL, engVEXPath, auth)
	if status != http.StatusNotFound {
		t.Fatalf("unpublished again: %d", status)
	}

	plain := seedRepo(t, a.db, "site-a-plain", "raw")
	if err := a.store.SetVEXPublish(ctx, plain, true); !errors.Is(err, ErrNotFederated) {
		t.Fatalf("SetVEXPublish on a repository that is not federated: %v", err)
	}
}

func TestPeerVEXTrustIsValidated(t *testing.T) {
	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")
	ctx := context.Background()

	_, err := b.store.CreatePeer(ctx, NewPeer{RepoID: b.repoID, Name: "bad", BaseURL: a.srv.URL, Token: a.token, VEXTrust: "always"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("CreatePeer with invalid vex_trust: %v", err)
	}

	p := b.addPeer(t, a)
	if p.VEXTrust != VEXTrustIgnore || p.VEXSyncedAt != nil || p.VEXError != "" {
		t.Fatalf("new peer VEX state = %q %v %q", p.VEXTrust, p.VEXSyncedAt, p.VEXError)
	}

	bad := "always"
	if _, err := b.store.UpdatePeer(ctx, b.repoID, p.ID, PeerPatch{VEXTrust: &bad}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("UpdatePeer with invalid vex_trust: %v", err)
	}

	apply := VEXTrustApply

	got, err := b.store.UpdatePeer(ctx, b.repoID, p.ID, PeerPatch{VEXTrust: &apply})
	if err != nil || got.VEXTrust != VEXTrustApply {
		t.Fatalf("UpdatePeer vex_trust apply: %v, %+v", err, got)
	}
}

// vexPair is site b pulling site a, which publishes its VEX decisions.
func vexPair(t *testing.T) (*engSite, *engSite, *fakeVEXReceiver, *Peer) {
	t.Helper()

	a, b := newEngSite(t, "site-a", "raw"), newEngSite(t, "site-b", "raw")
	a.fed.WithVEX(&fakeVEXPublisher{doc: []byte(`{"statements":[]}`)})

	if err := a.store.SetVEXPublish(context.Background(), a.repoID, true); err != nil {
		t.Fatalf("SetVEXPublish: %v", err)
	}

	recv := &fakeVEXReceiver{}
	b.opts.VEX = recv
	b.newEngine(t)

	a.put(t, "f", "payload")

	return a, b, recv, b.addPeer(t, a)
}

func setVEXTrust(t *testing.T, s *engSite, p *Peer, trust string) {
	t.Helper()

	if _, err := s.store.UpdatePeer(context.Background(), s.repoID, p.ID, PeerPatch{VEXTrust: &trust}); err != nil {
		t.Fatalf("set vex_trust %s: %v", trust, err)
	}
}

func TestEnginePullFetchesPeerVEXHourlyWhenTrusted(t *testing.T) {
	a, b, recv, p := vexPair(t)

	b.mustPull(t, p)
	b.mustRead(t, "f", "payload")

	if recv.count() != 0 || a.count(engVEXPath, nil) != 0 || b.peer(t, p).VEXSyncedAt != nil {
		t.Fatalf("ignore: VEX fetched (%d receipts, %d requests)", recv.count(), a.count(engVEXPath, nil))
	}

	for i, trust := range []string{VEXTrustSuggest, VEXTrustApply} {
		setVEXTrust(t, b, p, trust)

		b.mustPull(t, p)

		if recv.count() != i+1 {
			t.Fatalf("%s: %d receipts after the first pull, want %d", trust, recv.count(), i+1)
		}

		got := recv.all()[i]
		if got.repoID != b.repoID || got.format != "raw" || got.peerID != p.ID || got.doc != `{"statements":[]}` {
			t.Fatalf("%s: receipt = %+v", trust, got)
		}

		peer := b.peer(t, p)
		if peer.VEXSyncedAt == nil || peer.VEXError != "" {
			t.Fatalf("%s: VEX state after sync = %v %q", trust, peer.VEXSyncedAt, peer.VEXError)
		}

		// Within the hour another pull does not fetch again.
		b.mustPull(t, p)

		if recv.count() != i+1 {
			t.Fatalf("%s: fetched again within the hour (%d receipts)", trust, recv.count())
		}
	}

	b.exec(t, `UPDATE federation_peers SET vex_synced_at = now() - interval '61 minutes' WHERE id = $1::uuid`, p.ID)
	b.mustPull(t, p)

	if recv.count() != 3 {
		t.Fatalf("not fetched again after an hour (%d receipts)", recv.count())
	}
}

func TestEngineVEXFailuresAreRecordedWithoutFailingThePull(t *testing.T) {
	a, b, recv, p := vexPair(t)
	setVEXTrust(t, b, p, VEXTrustSuggest)

	recv.setErr(errors.New("pq: relation vex_suggestions does not exist"))
	b.mustPull(t, p)
	b.mustRead(t, "f", "payload")

	peer := b.peer(t, p)
	if peer.VEXError != ReasonInternal || peer.VEXSyncedAt == nil || peer.LastError != "" || peer.State() != PeerOK {
		t.Fatalf("receiver error: vex_error %q, synced %v, last_error %q, state %s",
			peer.VEXError, peer.VEXSyncedAt, peer.LastError, peer.State())
	}

	recv.setErr(nil)

	if err := a.store.SetVEXPublish(context.Background(), a.repoID, false); err != nil {
		t.Fatalf("SetVEXPublish(false): %v", err)
	}

	b.exec(t, `UPDATE federation_peers SET vex_synced_at = NULL WHERE id = $1::uuid`, p.ID)
	b.mustPull(t, p)

	peer = b.peer(t, p)
	if peer.VEXError != vexNotPublished || peer.LastError != "" || peer.State() != PeerOK {
		t.Fatalf("not published: vex_error %q, last_error %q, state %s", peer.VEXError, peer.LastError, peer.State())
	}

	if err := a.store.SetVEXPublish(context.Background(), a.repoID, true); err != nil {
		t.Fatalf("SetVEXPublish(true): %v", err)
	}

	b.exec(t, `UPDATE federation_peers SET vex_synced_at = NULL WHERE id = $1::uuid`, p.ID)
	b.mustPull(t, p)

	if peer = b.peer(t, p); peer.VEXError != "" {
		t.Fatalf("vex_error after a clean sync = %q", peer.VEXError)
	}
}
