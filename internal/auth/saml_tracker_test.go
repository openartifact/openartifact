// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/crewjam/saml"

	"github.com/openartifact/openartifact/internal/database/databasetest"
)

func TestSAMLRequestTracker_RoundTrip(t *testing.T) {
	key := []byte("tracker-key")
	now := time.Now()

	v := signSAMLRequestID(key, "okta", "id-123", now.Add(time.Minute))

	got, err := verifySAMLRequestID(key, "okta", v, now)
	if err != nil || got != "id-123" {
		t.Fatalf("expected id-123, got %q (%v)", got, err)
	}
}

func TestSAMLRequestTracker_Rejects(t *testing.T) {
	key := []byte("tracker-key")
	now := time.Now()
	good := signSAMLRequestID(key, "okta", "id-123", now.Add(time.Minute))

	cases := map[string]struct {
		key   []byte
		slug  string
		value string
		now   time.Time
	}{
		"wrong key":   {[]byte("other-key"), "okta", good, now},
		"other slug":  {key, "azure", good, now},
		"expired":     {key, "okta", good, now.Add(2 * time.Minute)},
		"tampered":    {key, "okta", good[:len(good)-1] + "x", now},
		"no mac":      {key, "okta", "abc", now},
		"empty":       {key, "okta", "", now},
		"empty id":    {key, "okta", signSAMLRequestID(key, "okta", "", now.Add(time.Minute)), now},
		"unsigned id": {key, "okta", "aWQtMTIzfDk5OTk5OTk5OTk", now},
	}

	for name, tc := range cases {
		if _, err := verifySAMLRequestID(tc.key, tc.slug, tc.value, tc.now); !errors.Is(err, ErrSAMLRequestUntracked) {
			t.Errorf("%s: expected ErrSAMLRequestUntracked, got %v", name, err)
		}
	}
}

type assertionDB struct {
	databasetest.BaseMockDB

	mu   sync.Mutex
	seen map[string]bool
	exp  time.Time
}

func (d *assertionDB) ConsumeSAMLAssertion(_ context.Context, slug, id string, expiresAt time.Time) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.seen == nil {
		d.seen = map[string]bool{}
	}

	d.exp = expiresAt

	k := slug + "|" + id
	if d.seen[k] {
		return false, nil
	}

	d.seen[k] = true

	return true, nil
}

func TestSAMLConsumeAssertion_ReplayRejected(t *testing.T) {
	db := &assertionDB{}
	p := &SAMLProvider{Slug: "okta"}
	a := &saml.Assertion{ID: "assertion-1", Conditions: &saml.Conditions{NotOnOrAfter: time.Now().Add(5 * time.Minute)}}

	if err := p.consumeAssertion(context.Background(), db, a); err != nil {
		t.Fatalf("first use must succeed: %v", err)
	}

	if err := p.consumeAssertion(context.Background(), db, a); !errors.Is(err, ErrSAMLReplay) {
		t.Fatalf("second use must be ErrSAMLReplay, got %v", err)
	}

	// The replay entry must outlive the assertion's validity plus clock skew.
	if !db.exp.After(a.Conditions.NotOnOrAfter.Add(saml.MaxClockSkew)) {
		t.Errorf("replay retention %v must exceed NotOnOrAfter+skew", db.exp)
	}
}

func TestSAMLConsumeAssertion_RequiresID(t *testing.T) {
	p := &SAMLProvider{Slug: "okta"}

	if err := p.consumeAssertion(context.Background(), &assertionDB{}, &saml.Assertion{}); !errors.Is(err, ErrSAMLReplay) {
		t.Fatalf("an assertion without an ID cannot be replay-checked and must be rejected, got %v", err)
	}
}
