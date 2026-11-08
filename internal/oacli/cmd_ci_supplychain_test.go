// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// seenRequest is a request a supplyChainServer got.
type seenRequest struct {
	method, path string
	query        url.Values
	auth         string
	body         map[string]any
}

// supplyChainServer answers every request with one canned reply and records
// the requests: the commands under test talk to endpoints other slices of the
// server own, so they run against a fake instead of the real router.
type supplyChainServer struct {
	*httptest.Server

	// routes answer 200 with their body for "METHOD /path", before the canned reply.
	routes map[string]string

	mu   sync.Mutex
	seen []seenRequest
}

// setRoutes installs the routes; the handler goroutine reads them under the same lock.
func (s *supplyChainServer) setRoutes(routes map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.routes = routes
}

func newSupplyChainServer(t *testing.T, status int, body string) *supplyChainServer {
	t.Helper()

	s := &supplyChainServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)

		var doc map[string]any

		_ = json.Unmarshal(raw, &doc)

		s.mu.Lock()
		s.seen = append(s.seen, seenRequest{r.Method, r.URL.Path, r.URL.Query(), r.Header.Get("Authorization"), doc})
		routed, ok := s.routes[r.Method+" "+r.URL.Path]
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		if ok {
			_, _ = w.Write([]byte(routed))
			return
		}

		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)

	return s
}

func (s *supplyChainServer) only(t *testing.T) seenRequest {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.seen) != 1 {
		t.Fatalf("requests = %d, want 1: %+v", len(s.seen), s.seen)
	}

	return s.seen[0]
}

const exchangeReply = `{"access_token":"oa_dep_secret","token_type":"Bearer","expires_in":900,"expires_at":"2026-01-01T00:15:00Z"}`

func TestOIDCExchange(t *testing.T) {
	t.Run("prints the token, sends no credentials and writes the docker config", func(t *testing.T) {
		srv := newSupplyChainServer(t, http.StatusOK, exchangeReply)
		dir := t.TempDir()
		tokenFile := filepath.Join(dir, "token")
		cfgDir := filepath.Join(dir, "docker")

		if err := os.WriteFile(tokenFile, []byte("header.payload.sig\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := os.MkdirAll(cfgDir, 0o700); err != nil {
			t.Fatal(err)
		}

		prior := `{"auths":{"quay.io":{"auth":"eDp5"}},"credsStore":"x"}`
		if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(prior), 0o600); err != nil {
			t.Fatal(err)
		}

		res := runOA(t, srv.Server, "", "oidc", "exchange", "--policy", "publish", "--token-file", tokenFile, "--docker-config", cfgDir)
		if res.code != 0 || strings.TrimSpace(res.stdout) != "oa_dep_secret" {
			t.Fatalf("exit %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
		}

		got := srv.only(t)
		if got.method != http.MethodPost || got.path != "/api/v1/auth/oidc/exchange" || got.auth != "" {
			t.Errorf("request = %+v, want an anonymous POST of the exchange", got)
		}

		if got.body["token"] != "header.payload.sig" || got.body["policy"] != "publish" || len(got.body) != 2 {
			t.Errorf("body = %v", got.body)
		}

		raw, err := os.ReadFile(filepath.Join(cfgDir, "config.json"))
		if err != nil {
			t.Fatal(err)
		}

		var cfg struct {
			Auths map[string]struct{ Auth string } `json:"auths"`
		}

		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}

		// the docker key credsStore must survive the merge
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			t.Fatal(err)
		}

		host := strings.TrimPrefix(srv.URL, "http://")
		want := base64.StdEncoding.EncodeToString([]byte("oa:oa_dep_secret"))

		if cfg.Auths[host].Auth != want || cfg.Auths["quay.io"].Auth != "eDp5" || string(keys["credsStore"]) != `"x"` {
			t.Errorf("docker config = %s", raw)
		}

		if fi, err := os.Stat(filepath.Join(cfgDir, "config.json")); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("config mode = %v, %v; want 0600", fi, err)
		}
	})

	t.Run("reads stdin and prints json", func(t *testing.T) {
		srv := newSupplyChainServer(t, http.StatusOK, exchangeReply)

		res := runOA(t, srv.Server, "from.stdin.jwt", "--format", "json", "oidc", "exchange", "--token-file", "-")
		if res.code != 0 || !strings.Contains(res.stdout, `"expires_in": 900`) {
			t.Fatalf("exit %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
		}

		got := srv.only(t)
		if got.body["token"] != "from.stdin.jwt" {
			t.Errorf("body = %v", got.body)
		}

		if _, ok := got.body["policy"]; ok {
			t.Errorf("an unset --policy must not be sent: %v", got.body)
		}
	})

	t.Run("a refusal fails with the server's message", func(t *testing.T) {
		srv := newSupplyChainServer(t, http.StatusUnauthorized, `{"error":"unauthorized","message":"token refused"}`)

		res := runOA(t, srv.Server, "a.b.c", "oidc", "exchange", "--token-file", "-")
		if res.code == 0 || !strings.Contains(res.stderr, "token refused") || res.stdout != "" {
			t.Errorf("exit %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
		}
	})

	for name, tc := range map[string]struct {
		args  []string
		stdin string
		fail  string
	}{
		"no token file":    {args: []string{"oidc", "exchange"}, fail: "--token-file is required"},
		"empty token":      {args: []string{"oidc", "exchange", "--token-file", "-"}, stdin: " \n", fail: "empty"},
		"missing file":     {args: []string{"oidc", "exchange", "--token-file", "/nonexistent/token"}, fail: "token file"},
		"oversized stdin":  {args: []string{"oidc", "exchange", "--token-file", "-"}, stdin: strings.Repeat("a", maxTokenFileBytes+1), fail: "larger than"},
		"unknown argument": {args: []string{"oidc", "exchange", "extra"}, fail: "Usage: oa oidc exchange"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newSupplyChainServer(t, http.StatusOK, exchangeReply)

			res := runOA(t, srv.Server, tc.stdin, tc.args...)
			if res.code == 0 || !strings.Contains(res.stderr, tc.fail) {
				t.Errorf("exit %d, stderr %q, want failure with %q", res.code, res.stderr, tc.fail)
			}

			srv.mu.Lock()
			defer srv.mu.Unlock()

			if len(srv.seen) != 0 {
				t.Errorf("a bad command line reached the server: %+v", srv.seen)
			}
		})
	}
}

func TestBundleCheckReport(t *testing.T) {
	const reply = `{"name":"conforma","status":"success","summary":"ok","details_url":"https://ci/1","reported_by":"ci","updated_at":"2026-01-01T00:00:00Z"}`

	srv := newSupplyChainServer(t, http.StatusOK, reply)

	res := runOA(
		t,
		srv.Server,
		"",
		"--format",
		"json",
		"bundle",
		"check",
		"report",
		"app",
		"1.0",
		"--name",
		"conforma",
		"--status",
		"success",
		"--summary",
		"ok",
		"--details-url",
		"https://ci/1",
		"--expires-at",
		"2030-01-01T00:00:00Z",
	)
	if res.code != 0 || !strings.Contains(res.stdout, `"reported_by": "ci"`) {
		t.Fatalf("exit %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
	}

	got := srv.only(t)
	want := map[string]any{
		"name": "conforma", "status": "success", "summary": "ok", "details_url": "https://ci/1", "expires_at": "2030-01-01T00:00:00Z",
	}

	if got.method != http.MethodPost || got.path != "/api/v1/bundles/app/1.0/checks" || got.auth != "Bearer t" {
		t.Errorf("request = %+v", got)
	}

	if len(got.body) != len(want) {
		t.Errorf("body = %v, want %v", got.body, want)
	}

	for k, v := range want {
		if got.body[k] != v {
			t.Errorf("body[%s] = %v, want %v", k, got.body[k], v)
		}
	}

	t.Run("text output", func(t *testing.T) {
		res := runOA(
			t,
			srv.Server,
			"",
			"bundle",
			"check",
			"report",
			"app",
			"1.0",
			"--name",
			"conforma",
			"--status",
			"success",
			"--summary",
			"ok",
		)
		if res.code != 0 || !strings.Contains(res.stdout, "Check conforma: success (ok)") {
			t.Errorf("exit %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
		}
	})

	for name, tc := range map[string]struct {
		args []string
		fail string
	}{
		"no name":    {[]string{"--status", "success"}, "--name is required"},
		"bad status": {[]string{"--name", "c", "--status", "ok"}, "invalid check status"},
		"no status":  {[]string{"--name", "c"}, "invalid check status"},
		"bad expiry": {[]string{"--name", "c", "--status", "pending", "--expires-at", "tomorrow"}, "RFC 3339"},
	} {
		t.Run(name, func(t *testing.T) {
			bad := newSupplyChainServer(t, http.StatusOK, reply)

			res := runOA(t, bad.Server, "", append([]string{"bundle", "check", "report", "app", "1.0"}, tc.args...)...)
			if res.code == 0 || !strings.Contains(res.stderr, tc.fail) {
				t.Errorf("exit %d, stderr %q, want failure with %q", res.code, res.stderr, tc.fail)
			}

			bad.mu.Lock()
			defer bad.mu.Unlock()

			if len(bad.seen) != 0 {
				t.Errorf("invalid flags reached the server: %+v", bad.seen)
			}
		})
	}

	t.Run("server refusal", func(t *testing.T) {
		deny := newSupplyChainServer(t, http.StatusForbidden, `{"error":"forbidden","message":"no write access"}`)

		res := runOA(t, deny.Server, "", "bundle", "check", "report", "app", "1.0", "--name", "c", "--status", "failure")
		if res.code == 0 || !strings.Contains(res.stderr, "no write access") {
			t.Errorf("exit %d, stderr %q", res.code, res.stderr)
		}
	})
}

func TestGate(t *testing.T) {
	const (
		passing = `{"pass":true,"reason":"all checks pass","checks":[{"name":"vulnerabilities","pass":true,"detail":"0 critical"}],"scan_status":"completed"}`
		failing = `{"pass":false,"reason":"1 check failed","checks":[{"name":"quarantine","pass":false,"detail":"quarantined"}],"scan_status":"completed"}`
		digest  = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	)

	tests := []struct {
		name     string
		args     []string
		status   int
		body     string
		wantCode int
		wantOut  string
		wantErr  string
		wantPath string
		wantQ    map[string]string
	}{
		{
			name: "digest passes", args: []string{"gate", "img", "--digest", digest}, status: 200, body: passing,
			wantOut: "PASS: all checks pass", wantPath: "/api/v1/repositories/img/gate", wantQ: map[string]string{"digest": digest},
		},
		{
			name:     "digest fails with exit 1 and still prints",
			args:     []string{"gate", "img", "--digest", digest},
			status:   200,
			body:     failing,
			wantCode: 1,
			wantOut:  "FAIL quarantine: quarantined",
			wantPath: "/api/v1/repositories/img/gate",
		},
		{
			name: "scan form as json", args: []string{"--format", "json", "gate", "img", "--scan", "s 1"}, status: 200, body: failing,
			wantCode: 1, wantOut: `"pass": false`, wantPath: "/api/v1/repositories/img/vuln/scans/s 1/gate",
		},
		{name: "needs a target", args: []string{"gate", "img"}, wantCode: 1, wantErr: "give --digest or --scan"},
		{name: "one target only", args: []string{"gate", "img", "--digest", digest, "--scan", "1"}, wantCode: 1, wantErr: "not both"},
		{
			name:     "server error is an error, not a verdict",
			args:     []string{"gate", "img", "--digest", digest},
			status:   403,
			body:     `{"error":"forbidden","message":"no read access"}`,
			wantCode: 1,
			wantErr:  "no read access",
			wantPath: "/api/v1/repositories/img/gate",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newSupplyChainServer(t, tc.status, tc.body)

			res := runOA(t, srv.Server, "", tc.args...)
			if res.code != tc.wantCode {
				t.Fatalf("exit %d, want %d (stdout %q, stderr %q)", res.code, tc.wantCode, res.stdout, res.stderr)
			}

			if !strings.Contains(res.stdout, tc.wantOut) || !strings.Contains(res.stderr, tc.wantErr) {
				t.Errorf("stdout %q stderr %q, want %q / %q", res.stdout, res.stderr, tc.wantOut, tc.wantErr)
			}

			srv.mu.Lock()
			defer srv.mu.Unlock()

			if tc.wantPath == "" {
				if len(srv.seen) != 0 {
					t.Errorf("reached the server: %+v", srv.seen)
				}

				return
			}

			if len(srv.seen) != 1 || srv.seen[0].method != http.MethodGet || srv.seen[0].path != tc.wantPath {
				t.Fatalf("requests = %+v, want GET %s", srv.seen, tc.wantPath)
			}

			for k, v := range tc.wantQ {
				if srv.seen[0].query.Get(k) != v {
					t.Errorf("query %s = %q, want %q", k, srv.seen[0].query.Get(k), v)
				}
			}
		})
	}
}

func TestAttestations(t *testing.T) {
	const (
		subject = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		reply   = `{"attestations":[{"predicate_type":"https://slsa.dev/provenance/v0.2","verified":true}]}`
	)

	srv := newSupplyChainServer(t, http.StatusOK, reply)

	res := runOA(t, srv.Server, "", "attestations", "img", "--subject", subject)
	if res.code != 0 || !strings.Contains(res.stdout, `"predicate_type": "https://slsa.dev/provenance/v0.2"`) {
		t.Fatalf("exit %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
	}

	got := srv.only(t)
	if got.method != http.MethodGet || got.path != "/api/v1/repositories/img/attestations" || got.query.Get("subject") != subject {
		t.Errorf("request = %+v", got)
	}

	res = runOA(t, srv.Server, "", "attestations", "img")
	if res.code == 0 || !strings.Contains(res.stderr, "--subject is required") {
		t.Errorf("without --subject: exit %d, stderr %q", res.code, res.stderr)
	}
}

func TestBundleAddDigest(t *testing.T) {
	const hexDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	newServer := func(t *testing.T, format string) *supplyChainServer {
		t.Helper()

		srv := newSupplyChainServer(t, http.StatusCreated, `{"items":[]}`)
		srv.setRoutes(map[string]string{
			"GET /api/v1/repository/img":         `{"id":"r1","name":"img","format":"` + format + `","repo_type":"hosted"}`,
			"GET /api/v1/bundles/app/1.0":        bundleJSON,
			"POST /api/v1/bundles/app/1.0/items": `{"items":[]}`,
		})

		return srv
	}

	t.Run("a docker digest is the item and its own path", func(t *testing.T) {
		srv := newServer(t, "docker")

		res := runOA(t, srv.Server, "", "--format", "json", "bundle", "add", "app", "1.0", "--repo", "img",
			"--digest", "SHA256:"+strings.ToUpper(hexDigest), "--digest", "sha256:"+strings.Repeat("a", 64))
		if res.code != 0 || !strings.Contains(res.stdout, `"added": 2`) || !strings.Contains(res.stdout, `"id": "b1"`) {
			t.Fatalf("exit %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
		}

		srv.mu.Lock()
		defer srv.mu.Unlock()

		var post *seenRequest

		for i := range srv.seen {
			if srv.seen[i].method == http.MethodPost {
				post = &srv.seen[i]
			}
		}

		if post == nil {
			t.Fatalf("no items request: %+v", srv.seen)
		}

		items, _ := post.body["items"].([]any)
		first, _ := items[0].(map[string]any)

		if len(items) != 2 || first["sha256"] != hexDigest || first["artifact_path"] != "sha256:"+hexDigest ||
			first["format"] != "docker" || first["source_repo_id"] != "r1" {
			t.Errorf("items = %v", items)
		}
	})

	for name, tc := range map[string]struct {
		format string
		args   []string
		fail   string
	}{
		"not a docker repository": {"raw", []string{"--digest", "sha256:" + hexDigest}, "docker repositories"},
		"malformed digest":        {"docker", []string{"--digest", "sha256:abc"}, "--digest wants"},
		"without a repo":          {"docker", []string{"--digest", "sha256:" + hexDigest, "--repo", "a", "--repo", "b"}, "exactly one --repo"},
		"with a path":             {"docker", []string{"--digest", "sha256:" + hexDigest, "--path", "p"}, "exclusive"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newServer(t, tc.format)
			args := append([]string{"bundle", "add", "app", "1.0"}, tc.args...)

			if !strings.Contains(strings.Join(tc.args, " "), "--repo") {
				args = append(args, "--repo", "img")
			}

			res := runOA(t, srv.Server, "", args...)
			if res.code == 0 || !strings.Contains(res.stderr, tc.fail) {
				t.Errorf("exit %d, stderr %q, want failure with %q", res.code, res.stderr, tc.fail)
			}

			srv.mu.Lock()
			defer srv.mu.Unlock()

			for _, r := range srv.seen {
				if r.method == http.MethodPost {
					t.Errorf("a refused command posted items: %+v", r)
				}
			}
		})
	}
}
