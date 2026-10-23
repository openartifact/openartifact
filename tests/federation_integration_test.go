// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pkgnpm "github.com/openartifact/openartifact/pkg/npm"
)

const (
	fedRawRepo = "fed-raw"
	fedNPMRepo = "fed-npm"
	fedWait    = 60 * time.Second
)

// fedNode is one real server process with its own database and storage.
type fedNode struct {
	name    string
	bin     string
	port    int
	url     string
	dbName  string
	storage string
	admin   *sql.DB
	dsn     *url.URL

	token  string
	cmd    *exec.Cmd
	stderr *syncBuffer
}

func newFedNode(t *testing.T, admin *sql.DB, bin, name string) *fedNode {
	t.Helper()

	dsn, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}

	n := &fedNode{name: name, bin: bin, admin: admin, dsn: dsn, port: freePort(t)}
	n.url = "http://127.0.0.1:" + strconv.Itoa(n.port)

	t.Cleanup(func() {
		n.stop()
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+n.dbName+` WITH (FORCE)`)
	})

	return n
}

// freshDatabase gives the node an empty database and storage directory.
func (n *fedNode) freshDatabase(t *testing.T) {
	t.Helper()

	if n.dbName != "" {
		_, _ = n.admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+n.dbName+` WITH (FORCE)`)
	}

	n.dbName = fmt.Sprintf("oa_fed_%s_%d", n.name, time.Now().UnixNano())

	if _, err := n.admin.ExecContext(context.Background(), `CREATE DATABASE `+n.dbName); err != nil {
		t.Fatalf("create database: %v", err)
	}

	n.storage = t.TempDir()
}

// start launches the server on the node's database and returns once it is
// healthy, with an admin API token in n.token.
func (n *fedNode) start(t *testing.T) {
	t.Helper()

	pw, _ := n.dsn.User.Password()
	n.stderr = &syncBuffer{}

	cmd := exec.CommandContext(context.Background(), n.bin)
	cmd.Dir = t.TempDir()
	cmd.Stderr = n.stderr
	cmd.Env = append(os.Environ(),
		"SERVER_HOST=127.0.0.1", "SERVER_PORT="+strconv.Itoa(n.port),
		"DB_DRIVER=postgres", "DB_HOST="+n.dsn.Hostname(), "DB_PORT="+n.dsn.Port(),
		"DB_USER="+n.dsn.User.Username(), "DB_PASSWORD="+pw, "DB_NAME="+n.dbName, "DB_SSLMODE=disable",
		"STORAGE_BACKEND=filesystem", "STORAGE_PATH="+n.storage,
		"OPENARTIFACT_BACKUP_DIR="+t.TempDir(),
		"LOG_LEVEL=error",
		"OPENARTIFACT_SECRET_KEY=federation-test-secret-key-0123456789",
		"OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true",
		"OPENARTIFACT_FEDERATION_ENABLED=true",
		"OPENARTIFACT_FEDERATION_POLL_INTERVAL=200ms",
		"OPENARTIFACT_FEDERATION_MAX_BACKOFF=2s",
	)

	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", n.name, err)
	}

	n.cmd = cmd

	srv := &declServer{url: n.url}
	srv.waitHealthy(t, n.stderr)
	srv.password = waitBootstrapPassword(t, n.stderr)
	n.token = srv.mintToken(t)
}

func (n *fedNode) stop() {
	if n.cmd == nil {
		return
	}

	_ = n.cmd.Process.Kill()
	_ = n.cmd.Wait()
	n.cmd = nil
}

// do performs an authenticated request and returns status and body.
func (n *fedNode) do(t *testing.T, method, path, contentType string, body []byte) (int, []byte) {
	t.Helper()

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, n.url+path, rdr)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Authorization", "Bearer "+n.token)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s %s: %v\n%s", n.name, method, path, err, n.stderr.String())
	}

	defer func() { _ = resp.Body.Close() }()

	data, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, data
}

// call is do with a JSON body that must answer one of want.
func (n *fedNode) call(t *testing.T, method, path string, in any, want ...int) []byte {
	t.Helper()

	var raw []byte

	if in != nil {
		raw, _ = json.Marshal(in)
	}

	code, data := n.do(t, method, path, "application/json", raw)

	if slices.Contains(want, code) {
		return data
	}

	t.Fatalf("%s %s %s: HTTP %d: %s", n.name, method, path, code, data)

	return nil
}

func (n *fedNode) putRaw(t *testing.T, path, content string) {
	t.Helper()

	if code, data := n.do(t, http.MethodPut, "/repos/raw/"+fedRawRepo+"/"+path, "application/octet-stream", []byte(content)); code != http.StatusCreated {
		t.Fatalf("%s put %s: HTTP %d: %s", n.name, path, code, data)
	}
}

func (n *fedNode) getRaw(t *testing.T, path string) (int, string) {
	t.Helper()

	code, data := n.do(t, http.MethodGet, "/repos/raw/"+fedRawRepo+"/"+path, "", nil)

	return code, string(data)
}

func (n *fedNode) deleteRaw(t *testing.T, path string) {
	t.Helper()

	if code, data := n.do(t, http.MethodDelete, "/repos/raw/"+fedRawRepo+"/"+path, "", nil); code != http.StatusNoContent {
		t.Fatalf("%s delete %s: HTTP %d: %s", n.name, path, code, data)
	}
}

// fedTarball is a minimal real npm tarball.
func fedTarball(t *testing.T, name, version string) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for _, f := range []struct{ name, body string }{
		{"package/package.json", fmt.Sprintf(`{"name":%q,"version":%q}`, name, version)},
		{"package/index.js", fmt.Sprintf("module.exports = %q;\n", name+"@"+version)},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body))}); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func (n *fedNode) publishNPM(t *testing.T, name, version string) {
	t.Helper()

	tgz := fedTarball(t, name, version)

	integrity, shasum, _, _, err := pkgnpm.ComputeIntegrity(bytes.NewReader(tgz))
	if err != nil {
		t.Fatal(err)
	}

	manifest := map[string]any{
		"name": name, "version": version, "license": "MIT",
		"dist": map[string]string{"integrity": integrity, "shasum": shasum, "tarball": n.url + "/placeholder.tgz"},
	}
	file := name + "-" + version + ".tgz"
	body, _ := json.Marshal(map[string]any{
		"_id": name, "name": name,
		"versions":  map[string]any{version: manifest},
		"dist-tags": map[string]string{"latest": version},
		"_attachments": map[string]any{file: map[string]any{
			"content_type": "application/octet-stream",
			"data":         base64.StdEncoding.EncodeToString(tgz),
			"length":       len(tgz),
		}},
	})

	if code, data := n.do(t, http.MethodPut, "/repos/"+fedNPMRepo+"/"+name, "application/json", body); code != http.StatusCreated {
		t.Fatalf("%s publish %s@%s: HTTP %d: %s", n.name, name, version, code, data)
	}
}

func (n *fedNode) setTag(t *testing.T, pkg, tag, version string) {
	t.Helper()

	n.call(t, http.MethodPut, "/repos/"+fedNPMRepo+"/-/package/"+pkg+"/dist-tags/"+tag, version, http.StatusOK, http.StatusCreated)
}

// packument returns the versions and dist-tags of an npm package, or nils when
// it does not exist.
func (n *fedNode) packument(t *testing.T, pkg string) (map[string]json.RawMessage, map[string]string) {
	t.Helper()

	code, data := n.do(t, http.MethodGet, "/repos/"+fedNPMRepo+"/"+pkg, "", nil)
	if code != http.StatusOK {
		return nil, nil
	}

	var doc struct {
		Versions map[string]json.RawMessage `json:"versions"`
		DistTags map[string]string          `json:"dist-tags"` //nolint:tagliatelle // npm registry wire format
	}

	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode packument: %v: %s", err, data)
	}

	return doc.Versions, doc.DistTags
}

func (n *fedNode) tag(t *testing.T, pkg, tag string) string {
	t.Helper()

	_, tags := n.packument(t, pkg)

	return tags[tag]
}

type fedPeerStatus struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	State          string  `json:"state"`
	CursorSeq      int64   `json:"cursor_seq"`
	RemoteHeadSeq  int64   `json:"remote_head_seq"`
	BacklogUnits   int64   `json:"backlog_units"`
	LagSeconds     float64 `json:"lag_seconds"`
	LastError      string  `json:"last_error"`
	ErrorCount     int     `json:"error_count"`
	AppliedTotal   int64   `json:"applied_total"`
	ConflictsTotal int64   `json:"conflicts_total"`
	DeferredUnits  int     `json:"deferred_units"`
	Resync         bool    `json:"resync_requested"`
}

type fedStatus struct {
	Enabled    bool            `json:"enabled"`
	Supported  bool            `json:"supported"`
	LogID      string          `json:"log_id"`
	HeadSeq    int64           `json:"head_seq"`
	Units      int64           `json:"units"`
	Tombstones int64           `json:"tombstones"`
	Peers      []fedPeerStatus `json:"peers"`
}

func fedAPI(repo string) string { return "/api/v1/repositories/" + repo + "/federation" }

func (n *fedNode) status(t *testing.T, repo string) fedStatus {
	t.Helper()

	var st fedStatus

	if err := json.Unmarshal(n.call(t, http.MethodGet, fedAPI(repo), nil, http.StatusOK), &st); err != nil {
		t.Fatal(err)
	}

	return st
}

// peer returns the (single) peer of the repository's federation status.
func (n *fedNode) peer(t *testing.T, repo string) fedPeerStatus {
	t.Helper()

	st := n.status(t, repo)
	if len(st.Peers) != 1 {
		t.Fatalf("%s %s: want 1 peer, got %d", n.name, repo, len(st.Peers))
	}

	return st.Peers[0]
}

// fedProxy sits between a node and the peer it pulls from; the test flips it
// to simulate a network partition.
type fedProxy struct {
	url  string
	mode atomic.Int32
}

const (
	proxyUp   = 0 // forward everything
	proxyHalf = 1 // forward /info and /log, drop unit and blob fetches
	proxyCut  = 2 // drop every connection
)

func newFedProxy(t *testing.T, target string) *fedProxy {
	t.Helper()

	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}

	rp := httputil.NewSingleHostReverseProxy(u)
	rp.ErrorLog = log.New(io.Discard, "", 0) // a stopped peer is expected while partitioned
	p := &fedProxy{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := p.mode.Load()
		if mode == proxyCut || (mode == proxyHalf && (r.URL.Path == "/federation/v1/unit" || r.URL.Path == "/federation/v1/blob")) {
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}

			http.Error(w, "partition", http.StatusBadGateway)

			return
		}

		rp.ServeHTTP(w, r)
	}))

	t.Cleanup(srv.Close)

	p.url = srv.URL

	return p
}

// fedPair is two federated instances that pull from each other.
type fedPair struct {
	a, b       *fedNode
	aToB, bToA *fedProxy // aToB: how B reaches A
	tokenForB  string    // token issued on A for B (raw repo)
	tokenForA  string    // token issued on B for A (raw repo)
}

// bootstrapFederatedRepos creates both repositories federated on the node.
func (n *fedNode) bootstrapFederatedRepos(t *testing.T) {
	t.Helper()

	n.call(t, http.MethodPost, "/api/v1/repositories",
		map[string]any{"name": fedRawRepo, "format": "raw", "repo_type": "hosted", "federated": true}, http.StatusCreated)
	n.call(t, http.MethodPost, "/api/v1/repositories",
		map[string]any{"name": fedNPMRepo, "format": "npm", "repo_type": "hosted", "federated": true}, http.StatusCreated)
}

// issueToken creates a peer token for the repository and returns its plaintext.
func (n *fedNode) issueToken(t *testing.T, repo, name string) string {
	t.Helper()

	var tok struct {
		Token string `json:"token"`
	}

	if err := json.Unmarshal(n.call(t, http.MethodPost, fedAPI(repo)+"/tokens", map[string]string{"name": name}, http.StatusCreated), &tok); err != nil || tok.Token == "" {
		t.Fatalf("token for %s: %v", repo, err)
	}

	return tok.Token
}

func (n *fedNode) addPeer(t *testing.T, repo, name, baseURL, token string) {
	t.Helper()

	n.call(t, http.MethodPost, fedAPI(repo)+"/peers",
		map[string]string{"name": name, "base_url": baseURL, "token": token}, http.StatusCreated)
}

func startFedPair(t *testing.T) *fedPair {
	t.Helper()

	admin := setupTestDB(t)
	bin := filepath.Join(t.TempDir(), "openartifact")
	goBuild(t, repoRoot(t), bin, "./cmd/server")

	p := &fedPair{a: newFedNode(t, admin, bin, "a"), b: newFedNode(t, admin, bin, "b")}

	for _, n := range []*fedNode{p.a, p.b} {
		n.freshDatabase(t)
		n.start(t)
		n.bootstrapFederatedRepos(t)
	}

	p.aToB = newFedProxy(t, p.a.url)
	p.bToA = newFedProxy(t, p.b.url)

	for _, repo := range []string{fedRawRepo, fedNPMRepo} {
		tokA := p.a.issueToken(t, repo, "for-b")
		tokB := p.b.issueToken(t, repo, "for-a")

		if repo == fedRawRepo {
			p.tokenForB, p.tokenForA = tokA, tokB
		}

		p.b.addPeer(t, repo, "a", p.aToB.url, tokA)
		p.a.addPeer(t, repo, "b", p.bToA.url, tokB)
	}

	return p
}

func (p *fedPair) setMode(mode int32) {
	p.aToB.mode.Store(mode)
	p.bToA.mode.Store(mode)
}

// converged reports whether both nodes serve the same content for the raw
// paths: identical status and body.
func (p *fedPair) sameRaw(t *testing.T, paths ...string) bool {
	t.Helper()

	for _, path := range paths {
		ca, ba := p.a.getRaw(t, path)
		cb, bb := p.b.getRaw(t, path)

		if ca != cb || ba != bb {
			return false
		}
	}

	return true
}

// idle reports whether both nodes have caught up with every peer of the repo.
func (p *fedPair) idle(t *testing.T, repo string) bool {
	t.Helper()

	for _, n := range []*fedNode{p.a, p.b} {
		pr := n.peer(t, repo)
		if pr.State != "ok" || pr.BacklogUnits != 0 || pr.CursorSeq != pr.RemoteHeadSeq {
			return false
		}
	}

	return true
}

func TestFederationTwoInstances(t *testing.T) {
	p := startFedPair(t)

	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("--- a stderr ---\n%s\n--- b stderr ---\n%s", p.a.stderr.String(), p.b.stderr.String())
		}
	})

	for _, sub := range []struct {
		name string
		run  func(*testing.T, *fedPair)
	}{
		{"peer endpoints require a valid token", fedSubPeerAuth},
		{"concurrent uploads on both instances converge", fedSubConcurrentUploads},
		{"partition: errors and lag are visible, then it catches up", fedSubPartition},
		{"a concurrently moved tag resolves to the later writer", fedSubTagConflict},
		{"a delete propagates and is not resurrected by a resync", fedSubDeletePropagates},
		{"a deleted peer is rebuilt by full resync", fedSubRebuild},
	} {
		t.Run(sub.name, func(t *testing.T) { sub.run(t, p) })
	}
}

func fedSubPeerAuth(t *testing.T, p *fedPair) {
	t.Helper()

	for name, auth := range map[string]string{"none": "", "wrong": "Bearer oafed_nope", "api token": "Bearer " + p.a.token} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, p.a.url+"/federation/v1/info", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: HTTP %d, want 401", name, resp.StatusCode)
		}
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, p.a.url+"/federation/v1/info", nil)
	req.Header.Set("Authorization", "Bearer "+p.tokenForB)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("valid peer token: HTTP %d, want 200", resp.StatusCode)
	}
}

func fedSubConcurrentUploads(t *testing.T, p *fedPair) {
	t.Helper()

	var wg sync.WaitGroup

	upload := func(n *fedNode, prefix string) {
		defer wg.Done()

		for i := range 15 {
			n.putRaw(t, fmt.Sprintf("conc/%s-%02d.txt", prefix, i), fmt.Sprintf("%s payload %d", prefix, i))
		}
		// The same path written on both sides: last writer wins everywhere.
		n.putRaw(t, "conc/shared.txt", "shared written by "+prefix)
	}

	wg.Add(2)

	go upload(p.a, "a")
	go upload(p.b, "b")

	wg.Wait()

	paths := []string{"conc/shared.txt"}
	for i := range 15 {
		paths = append(paths, fmt.Sprintf("conc/a-%02d.txt", i), fmt.Sprintf("conc/b-%02d.txt", i))
	}

	poll(t, "raw convergence", fedWait, func() bool { return p.sameRaw(t, paths...) })

	for _, path := range paths {
		if code, _ := p.a.getRaw(t, path); code != http.StatusOK {
			t.Errorf("%s: HTTP %d on a", path, code)
		}
	}

	// npm: one package published on each side.
	p.a.publishNPM(t, "pkg-from-a", "1.0.0")
	p.b.publishNPM(t, "pkg-from-b", "1.0.0")

	poll(t, "npm convergence", fedWait, func() bool {
		for _, pkg := range []string{"pkg-from-a", "pkg-from-b"} {
			va, _ := p.a.packument(t, pkg)
			vb, _ := p.b.packument(t, pkg)

			if len(va) != 1 || len(vb) != 1 {
				return false
			}
		}

		return true
	})

	poll(t, "idle", fedWait, func() bool { return p.idle(t, fedRawRepo) && p.idle(t, fedNPMRepo) })
}

func fedSubPartition(t *testing.T, p *fedPair) {
	t.Helper()

	// Fully cut: B cannot reach A.
	p.aToB.mode.Store(proxyCut)
	p.a.putRaw(t, "part/while-cut.txt", "written during the partition")

	poll(t, "error visible on b", fedWait, func() bool {
		pr := p.b.peer(t, fedRawRepo)

		return pr.State == "error" && pr.ErrorCount > 0 && pr.LastError != ""
	})

	if code, _ := p.b.getRaw(t, "part/while-cut.txt"); code != http.StatusNotFound {
		t.Fatalf("b served a file written during the partition: HTTP %d", code)
	}

	// Half-open: B sees the head move (lag, backlog) but cannot fetch units.
	p.aToB.mode.Store(proxyHalf)

	poll(t, "backlog and lag visible on b", fedWait, func() bool {
		pr := p.b.peer(t, fedRawRepo)

		return pr.BacklogUnits > 0 && pr.LagSeconds > 0 && pr.State == "error" && pr.LastError != ""
	})

	// Healed: B catches up and the peer is healthy again.
	p.aToB.mode.Store(proxyUp)

	poll(t, "catch up after partition", fedWait, func() bool {
		code, body := p.b.getRaw(t, "part/while-cut.txt")

		return code == http.StatusOK && body == "written during the partition"
	})

	poll(t, "healthy after partition", fedWait, func() bool {
		pr := p.b.peer(t, fedRawRepo)

		return pr.State == "ok" && pr.BacklogUnits == 0 && pr.LastError == ""
	})
}

func fedSubTagConflict(t *testing.T, p *fedPair) {
	t.Helper()

	const pkg = "tagged"

	for _, v := range []string{"1.0.0", "2.0.0", "3.0.0"} {
		p.a.publishNPM(t, pkg, v)
	}

	p.a.setTag(t, pkg, "stable", "1.0.0")

	poll(t, "tagged package on b", fedWait, func() bool {
		vb, tags := p.b.packument(t, pkg)

		return len(vb) == 3 && tags["stable"] == "1.0.0"
	})

	for _, c := range []struct{ first, second *fedNode }{{p.a, p.b}, {p.b, p.a}} {
		// Both sides move the tag while partitioned; the second write is the later one.
		p.setMode(proxyCut)

		c.first.setTag(t, pkg, "stable", "2.0.0")
		c.second.setTag(t, pkg, "stable", "3.0.0")

		p.setMode(proxyUp)

		poll(t, "tag convergence", fedWait, func() bool {
			return p.a.tag(t, pkg, "stable") == "3.0.0" && p.b.tag(t, pkg, "stable") == "3.0.0"
		})

		// Reset for the reverse order.
		c.first.setTag(t, pkg, "stable", "1.0.0")

		poll(t, "tag reset", fedWait, func() bool {
			return p.a.tag(t, pkg, "stable") == "1.0.0" && p.b.tag(t, pkg, "stable") == "1.0.0"
		})
	}

	// The superseded local writes were counted as conflicts on both sides.
	for _, n := range []*fedNode{p.a, p.b} {
		if pr := n.peer(t, fedNPMRepo); pr.ConflictsTotal < 1 {
			t.Errorf("%s: conflicts_total = %d, want >= 1", n.name, pr.ConflictsTotal)
		}
	}
}

func fedSubDeletePropagates(t *testing.T, p *fedPair) {
	t.Helper()

	p.a.putRaw(t, "del/doomed.txt", "short lived")
	poll(t, "doomed on b", fedWait, func() bool { return p.sameRaw(t, "del/doomed.txt") })

	p.a.deleteRaw(t, "del/doomed.txt")

	poll(t, "delete on b", fedWait, func() bool {
		code, _ := p.b.getRaw(t, "del/doomed.txt")

		return code == http.StatusNotFound
	})

	for _, n := range []*fedNode{p.a, p.b} {
		n.call(t, http.MethodPost, fedAPI(fedRawRepo)+"/peers/"+n.peer(t, fedRawRepo).ID+"/resync", nil, http.StatusAccepted, http.StatusOK, http.StatusNoContent)
	}

	poll(t, "resync done", fedWait, func() bool {
		return p.idle(t, fedRawRepo) && !p.a.peer(t, fedRawRepo).Resync && !p.b.peer(t, fedRawRepo).Resync
	})

	for _, n := range []*fedNode{p.a, p.b} {
		if code, _ := n.getRaw(t, "del/doomed.txt"); code != http.StatusNotFound {
			t.Errorf("%s resurrected the deleted file: HTTP %d", n.name, code)
		}
	}
}

func fedSubRebuild(t *testing.T, p *fedPair) {
	t.Helper()

	paths := []string{"conc/shared.txt", "part/while-cut.txt", "del/doomed.txt"}
	for i := range 15 {
		paths = append(paths, fmt.Sprintf("conc/a-%02d.txt", i), fmt.Sprintf("conc/b-%02d.txt", i))
	}

	type served struct {
		code int
		body string
	}

	want := map[string]served{}

	for _, path := range paths {
		code, body := p.a.getRaw(t, path)
		want[path] = served{code, body}
	}

	// B is destroyed: database and blobs gone, process restarted empty.
	p.b.stop()
	p.b.freshDatabase(t)
	p.b.start(t)
	p.b.bootstrapFederatedRepos(t)

	for _, repo := range []string{fedRawRepo, fedNPMRepo} {
		tokB := p.b.issueToken(t, repo, "for-a-rebuilt")

		// A learns B's new token and keeps pulling; B re-joins A.
		pa := p.a.peer(t, repo)
		p.a.call(t, http.MethodPatch, fedAPI(repo)+"/peers/"+pa.ID, map[string]string{"token": tokB}, http.StatusOK)

		tokA := p.a.issueToken(t, repo, "for-b-rebuilt")
		p.b.addPeer(t, repo, "a", p.aToB.url, tokA)
	}

	poll(t, "b rebuilt", fedWait, func() bool {
		for path, w := range want {
			if code, body := p.b.getRaw(t, path); code != w.code || (code == http.StatusOK && body != w.body) {
				return false
			}
		}

		for _, pkg := range []string{"pkg-from-a", "pkg-from-b", "tagged"} {
			if v, _ := p.b.packument(t, pkg); len(v) == 0 {
				return false
			}
		}

		return true
	})

	if v, _ := p.b.packument(t, "tagged"); len(v) != 3 {
		t.Errorf("tagged has %d versions on rebuilt b, want 3", len(v))
	}

	if tag := p.b.tag(t, "tagged", "stable"); tag != "1.0.0" {
		t.Errorf("rebuilt b stable tag = %q, want 1.0.0", tag)
	}

	// The rebuilt instance has a new log; A noticed and is healthy again.
	poll(t, "a healthy against rebuilt b", fedWait, func() bool {
		return p.idle(t, fedRawRepo) && p.idle(t, fedNPMRepo)
	})

	for path, w := range want {
		if code, body := p.a.getRaw(t, path); code != w.code || (code == http.StatusOK && body != w.body) {
			t.Errorf("a changed %s during the rebuild", path)
		}
	}
}
