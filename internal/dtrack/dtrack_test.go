// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package dtrack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

const testKey = "odt_secret_key"

func testLogger(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	require.NoError(t, err)

	return log
}

func useEncryption(t *testing.T) {
	t.Helper()
	require.NoError(t, crypto.Init("0123456789abcdef0123456789abcdef0123456789"))
	t.Cleanup(func() { _ = crypto.Init("") })
}

// ---- fake store ----------------------------------------------------------

type fakeStore struct {
	mu       sync.Mutex
	settings map[string]string
	repoRows map[string]*models.RepositoryDependencyTrack
	repos    map[string]*models.Repository
	pushes   map[string]*models.DependencyTrackPush
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		settings: map[string]string{},
		repoRows: map[string]*models.RepositoryDependencyTrack{},
		repos:    map[string]*models.Repository{"r1": {ID: "r1", Name: "libs", Format: "npm"}},
		pushes:   map[string]*models.DependencyTrackPush{},
	}
}

func (s *fakeStore) GetSystemSetting(_ context.Context, k string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.settings[k], nil
}

func (s *fakeStore) SetSystemSetting(_ context.Context, k, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings[k] = v

	return nil
}

func (s *fakeStore) GetRepositoryDependencyTrack(_ context.Context, id string) (*models.RepositoryDependencyTrack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.repoRows[id], nil
}

func (s *fakeStore) GetRepositoryByID(_ context.Context, id string) (*models.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.repos[id], nil
}

func (s *fakeStore) UpsertDependencyTrackPush(_ context.Context, p *models.DependencyTrackPush) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	c := *p
	s.pushes[p.RepositoryID+"/"+p.ArtifactID] = &c

	return nil
}

func (s *fakeStore) GetDependencyTrackPush(_ context.Context, repoID, artifactID string) (*models.DependencyTrackPush, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if p := s.pushes[repoID+"/"+artifactID]; p != nil {
		c := *p
		return &c, nil
	}

	return nil, nil
}

func (s *fakeStore) push(repoID, artifactID string) *models.DependencyTrackPush {
	p, _ := s.GetDependencyTrackPush(context.Background(), repoID, artifactID)
	return p
}

func (s *fakeStore) waitState(t *testing.T, repoID, artifactID string, states ...string) *models.DependencyTrackPush {
	t.Helper()

	var got *models.DependencyTrackPush

	require.Eventually(t, func() bool {
		got = s.push(repoID, artifactID)
		if got == nil {
			return false
		}

		return slices.Contains(states, got.State)
	}, 5*time.Second, 5*time.Millisecond)

	return got
}

func (s *fakeStore) enableGlobal(t *testing.T, baseURL string, vex bool) {
	t.Helper()
	require.NoError(t, SaveGlobal(context.Background(), s, Settings{
		Enabled: true, BaseURL: baseURL, APIKey: testKey, AutoPush: true, PushVEX: vex,
	}))
}

// ---- fake sources ----------------------------------------------------------

type fakeSources struct {
	bomCalls atomic.Int32
	gate     chan struct{} // when set, BOM blocks until it is closed
	resolved map[string]sbom.VulnRef
	vexCount int
}

func (f *fakeSources) BOM(_ context.Context, _ *models.Repository, id string) ([]byte, int, error) {
	f.bomCalls.Add(1)

	if f.gate != nil {
		<-f.gate
	}

	return []byte(`{"bomFormat":"CycloneDX","specVersion":"1.5","x":"` + id + `"}`), 3, nil
}

func (f *fakeSources) VEX(_ context.Context, _ *models.Repository, _ string, o sbom.VEXOptions) ([]byte, int, error) {
	f.resolved = map[string]sbom.VulnRef{}

	for _, id := range []string{"CVE-2026-1", "GHSA-none"} {
		if ref, ok := o.ResolveVuln(id); ok {
			f.resolved[id] = ref
		}
	}

	if !o.DecidedOnly {
		return nil, 0, errors.New("pusher must only send decided statements")
	}

	return []byte(`{"vex":true}`), f.vexCount, nil
}

// ---- fake Dependency-Track ---------------------------------------------------

type fakeDT struct {
	srv *httptest.Server

	mu         sync.Mutex
	bomForm    map[string]string
	bomBody    string
	vexForm    map[string]string
	vexDone    bool
	busyPolls  int
	failBOM    int // HTTP status to fail the BOM upload with, 0 = ok
	tokenCalls int
}

func newFakeDT(t *testing.T) *fakeDT {
	t.Helper()

	f := &fakeDT{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/bom", f.upload(true))
	mux.HandleFunc("POST /api/v1/vex", f.upload(false))
	mux.HandleFunc("GET /api/v1/event/token/", f.token)
	mux.HandleFunc("GET /api/v1/project/lookup", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"uuid":"proj-uuid"}`))
	})
	mux.HandleFunc("GET /api/v1/finding/project/", f.findings)
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"4.14.2"}`))
	})
	mux.HandleFunc("GET /api/v1/team/self", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"permissions":[{"name":"BOM_UPLOAD"},{"name":"VIEW_PORTFOLIO"}]}`))
	})

	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)

	return f
}

func (f *fakeDT) client(t *testing.T, key string) *Client {
	t.Helper()

	c, err := NewClientWithHTTP(f.srv.URL, key, f.srv.Client())
	require.NoError(t, err)

	return c
}

func (f *fakeDT) factory() ClientFactory {
	return func(baseURL, apiKey string) (*Client, error) {
		return NewClientWithHTTP(baseURL, apiKey, f.srv.Client())
	}
}

func (f *fakeDT) upload(bom bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		form := map[string]string{}
		for k, v := range r.MultipartForm.Value {
			form[k] = v[0]
		}

		f.mu.Lock()
		defer f.mu.Unlock()

		if bom {
			if f.failBOM != 0 {
				w.WriteHeader(f.failBOM)
				_, _ = w.Write([]byte("secret upstream body " + testKey))

				return
			}

			f.bomForm = form
			file, _, _ := r.FormFile("bom")
			buf := make([]byte, 4096)
			n, _ := file.Read(buf)
			f.bomBody = string(buf[:n])
		} else {
			f.vexForm = form
			f.vexDone = true
		}

		_, _ = w.Write([]byte(`{"token":"tok-1"}`))
	}
}

func (f *fakeDT) token(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.tokenCalls++
	busy := f.busyPolls > 0

	if busy {
		f.busyPolls--
	}

	_ = json.NewEncoder(w).Encode(map[string]bool{"processing": busy})
}

func (f *fakeDT) findings(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	analysis := `{}`
	if f.vexDone {
		analysis = `{"state":"NOT_AFFECTED"}`
	}

	_, _ = w.Write([]byte(`[{"component":{"purl":"pkg:npm/left-pad@1.3.0"},
	  "vulnerability":{"vulnId":"INT-2026-0001","source":"INTERNAL","aliases":[{"cveId":"CVE-2026-1"}]},
	  "analysis":` + analysis + `}]`))
}

func newTestPusher(t *testing.T, st *fakeStore, src Sources, dt *fakeDT) *Pusher {
	t.Helper()

	p := NewPusher(st, src, testLogger(t)).WithTimings(time.Millisecond, 5*time.Second).WithClientFactory(dt.factory())
	p.Start(context.Background())
	t.Cleanup(p.Stop)

	return p
}

// ---- tests -------------------------------------------------------------------

func TestPusherPushesBOMThenVEXWithDependencyTrackIdentities(t *testing.T) {
	useEncryption(t)

	st, dt := newFakeStore(), newFakeDT(t)
	st.enableGlobal(t, dt.srv.URL, true)

	dt.busyPolls = 2
	src := &fakeSources{vexCount: 1}
	p := newTestPusher(t, st, src, dt)

	require.True(t, p.Enqueue(Job{RepoID: "r1", ArtifactID: "left-pad@1.3.0", Trigger: TriggerManual}))

	got := st.waitState(t, "r1", "left-pad@1.3.0", models.DTrackPushDone, models.DTrackPushFailed)
	require.Equal(t, models.DTrackPushDone, got.State, got.Detail)

	dt.mu.Lock()
	defer dt.mu.Unlock()

	assert.Equal(t, "libs/left-pad", dt.bomForm["projectName"])
	assert.Equal(t, "1.3.0", dt.bomForm["projectVersion"])
	assert.Equal(t, "true", dt.bomForm["autoCreate"])
	assert.Contains(t, dt.bomBody, "CycloneDX")
	assert.Equal(t, "proj-uuid", dt.vexForm["project"])

	// The VEX is built with DT's own vulnerability identity: the CVE alias
	// resolves to INT-2026-0001 / INTERNAL, an unknown id does not resolve.
	assert.Equal(t, map[string]sbom.VulnRef{"CVE-2026-1": {ID: "INT-2026-0001", Source: "INTERNAL"}}, src.resolved)

	assert.Equal(t, "proj-uuid", got.ProjectUUID)
	assert.Equal(t, 3, got.Components)
	assert.Equal(t, 1, got.VEXStatements)
	assert.Equal(t, 1, got.VEXApplied, "the NOT_AFFECTED analysis is counted after the import")
	assert.NotNil(t, got.BOMPushedAt)
	assert.NotNil(t, got.VEXPushedAt)
	assert.GreaterOrEqual(t, dt.tokenCalls, 3, "the processing token is polled until DT is done")
}

func TestPusherWithoutDecisionsSkipsVEXUpload(t *testing.T) {
	useEncryption(t)

	st, dt := newFakeStore(), newFakeDT(t)
	st.enableGlobal(t, dt.srv.URL, true)

	p := newTestPusher(t, st, &fakeSources{vexCount: 0}, dt)
	require.True(t, p.Enqueue(Job{RepoID: "r1", ArtifactID: "left-pad@1.3.0"}))

	got := st.waitState(t, "r1", "left-pad@1.3.0", models.DTrackPushDone, models.DTrackPushFailed)
	require.Equal(t, models.DTrackPushDone, got.State)
	assert.Contains(t, got.Detail, "no VEX decision")
	assert.Nil(t, got.VEXPushedAt)
	assert.False(t, dt.vexDone)
}

func TestPusherDoesNotPushVEXWhenDisabled(t *testing.T) {
	useEncryption(t)

	st, dt := newFakeStore(), newFakeDT(t)
	st.enableGlobal(t, dt.srv.URL, false)

	p := newTestPusher(t, st, &fakeSources{vexCount: 1}, dt)
	require.True(t, p.Enqueue(Job{RepoID: "r1", ArtifactID: "left-pad@1.3.0"}))

	got := st.waitState(t, "r1", "left-pad@1.3.0", models.DTrackPushDone, models.DTrackPushFailed)
	require.Equal(t, models.DTrackPushDone, got.State)
	assert.False(t, dt.vexDone)
	assert.Zero(t, got.VEXStatements)
}

func TestPusherFailureDetailNeverLeaksUpstreamBody(t *testing.T) {
	useEncryption(t)

	st, dt := newFakeStore(), newFakeDT(t)
	st.enableGlobal(t, dt.srv.URL, true)

	dt.failBOM = http.StatusForbidden
	p := newTestPusher(t, st, &fakeSources{}, dt)
	require.True(t, p.Enqueue(Job{RepoID: "r1", ArtifactID: "left-pad@1.3.0"}))

	got := st.waitState(t, "r1", "left-pad@1.3.0", models.DTrackPushDone, models.DTrackPushFailed)
	require.Equal(t, models.DTrackPushFailed, got.State)
	assert.Contains(t, got.Detail, "rejected the API key")
	assert.NotContains(t, got.Detail, testKey)
	assert.NotContains(t, got.Detail, "secret upstream body")
}

func TestPusherSkipsWhenNotConfigured(t *testing.T) {
	st, dt := newFakeStore(), newFakeDT(t)

	p := newTestPusher(t, st, &fakeSources{}, dt)
	require.True(t, p.Enqueue(Job{RepoID: "r1", ArtifactID: "left-pad@1.3.0"}))

	got := st.waitState(t, "r1", "left-pad@1.3.0", models.DTrackPushSkipped, models.DTrackPushFailed)
	require.Equal(t, models.DTrackPushSkipped, got.State)
	assert.Contains(t, got.Detail, "not enabled")
}

func TestPusherCoalescesAndRerunsRequestsMadeWhileRunning(t *testing.T) {
	useEncryption(t)

	st, dt := newFakeStore(), newFakeDT(t)
	st.enableGlobal(t, dt.srv.URL, false)

	src := &fakeSources{gate: make(chan struct{})}
	p := newTestPusher(t, st, src, dt)
	job := Job{RepoID: "r1", ArtifactID: "left-pad@1.3.0"}

	require.True(t, p.Enqueue(job))
	require.Eventually(t, func() bool { return src.bomCalls.Load() == 1 }, 5*time.Second, time.Millisecond)

	// While the first push is blocked, any number of requests collapse into one rerun.
	for range 5 {
		require.True(t, p.Enqueue(job))
	}

	close(src.gate)
	require.Eventually(t, func() bool { return src.bomCalls.Load() == 2 }, 5*time.Second, time.Millisecond)
	st.waitState(t, "r1", "left-pad@1.3.0", models.DTrackPushDone)

	assert.Never(t, func() bool { return src.bomCalls.Load() > 2 }, 100*time.Millisecond, 10*time.Millisecond)
}

type recordingPublisher struct{ events atomic.Int32 }

func (r *recordingPublisher) Dispatch(context.Context, string, string, map[string]any) {
	r.events.Add(1)
}

func TestUploadPublisherEnqueuesOnlyUploadsOfEnabledRepos(t *testing.T) {
	useEncryption(t)

	st, dt := newFakeStore(), newFakeDT(t)
	src := &fakeSources{}
	p := newTestPusher(t, st, src, dt)
	next := &recordingPublisher{}
	pub := NewUploadPublisher(next, p)
	payload := map[string]any{"artifact": map[string]any{"name": "left-pad", "version": "1.3.0"}}

	// Not configured: forwarded, nothing pushed.
	pub.Dispatch(context.Background(), "r1", webhook.EventArtifactUploaded, payload)
	assert.Never(t, func() bool { return src.bomCalls.Load() > 0 }, 100*time.Millisecond, 10*time.Millisecond)
	assert.EqualValues(t, 1, next.events.Load())

	// Enabled: an upload pushes, another event type does not.
	st.enableGlobal(t, dt.srv.URL, false)
	pub.Dispatch(context.Background(), "r1", "artifact.deleted", payload)
	pub.Dispatch(context.Background(), "r1", webhook.EventArtifactUploaded, payload)

	st.waitState(t, "r1", "left-pad@1.3.0", models.DTrackPushDone)
	assert.EqualValues(t, 1, src.bomCalls.Load())
	assert.EqualValues(t, 3, next.events.Load())

	// auto_push off: uploads no longer push.
	require.NoError(t, SaveGlobal(context.Background(), st, Settings{Enabled: true, BaseURL: dt.srv.URL, APIKey: testKey}))
	pub.Dispatch(context.Background(), "r1", webhook.EventArtifactUploaded, payload)
	assert.Never(t, func() bool { return src.bomCalls.Load() > 1 }, 100*time.Millisecond, 10*time.Millisecond)
}

func TestArtifactIDFromPayload(t *testing.T) {
	for name, tc := range map[string]struct {
		payload map[string]any
		want    string
	}{
		"name and version": {map[string]any{"artifact": map[string]any{"name": "a", "version": "1"}}, "a@1"},
		"name only":        {map[string]any{"artifact": map[string]any{"name": "a"}}, "a"},
		"no artifact":      {map[string]any{}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, ArtifactIDFromPayload(tc.payload))
		})
	}
}

func TestClientCheckReportsMissingPermissions(t *testing.T) {
	dt := newFakeDT(t)

	info, err := dt.client(t, testKey).Check(context.Background(), true)
	require.NoError(t, err)
	assert.Equal(t, "4.14.2", info.Version)
	assert.ElementsMatch(t, []string{"BOM_UPLOAD", "VIEW_PORTFOLIO"}, info.Permissions)
	assert.ElementsMatch(t, []string{PermProjectCreation, PermViewVuln, PermVulnAnalysis}, info.MissingPermissions)

	info, err = dt.client(t, testKey).Check(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, []string{PermProjectCreation}, info.MissingPermissions)
}

func TestClientRejectsBadKeyAndBadURL(t *testing.T) {
	dt := newFakeDT(t)

	_, err := dt.client(t, "wrong").Check(context.Background(), false)

	var he *HTTPError

	require.ErrorAs(t, err, &he)
	assert.Equal(t, http.StatusUnauthorized, he.Status)

	for _, u := range []string{"", "ftp://x", "http://", "http://x?a=b", "not a url"} {
		_, err := NewClient(u, "k")
		assert.ErrorIs(t, err, ErrInvalidURL, u)
	}
}

func TestClientMapsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)

	c, err := NewClientWithHTTP(srv.URL, "k", srv.Client())
	require.NoError(t, err)

	_, err = c.ProjectUUID(context.Background(), "x", "1")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestSafeClientBlocksLoopbackTargets(t *testing.T) {
	dt := newFakeDT(t)

	c, err := NewClient(dt.srv.URL, testKey)
	require.NoError(t, err)

	_, err = c.Check(context.Background(), false)
	require.Error(t, err, "the production client must refuse private addresses (SSRF guard)")
}

func TestResolveLayersRepositoryOverrideOverGlobal(t *testing.T) {
	useEncryption(t)

	yes, no := true, false
	enc, err := crypto.Encrypt("repo-key")
	require.NoError(t, err)

	global := Settings{Enabled: true, BaseURL: "https://g", APIKey: "g-key", ProjectName: "{name}", AutoPush: true, PushVEX: true}

	for name, tc := range map[string]struct {
		row  *models.RepositoryDependencyTrack
		want Effective
	}{
		"no override": {nil, Effective{Settings: global, Source: SourceGlobal}},
		"opt out":     {&models.RepositoryDependencyTrack{Enabled: false}, effective(Settings{BaseURL: "https://g", APIKey: "g-key", ProjectName: "{name}", AutoPush: true, PushVEX: true}, SourceRepository)},
		"full override": {
			&models.RepositoryDependencyTrack{Enabled: true, BaseURL: "https://r", APIKey: enc, ProjectName: "x/{name}", ProjectVersion: "v", AutoPush: &no, PushVEX: &yes},
			effective(Settings{Enabled: true, BaseURL: "https://r", APIKey: "repo-key", ProjectName: "x/{name}", ProjectVersion: "v", PushVEX: true}, SourceRepository),
		},
		"inherit empty": {&models.RepositoryDependencyTrack{Enabled: true}, Effective{Settings: global, Source: SourceRepository}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Resolve(global, tc.row)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	got, err := Resolve(Settings{}, nil)
	require.NoError(t, err)
	assert.Equal(t, SourceNone, got.Source)
	assert.False(t, got.Ready())
}

func TestGlobalSettingsAreEncryptedAtRest(t *testing.T) {
	useEncryption(t)

	st := newFakeStore()
	want := Settings{Enabled: true, BaseURL: "https://dt", APIKey: testKey, ProjectName: "{repo}", AutoPush: true}
	require.NoError(t, SaveGlobal(context.Background(), st, want))

	assert.NotContains(t, st.settings[SettingGlobal], testKey)

	got, err := LoadGlobal(context.Background(), st)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestProjectTemplates(t *testing.T) {
	v := ProjectVars{Repo: "libs", Name: "left-pad", Version: "1.3.0", ArtifactID: "left-pad@1.3.0", Format: "npm"}

	assert.Equal(t, "libs/left-pad", Render("", DefaultProjectName, v))
	assert.Equal(t, "1.3.0", Render("", DefaultProjectVersion, v))
	assert.Equal(t, "npm:left-pad@1.3.0", Render("{format}:{artifact_id}", "", v))
	assert.Empty(t, Render("", DefaultProjectVersion, ProjectVars{Version: "unknown"}))
	assert.LessOrEqual(t, len(Render(strings.Repeat("a", 1000), "", v)), 255)

	require.NoError(t, ValidateTemplate("{repo}/{name}-{version}"))
	require.NoError(t, ValidateTemplate(""))
	require.Error(t, ValidateTemplate("{nope}"))
	require.Error(t, ValidateTemplate("{repo"))
	require.Error(t, ValidateTemplate(strings.Repeat("a", 1000)))
}

func TestVulnIndexResolvesAliasesAndCountsAnalyses(t *testing.T) {
	idx := newVulnIndex([]Finding{
		{VulnID: "GHSA-aaaa", Source: "GITHUB", Aliases: []string{"CVE-2026-9"}},
		{VulnID: "CVE-2026-9", Source: "NVD"},
	})

	ref, ok := idx.resolve("cve-2026-9")
	require.True(t, ok)
	assert.Equal(t, sbom.VulnRef{ID: "CVE-2026-9", Source: "NVD"}, ref, "a finding's own id wins over another finding's alias")

	_, ok = idx.resolve("CVE-0")
	assert.False(t, ok)

	assert.Equal(t, 1, idx.applied([]Finding{
		{VulnID: "GHSA-aaaa", State: "NOT_AFFECTED"},
		{VulnID: "CVE-2026-9", State: "NOT_SET"},
		{VulnID: "OTHER", State: "EXPLOITABLE"},
	}))
}

func effective(s Settings, source string) Effective {
	var e Effective

	e.Settings, e.Source = s, source

	return e
}
