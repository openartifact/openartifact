// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer safe for use as a process's stderr.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func repoRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	return filepath.Dir(wd)
}

func goBuild(t *testing.T, root, out, pkg string) {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", out, pkg)
	cmd.Dir = root

	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, b)
	}
}

func freePort(t *testing.T) int {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = l.Close() }()

	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("not a TCP address")
	}

	return addr.Port
}

// declServer is a real server process on a fresh database.
type declServer struct {
	url      string
	password string
	token    string
	sess     *session // admin cookie session, for the calls API tokens cannot make
	root     string
	oaBin    string
	home     string
	// dbName and dbURL locate the server's database on the TEST_DATABASE_URL server.
	dbName string
	dbURL  *url.URL
}

func startDeclServer(t *testing.T) *declServer {
	t.Helper()

	s, stderr := startServerProc(t, nil)
	s.password = waitBootstrapPassword(t, stderr)
	s.token = s.mintToken(t)

	return s
}

// startServerProc builds and starts a server on a fresh database with extraEnv
// added and waits until it is healthy. It does not log in.
func startServerProc(t *testing.T, extraEnv []string) (*declServer, *syncBuffer) {
	t.Helper()

	admin := setupTestDB(t)
	ctx := context.Background()
	dbName := fmt.Sprintf("oa_decl_%d", time.Now().UnixNano())

	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+dbName); err != nil {
		t.Fatalf("create database: %v", err)
	}

	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+dbName+` WITH (FORCE)`)
	})

	dsn, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}

	root := repoRoot(t)
	bin := t.TempDir()
	serverBin := filepath.Join(bin, "openartifact")
	oaBin := filepath.Join(bin, "oa")

	goBuild(t, root, serverBin, "./cmd/server")
	goBuild(t, root, oaBin, "./cmd/oa")

	port := freePort(t)
	pw, _ := dsn.User.Password()
	stderr := &syncBuffer{}

	cmd := exec.CommandContext(ctx, serverBin)
	cmd.Dir = t.TempDir()
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(),
		"SERVER_HOST=127.0.0.1", "SERVER_PORT="+strconv.Itoa(port),
		"DB_DRIVER=postgres", "DB_HOST="+dsn.Hostname(), "DB_PORT="+dsn.Port(),
		"DB_USER="+dsn.User.Username(), "DB_PASSWORD="+pw, "DB_NAME="+dbName, "DB_SSLMODE=disable",
		"STORAGE_BACKEND=filesystem", "STORAGE_PATH="+t.TempDir(),
		"OPENARTIFACT_BACKUP_DIR="+t.TempDir(),
		"LOG_LEVEL=error",
		// Webhook URLs are resolved by the server; the test points them at loopback.
		"OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true",
	)
	cmd.Env = append(cmd.Env, extraEnv...)

	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	s := &declServer{url: "http://127.0.0.1:" + strconv.Itoa(port), root: root, oaBin: oaBin, home: t.TempDir(), dbName: dbName, dbURL: dsn}
	s.waitHealthy(t, stderr)

	return s, stderr
}

// poll runs fn until it returns true or the deadline passes.
func poll(t *testing.T, what string, timeout time.Duration, fn func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for !fn() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(50 * time.Millisecond)
	}
}

func (s *declServer) waitHealthy(t *testing.T, stderr *syncBuffer) {
	t.Helper()

	poll(t, "server health\n"+stderr.String(), 2*time.Minute, func() bool {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, s.url+"/health", nil)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}

		_ = resp.Body.Close()

		return resp.StatusCode == http.StatusOK
	})
}

var bootstrapPasswordRe = regexp.MustCompile(`admin password: (\S+)`)

func waitBootstrapPassword(t *testing.T, stderr *syncBuffer) string {
	t.Helper()

	var pw string

	poll(t, "bootstrap password", 30*time.Second, func() bool {
		if m := bootstrapPasswordRe.FindStringSubmatch(stderr.String()); m != nil {
			pw = m[1]
			return true
		}

		return false
	})

	return pw
}

// session is a cookie session that echoes the CSRF cookie like the CLI does.
type session struct {
	c    *http.Client
	base string
}

func (s *session) do(t *testing.T, method, path string, body any, want ...int) []byte {
	t.Helper()

	var rdr io.Reader

	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, s.base+path, rdr)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	u, _ := url.Parse(s.base)
	for _, ck := range s.c.Jar.Cookies(u) {
		if ck.Name == "oa_csrf" {
			req.Header.Set("X-Csrf-Token", ck.Value)
		}
	}

	resp, err := s.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	data, _ := io.ReadAll(resp.Body)

	ok := false

	for _, w := range want {
		ok = ok || resp.StatusCode == w
	}

	if !ok {
		t.Fatalf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, data)
	}

	return data
}

func (s *declServer) mintToken(t *testing.T) string {
	t.Helper()

	jar, _ := cookiejar.New(nil)
	sess := &session{c: &http.Client{Jar: jar, Timeout: 30 * time.Second}, base: s.url}
	s.sess = sess

	var user struct {
		MustChangePassword bool `json:"must_change_password"`
	}

	data := sess.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "admin", "password": s.password}, http.StatusOK)
	_ = json.Unmarshal(data, &user)

	if user.MustChangePassword {
		next := s.password + "-Changed1!"
		sess.do(t, http.MethodPost, "/api/v1/auth/change-password",
			map[string]string{"current_password": s.password, "new_password": next}, http.StatusOK, http.StatusNoContent)
		sess.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "admin", "password": next}, http.StatusOK)
	}

	var tok struct {
		Token string `json:"token"`
	}

	data = sess.do(t, http.MethodPost, "/api/v1/auth/tokens", map[string]string{"name": "decl-test", "kind": "access"}, http.StatusOK, http.StatusCreated)
	_ = json.Unmarshal(data, &tok)

	if tok.Token == "" {
		t.Fatal("no token returned")
	}

	return tok.Token
}

// api calls the REST API with the admin token.
func (s *declServer) api(t *testing.T, method, path string, body any, want ...int) []byte {
	t.Helper()

	var rdr io.Reader

	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, s.url+path, rdr)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	data, _ := io.ReadAll(resp.Body)

	if slices.Contains(want, resp.StatusCode) {
		return data
	}

	t.Fatalf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, data)

	return nil
}

// oa runs the real CLI against the server and returns stdout, stderr and the
// exit code.
func (s *declServer) oa(t *testing.T, extraEnv []string, args ...string) (string, string, int) {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), s.oaBin, args...)
	cmd.Dir = s.root
	cmd.Env = append(os.Environ(),
		"HOME="+s.home, "XDG_CONFIG_HOME="+s.home,
		"OA_SERVER="+s.url, "OA_TOKEN="+s.token,
		"NPM_UPSTREAM_PASSWORD=upstream-pw", "AUDIT_WEBHOOK_SECRET=hook-secret", "OIDC_CLIENT_SECRET=oidc-secret",
	)
	cmd.Env = append(cmd.Env, extraEnv...)

	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	code := 0

	var ee *exec.ExitError

	switch {
	case errors.As(err, &ee):
		code = ee.ExitCode()
	case err != nil:
		t.Fatalf("run oa %v: %v", args, err)
	}

	return stdout.String(), stderr.String(), code
}

func (s *declServer) expect(t *testing.T, wantCode int, wantOut string, args ...string) string {
	t.Helper()

	out, errOut, code := s.oa(t, nil, args...)
	t.Logf("$ oa %s  -> exit %d\n%s%s", strings.Join(args, " "), code, out, errOut)

	if code != wantCode {
		t.Fatalf("oa %v: exit %d, want %d", args, code, wantCode)
	}

	if !strings.Contains(out+errOut, wantOut) {
		t.Fatalf("oa %v: output does not contain %q", args, wantOut)
	}

	return out
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()

	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func replaceInFile(t *testing.T, path, old, repl string) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(data), old) {
		t.Fatalf("%s does not contain %q", path, old)
	}

	if err := os.WriteFile(path, []byte(strings.Replace(string(data), old, repl, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDeclarativeApplyEndToEnd(t *testing.T) {
	s := startDeclServer(t)

	// tiering.yaml needs a second storage backend to point at.
	s.api(t, http.MethodPost, "/api/v1/admin/storage",
		map[string]any{"name": "cold", "kind": "filesystem", "config": map[string]string{"path": t.TempDir()}},
		http.StatusOK, http.StatusCreated)

	examples := filepath.Join(s.root, "examples", "declarative")
	work := t.TempDir()
	copyDir(t, examples, work)
	replaceInFile(t, filepath.Join(work, "webhooks.yaml"), "https://hooks.example.com/openartifact", "http://127.0.0.1:9/openartifact")
	replaceInFile(t, filepath.Join(work, "webhooks.yaml"), "https://hooks.example.com/chat", "http://127.0.0.1:9/chat")

	t.Run("empty server differs", func(t *testing.T) {
		s.expect(t, 2, "Repository/npm-hosted", "diff", "-f", work)
	})

	t.Run("apply creates everything", func(t *testing.T) {
		s.expect(t, 0, "ok", "apply", "-f", work)
	})

	t.Run("second diff and apply are no-ops", func(t *testing.T) {
		s.expect(t, 0, "No changes", "diff", "-f", work)

		out := s.expect(t, 0, "0 to create, 0 to update, 0 to delete", "apply", "-f", work)
		if strings.Contains(out, "FAILED") {
			t.Fatal("unexpected failure")
		}
	})

	t.Run("field change is shown then applied", func(t *testing.T) {
		replaceInFile(t, filepath.Join(work, "repositories.yaml"), "description: Internal npm packages", "description: Internal npm packages (edited)")

		out := s.expect(t, 2, "description", "diff", "-f", work)
		if !strings.Contains(out, `+   description: "Internal npm packages (edited)"`) {
			t.Fatalf("diff does not show the new value:\n%s", out)
		}

		s.expect(t, 0, "1 to update", "apply", "-f", work)
		s.expect(t, 0, "No changes", "diff", "-f", work)

		var repo struct {
			Description string `json:"description"`
		}

		_ = json.Unmarshal(s.api(t, http.MethodGet, "/api/v1/repository/npm-hosted", nil, http.StatusOK), &repo)
		if repo.Description != "Internal npm packages (edited)" {
			t.Fatalf("server description = %q", repo.Description)
		}
	})

	t.Run("json output", func(t *testing.T) {
		out := s.expect(t, 0, `"changes": false`, "diff", "-f", work, "-o", "json")
		if strings.Contains(out, "upstream-pw") || strings.Contains(out, "hook-secret") {
			t.Fatal("secret value leaked into the report")
		}
	})

	t.Run("invalid manifest fails with file and line", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), "bad.yaml")
		if err := os.WriteFile(bad, []byte("apiVersion: openartifact.io/v1\nkind: Repository\nmetadata:\n  name: x\nspec:\n  format: nope\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		s.expect(t, 1, "bad.yaml:6", "apply", "-f", bad)
	})

	t.Run("immutable change is refused", func(t *testing.T) {
		replaceInFile(t, filepath.Join(work, "repositories.yaml"), "name: platform-libs\n  project: platform\nspec:\n  format: maven", "name: platform-libs\n  project: platform\nspec:\n  format: npm")
		s.expect(t, 1, "immutable field(s) format", "apply", "-f", work)
		replaceInFile(t, filepath.Join(work, "repositories.yaml"), "name: platform-libs\n  project: platform\nspec:\n  format: npm", "name: platform-libs\n  project: platform\nspec:\n  format: maven")
	})

	t.Run("export round trips to no changes", func(t *testing.T) {
		exported := t.TempDir()
		s.expect(t, 0, "Exported", "export", "-o", exported, "--with-members")

		// Exported secrets are placeholders; give every one a value.
		var env []string

		_ = filepath.Walk(exported, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}

			data, _ := os.ReadFile(p)
			for _, m := range regexp.MustCompile(`env: (OA_[A-Z0-9_]+)`).FindAllStringSubmatch(string(data), -1) {
				env = append(env, m[1]+"=placeholder")
			}

			return nil
		})

		out, errOut, code := s.oa(t, env, "diff", "-R", "-f", exported)
		t.Logf("diff of the export -> exit %d\n%s%s", code, out, errOut)

		if code != 0 {
			t.Fatalf("re-applying the export is not a no-op (exit %d)", code)
		}
	})

	t.Run("prune deletes managed objects only", func(t *testing.T) {
		s.api(t, http.MethodPost, "/api/v1/repositories",
			map[string]any{"name": "manual-repo", "format": "raw", "repo_type": "hosted"}, http.StatusOK, http.StatusCreated)

		if err := os.Remove(filepath.Join(work, "webhooks.yaml")); err != nil {
			t.Fatal(err)
		}

		out := s.expect(t, 2, "Webhook/npm-hosted/chat-notify", "diff", "-f", work, "--prune")
		if strings.Contains(out, "manual-repo") {
			t.Fatal("--prune must not touch unmanaged objects")
		}

		s.expect(t, 0, "2 to delete", "apply", "-f", work, "--prune")
		s.api(t, http.MethodGet, "/api/v1/repository/manual-repo", nil, http.StatusOK)

		hooks, _, _ := s.oa(t, nil, "export", "--kind", "Webhook")
		if strings.TrimSpace(hooks) != "" {
			t.Fatalf("webhooks still exist after prune:\n%s", hooks)
		}

		s.expect(t, 2, "Repository/manual-repo", "diff", "-f", work, "--prune-all", "--kind", "Repository")
	})
}
