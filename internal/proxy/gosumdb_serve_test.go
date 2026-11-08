// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/mod/sumdb"
)

const relayHost = "sumdb.oa.test"

// relayClientOps is the sumdb.ClientOps of a go command that reaches the
// checksum database only through a Go proxy repository: ReadRemote goes
// through the relay and the paths it asked for are recorded.
type relayClientOps struct {
	t      *testing.T
	e      *goEnv
	vkey   string
	mu     sync.Mutex
	latest []byte
	paths  []string
}

func (o *relayClientOps) ReadRemote(path string) ([]byte, error) {
	o.paths = append(o.paths, path)

	w := o.e.get("sumdb/" + relayHost + path)
	if w.Code != http.StatusOK {
		return nil, os.ErrNotExist
	}

	return w.Body.Bytes(), nil
}

func (o *relayClientOps) ReadConfig(file string) ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	switch file {
	case "key":
		return []byte(o.vkey), nil
	case relayHost + "/latest":
		return bytes.Clone(o.latest), nil
	}

	return nil, os.ErrNotExist
}

func (o *relayClientOps) WriteConfig(file string, oldData, newData []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if file != relayHost+"/latest" || !bytes.Equal(o.latest, oldData) {
		return sumdb.ErrWriteConflict
	}

	o.latest = bytes.Clone(newData)

	return nil
}

func (o *relayClientOps) ReadCache(string) ([]byte, error) { return nil, os.ErrNotExist }
func (o *relayClientOps) WriteCache(string, []byte)        {}
func (o *relayClientOps) Log(string)                       {}
func (o *relayClientOps) SecurityError(msg string)         { o.t.Errorf("security error: %s", msg) }

// newRelayEnv is a go proxy whose relay reaches the in-memory checksum
// database of newGoEnv under the name relayHost; the module verification is off
// so that only the relay talks to the database.
func newRelayEnv(t *testing.T) (*goEnv, string) {
	t.Helper()

	e := newGoEnv(t)
	vkey := e.svc.sumdb.vkey
	e.svc.sumdb = nil
	e.svc.sumdbUpstreams = map[string]string{relayHost: e.sumdb.URL}

	return e, vkey
}

func TestGoSumdbRelay_ClientVerifiesThroughTheProxy(t *testing.T) {
	e, vkey := newRelayEnv(t)
	ops := &relayClientOps{t: t, e: e, vkey: vkey}

	// The zip and the go.mod are separate records of one lookup.
	for _, suffix := range []string{"", "/go.mod"} {
		lines, err := sumdb.NewClient(ops).Lookup(goMod1, goVersion1+suffix)
		if err != nil {
			t.Fatalf("Lookup%s through the relay: %v", suffix, err)
		}

		if len(lines) != 1 || !strings.Contains(string(e.known[goMod1+"@"+goVersion1]), lines[0]) {
			t.Errorf("go.sum lines of %q = %q, not among %q", suffix, lines, e.known[goMod1+"@"+goVersion1])
		}
	}

	if !slices.ContainsFunc(ops.paths, func(p string) bool { return strings.HasPrefix(p, "/tile/") }) {
		t.Fatalf("the client read no tile through the relay: %v", ops.paths)
	}

	// The upstream goes away: every tile read so far is still served (it never
	// changes), a lookup that was never made is a gateway error.
	e.sumdb.Close()

	for _, p := range ops.paths {
		if !strings.HasPrefix(p, "/tile/") {
			continue
		}

		if w := e.get("sumdb/" + relayHost + p); w.Code != http.StatusOK {
			t.Errorf("cached tile %s with the upstream down: status %d, want 200", p, w.Code)
		}
	}

	if w := e.get("sumdb/" + relayHost + "/lookup/example.com/other@v1.0.0"); w.Code != http.StatusBadGateway {
		t.Errorf("uncached lookup with the upstream down: status %d, want 502", w.Code)
	}
}

func TestGoSumdbRelay_ServesOnlyAllowedDatabases(t *testing.T) {
	e, _ := newRelayEnv(t)

	tests := []struct {
		name, method, path string
		code               int
		upstream           int32 // requests the database saw
	}{
		{"supported is answered locally", http.MethodGet, "sumdb/" + relayHost + "/supported", http.StatusOK, 0},
		{"supported by HEAD", http.MethodHead, "sumdb/" + relayHost + "/supported", http.StatusOK, 0},
		{"other database supported", http.MethodGet, "sumdb/evil.example/supported", http.StatusNotFound, 0},
		{"other database latest", http.MethodGet, "sumdb/evil.example/latest", http.StatusNotFound, 0},
		{"other database tile", http.MethodGet, "sumdb/evil.example/tile/8/0/000", http.StatusNotFound, 0},
		{"host case differs", http.MethodGet, "sumdb/SUMDB.oa.test/supported", http.StatusNotFound, 0},
		{"no endpoint", http.MethodGet, "sumdb/" + relayHost + "/", http.StatusNotFound, 0},
		{"unknown endpoint", http.MethodGet, "sumdb/" + relayHost + "/config", http.StatusNotFound, 0},
		{"lookup of a query", http.MethodGet, "sumdb/" + relayHost + "/lookup/example.com/mod@master", http.StatusNotFound, 0},
		{"lookup without version", http.MethodGet, "sumdb/" + relayHost + "/lookup/example.com/mod", http.StatusNotFound, 0},
		{"lookup of a bad module path", http.MethodGet, "sumdb/" + relayHost + "/lookup/nodot/mod@v1.0.0", http.StatusNotFound, 0},
		{"tile index too short", http.MethodGet, "sumdb/" + relayHost + "/tile/8/0/1", http.StatusNotFound, 0},
		{"tile partial width too wide", http.MethodGet, "sumdb/" + relayHost + "/tile/8/0/000.p/256", http.StatusNotFound, 0},
		{"tile height out of range", http.MethodGet, "sumdb/" + relayHost + "/tile/40/0/000", http.StatusNotFound, 0},
		{"tile path escapes", http.MethodGet, "sumdb/" + relayHost + "/tile/8/0/../../../latest", http.StatusNotFound, 0},
		{"latest", http.MethodGet, "sumdb/" + relayHost + "/latest", http.StatusOK, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := e.sumdbReq.Load()

			w := e.do(tt.method, tt.path)
			if w.Code != tt.code {
				t.Fatalf("status %d, want %d (%s)", w.Code, tt.code, w.Body.String())
			}

			if got := e.sumdbReq.Load() - before; got != tt.upstream {
				t.Errorf("database saw %d requests, want %d", got, tt.upstream)
			}
		})
	}

	if w := e.get("sumdb/" + relayHost + "/supported"); w.Body.Len() != 0 {
		t.Errorf("supported body = %q, want empty", w.Body.String())
	}
}

func TestGoSumdbRelay_LatestIsCached(t *testing.T) {
	e, _ := newRelayEnv(t)

	first := e.get("sumdb/" + relayHost + "/latest")
	second := e.get("sumdb/" + relayHost + "/latest")

	if first.Code != http.StatusOK || second.Code != http.StatusOK || first.Body.String() != second.Body.String() {
		t.Fatalf("latest: %d then %d", first.Code, second.Code)
	}

	if got := e.sumdbReq.Load(); got != 1 {
		t.Errorf("database saw %d requests for two reads of latest, want 1", got)
	}
}

func TestGoSumdbRelay_RefusesBodiesThatAreNotTheFile(t *testing.T) {
	e, _ := newRelayEnv(t)
	e.repo.UpstreamUsername = "builder"

	var (
		requests atomic.Int32
		sawAuth  atomic.Bool
	)

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)

		if r.Header.Get("Authorization") != "" {
			sawAuth.Store(true)
		}

		switch {
		case strings.HasPrefix(r.URL.Path, "/tile/8/0/"):
			_, _ = w.Write(bytes.Repeat([]byte{1}, 31)) // not a multiple of the hash size
		case strings.HasPrefix(r.URL.Path, "/tile/8/data/"):
			w.WriteHeader(http.StatusOK) // empty
		default:
			_, _ = w.Write([]byte("<html>captive portal</html>"))
		}
	}))
	t.Cleanup(fake.Close)

	e.svc.sumdbUpstreams[relayHost] = fake.URL

	for _, p := range []string{"/latest", "/lookup/example.com/mod@v1.0.0", "/tile/8/0/000", "/tile/8/0/000.p/3", "/tile/8/data/000"} {
		before := requests.Load()

		if w := e.get("sumdb/" + relayHost + p); w.Code != http.StatusBadGateway {
			t.Errorf("GET %s: status %d, want 502", p, w.Code)
		}

		// Nothing was stored: the next request asks the database again.
		if w := e.get("sumdb/" + relayHost + p); w.Code != http.StatusBadGateway {
			t.Errorf("second GET %s: status %d, want 502", p, w.Code)
		}

		if got := requests.Load() - before; got != 2 {
			t.Errorf("GET %s twice: database saw %d requests, want 2", p, got)
		}
	}

	if sawAuth.Load() {
		t.Error("the repository's upstream credentials were sent to the checksum database")
	}
}

func TestGoSumdbRelay_Allowlist(t *testing.T) {
	e := newGoEnv(t)

	// The default is the public database over https.
	wantDefault := map[string]string{"sum.golang.org": "https://sum.golang.org"}
	if got := NewService(e.svc.log, e.db, nil).sumdbUpstreams; !maps.Equal(got, wantDefault) {
		t.Errorf("default allow-list = %v", got)
	}

	e.svc.WithGoSumdbHosts([]string{" Sum.Golang.Org ", "", "db.example:8443"})

	want := map[string]string{"sum.golang.org": "https://sum.golang.org", "db.example:8443": "https://db.example:8443"}
	if !maps.Equal(e.svc.sumdbUpstreams, want) {
		t.Errorf("allow-list = %v, want %v", e.svc.sumdbUpstreams, want)
	}

	// An empty list turns the relay off.
	e.svc.WithGoSumdbHosts(nil)

	if w := e.get("sumdb/sum.golang.org/supported"); w.Code != http.StatusNotFound {
		t.Errorf("relay switched off: status %d, want 404", w.Code)
	}
}

func TestGoSumdbImmutable(t *testing.T) {
	for key, want := range map[string]bool{
		"sumdb/sum.golang.org/tile/8/0/000":                  true,
		"sumdb/sum.golang.org/tile/8/0/x001/234.p/12":        true,
		"sumdb/sum.golang.org/tile/8/data/000":               true,
		"sumdb/sum.golang.org/latest":                        false,
		"sumdb/sum.golang.org/supported":                     false,
		"sumdb/sum.golang.org/lookup/example.com/mod@v1.0.0": false,
		"example.com/mod/@v/v1.0.0.zip":                      true,
		"example.com/mod/@v/list":                            false,
	} {
		if got := goImmutable(key); got != want {
			t.Errorf("goImmutable(%q) = %v, want %v", key, got, want)
		}
	}
}
