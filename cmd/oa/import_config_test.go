// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/declarative"
)

// fakeConfigSource serves what the exporter reads from a 0.14 instance: two
// users (one of them the seeded admin), three repositories (hosted with a
// quota and a cleanup policy, a proxy, a group) and a webhook with a secret.
// Every other list is empty.
func fakeConfigSource(t *testing.T) *httptest.Server {
	t.Helper()

	answers := map[string]any{
		"/api/v1/users": map[string]any{"total": 2, "data": []map[string]any{
			{"id": "u1", "username": "admin", "email": "admin@example.com"},
			{"id": "u2", "username": "alice", "email": "alice@example.com", "full_name": "Alice Example"},
		}},
		"/api/v1/repositories": map[string]any{"total": 3, "repositories": []map[string]any{
			{"id": "r1", "name": "releases", "format": "maven", "repo_type": "hosted", "public": true, "immutable_releases": true},
			{
				"id": "r2", "name": "npm-mirror", "format": "npm", "repo_type": "proxy", "upstream_url": "https://registry.npmjs.org",
				"upstream_username": "bot", "cache_ttl_seconds": 3600,
			},
			{"id": "r3", "name": "all-npm", "format": "npm", "repo_type": "group"},
		}},
		"/api/v1/repositories/releases/quota": map[string]any{
			"max_size_bytes":         1 << 30,
			"max_artifact_count":     0,
			"warn_threshold_percent": 80,
		},
		"/api/v1/repositories/releases/cleanup-policies": map[string]any{"policies": []map[string]any{
			{"id": "p1", "name": "keep-ten", "enabled": true, "keep_last_n": 10, "schedule_cron": "0 3 * * *"},
		}},
		"/api/v1/repositories/all-npm/members": map[string]any{"members": []map[string]any{{"name": "npm-mirror", "priority": 1}}},
		"/api/v1/webhooks": map[string]any{"webhooks": []map[string]any{{
			"id": "w1", "name": "notify", "url": "https://hooks.example.com/oa", "enabled": true,
			"events": []string{"artifact.uploaded"}, "content_type": "application/json", "payload_format": "openartifact",
			"has_secret": true, "ssl_verify": true, "timeout_seconds": 10, "max_retries": 3,
		}}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+oaSrcToken {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		if a, ok := answers[r.URL.Path]; ok {
			_ = json.NewEncoder(w).Encode(a)
			return
		}

		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	return srv
}

// configGolden are the manifests `oa import config` writes for fakeConfigSource.
//
//nolint:gochecknoglobals // golden files
var configGolden = map[string]string{
	"user/alice.yaml": `apiVersion: openartifact.io/v1
kind: User
metadata:
  name: alice
spec:
  disabled: false
  email: alice@example.com
  full_name: Alice Example
  must_change_password: true
`,
	"repository/releases.yaml": `apiVersion: openartifact.io/v1
kind: Repository
metadata:
  name: releases
spec:
  cleanup_policies:
    - enabled: true
      keep_last_n: 10
      name: keep-ten
      name_pattern: ""
      schedule_cron: 0 3 * * *
  description: ""
  format: maven
  immutable_releases: true
  public: true
  quota:
    max_artifact_count: 0
    max_size_bytes: 1073741824
    warn_threshold_percent: 80
  repo_type: hosted
`,
	"repository/npm-mirror.yaml": `apiVersion: openartifact.io/v1
kind: Repository
metadata:
  name: npm-mirror
spec:
  cache_ttl_seconds: 3600
  cleanup_policies: []
  description: ""
  format: npm
  immutable_releases: false
  public: false
  quota:
    max_artifact_count: 0
    max_size_bytes: 0
    warn_threshold_percent: 0
  repo_type: proxy
  upstream_url: https://registry.npmjs.org
  upstream_username: bot
`,
	"repository/all-npm.yaml": `apiVersion: openartifact.io/v1
kind: Repository
metadata:
  name: all-npm
spec:
  cleanup_policies: []
  description: ""
  format: npm
  immutable_releases: false
  member_repos:
    - npm-mirror
  merge_proxy_versions: false
  public: false
  quota:
    max_artifact_count: 0
    max_size_bytes: 0
    warn_threshold_percent: 0
  repo_type: group
`,
	"webhook/notify.yaml": `apiVersion: openartifact.io/v1
kind: Webhook
metadata:
  name: notify
spec:
  content_type: application/json
  enabled: true
  events:
    - artifact.uploaded
  max_retries: 3
  payload_format: openartifact
  secret:
    secretRef:
      env: OA_WEBHOOK_NOTIFY_SECRET
  ssl_verify: true
  timeout_seconds: 10
  url: https://hooks.example.com/oa
`,
}

func TestImportConfigWritesManifestsAndReport(t *testing.T) {
	srv := fakeConfigSource(t)
	dir := t.TempDir()

	rep, err := runImportConfig(context.Background(), importConfigOptions{
		SourceURL: srv.URL, Token: oaSrcToken, Dir: dir, HTTP: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}

	read := func(rel string) string {
		t.Helper()

		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}

		return string(b)
	}

	for rel, want := range configGolden {
		if got := read(rel); got != want {
			t.Errorf("%s:\n--- got\n%s--- want\n%s", rel, got, want)
		}
	}

	// the seeded admin is not exported; the manifests of the rest are there
	if _, err := os.Stat(filepath.Join(dir, "user", "admin.yaml")); err == nil {
		t.Error("the built-in admin user was exported")
	}

	for _, rel := range []string{"repository/releases.yaml", "repository/npm-mirror.yaml", "repository/all-npm.yaml", "webhook/notify.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}

	releases := read("repository/releases.yaml")
	for _, want := range []string{"max_size_bytes: 1073741824", "keep_last_n: 10", "immutable_releases: true", "schedule_cron: 0 3 * * *"} {
		if !strings.Contains(releases, want) {
			t.Errorf("repository/releases.yaml lacks %q:\n%s", want, releases)
		}
	}

	if got := read("repository/all-npm.yaml"); !strings.Contains(got, "- npm-mirror") {
		t.Errorf("group manifest lacks its member:\n%s", got)
	}

	webhook := read("webhook/notify.yaml")
	if !strings.Contains(webhook, "secretRef") {
		t.Errorf("the webhook secret is not a placeholder:\n%s", webhook)
	}

	// the output is a valid set of manifests: what `oa apply -f DIR -R` loads
	objs, err := declarative.Load(declarative.DefaultRegistry(), declarative.LoadOptions{Paths: []string{dir}, Recursive: true})
	if err != nil {
		t.Fatalf("the written manifests do not load: %v", err)
	}

	if len(objs) != 5 { // alice, three repositories, one webhook
		t.Errorf("%d manifests loaded, want 5", len(objs))
	}

	// the report
	status := map[string]string{}
	for _, e := range rep.Entries {
		status[e.Kind+"/"+e.Name] = e.Status
	}

	want := map[string]string{
		"User/alice":              mapApproximated, // no password
		"Repository/releases":     mapTranslated,
		"Repository/npm-mirror":   mapApproximated, // the upstream password is not readable
		"Repository/all-npm":      mapTranslated,
		"Webhook/notify":          mapApproximated, // the secret is a placeholder
		"Repository/doesnt-exist": "",
	}

	for k, w := range want {
		if status[k] != w {
			t.Errorf("%s = %q, want %q (entries %+v)", k, status[k], w, rep.Entries)
		}
	}

	if rep.Translated != 2 || rep.Approximated != 3 || rep.Skipped != 0 {
		t.Errorf("report counts = %d/%d/%d, want 2 translated, 3 approximated, 0 skipped", rep.Translated, rep.Approximated, rep.Skipped)
	}

	var onDisk mappingReport
	if err := json.Unmarshal([]byte(read(mappingReportFile)), &onDisk); err != nil {
		t.Fatalf("the report file is not JSON: %v", err)
	}

	if onDisk.Translated != rep.Translated || len(onDisk.OutOfScope) == 0 {
		t.Errorf("report file = %+v, want the report with its out-of-scope list", onDisk)
	}

	for _, item := range []string{"scan results", "audit log", "sessions", "tokens", "secrets"} {
		found := false

		for _, o := range onDisk.OutOfScope {
			found = found || strings.Contains(o.Item, item)
		}

		if !found {
			t.Errorf("the report does not state that %q is out of scope", item)
		}
	}

	var human bytes.Buffer

	rep.print(&human, dir)

	for _, want := range []string{"translated:", "approximated:", "approximated User/alice", "Webhook/notify", "Not carried over:", "oa apply -f"} {
		if !strings.Contains(human.String(), want) {
			t.Errorf("summary lacks %q:\n%s", want, human.String())
		}
	}
}

func TestImportConfigKindFilterAndUnauthorized(t *testing.T) {
	srv := fakeConfigSource(t)
	dir := t.TempDir()

	rep, err := runImportConfig(context.Background(), importConfigOptions{
		SourceURL: srv.URL, Token: oaSrcToken, Dir: dir, HTTP: srv.Client(), Kinds: []string{"Webhook"},
	})
	if err != nil || len(rep.Entries) != 1 || rep.Entries[0].Kind != "Webhook" {
		t.Errorf("--kind Webhook: %+v, %v", rep, err)
	}

	if _, err := runImportConfig(context.Background(), importConfigOptions{
		SourceURL: srv.URL, Token: "wrong", Dir: t.TempDir(), HTTP: srv.Client(),
	}); err == nil {
		t.Error("a wrong source token was accepted")
	}
}

func TestClassifyConfigDoc(t *testing.T) {
	doc := func(kind, name, spec string) declarative.ExportDoc {
		return declarative.ExportDoc{
			Kind: kind, Key: name,
			YAML: []byte("apiVersion: openartifact.io/v1\nkind: " + kind + "\nmetadata:\n  name: " + name + "\nspec:\n" + spec),
		}
	}

	cases := []struct {
		doc    declarative.ExportDoc
		status string
		reason string
	}{
		{doc("Repository", "r", "  format: maven\n  repo_type: hosted\n"), mapTranslated, "content is copied by `oa import`"},
		{doc("Repository", "p", "  format: npm\n  repo_type: proxy\n"), mapTranslated, "proxy cache is not copied"},
		{doc("Repository", "rpm", "  format: rpm\n  repo_type: hosted\n  sign_packages: true\n"), mapApproximated, "key of its own"},
		{doc("StorageTier", "cold", "  backend: s3-cold\n"), mapApproximated, `"s3-cold"`},
		{doc("StorageTier", "warm", "  rank: 2\n"), mapTranslated, ""},
		{
			doc("SSOProvider", "okta", "  client_secret:\n    secretRef:\n      env: OA_SSO_OKTA_CLIENT_SECRET\n"),
			mapApproximated,
			"OA_SSO_OKTA_CLIENT_SECRET",
		},
		{doc("Group", "devs", "  description: d\n"), mapTranslated, ""},
	}

	for _, c := range cases {
		e, err := classifyConfigDoc(c.doc)
		if err != nil {
			t.Fatal(err)
		}

		if e.Status != c.status || (c.reason != "" && !strings.Contains(strings.Join(e.Reasons, "; "), c.reason)) {
			t.Errorf("%s/%s = %q %v, want %q mentioning %q", c.doc.Kind, c.doc.Key, e.Status, e.Reasons, c.status, c.reason)
		}
	}
}

func TestParseImportConfigFlags(t *testing.T) {
	env := func(k string) string {
		if k == envSourceToken {
			return "tok"
		}

		return ""
	}

	opts, err := parseImportConfigFlags(
		[]string{
			"openartifact",
			"--source-url",
			"http://localhost:8080/",
			"--output",
			"out",
			"--kind",
			"Group",
		},
		env,
		strings.NewReader(""),
	)
	if err != nil || opts.SourceURL != "http://localhost:8080" || opts.Token != "tok" || opts.Dir != "out" || len(opts.Kinds) != 1 {
		t.Errorf("opts = %+v, %v", opts, err)
	}

	for _, args := range [][]string{
		{"nexus", "--source-url", "http://localhost:8080", "--output", "o"},
		{"--source-url", "http://localhost:8080"},
		{"--output", "o"},
		{"--source-url", "http://example.com", "--output", "o"}, // credentials over plain http
	} {
		if _, err := parseImportConfigFlags(args, env, strings.NewReader("")); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}

	_, err = parseImportConfigFlags(
		[]string{"--source-url", "http://localhost:8080", "--output", "o"},
		func(string) string { return "" },
		strings.NewReader(""),
	)
	if err == nil || !errors.Is(err, errImportConfigUsage) {
		t.Errorf("a missing token: err = %v, want a usage error", err)
	}
}
