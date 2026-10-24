// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ciJSON runs `oa --format json <args>` as the holder of token in dir and
// returns the decoded stdout, the exit code and stderr.
func (s *declServer) ciJSON(t *testing.T, token, dir string, args ...string) (map[string]any, int, string) {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), s.oaBin, append([]string{"--format", "json"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+s.home, "XDG_CONFIG_HOME="+s.home, "OA_SERVER="+s.url, "OA_TOKEN="+token)

	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	code := 0

	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run oa %v: %v", args, err)
		}

		code = ee.ExitCode()
	}

	t.Logf("$ oa --format json %v -> exit %d\n%s%s", args, code, stdout.String(), stderr.String())

	var out map[string]any

	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatalf("oa %v: stdout is not JSON: %v\n%s", args, err, stdout.String())
		}
	}

	return out, code, stderr.String()
}

// ciOK runs an oa command that must succeed.
func (s *declServer) ciOK(t *testing.T, token, dir string, args ...string) map[string]any {
	t.Helper()

	out, code, stderr := s.ciJSON(t, token, dir, args...)
	if code != 0 {
		t.Fatalf("oa %v: exit %d: %s", args, code, stderr)
	}

	return out
}

// statusAs sends a bare request with token and returns the HTTP status.
func (s *declServer) statusAs(t *testing.T, token, method, path string, body []byte) (int, []byte) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), method, s.url+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	data, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, data
}

func jsonField[T any](t *testing.T, raw []byte, key string) T {
	t.Helper()

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}

	v, ok := m[key].(T)
	if !ok {
		t.Fatalf("field %q of %s has the wrong type", key, raw)
	}

	return v
}

// ciUser creates a user holding a deploy token that may read (and, with write,
// write) the repositories matched by filter, and nothing else.
func (s *declServer) ciUser(t *testing.T, name, filter string, actions ...string) string {
	t.Helper()

	userID := jsonField[string](t, s.api(t, http.MethodPost, "/api/v1/users",
		map[string]any{"username": name, "email": name + "@example.com", "password": "ci-password-12345"}, http.StatusCreated), "id")

	targetID := jsonField[string](t, s.api(t, http.MethodPost, "/api/v1/permission-targets",
		map[string]any{"name": name + "-target", "repository_filter": filter, "path_pattern": ".*", "actions": actions}, http.StatusCreated), "id")

	s.api(t, http.MethodPost, "/api/v1/permission-targets/"+targetID+"/assignments",
		map[string]any{"principal_type": "user", "principal_id": userID}, http.StatusNoContent)

	return jsonField[string](t, s.sess.do(t, http.MethodPost, "/api/v1/users/"+userID+"/tokens",
		map[string]any{"name": name, "kind": "deploy"}, http.StatusOK, http.StatusCreated), "token")
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))

	return hex.EncodeToString(h[:])
}

func items(t *testing.T, out map[string]any) map[string]string {
	t.Helper()

	list, _ := out["items"].([]any)
	got := make(map[string]string, len(list))

	for _, it := range list {
		m, _ := it.(map[string]any)
		path, _ := m["path"].(string)
		digest, _ := m["sha256"].(string)
		got[path] = digest
	}

	return got
}

// ciFixture is a server with the operator's setup done and three deploy tokens:
// ci (read+write on both repositories), reader (read on the build repository)
// and outsider (access to neither).
type ciFixture struct {
	*declServer

	ci, reader, outsider string
	ws                   string
	files                map[string]string // workspace path -> content
	want                 map[string]string // remote path -> sha256 of the build under test
}

func newCIFixture(t *testing.T) *ciFixture {
	t.Helper()

	f := &ciFixture{
		declServer: startDeclServer(t),
		ws:         t.TempDir(),
		files:      map[string]string{"dist/app.tar.gz": "app v1", "dist/docs.txt": "docs v1", "dist/sub/extra.bin": "extra"},
		want:       map[string]string{},
	}

	var repoID [2]string

	for i, name := range []string{"ci-dev", "ci-prod"} {
		f.api(t, http.MethodPost, "/api/v1/repositories", map[string]any{"name": name, "format": "raw"}, http.StatusCreated)
		repoID[i] = jsonField[string](t, f.api(t, http.MethodGet, "/api/v1/repository/"+name, nil, http.StatusOK), "id")
	}

	f.api(t, http.MethodPost, "/api/v1/pipelines", map[string]any{
		"name":   "release",
		"stages": []map[string]any{{"name": "dev", "repo_id": repoID[0]}, {"name": "prod", "repo_id": repoID[1]}},
	}, http.StatusCreated)

	f.ci = f.ciUser(t, "ci-bot", "^ci-(dev|prod)$", "read", "write")
	f.reader = f.ciUser(t, "ci-reader", "^ci-dev$", "read")
	f.outsider = f.ciUser(t, "ci-outsider", "^unrelated$", "read", "write")

	for name, content := range f.files {
		f.write(t, name, content)
		f.want["builds/42/"+name] = sha256Hex(content)
	}

	f.write(t, "other.txt", "an older build")

	return f
}

// write creates a workspace file.
func (f *ciFixture) write(t *testing.T, name, content string) {
	t.Helper()

	p := filepath.Join(f.ws, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// upload runs the two builds and checks the report, the properties and the stored bytes.
func (f *ciFixture) upload(t *testing.T) {
	t.Helper()

	up := f.ciOK(t, f.ci, f.ws, "upload", "ci-dev", "dist/**", "--target-path", "builds/42/",
		"--build-name", "shop/main", "--build-number", "42", "--build-url", "http://jenkins/job/shop/42",
		"--vcs-revision", "1a2b3c4", "--property", "team=core")

	uploaded, _ := up["uploaded"].([]any)
	if len(uploaded) != len(f.files) {
		t.Fatalf("uploaded %d files, want %d: %v", len(uploaded), len(f.files), up)
	}

	var firstID string

	for _, u := range uploaded {
		m, _ := u.(map[string]any)
		path, _ := m["path"].(string)

		if m["repo"] != "ci-dev" || m["sha256"] != f.want[path] || m["url"] == "" || m["id"] == "" {
			t.Fatalf("uploaded entry %v, want sha %q", m, f.want[path])
		}

		firstID, _ = m["id"].(string)
	}

	// An older build that must not leak into the bundle.
	f.ciOK(t, f.ci, f.ws, "upload", "ci-dev", "other.txt", "--target-path", "builds/41/", "--build-name", "shop/main", "--build-number", "41")

	code, body := f.statusAs(t, f.ci, http.MethodGet, "/api/v1/repositories/ci-dev/artifacts/"+firstID+"/properties", nil)
	if code != http.StatusOK || !bytes.Contains(body, []byte("shop/main")) || !bytes.Contains(body, []byte("1a2b3c4")) || !bytes.Contains(body, []byte("core")) {
		t.Fatalf("properties: HTTP %d %s", code, body)
	}

	code, body = f.statusAs(t, f.ci, http.MethodGet, "/repos/raw/ci-dev/builds/42/dist/docs.txt", nil)
	if code != http.StatusOK || string(body) != "docs v1" {
		t.Fatalf("download: HTTP %d %q", code, body)
	}

	if _, code, _ := f.ciJSON(t, f.ci, f.ws, "upload", "ci-dev", "dist/app.tar.gz", "--property", "novalue"); code == 0 {
		t.Fatal("a malformed --property must fail")
	}

	if out, code, _ := f.ciJSON(t, f.outsider, f.ws, "upload", "ci-dev", "other.txt", "--flat"); code == 0 || out["error"] == nil {
		t.Fatalf("upload without write access must fail with an error: %v", out)
	}
}

// draftBundle creates the bundle and fills it from the build.
func (f *ciFixture) draftBundle(t *testing.T) {
	t.Helper()

	if _, code, _ := f.ciJSON(t, f.ci, f.ws, "bundle", "create", "shop", "0.9.0", "--pipeline", "missing"); code == 0 {
		t.Fatal("an unknown pipeline must fail")
	}

	created := f.ciOK(t, f.ci, f.ws, "bundle", "create", "shop", "1.0.0", "--description", "shop 1.0.0", "--pipeline", "release")
	if created["state"] != "draft" || created["bundle"] != "shop" || created["created_by"] != "ci-bot" {
		t.Fatalf("create = %v", created)
	}

	added := f.ciOK(t, f.ci, f.ws, "bundle", "add", "shop", "1.0.0", "--from-build", "shop/main/42")
	if added["added"] != float64(len(f.files)) {
		t.Fatalf("added = %v", added["added"])
	}

	got := items(t, added)
	if len(got) != len(f.want) {
		t.Fatalf("items = %v, want %v", got, f.want)
	}

	for path, digest := range f.want {
		if got[path] != digest {
			t.Fatalf("item %s = %q, want %q", path, got[path], digest)
		}
	}

	// A build that produced nothing is an error, not an empty bundle.
	if _, code, _ := f.ciJSON(t, f.ci, f.ws, "bundle", "add", "shop", "1.0.0", "--from-build", "shop/main/99"); code == 0 {
		t.Fatal("--from-build without a match must fail")
	}

	// The reader sees a bundle whose sources it can read but cannot change it; the outsider does not see it.
	if out := f.ciOK(t, f.reader, f.ws, "bundle", "show", "shop", "1.0.0"); out["state"] != "draft" {
		t.Fatalf("reader show = %v", out)
	}

	if code, _ := f.statusAs(t, f.reader, http.MethodPost, "/api/v1/bundles/shop/1.0.0/seal", nil); code != http.StatusForbidden {
		t.Fatalf("seal by a non-owner = HTTP %d, want 403", code)
	}

	if code, _ := f.statusAs(t, f.outsider, http.MethodGet, "/api/v1/bundles/shop/1.0.0", nil); code != http.StatusNotFound {
		t.Fatalf("show by an outsider = HTTP %d, want 404", code)
	}
}

// sealAndPromote freezes the bundle and moves it to the prod stage.
func (f *ciFixture) sealAndPromote(t *testing.T) {
	t.Helper()

	if _, code, _ := f.ciJSON(t, f.ci, f.ws, "promote", "shop", "1.0.0", "--stage", "prod"); code == 0 {
		t.Fatal("promoting a draft must fail")
	}

	sealed := f.ciOK(t, f.ci, f.ws, "bundle", "seal", "shop", "1.0.0")
	if sealed["state"] != "sealed" || sealed["sealed_at"] == nil {
		t.Fatalf("seal = %v", sealed)
	}

	// Promotion needs write on the target: the reader cannot even see the target repository.
	if code, _ := f.statusAs(t, f.reader, http.MethodPost, "/api/v1/bundles/shop/1.0.0/promote", []byte(`{"to_stage":"prod"}`)); code != http.StatusNotFound {
		t.Fatalf("promote by a token without access to the target = HTTP %d, want 404", code)
	}

	promoted := f.ciOK(t, f.ci, f.ws, "promote", "shop", "1.0.0", "--stage", "prod", "--notes", "from build 42")

	pr, _ := promoted["promoted"].(map[string]any)
	if promoted["current_stage"] != "prod" || pr["stage"] != "prod" || pr["target_repo"] != "ci-prod" ||
		pr["items"] != float64(len(f.files)) || pr["copied"] != float64(len(f.files)) || pr["existing"] != float64(0) {
		t.Fatalf("promote = %v", promoted)
	}

	code, body := f.statusAs(t, f.ci, http.MethodGet, "/repos/raw/ci-prod/builds/42/dist/app.tar.gz", nil)
	if code != http.StatusOK || string(body) != "app v1" {
		t.Fatalf("promoted download: HTTP %d %q", code, body)
	}

	shown := f.ciOK(t, f.ci, f.ws, "bundle", "show", "shop", "1.0.0")
	if shown["current_stage"] != "prod" || len(items(t, shown)) != len(f.files) {
		t.Fatalf("show = %v", shown)
	}
}

// administrative checks what a CI token must not be able to do.
func (f *ciFixture) administrative(t *testing.T) {
	t.Helper()

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/bundles"},
		{http.MethodGet, "/api/v1/pipelines"},
		{http.MethodPost, "/api/v1/repositories"},
		{http.MethodGet, "/api/v1/users"},
	} {
		if code, _ := f.statusAs(t, f.ci, c.method, c.path, []byte(`{}`)); code != http.StatusForbidden {
			t.Errorf("%s %s with a CI token = HTTP %d, want 403", c.method, c.path, code)
		}
	}
}

// TestCICLIEndToEnd drives the CI commands of oa against a real server with a
// non-admin deploy token that holds read and write on two repositories only:
// upload with build properties, a bundle assembled from the build, seal and
// promote. The operator-side setup (repositories, pipeline, token) is the only
// part that needs an administrator.
func TestCICLIEndToEnd(t *testing.T) {
	f := newCIFixture(t)

	if out := f.ciOK(t, f.ci, f.ws, "version"); out["version"] == "" || out["os"] == "" {
		t.Fatalf("version = %v", out)
	}

	f.upload(t)
	f.draftBundle(t)
	f.sealAndPromote(t)
	f.administrative(t)
}
