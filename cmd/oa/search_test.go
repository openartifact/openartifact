// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	orig := os.Stdout

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	os.Stdout = w

	defer func() { os.Stdout = orig }()

	fn()

	_ = w.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	return string(out)
}

func TestSearchPrintsRepositoriesBeforePackages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/search" || r.URL.Query().Get("q") != "ui-kit" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"packages":[{"name":"ui-kit-pkg","repository":"npm-main","version":"1.0.0","format":"npm"}],` +
			`"repositories":[{"name":"ui-kit","format":"npm","description":"UI components"},` +
			`{"name":"ui-kit-deb","format":"deb","description":"debs"}]}`))
	}))
	defer srv.Close()

	client := newClient(srv.URL, "")

	table := captureStdout(t, func() { handleSearch([]string{"ui-kit"}, client, "table") })

	ri := strings.Index(table, "ui-kit  ")
	pi := strings.Index(table, "ui-kit-pkg")

	if ri < 0 || pi < 0 || ri > pi || !strings.Contains(table, "UI components") {
		t.Fatalf("repositories must precede packages:\n%s", table)
	}

	filtered := captureStdout(t, func() { handleSearch([]string{"--fmt", "deb", "ui-kit"}, client, "table") })
	if strings.Contains(filtered, "UI components") || !strings.Contains(filtered, "ui-kit-deb") {
		t.Fatalf("format filter not applied:\n%s", filtered)
	}

	limited := captureStdout(t, func() { handleSearch([]string{"--limit", "1", "ui-kit"}, client, "table") })
	if strings.Contains(limited, "ui-kit-deb") {
		t.Fatalf("limit not applied:\n%s", limited)
	}

	js := captureStdout(t, func() { handleSearch([]string{"ui-kit"}, client, "json") })
	if !strings.Contains(js, `"repositories"`) || !strings.Contains(js, `"results"`) || strings.Index(js, `"repositories"`) > strings.Index(js, `"results"`) {
		t.Fatalf("json output: %s", js)
	}
}
