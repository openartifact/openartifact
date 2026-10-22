// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/format/base"
)

// assertDocGateBounds checks that serving path reads its upstream document
// only while holding one of the bounded in-memory slots: with every slot taken
// a request whose context ends writes nothing, and it succeeds again once a
// slot is free. path must already be cached (primed) so no fetch is involved.
func assertDocGateBounds(t *testing.T, f *fixture, path string) {
	t.Helper()

	if w := f.do(http.MethodGet, path); w.Code != http.StatusOK {
		t.Fatalf("priming %s: status %d: %s", path, w.Code, w.Body.String())
	}

	f.svc.packuments = base.NewGate(1)
	if err := f.svc.packuments.Acquire(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	f.svc.Serve(w, req, f.repo, path)

	if w.Body.Len() != 0 {
		t.Errorf("%s: rendered %d bytes with every slot taken", path, w.Body.Len())
	}

	f.svc.packuments.Release()

	if w := f.do(http.MethodGet, path); w.Code != http.StatusOK {
		t.Errorf("%s after release: status %d", path, w.Code)
	}

	// Every slot must be free again: a leaked one would block this forever.
	if err := f.svc.packuments.Acquire(t.Context()); err != nil {
		t.Fatal(err)
	}

	f.svc.packuments.Release()
}

func TestPyPIProxy_PageRenderWaitsForASlot(t *testing.T) {
	f := newPyPIFixture(t, &pypiUpstream{page: htmlPage(sum(wheelBody))})

	assertDocGateBounds(t, f, "simple/requests/")
	assertDocGateBounds(t, f, "simple/")
}

func TestHelmProxy_IndexRenderWaitsForASlot(t *testing.T) {
	f, _ := helmFixture(t)

	assertDocGateBounds(t, f, "index.yaml")
}

func TestNuGetProxy_DocumentRenderWaitsForASlot(t *testing.T) {
	f, _ := nugetFixture(t)

	assertDocGateBounds(t, f, "v3/registration/lib.a/index.json")
	assertDocGateBounds(t, f, "v3/package/lib.a/index.json")
}
