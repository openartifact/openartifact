// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"crypto/sha256"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// uploadLeases is the lease, position and state columns of a fake database's
// docker_uploads table, with the semantics of the real statements: a lease is
// taken only while none is live, and the position moves only for the holder of
// the lease and from the offset it expects.
type uploadLeases struct {
	leaseMu sync.Mutex
	leases  map[string]leaseHold
}

type leaseHold struct {
	token string
	until time.Time
}

func (l *uploadLeases) acquire(key, token string, ttl time.Duration) bool {
	l.leaseMu.Lock()
	defer l.leaseMu.Unlock()

	if h, ok := l.leases[key]; ok && h.until.After(time.Now()) {
		return false
	}

	if l.leases == nil {
		l.leases = map[string]leaseHold{}
	}

	l.leases[key] = leaseHold{token: token, until: time.Now().Add(ttl)}

	return true
}

func (l *uploadLeases) renew(key, token string, ttl time.Duration) bool {
	l.leaseMu.Lock()
	defer l.leaseMu.Unlock()

	if h, ok := l.leases[key]; !ok || h.token != token {
		return false
	}

	l.leases[key] = leaseHold{token: token, until: time.Now().Add(ttl)}

	return true
}

func (l *uploadLeases) release(key, token string) {
	l.leaseMu.Lock()
	defer l.leaseMu.Unlock()

	if h, ok := l.leases[key]; ok && h.token == token {
		delete(l.leases, key)
	}
}

func (l *uploadLeases) drop(key string) {
	l.leaseMu.Lock()
	defer l.leaseMu.Unlock()

	delete(l.leases, key)
}

// advance moves u like AdvanceDockerUpload: only for the lease holder and from
// oldOffset.
func (l *uploadLeases) advance(
	key string,
	u *models.DockerUpload,
	token string,
	oldOffset, newOffset int64,
	hashState []byte,
	stageState string,
	ttl time.Duration,
	keepLease bool,
) bool {
	l.leaseMu.Lock()
	defer l.leaseMu.Unlock()

	if h, ok := l.leases[key]; !ok || h.token != token || u.Offset != oldOffset {
		return false
	}

	u.Offset = newOffset
	u.HashState = slices.Clone(hashState)
	u.StageState = stageState

	if keepLease {
		l.leases[key] = leaseHold{token: token, until: time.Now().Add(ttl)}
	} else {
		delete(l.leases, key)
	}

	return true
}

// hold takes the lease for a request the test plays by hand and returns its
// token, so a request on the session meets a live lease.
func (l *uploadLeases) hold(key string) string {
	token := "held-by-test"
	l.acquire(key, token, time.Hour)

	return token
}

// hashStateOf is the digest state a session records after data was appended.
func hashStateOf(t *testing.T, data string) []byte {
	t.Helper()

	h := sha256.New()
	_, _ = h.Write([]byte(data))

	state, err := digestState(h)
	if err != nil {
		t.Fatal(err)
	}

	return state
}

// uploadObjects lists the ids of every upload the handler's upload store holds.
func uploadObjects(t *testing.T, h *HTTPHandler) []string {
	t.Helper()

	var ids []string

	err := h.uploads.WalkUploads(context.Background(), time.Now().Add(time.Hour), func(batch []string) error {
		ids = append(ids, batch...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk uploads: %v", err)
	}

	return ids
}
