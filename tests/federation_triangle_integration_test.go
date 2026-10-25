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
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	fedDockerRepo = "fed-docker"
	// fedBigBlob is the size of the blob whose transfer is cut mid-way.
	fedBigBlob = 64 << 20
	// fedCutAfter is how much of the big blob crosses before each cut.
	fedCutAfter = 20 << 20
)

var errFedCut = errors.New("connection cut by the fault-injection proxy")

// fedLink is the network path from one node to a peer: a reverse proxy the
// test can cut, and that can kill blob transfers part of the way through.
type fedLink struct {
	url  string
	mode atomic.Int32 // proxyUp or proxyCut

	// cutsLeft blob responses are aborted after cutAfter body bytes.
	cutsLeft atomic.Int32
	cutAfter atomic.Int64
	// blobBytes counts the blob body bytes that crossed the link.
	blobBytes atomic.Int64
}

func newFedLink(t *testing.T, target string) *fedLink {
	t.Helper()

	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}

	l := &fedLink{}
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.ErrorLog = log.New(io.Discard, "", 0) // aborted transfers are the point
	rp.ModifyResponse = func(resp *http.Response) error {
		if resp.Request.URL.Path != "/federation/v1/blob" {
			return nil
		}

		left := int64(-1)

		if resp.ContentLength > l.cutAfter.Load() && l.cutsLeft.Add(-1) >= 0 {
			left = l.cutAfter.Load()
		}

		resp.Body = &fedCountBody{ReadCloser: resp.Body, link: l, left: left}

		return nil
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l.mode.Load() == proxyCut {
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()

				return
			}
		}

		rp.ServeHTTP(w, r)
	}))

	t.Cleanup(srv.Close)

	l.url = srv.URL

	return l
}

// fedCountBody counts the bytes of a blob response and, when left >= 0, fails
// after that many.
type fedCountBody struct {
	io.ReadCloser

	link *fedLink
	left int64
}

func (b *fedCountBody) Read(p []byte) (int, error) {
	if b.left == 0 {
		return 0, errFedCut
	}

	if b.left > 0 && int64(len(p)) > b.left {
		p = p[:b.left]
	}

	n, err := b.ReadCloser.Read(p)
	b.link.blobBytes.Add(int64(n))

	if b.left > 0 {
		b.left -= int64(n)
	}

	return n, err //nolint:wrapcheck // io.Reader contract
}

// fedMesh is three instances that pull from each other for every repository.
type fedMesh struct {
	nodes []*fedNode
	// links[puller][source] is how the puller reaches the source.
	links map[*fedNode]map[*fedNode]*fedLink
}

// fedMeshRepos are the federated repositories every node holds.
func fedMeshRepos() []string { return []string{fedRawRepo, fedNPMRepo, fedDockerRepo} }

func (n *fedNode) createFederatedRepo(t *testing.T, name, format string) {
	t.Helper()

	n.call(t, http.MethodPost, "/api/v1/repositories",
		map[string]any{"name": name, "format": format, "repo_type": "hosted", "federated": true}, http.StatusCreated)
}

func startFedMesh(t *testing.T) *fedMesh {
	t.Helper()

	admin := setupTestDB(t)
	bin := filepath.Join(t.TempDir(), "openartifact")
	goBuild(t, repoRoot(t), bin, "./cmd/server")

	m := &fedMesh{links: map[*fedNode]map[*fedNode]*fedLink{}}

	for _, name := range []string{"a", "b", "c"} {
		n := newFedNode(t, admin, bin, name)
		n.freshDatabase(t)
		n.start(t)

		for _, repo := range []struct{ name, format string }{{fedRawRepo, "raw"}, {fedNPMRepo, "npm"}, {fedDockerRepo, "docker"}} {
			n.createFederatedRepo(t, repo.name, repo.format)
		}

		m.nodes = append(m.nodes, n)
		m.links[n] = map[*fedNode]*fedLink{}
	}

	t.Cleanup(func() {
		if t.Failed() {
			for _, n := range m.nodes {
				t.Logf("--- %s stderr ---\n%s", n.name, n.stderr.String())
			}
		}
	})

	for _, puller := range m.nodes {
		for _, source := range m.nodes {
			if puller == source {
				continue
			}

			link := newFedLink(t, source.url)
			m.links[puller][source] = link

			for _, repo := range fedMeshRepos() {
				puller.addPeer(t, repo, source.name, link.url, source.issueToken(t, repo, "for-"+puller.name))
			}
		}
	}

	return m
}

// setAll cuts or restores every link.
func (m *fedMesh) setAll(mode int32) {
	for _, row := range m.links {
		for _, l := range row {
			l.mode.Store(mode)
		}
	}
}

// idle reports whether every node has caught up with every peer, in all repos.
func (m *fedMesh) idle(t *testing.T) bool {
	t.Helper()

	for _, n := range m.nodes {
		for _, repo := range fedMeshRepos() {
			st := n.status(t, repo)
			if len(st.Peers) != len(m.nodes)-1 {
				return false
			}

			for _, p := range st.Peers {
				if p.State != "ok" || p.BacklogUnits != 0 || p.CursorSeq != p.RemoteHeadSeq {
					return false
				}
			}
		}
	}

	return true
}

// everywhere polls until check holds on every node.
func (m *fedMesh) everywhere(t *testing.T, what string, check func(n *fedNode) bool) {
	t.Helper()

	poll(t, what, fedWait, func() bool {
		for _, n := range m.nodes {
			if !check(n) {
				return false
			}
		}

		return true
	})
}

func (m *fedMesh) rawIs(t *testing.T, path string, code int, body string) func(n *fedNode) bool {
	t.Helper()

	return func(n *fedNode) bool {
		c, b := n.getRaw(t, path)

		return c == code && (code != http.StatusOK || b == body)
	}
}

// fedImage is a minimal OCI image: a config blob, one layer and a manifest.
type fedImage struct {
	config, layer []byte
	manifest      []byte
}

func fedDigest(b []byte) string {
	s := sha256.Sum256(b)

	return "sha256:" + hex.EncodeToString(s[:])
}

func newFedImage(name string) *fedImage {
	layer := bytes.Repeat([]byte("layer of "+name+"\n"), 200)
	config := []byte(fmt.Sprintf(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[%q]},"config":{"Labels":{"name":%q}}}`, fedDigest(layer), name))
	manifest, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config":        map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": fedDigest(config), "size": len(config)},
		"layers":        []any{map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": fedDigest(layer), "size": len(layer)}},
	})

	return &fedImage{config: config, layer: layer, manifest: manifest}
}

// pushImage uploads the image to the node as fed-docker/<image>:<tag>.
func (n *fedNode) pushImage(t *testing.T, image, tag string, img *fedImage) {
	t.Helper()

	base := "/v2/" + fedDockerRepo + "/" + image

	for _, blob := range [][]byte{img.config, img.layer} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, n.url+base+"/blobs/uploads/", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+n.token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		_ = resp.Body.Close()

		loc := resp.Header.Get("Location")
		if resp.StatusCode != http.StatusAccepted || loc == "" {
			t.Fatalf("%s start upload: HTTP %d", n.name, resp.StatusCode)
		}

		sep := "?"
		if u, err := url.Parse(loc); err == nil && u.RawQuery != "" {
			sep = "&"
		}

		if code, body := n.do(t, http.MethodPut, loc+sep+"digest="+fedDigest(blob), "application/octet-stream", blob); code != http.StatusCreated {
			t.Fatalf("%s upload blob: HTTP %d: %s", n.name, code, body)
		}
	}

	if code, body := n.do(t, http.MethodPut, base+"/manifests/"+tag, "application/vnd.oci.image.manifest.v1+json", img.manifest); code != http.StatusCreated {
		t.Fatalf("%s push manifest: HTTP %d: %s", n.name, code, body)
	}
}

// image reports whether the node serves the tag with the given manifest and layer.
func (n *fedNode) hasImage(t *testing.T, image, tag string, img *fedImage) bool {
	t.Helper()

	base := "/v2/" + fedDockerRepo + "/" + image

	code, body := n.do(t, http.MethodGet, base+"/manifests/"+tag, "", nil)
	if code != http.StatusOK || !bytes.Equal(body, img.manifest) {
		return false
	}

	code, body = n.do(t, http.MethodGet, base+"/blobs/"+fedDigest(img.layer), "", nil)

	return code == http.StatusOK && bytes.Equal(body, img.layer)
}

func (n *fedNode) hasPackage(t *testing.T, pkg string) bool {
	t.Helper()

	versions, _ := n.packument(t, pkg)

	return len(versions) == 1
}

var fedMetricRe = regexp.MustCompile(`(?m)^(\w+)(\{[^}]*\})? ([0-9.eE+-]+)$`)

// metricSum scrapes /metrics and sums the series of name whose labels contain
// every want substring.
func (n *fedNode) metricSum(t *testing.T, name string, want ...string) float64 {
	t.Helper()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, n.url+"/metrics", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+fedMetricsToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)

	var sum float64

next:
	for _, m := range fedMetricRe.FindAllStringSubmatch(string(body), -1) {
		if m[1] != name {
			continue
		}

		for _, w := range want {
			if !strings.Contains(m[2], w) {
				continue next
			}
		}

		v, _ := strconv.ParseFloat(m[3], 64)
		sum += v
	}

	return sum
}

// conflicts is the number of superseded local writes the node counted.
func (n *fedNode) conflicts(t *testing.T, repo string) int64 {
	t.Helper()

	var total int64

	for _, p := range n.status(t, repo).Peers {
		total += p.ConflictsTotal
	}

	return total
}

// waitClockTick returns once the wall clock has moved past the millisecond of
// the last write, so the next write carries a strictly later stamp.
func waitClockTick(t *testing.T) {
	t.Helper()

	start := time.Now().UnixMilli()

	poll(t, "the clock to advance", fedWait, func() bool { return time.Now().UnixMilli() > start+2 })
}

func TestFederationTriangle(t *testing.T) {
	m := startFedMesh(t)
	for _, sub := range []struct {
		name string
		run  func(*testing.T, *fedMesh)
	}{
		{"raw, npm and docker published on each node converge everywhere", fedTriContent},
		{"a delete converges as a tombstone and is not resurrected", fedTriTombstone},
		{"the same key written on all three while partitioned resolves to the last writer", fedTriConflict},
		{"a blob transfer cut mid-way is resumed, not restarted", fedTriResume},
	} {
		t.Run(sub.name, func(t *testing.T) { sub.run(t, m) })
	}
}

func fedTriContent(t *testing.T, m *fedMesh) {
	t.Helper()

	images := map[*fedNode]*fedImage{}

	for _, n := range m.nodes {
		n.putRaw(t, "content/from-"+n.name+".txt", "raw written on "+n.name)
		n.publishNPM(t, "pkg-from-"+n.name, "1.0.0")

		images[n] = newFedImage(n.name)
		n.pushImage(t, "app-"+n.name, "v1", images[n])
	}

	m.everywhere(t, "all content on all nodes", func(n *fedNode) bool {
		for _, src := range m.nodes {
			code, body := n.getRaw(t, "content/from-"+src.name+".txt")
			if code != http.StatusOK || body != "raw written on "+src.name {
				return false
			}

			if !n.hasPackage(t, "pkg-from-"+src.name) || !n.hasImage(t, "app-"+src.name, "v1", images[src]) {
				return false
			}
		}

		return true
	})

	poll(t, "mesh idle", fedWait, func() bool { return m.idle(t) })
}

func fedTriTombstone(t *testing.T, m *fedMesh) {
	t.Helper()

	a := m.nodes[0]

	a.putRaw(t, "tomb/doomed.txt", "short lived")
	m.everywhere(t, "doomed everywhere", m.rawIs(t, "tomb/doomed.txt", http.StatusOK, "short lived"))

	a.deleteRaw(t, "tomb/doomed.txt")
	m.everywhere(t, "tombstone everywhere", m.rawIs(t, "tomb/doomed.txt", http.StatusNotFound, ""))
	poll(t, "mesh idle", fedWait, func() bool { return m.idle(t) })

	// A full resync of a node that is not the origin must not bring it back.
	b := m.nodes[1]
	for _, p := range b.status(t, fedRawRepo).Peers {
		b.call(t, http.MethodPost, fedAPI(fedRawRepo)+"/peers/"+p.ID+"/resync", nil, http.StatusAccepted, http.StatusOK, http.StatusNoContent)
	}

	poll(t, "resync done", fedWait, func() bool {
		if !m.idle(t) {
			return false
		}

		for _, p := range b.status(t, fedRawRepo).Peers {
			if p.Resync {
				return false
			}
		}

		return true
	})

	m.everywhere(t, "still deleted", m.rawIs(t, "tomb/doomed.txt", http.StatusNotFound, ""))
}

func fedTriConflict(t *testing.T, m *fedMesh) {
	t.Helper()

	a, b, c := m.nodes[0], m.nodes[1], m.nodes[2]

	m.setAll(proxyCut)

	for _, n := range []*fedNode{a, b, c} {
		n.putRaw(t, "lww/shared.txt", "written by "+n.name)
		waitClockTick(t)
	}

	m.setAll(proxyUp)

	// c wrote last: its value wins on every node.
	m.everywhere(t, "last writer wins everywhere", m.rawIs(t, "lww/shared.txt", http.StatusOK, "written by c"))
	poll(t, "mesh idle", fedWait, func() bool { return m.idle(t) })

	for _, n := range []*fedNode{a, b} {
		if got := n.conflicts(t, fedRawRepo); got < 1 {
			t.Errorf("%s: conflicts_total = %d, want >= 1 (its own write was superseded)", n.name, got)
		}
	}
}

func fedTriResume(t *testing.T, m *fedMesh) {
	t.Helper()

	a, b := m.nodes[0], m.nodes[1]
	viaA := m.links[b][a]

	// b can only get the blob from a, through the link that kills transfers.
	m.links[b][m.nodes[2]].mode.Store(proxyCut)

	viaA.cutAfter.Store(fedCutAfter)
	viaA.cutsLeft.Store(2)

	before := viaA.blobBytes.Load()
	resumedBefore := b.metricSum(t, "openartifact_federation_resumed_total", `peer="a"`, `repo="`+fedRawRepo+`"`)

	big := make([]byte, fedBigBlob)
	rand.NewChaCha8([32]byte{1}).Read(big)

	sum := sha256.Sum256(big)
	want := hex.EncodeToString(sum[:])

	if code, body := a.do(t, http.MethodPut, "/repos/raw/"+fedRawRepo+"/big/blob.bin", "application/octet-stream", big); code != http.StatusCreated {
		t.Fatalf("a: put big blob: HTTP %d: %s", code, body)
	}

	poll(t, "big blob on b", 2*time.Minute, func() bool {
		code, data := b.do(t, http.MethodGet, "/repos/raw/"+fedRawRepo+"/big/blob.bin", "", nil)
		if code != http.StatusOK {
			return false
		}

		got := sha256.Sum256(data)

		return hex.EncodeToString(got[:]) == want
	})

	if left := viaA.cutsLeft.Load(); left > 0 {
		t.Errorf("only %d of 2 transfers were cut: the blob did not cross the faulty link twice", 2-left)
	}

	moved := viaA.blobBytes.Load() - before
	if moved < fedBigBlob || moved >= 2*fedBigBlob {
		t.Errorf("%d blob bytes crossed the faulty link for a %d byte blob, want at least the blob and less than twice it", moved, fedBigBlob)
	}

	t.Logf("%d blob bytes crossed the faulty link for a %d byte blob", moved, fedBigBlob)

	if got := b.metricSum(t, "openartifact_federation_resumed_total", `peer="a"`, `repo="`+fedRawRepo+`"`); got <= resumedBefore {
		t.Errorf("resumed_total = %v (was %v), want it to count the resumed transfers", got, resumedBefore)
	}

	m.links[b][m.nodes[2]].mode.Store(proxyUp)
	m.everywhere(t, "big blob everywhere", func(n *fedNode) bool {
		code, data := n.do(t, http.MethodGet, "/repos/raw/"+fedRawRepo+"/big/blob.bin", "", nil)
		if code != http.StatusOK {
			return false
		}

		got := sha256.Sum256(data)

		return hex.EncodeToString(got[:]) == want
	})
	poll(t, "mesh idle", fedWait, func() bool { return m.idle(t) })
}
