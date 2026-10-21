// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/netutil"
)

func TestListModelsRejectsLoopback(t *testing.T) {
	t.Setenv(netutil.EnvAllowPrivate, "")

	var hit bool

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()

	_, err := NewClient(Config{Provider: "openai", BaseURL: srv.URL}).ListModels(context.Background())
	if !errors.Is(err, netutil.ErrBlockedAddress) {
		t.Fatalf("want ErrBlockedAddress, got %v", err)
	}

	if hit {
		t.Fatal("request reached loopback server")
	}
}

func TestListModelsAllowsLoopbackWhenEnabled(t *testing.T) {
	t.Setenv(netutil.EnvAllowPrivate, "true")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"b"},{"id":"a"}]}`))
	}))
	defer srv.Close()

	models, err := NewClient(Config{Provider: "ollama", BaseURL: srv.URL}).ListModels(context.Background())
	if err != nil || len(models) != 2 || models[0] != "a" {
		t.Fatalf("models=%v err=%v", models, err)
	}
}
