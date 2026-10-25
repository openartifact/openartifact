// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/ai"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	testRepo     = "repo-1"
	waitDeadline = 10 * time.Second
)

var errBlobGone = errors.New("blob gone: /secret/internal/path")

// aiFakeDB records analyses and serves a fixed package listing.
type aiFakeDB struct {
	databasetest.BaseMockDB

	mu       sync.Mutex
	blobs    map[string]*models.ComponentBlob
	listing  []*models.Component
	saved    map[string]*models.AIAnalysis
	conanKey models.ConanFileKey
	conanRow *models.ConanPackage
	updates  chan struct{}
}

func newAIFakeDB() *aiFakeDB {
	return &aiFakeDB{
		blobs:   map[string]*models.ComponentBlob{},
		saved:   map[string]*models.AIAnalysis{},
		updates: make(chan struct{}, 1000),
	}
}

func (d *aiFakeDB) GetRepoComponentBlob(_ context.Context, _, id string) (*models.ComponentBlob, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.blobs[id], nil
}

func (d *aiFakeDB) UpsertAIAnalysis(_ context.Context, a *models.AIAnalysis) error {
	d.mu.Lock()
	cp := *a
	d.saved[a.ArtifactID] = &cp
	d.mu.Unlock()

	d.updates <- struct{}{}

	return nil
}

func (d *aiFakeDB) SearchComponents(context.Context, string, database.ComponentListOptions) ([]*models.Component, int, error) {
	return d.listing, len(d.listing), nil
}

func (d *aiFakeDB) GetConanPackage(_ context.Context, _ string, key models.ConanFileKey) (*models.ConanPackage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.conanKey = key

	if key.Filename == "conanfile.py" {
		return d.conanRow, nil
	}

	return nil, nil
}

func (d *aiFakeDB) get(id string) *models.AIAnalysis {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.saved[id]
}

// await blocks until the record for id satisfies ok, failing at the deadline.
func (d *aiFakeDB) await(t *testing.T, id string, ok func(*models.AIAnalysis) bool) *models.AIAnalysis {
	t.Helper()

	deadline := time.After(waitDeadline)

	for {
		if a := d.get(id); a != nil && ok(a) {
			return a
		}

		select {
		case <-d.updates:
		case <-deadline:
			t.Fatalf("timed out waiting for %s; last record: %+v", id, d.get(id))
		}
	}
}

func (d *aiFakeDB) awaitStatus(t *testing.T, id, status string) *models.AIAnalysis {
	t.Helper()

	return d.await(t, id, func(a *models.AIAnalysis) bool { return a.Status == status })
}

type fakeBlobs struct {
	storage.BlobStore

	data map[string][]byte
}

func (f fakeBlobs) Get(_ context.Context, id string) (io.ReadCloser, error) {
	b, ok := f.data[id]
	if !ok {
		return nil, errBlobGone
	}

	return io.NopCloser(bytes.NewReader(b)), nil
}

// fakeLLM is an OpenAI-compatible chat endpoint that records what it is sent.
type fakeLLM struct {
	srv *httptest.Server

	mu       sync.Mutex
	prompts  []string
	requests []string // model requested
}

func newFakeLLM(t *testing.T) *fakeLLM {
	t.Helper()

	t.Setenv(netutil.EnvAllowPrivate, "true")

	l := &fakeLLM{}

	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}

		_ = json.NewDecoder(r.Body).Decode(&req)

		l.mu.Lock()
		l.requests = append(l.requests, req.Model)

		for _, m := range req.Messages {
			if m.Role == "user" {
				l.prompts = append(l.prompts, m.Content)
			}
		}
		l.mu.Unlock()

		content := `{"artifact_summary":"a fine package","tags":["demo"],"license_status":"SAFE","license_name":"MIT","security_notes":"","quarantine_recommended":false}`
		resp := map[string]any{
			"model":   req.Model + "-snapshot",
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
			"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 7},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))

	t.Cleanup(l.srv.Close)

	return l
}

func (l *fakeLLM) calls() ([]string, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.prompts...), append([]string(nil), l.requests...)
}

func (l *fakeLLM) settings(model string, auto bool) ai.Settings {
	return ai.Settings{
		Enabled:     true,
		AutoAnalyze: auto,
		Config:      ai.Config{Provider: "custom", BaseURL: l.srv.URL, Model: model, TimeoutSecs: 10},
	}
}

func testLog(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	return log
}

func startWorker(t *testing.T, db database.Database, blobs storage.BlobStore) *AIAnalysisWorker {
	t.Helper()

	w := NewAIAnalysisWorker(db, nil, testLog(t)).WithDebounce(time.Millisecond)
	if blobs != nil {
		w.WithBlobStore(blobs)
	}

	w.Start()
	t.Cleanup(w.Stop)

	return w
}

func npmTgz(t *testing.T, pkgJSON string) []byte {
	t.Helper()

	var tarBuf bytes.Buffer

	tw := tar.NewWriter(&tarBuf)
	_ = tw.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o644, Size: int64(len(pkgJSON)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(pkgJSON))
	_ = tw.Close()

	var out bytes.Buffer

	zw := gzip.NewWriter(&out)
	_, _ = zw.Write(tarBuf.Bytes())
	_ = zw.Close()

	return out.Bytes()
}

func TestWorkerExtractsManifestFromStoredArtifact(t *testing.T) {
	llm := newFakeLLM(t)
	db := newAIFakeDB()
	pkgJSON := `{"name":"left-pad","version":"1.2.3","license":"MIT"}`
	db.blobs["art-1"] = &models.ComponentBlob{ID: "art-1", Format: "npm", Filename: "left-pad-1.2.3.tgz", Name: "left-pad", Version: "1.2.3", SHA256: "sha-1"}

	w := startWorker(t, db, fakeBlobs{data: map[string][]byte{"sha-1": npmTgz(t, pkgJSON)}})
	w.Configure(llm.settings("test-model", false))

	w.Notify(AIAnalysisRequest{RepositoryID: testRepo, ArtifactID: "art-1"})

	got := db.awaitStatus(t, "art-1", "completed")

	if got.ArtifactName != "left-pad" || got.ArtifactVersion != "1.2.3" || got.ArtifactFormat != "npm" {
		t.Errorf("identity = %q %q %q", got.ArtifactFormat, got.ArtifactName, got.ArtifactVersion)
	}

	if got.ModelUsed != "test-model-snapshot" {
		t.Errorf("model_used = %q, want the model the provider reported", got.ModelUsed)
	}

	if got.PromptTokens != 11 || got.CompletionTokens != 7 || got.Summary != "a fine package" {
		t.Errorf("result not stored: %+v", got)
	}

	prompts, _ := llm.calls()
	if len(prompts) != 1 || !strings.Contains(prompts[0], pkgJSON) {
		t.Errorf("the LLM did not receive the extracted package.json: %q", prompts)
	}
}

func TestWorkerCallerManifestOverridesExtraction(t *testing.T) {
	llm := newFakeLLM(t)
	db := newAIFakeDB() // artifact unknown to the database
	w := startWorker(t, db, nil)
	w.Configure(llm.settings("m", false))

	w.Notify(AIAnalysisRequest{
		RepositoryID: testRepo, ArtifactID: "ext-1", ArtifactFormat: "npm", ArtifactName: "x", ArtifactVersion: "9", ManifestText: "caller supplied",
	})

	got := db.awaitStatus(t, "ext-1", "completed")

	if prompts, _ := llm.calls(); len(prompts) != 1 || prompts[0] != "caller supplied" {
		t.Errorf("prompts = %q", prompts)
	}

	if got.ArtifactName != "x" || got.ArtifactVersion != "9" {
		t.Errorf("caller identity lost: %+v", got)
	}
}

func TestWorkerRecordsFixedFailureReasons(t *testing.T) {
	llm := newFakeLLM(t)
	db := newAIFakeDB()
	db.blobs["docker-1"] = &models.ComponentBlob{ID: "docker-1", Format: "docker", Filename: "sha256:abc", Name: "img", Version: "1", SHA256: "sha-d"}
	db.blobs["gone-1"] = &models.ComponentBlob{ID: "gone-1", Format: "npm", Filename: "g.tgz", Name: "g", Version: "1", SHA256: "sha-missing"}
	db.blobs["nosha-1"] = &models.ComponentBlob{ID: "nosha-1", Format: "npm", Filename: "n.tgz", Name: "n", Version: "1"}

	w := startWorker(t, db, fakeBlobs{data: map[string][]byte{"sha-d": []byte("{}")}})
	w.Configure(llm.settings("m", false))

	tests := []struct {
		id   string
		want string
	}{
		{"missing-1", aiErrArtifactNotFound},
		{"docker-1", aiErrUnsupported},
		{"gone-1", aiErrExtract},
		{"nosha-1", aiErrNoFile},
	}

	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			w.Notify(AIAnalysisRequest{RepositoryID: testRepo, ArtifactID: tc.id})

			got := db.awaitStatus(t, tc.id, "failed")
			if got.ErrorMessage != tc.want {
				t.Errorf("error_message = %q, want %q", got.ErrorMessage, tc.want)
			}

			if strings.Contains(got.ErrorMessage, "/secret") {
				t.Errorf("internal detail leaked: %q", got.ErrorMessage)
			}
		})
	}

	if prompts, _ := llm.calls(); len(prompts) != 0 {
		t.Errorf("the LLM was called for artifacts with no readable manifest: %q", prompts)
	}
}

func TestWorkerWithoutBlobStoreAsksForManifestText(t *testing.T) {
	llm := newFakeLLM(t)
	db := newAIFakeDB()
	db.blobs["a"] = &models.ComponentBlob{ID: "a", Format: "npm", Filename: "a.tgz", Name: "a", Version: "1", SHA256: "s"}

	w := startWorker(t, db, nil)
	w.Configure(llm.settings("m", false))
	w.Notify(AIAnalysisRequest{RepositoryID: testRepo, ArtifactID: "a"})

	if got := db.awaitStatus(t, "a", "failed"); got.ErrorMessage != aiErrNoStore {
		t.Errorf("error_message = %q", got.ErrorMessage)
	}
}

func TestWorkerDisabledLeavesPending(t *testing.T) {
	db := newAIFakeDB()
	w := startWorker(t, db, nil)

	if w.Enabled() || w.AutoAnalyze() {
		t.Fatal("a worker without a client must report disabled")
	}

	w.Notify(AIAnalysisRequest{RepositoryID: testRepo, ArtifactID: "p-1", ArtifactFormat: "npm", ManifestText: "x"})
	db.awaitStatus(t, "p-1", "pending")
}

// TestConfigureAppliesWithoutRestart swaps the model and the endpoint on a
// running worker and checks the next analysis uses them.
func TestConfigureAppliesWithoutRestart(t *testing.T) {
	first, second := newFakeLLM(t), newFakeLLM(t)
	db := newAIFakeDB()
	w := startWorker(t, db, nil)

	req := func(id string) AIAnalysisRequest {
		return AIAnalysisRequest{RepositoryID: testRepo, ArtifactID: id, ArtifactFormat: "npm", ArtifactName: "n", ArtifactVersion: "1", ManifestText: "m"}
	}

	w.Configure(first.settings("model-a", false))

	if !w.Enabled() || w.AutoAnalyze() {
		t.Fatalf("enabled=%v auto=%v", w.Enabled(), w.AutoAnalyze())
	}

	w.Notify(req("one"))

	if got := db.awaitStatus(t, "one", "completed"); got.ModelUsed != "model-a-snapshot" {
		t.Errorf("model_used = %q", got.ModelUsed)
	}

	w.Configure(first.settings("model-b", true))

	if !w.AutoAnalyze() {
		t.Error("auto-analyze not applied")
	}

	w.Notify(req("two"))

	if got := db.awaitStatus(t, "two", "completed"); got.ModelUsed != "model-b-snapshot" {
		t.Errorf("model_used = %q after swapping the model", got.ModelUsed)
	}

	w.Configure(second.settings("model-c", false))
	w.Notify(req("three"))
	db.awaitStatus(t, "three", "completed")

	if _, m := second.calls(); len(m) != 1 || m[0] != "model-c" {
		t.Errorf("swapped endpoint saw %v", m)
	}

	if _, m := first.calls(); len(m) != 2 {
		t.Errorf("old endpoint saw %v, want exactly the first two analyses", m)
	}

	w.Configure(ai.Settings{Enabled: false, AutoAnalyze: true})

	if w.Enabled() || w.AutoAnalyze() {
		t.Error("disabling must switch both off, whatever auto_analyze says")
	}
}

type recordingPublisher struct {
	mu     sync.Mutex
	events []string
}

func (r *recordingPublisher) Dispatch(_ context.Context, _, eventType string, _ map[string]any) {
	r.mu.Lock()
	r.events = append(r.events, eventType)
	r.mu.Unlock()
}

func uploadEvent(name, version, sha string) map[string]any {
	return map[string]any{
		"format":   "npm",
		"artifact": map[string]any{"name": name, "version": version, "path": name + "-" + version + ".tgz", "sha256": sha},
	}
}

func TestAutoAnalyzeOnUpload(t *testing.T) {
	llm := newFakeLLM(t)
	db := newAIFakeDB()
	db.listing = []*models.Component{
		{ID: "row-new", Name: "left-pad", Version: "2.0.0", Format: "npm", Filename: "left-pad-2.0.0.tgz", SHA256: "sha-new"},
		{ID: "row-old", Name: "left-pad", Version: "1.0.0", Format: "npm", Filename: "left-pad-1.0.0.tgz", SHA256: "sha-old"},
	}

	for _, c := range db.listing {
		db.blobs[c.ID] = &models.ComponentBlob{ID: c.ID, Format: c.Format, Filename: c.Filename, Name: c.Name, Version: c.Version, SHA256: c.SHA256}
	}

	blobs := fakeBlobs{data: map[string][]byte{
		"sha-new": npmTgz(t, `{"name":"left-pad","version":"2.0.0"}`),
		"sha-old": npmTgz(t, `{"name":"left-pad","version":"1.0.0"}`),
	}}

	w := startWorker(t, db, blobs)
	next := &recordingPublisher{}
	pub := NewAIAutoAnalyzePublisher(next, w)

	// Auto-analysis off: the upload is forwarded but nothing is queued.
	w.Configure(llm.settings("m", false))
	pub.Dispatch(context.Background(), testRepo, webhook.EventArtifactUploaded, uploadEvent("left-pad", "1.0.0", "sha-old"))

	// Switch on; this upload is analysed. The worker handles notifications in
	// order, so once it has finished the first nothing for "row-old" can still
	// be on its way.
	w.Configure(llm.settings("m", true))
	pub.Dispatch(context.Background(), testRepo, webhook.EventArtifactUploaded, uploadEvent("left-pad", "2.0.0", "sha-new"))

	got := db.awaitStatus(t, "row-new", "completed")
	if got.ArtifactName != "left-pad" || got.ArtifactVersion != "2.0.0" || got.ModelUsed == "" {
		t.Errorf("record = %+v", got)
	}

	if db.get("row-old") != nil {
		t.Error("an upload with auto-analysis off was analysed")
	}

	if prompts, _ := llm.calls(); len(prompts) != 1 || !strings.Contains(prompts[0], `"version":"2.0.0"`) {
		t.Errorf("prompts = %q", prompts)
	}

	// Non-upload events pass through and never reach the worker.
	pub.Dispatch(context.Background(), testRepo, webhook.EventArtifactDeleted, uploadEvent("left-pad", "1.0.0", "sha-old"))

	next.mu.Lock()
	defer next.mu.Unlock()

	if len(next.events) != 3 || next.events[0] != webhook.EventArtifactUploaded || next.events[2] != webhook.EventArtifactDeleted {
		t.Errorf("events not forwarded to the wrapped publisher: %v", next.events)
	}
}

func TestMatchUpload(t *testing.T) {
	t.Parallel()

	comps := []*models.Component{
		{ID: "1", Name: "a", Version: "1", Filename: "a-1.tgz", SHA256: "s1"},
		{ID: "2", Name: "a", Version: "2", Filename: "org/a/2/a-2.pom", SHA256: "s2"},
		{ID: "3", Name: "b", Version: "1", Filename: "b-1.tgz", SHA256: "s3"},
	}

	tests := []struct {
		name string
		ref  UploadRef
		want []string
	}{
		{"by digest", UploadRef{SHA256: "s3", Name: "zzz"}, []string{"3"}},
		{"by path suffix", UploadRef{Path: "org/a/2/a-2.pom"}, []string{"2"}},
		{"by name and version", UploadRef{Name: "a", Version: "1"}, []string{"1"}},
		{"digest that matches nothing falls back to the path", UploadRef{SHA256: "other", Path: "b-1.tgz"}, []string{"3"}},
		{"no match", UploadRef{Name: "c", Version: "1"}, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var ids []string
			for _, c := range matchUpload(tc.ref, comps) {
				ids = append(ids, c.ID)
			}

			if strings.Join(ids, ",") != strings.Join(tc.want, ",") {
				t.Errorf("got %v, want %v", ids, tc.want)
			}
		})
	}
}

func TestParseConanReference(t *testing.T) {
	t.Parallel()

	key, ok := models.ParseConanReference("zlib/1.3@acme/stable#abc123")
	if !ok || key.Name != "zlib" || key.Version != "1.3" || key.UserChannel != "acme/stable" || key.RecipeRevision != "abc123" {
		t.Errorf("key = %+v ok=%v", key, ok)
	}

	for _, bad := range []string{"", "zlib", "zlib/1.3", "zlib/1.3@_/_", "/1.3@_/_#r"} {
		if _, ok := models.ParseConanReference(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestConanAnalysisReadsRecipeSibling(t *testing.T) {
	llm := newFakeLLM(t)
	db := newAIFakeDB()
	db.blobs["c-1"] = &models.ComponentBlob{ID: "c-1", Format: "conan", Filename: "zlib/1.3@_/_#rev1", Name: "zlib", Version: "1.3", SHA256: "sha-manifest"}
	db.conanRow = &models.ConanPackage{BlobKey: "key-recipe"}

	blobs := fakeBlobs{data: map[string][]byte{
		"sha-manifest": []byte("1700000000\n"),
		"key-recipe":   []byte("class Zlib(ConanFile):\n    name = \"zlib\"\n"),
	}}

	w := startWorker(t, db, blobs)
	w.Configure(llm.settings("m", false))
	w.Notify(AIAnalysisRequest{RepositoryID: testRepo, ArtifactID: "c-1"})

	db.awaitStatus(t, "c-1", "completed")

	if prompts, _ := llm.calls(); len(prompts) != 1 || !strings.Contains(prompts[0], "class Zlib(ConanFile)") {
		t.Errorf("prompts = %q", prompts)
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	if db.conanKey.Name != "zlib" || db.conanKey.RecipeRevision != "rev1" || db.conanKey.UserChannel != "_/_" {
		t.Errorf("recipe looked up with %+v", db.conanKey)
	}
}
