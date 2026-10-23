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
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	mustNoErr(t, err)

	return log
}

func useEncryption(t *testing.T) {
	t.Helper()
	mustNoErr(t, crypto.Init("0123456789abcdef0123456789abcdef0123456789"))
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

	mustEventually(t, func() bool {
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
	mustNoErr(t, SaveGlobal(context.Background(), s, Settings{
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
	mustNoErr(t, err)

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

	mustTrue(t, p.Enqueue(Job{RepoID: "r1", ArtifactID: "left-pad@1.3.0", Trigger: TriggerManual}))

	got := st.waitState(t, "r1", "left-pad@1.3.0", models.DTrackPushDone, models.DTrackPushFailed)
	mustEqual(t, models.DTrackPushDone, got.State, got.Detail)

	dt.mu.Lock()
	defer dt.mu.Unlock()

	checkEqual(t, "libs/left-pad", dt.bomForm["projectName"])
	checkEqual(t, "1.3.0", dt.bomForm["projectVersion"])
	checkEqual(t, "true", dt.bomForm["autoCreate"])
	checkContains(t, dt.bomBody, "CycloneDX")
	checkEqual(t, "proj-uuid", dt.vexForm["project"])

	// The VEX is built with DT's own vulnerability identity: the CVE alias
	// resolves to INT-2026-0001 / INTERNAL, an unknown id does not resolve.
	checkEqual(t, map[string]sbom.VulnRef{"CVE-2026-1": {ID: "INT-2026-0001", Source: "INTERNAL"}}, src.resolved)

	checkEqual(t, "proj-uuid", got.ProjectUUID)
	checkEqual(t, 3, got.Components)
	checkEqual(t, 1, got.VEXStatements)
	checkEqual(t, 1, got.VEXApplied, "the NOT_AFFECTED analysis is counted after the import")
	checkNotNil(t, got.BOMPushedAt)
	checkNotNil(t, got.VEXPushedAt)
	checkGreaterOrEqual(t, dt.tokenCalls, 3, "the processing token is polled until DT is done")
}

func TestPusherWithoutDecisionsSkipsVEXUpload(t *testing.T) {
	useEncryption(t)

	st, dt := newFakeStore(), newFakeDT(t)
	st.enableGlobal(t, dt.srv.URL, true)

	p := newTestPusher(t, st, &fakeSources{vexCount: 0}, dt)
	mustTrue(t, p.Enqueue(Job{RepoID: "r1", ArtifactID: "left-pad@1.3.0"}))

	got := st.waitState(t, "r1", "left-pad@1.3.0", models.DTrackPushDone, models.DTrackPushFailed)
	mustEqual(t, models.DTrackPushDone, got.State)
	checkContains(t, got.Detail, "no VEX decision")
	checkNil(t, got.VEXPushedAt)
	checkFalse(t, dt.vexDone)
}

func TestPusherDoesNotPushVEXWhenDisabled(t *testing.T) {
	useEncryption(t)

	st, dt := newFakeStore(), newFakeDT(t)
	st.enableGlobal(t, dt.srv.URL, false)

	p := newTestPusher(t, st, &fakeSources{vexCount: 1}, dt)
	mustTrue(t, p.Enqueue(Job{RepoID: "r1", ArtifactID: "left-pad@1.3.0"}))

	got := st.waitState(t, "r1", "left-pad@1.3.0", models.DTrackPushDone, models.DTrackPushFailed)
	mustEqual(t, models.DTrackPushDone, got.State)
	checkFalse(t, dt.vexDone)
	checkZero(t, got.VEXStatements)
}

func TestPusherFailureDetailNeverLeaksUpstreamBody(t *testing.T) {
	useEncryption(t)

	st, dt := newFakeStore(), newFakeDT(t)
	st.enableGlobal(t, dt.srv.URL, true)

	dt.failBOM = http.StatusForbidden
	p := newTestPusher(t, st, &fakeSources{}, dt)
	mustTrue(t, p.Enqueue(Job{RepoID: "r1", ArtifactID: "left-pad@1.3.0"}))

	got := st.waitState(t, "r1", "left-pad@1.3.0", models.DTrackPushDone, models.DTrackPushFailed)
	mustEqual(t, models.DTrackPushFailed, got.State)
	checkContains(t, got.Detail, "rejected the API key")
	checkNotContains(t, got.Detail, testKey)
	checkNotContains(t, got.Detail, "secret upstream body")
}

func TestPusherSkipsWhenNotConfigured(t *testing.T) {
	st, dt := newFakeStore(), newFakeDT(t)

	p := newTestPusher(t, st, &fakeSources{}, dt)
	mustTrue(t, p.Enqueue(Job{RepoID: "r1", ArtifactID: "left-pad@1.3.0"}))

	got := st.waitState(t, "r1", "left-pad@1.3.0", models.DTrackPushSkipped, models.DTrackPushFailed)
	mustEqual(t, models.DTrackPushSkipped, got.State)
	checkContains(t, got.Detail, "not enabled")
}

func TestPusherCoalescesAndRerunsRequestsMadeWhileRunning(t *testing.T) {
	useEncryption(t)

	st, dt := newFakeStore(), newFakeDT(t)
	st.enableGlobal(t, dt.srv.URL, false)

	src := &fakeSources{gate: make(chan struct{})}
	p := newTestPusher(t, st, src, dt)
	job := Job{RepoID: "r1", ArtifactID: "left-pad@1.3.0"}

	mustTrue(t, p.Enqueue(job))
	mustEventually(t, func() bool { return src.bomCalls.Load() == 1 }, 5*time.Second, time.Millisecond)

	// While the first push is blocked, any number of requests collapse into one rerun.
	for range 5 {
		mustTrue(t, p.Enqueue(job))
	}

	close(src.gate)
	mustEventually(t, func() bool { return src.bomCalls.Load() == 2 }, 5*time.Second, time.Millisecond)
	st.waitState(t, "r1", "left-pad@1.3.0", models.DTrackPushDone)

	checkNever(t, func() bool { return src.bomCalls.Load() > 2 }, 100*time.Millisecond, 10*time.Millisecond)
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
	checkNever(t, func() bool { return src.bomCalls.Load() > 0 }, 100*time.Millisecond, 10*time.Millisecond)
	checkEqual(t, int64(1), int64(next.events.Load()))

	// Enabled: an upload pushes, another event type does not.
	st.enableGlobal(t, dt.srv.URL, false)
	pub.Dispatch(context.Background(), "r1", "artifact.deleted", payload)
	pub.Dispatch(context.Background(), "r1", webhook.EventArtifactUploaded, payload)

	st.waitState(t, "r1", "left-pad@1.3.0", models.DTrackPushDone)
	checkEqual(t, int64(1), int64(src.bomCalls.Load()))
	checkEqual(t, int64(3), int64(next.events.Load()))

	// auto_push off: uploads no longer push.
	mustNoErr(t, SaveGlobal(context.Background(), st, Settings{Enabled: true, BaseURL: dt.srv.URL, APIKey: testKey}))
	pub.Dispatch(context.Background(), "r1", webhook.EventArtifactUploaded, payload)
	checkNever(t, func() bool { return src.bomCalls.Load() > 1 }, 100*time.Millisecond, 10*time.Millisecond)
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
			checkEqual(t, tc.want, ArtifactIDFromPayload(tc.payload))
		})
	}
}

func TestClientCheckReportsMissingPermissions(t *testing.T) {
	dt := newFakeDT(t)

	info, err := dt.client(t, testKey).Check(context.Background(), true)
	mustNoErr(t, err)
	checkEqual(t, "4.14.2", info.Version)
	checkElementsMatch(t, []string{"BOM_UPLOAD", "VIEW_PORTFOLIO"}, info.Permissions)
	checkElementsMatch(t, []string{PermProjectCreation, PermViewVuln, PermVulnAnalysis}, info.MissingPermissions)

	info, err = dt.client(t, testKey).Check(context.Background(), false)
	mustNoErr(t, err)
	checkEqual(t, []string{PermProjectCreation}, info.MissingPermissions)
}

func TestClientRejectsBadKeyAndBadURL(t *testing.T) {
	dt := newFakeDT(t)

	_, err := dt.client(t, "wrong").Check(context.Background(), false)

	var he *HTTPError

	mustErrAs(t, err, &he)
	checkEqual(t, http.StatusUnauthorized, he.Status)

	for _, u := range []string{"", "ftp://x", "http://", "http://x?a=b", "not a url"} {
		_, err := NewClient(u, "k")
		checkErrorIs(t, err, ErrInvalidURL, u)
	}
}

func TestClientMapsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)

	c, err := NewClientWithHTTP(srv.URL, "k", srv.Client())
	mustNoErr(t, err)

	_, err = c.ProjectUUID(context.Background(), "x", "1")
	checkErrorIs(t, err, ErrNotFound)
}

func TestSafeClientBlocksLoopbackTargets(t *testing.T) {
	dt := newFakeDT(t)

	c, err := NewClient(dt.srv.URL, testKey)
	mustNoErr(t, err)

	_, err = c.Check(context.Background(), false)
	mustErr(t, err, "the production client must refuse private addresses (SSRF guard)")
}

func TestResolveLayersRepositoryOverrideOverGlobal(t *testing.T) {
	useEncryption(t)

	yes, no := true, false
	enc, err := crypto.Encrypt("repo-key")
	mustNoErr(t, err)

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
			mustNoErr(t, err)
			checkEqual(t, tc.want, got)
		})
	}

	got, err := Resolve(Settings{}, nil)
	mustNoErr(t, err)
	checkEqual(t, SourceNone, got.Source)
	checkFalse(t, got.Ready())
}

func TestGlobalSettingsAreEncryptedAtRest(t *testing.T) {
	useEncryption(t)

	st := newFakeStore()
	want := Settings{Enabled: true, BaseURL: "https://dt", APIKey: testKey, ProjectName: "{repo}", AutoPush: true}
	mustNoErr(t, SaveGlobal(context.Background(), st, want))

	checkNotContains(t, st.settings[SettingGlobal], testKey)

	got, err := LoadGlobal(context.Background(), st)
	mustNoErr(t, err)
	checkEqual(t, want, got)
}

func TestProjectTemplates(t *testing.T) {
	v := ProjectVars{Repo: "libs", Name: "left-pad", Version: "1.3.0", ArtifactID: "left-pad@1.3.0", Format: "npm"}

	checkEqual(t, "libs/left-pad", Render("", DefaultProjectName, v))
	checkEqual(t, "1.3.0", Render("", DefaultProjectVersion, v))
	checkEqual(t, "npm:left-pad@1.3.0", Render("{format}:{artifact_id}", "", v))
	checkEmpty(t, Render("", DefaultProjectVersion, ProjectVars{Version: "unknown"}))
	checkLessOrEqual(t, len(Render(strings.Repeat("a", 1000), "", v)), 255)

	mustNoErr(t, ValidateTemplate("{repo}/{name}-{version}"))
	mustNoErr(t, ValidateTemplate(""))
	mustErr(t, ValidateTemplate("{nope}"))
	mustErr(t, ValidateTemplate("{repo"))
	mustErr(t, ValidateTemplate(strings.Repeat("a", 1000)))
}

func TestVulnIndexResolvesAliasesAndCountsAnalyses(t *testing.T) {
	idx := newVulnIndex([]Finding{
		{VulnID: "GHSA-aaaa", Source: "GITHUB", Aliases: []string{"CVE-2026-9"}},
		{VulnID: "CVE-2026-9", Source: "NVD"},
	})

	ref, ok := idx.resolve("cve-2026-9")
	mustTrue(t, ok)
	checkEqual(t, sbom.VulnRef{ID: "CVE-2026-9", Source: "NVD"}, ref, "a finding's own id wins over another finding's alias")

	_, ok = idx.resolve("CVE-0")
	checkFalse(t, ok)

	checkEqual(t, 1, idx.applied([]Finding{
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

func mustNoErr(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func mustErr(t *testing.T, err error, msg ...any) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected an error %v", msg)
	}
}

func mustErrAs(t *testing.T, err error, target any) {
	t.Helper()

	if !errors.As(err, target) {
		t.Fatalf("error %v is not of the expected type", err)
	}
}

func mustTrue(t *testing.T, ok bool, msg ...any) {
	t.Helper()

	if !ok {
		t.Fatalf("expected true %v", msg)
	}
}

func mustEqual(t *testing.T, want, got any, msg ...any) {
	t.Helper()

	if !reflect.DeepEqual(want, got) {
		t.Fatalf("want %#v, got %#v %v", want, got, msg)
	}
}

func checkEqual(t *testing.T, want, got any, msg ...any) {
	t.Helper()

	if !reflect.DeepEqual(want, got) {
		t.Errorf("want %#v, got %#v %v", want, got, msg)
	}
}

func checkContains(t *testing.T, s, sub string, msg ...any) {
	t.Helper()

	if !strings.Contains(s, sub) {
		t.Errorf("%q does not contain %q %v", s, sub, msg)
	}
}

func checkNotContains(t *testing.T, s, sub string, msg ...any) {
	t.Helper()

	if strings.Contains(s, sub) {
		t.Errorf("%q must not contain %q %v", s, sub, msg)
	}
}

func checkErrorIs(t *testing.T, err, target error, msg ...any) {
	t.Helper()

	if !errors.Is(err, target) {
		t.Errorf("error %v is not %v %v", err, target, msg)
	}
}

func checkElementsMatch(t *testing.T, want, got []string) {
	t.Helper()

	w, g := slices.Clone(want), slices.Clone(got)
	slices.Sort(w)
	slices.Sort(g)

	if !slices.Equal(w, g) {
		t.Errorf("want elements %v, got %v", want, got)
	}
}

func checkGreaterOrEqual(t *testing.T, got, floor int, msg ...any) {
	t.Helper()

	if got < floor {
		t.Errorf("got %d, want >= %d %v", got, floor, msg)
	}
}

func checkLessOrEqual(t *testing.T, got, ceil int, msg ...any) {
	t.Helper()

	if got > ceil {
		t.Errorf("got %d, want <= %d %v", got, ceil, msg)
	}
}

func isNil(v any) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)

	return rv.Kind() == reflect.Pointer && rv.IsNil()
}

func checkNil(t *testing.T, v any) {
	t.Helper()

	if !isNil(v) {
		t.Errorf("expected nil, got %#v", v)
	}
}

func checkNotNil(t *testing.T, v any) {
	t.Helper()

	if isNil(v) {
		t.Errorf("expected non-nil")
	}
}

func checkFalse(t *testing.T, ok bool, msg ...any) {
	t.Helper()

	if ok {
		t.Errorf("expected false %v", msg)
	}
}

func checkEmpty(t *testing.T, s string) {
	t.Helper()

	if s != "" {
		t.Errorf("expected empty, got %q", s)
	}
}

func checkZero(t *testing.T, n int) {
	t.Helper()

	if n != 0 {
		t.Errorf("expected zero, got %d", n)
	}
}

// mustEventually polls cond until it holds or the timeout elapses, yielding
// the processor between attempts instead of sleeping.
func mustEventually(t *testing.T, cond func() bool, timeout, _ time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s", timeout)
		}

		runtime.Gosched()
	}
}

// checkNever fails if cond holds at any point within the window.
func checkNever(t *testing.T, cond func() bool, window, _ time.Duration) {
	t.Helper()

	deadline := time.Now().Add(window)

	for time.Now().Before(deadline) {
		if cond() {
			t.Errorf("condition unexpectedly held")

			return
		}

		runtime.Gosched()
	}
}
