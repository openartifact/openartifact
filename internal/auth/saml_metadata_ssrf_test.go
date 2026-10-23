// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/netutil"
)

func TestFetchIDPMetadataFromURL_LoopbackPolicy(t *testing.T) {
	var hits int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++

		_, _ = w.Write([]byte("not metadata"))
	}))
	defer srv.Close()

	t.Setenv(netutil.EnvAllowPrivate, "false")

	fresh := func() *idpMetadataCache { return newIDPMetadataCache(time.Now, downloadIDPMetadata) }

	_, err := fresh().fromURL(context.Background(), srv.URL, time.Hour, nil)
	if !errors.Is(err, ErrIDPMetadataUnavailable) {
		t.Fatalf("expected ErrIDPMetadataUnavailable, got %v", err)
	}

	if hits != 0 {
		t.Fatalf("loopback metadata URL was contacted %d times", hits)
	}

	t.Setenv(netutil.EnvAllowPrivate, "true")

	_, _ = fresh().fromURL(context.Background(), srv.URL, time.Hour, nil)

	if hits != 1 {
		t.Fatalf("expected request to reach loopback server when allowed, hits=%d", hits)
	}
}
