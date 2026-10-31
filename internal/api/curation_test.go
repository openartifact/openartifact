// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/curation"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

// curationMockDBStore is what curationMockDB is used as.
type curationMockDBStore interface {
	CurationStore
	auth.EvaluatorStore
}

// curationMockDB keeps repositories and rules in memory. Like the real store it
// scopes a rule id to its repository and caps the rules of a repository.
type curationMockDB struct {
	curationMockDBStore

	repos    map[string]*models.Repository
	rules    []curation.Rule
	next     int
	maxRules int
	holds    models.VersionHolds
	holdsErr error
	listErr  error
}

func newCurationDB() *curationMockDB {
	return &curationMockDB{
		repos: map[string]*models.Repository{
			"px":     {ID: "r-px", Name: "px", Format: "npm", RepoType: "proxy", CurationMode: curation.ModeDenylist},
			"other":  {ID: "r-other", Name: "other", Format: "npm", RepoType: "proxy"},
			"hosted": {ID: "r-h", Name: "hosted", Format: "npm", RepoType: "hosted"},
			"grp":    {ID: "r-g", Name: "grp", Format: "npm", RepoType: "group"},
		},
		maxRules: curation.MaxRules,
	}
}

func (db *curationMockDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return db.repos[name], nil
}

func (db *curationMockDB) ListCurationRules(_ context.Context, repoID string) ([]curation.Rule, error) {
	if db.listErr != nil {
		return nil, db.listErr
	}

	var out []curation.Rule

	for _, r := range db.rules {
		if r.RepositoryID == repoID {
			out = append(out, r)
		}
	}

	return out, nil
}

func (db *curationMockDB) GetCurationRule(_ context.Context, repoID, id string) (*curation.Rule, error) {
	for _, r := range db.rules {
		if r.RepositoryID == repoID && r.ID == id {
			return &r, nil
		}
	}

	return nil, database.ErrCurationRuleNotFound
}

func (db *curationMockDB) CreateCurationRule(ctx context.Context, rule *curation.Rule) (*curation.Rule, error) {
	existing, _ := db.ListCurationRules(ctx, rule.RepositoryID)
	if len(existing) >= db.maxRules {
		return nil, database.ErrCurationRuleLimit
	}

	db.next++
	created := *rule
	created.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", db.next)
	created.CreatedAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	db.rules = append(db.rules, created)

	return &created, nil
}

func (db *curationMockDB) UpdateCurationRule(_ context.Context, rule *curation.Rule) (*curation.Rule, error) {
	for i := range db.rules {
		if db.rules[i].RepositoryID == rule.RepositoryID && db.rules[i].ID == rule.ID {
			rule.CreatedBy, rule.CreatedAt = db.rules[i].CreatedBy, db.rules[i].CreatedAt
			db.rules[i] = *rule

			return rule, nil
		}
	}

	return nil, database.ErrCurationRuleNotFound
}

func (db *curationMockDB) DeleteCurationRule(_ context.Context, repoID, id string) error {
	for i := range db.rules {
		if db.rules[i].RepositoryID == repoID && db.rules[i].ID == id {
			db.rules = append(db.rules[:i], db.rules[i+1:]...)
			return nil
		}
	}

	return database.ErrCurationRuleNotFound
}

func (db *curationMockDB) PackageVersionHolds(context.Context, string, string, string) (models.VersionHolds, error) {
	return db.holds, db.holdsErr
}

// fakePublished answers PublishTime from a fixed table keyed "pkg@version".
type fakePublished map[string]time.Time

func (f fakePublished) PublishTime(_ context.Context, _ *models.Repository, pkg, version string) (time.Time, bool) {
	t, ok := f[pkg+"@"+version]

	return t, ok
}

func curationRequest(
	t *testing.T, db *curationMockDB, published PublishTimeSource, method, path, body string,
) *httptest.ResponseRecorder {
	t.Helper()

	log := newFixTestLogger(t)
	r := chi.NewRouter()
	NewCurationHandler(db, log, auth.NewEvaluator(db, log, false), published).RegisterAuthed(r)

	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))

	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, req)

	return rw
}

func decodeResp[T any](t *testing.T, rw *httptest.ResponseRecorder) T {
	t.Helper()

	var v T
	if err := json.Unmarshal(rw.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rw.Body.String(), err)
	}

	return v
}

const (
	rulesPath  = "/api/v1/repositories/px/curation-rules"
	missingID  = "11111111-1111-4111-8111-111111111111"
	blockBody  = `{"action":"block","package_pattern":"left-pad","version_pattern":"1.*","reason":"malware"}`
	allowBody  = `{"action":"allow","package_pattern":"@acme/*"}`
	firstRuleP = rulesPath + "/00000000-0000-4000-8000-000000000001"
)

// curationCall sends a request and fails unless the status is want.
func curationCall(t *testing.T, db *curationMockDB, method, path, body string, want int) *httptest.ResponseRecorder {
	t.Helper()

	rw := curationRequest(t, db, nil, method, path, body)
	if rw.Code != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, rw.Code, want, rw.Body.String())
	}

	return rw
}

func listedRules(t *testing.T, db *curationMockDB) []responses.CurationRuleResponse {
	t.Helper()

	return decodeResp[responses.CurationRulesResponse](t, curationCall(t, db, http.MethodGet, rulesPath, "", http.StatusOK)).Rules
}

func TestCurationRuleLifecycle(t *testing.T) {
	rows := recordAudit(t)
	db := newCurationDB()

	created := decodeResp[responses.CurationRuleResponse](t, curationCall(t, db, http.MethodPost, rulesPath, blockBody, http.StatusCreated))
	want := responses.CurationRuleResponse{
		ID: created.ID, Action: "block", PackagePattern: "left-pad", VersionPattern: "1.*", Reason: "malware",
		CreatedBy: "admin", CreatedAt: "2026-01-02T03:04:05Z",
	}

	if created != want {
		t.Errorf("created = %+v, want %+v", created, want)
	}

	curationCall(t, db, http.MethodPost, rulesPath, allowBody, http.StatusCreated)

	if list := listedRules(t, db); len(list) != 2 || list[0] != want || list[1].PackagePattern != "@acme/*" {
		t.Fatalf("list = %+v, want the block rule then the allow rule", list)
	}

	rw := curationCall(t, db, http.MethodPatch, rulesPath+"/"+created.ID, `{"reason":"confirmed","version_pattern":"1.0.*"}`, http.StatusOK)

	want.Reason, want.VersionPattern = "confirmed", "1.0.*"
	if updated := decodeResp[responses.CurationRuleResponse](t, rw); updated != want {
		t.Fatalf("updated = %+v, want %+v (absent fields keep their value)", updated, want)
	}

	curationCall(t, db, http.MethodDelete, rulesPath+"/"+created.ID, "", http.StatusNoContent)

	if left := listedRules(t, db); len(left) != 1 || left[0].PackagePattern != "@acme/*" {
		t.Errorf("rules left = %+v, want only the allow rule", left)
	}

	got := wantAudit(t, rows(), audit.ActionCurationRuleCreated, audit.ActionCurationRuleCreated,
		audit.ActionCurationRuleUpdated, audit.ActionCurationRuleDeleted)
	wantMessage(t, got[0], "px", `package_pattern="left-pad"`, `version_pattern="1.*"`, `reason="malware"`, "action=block")
	wantMessage(t, got[2], `version_pattern="1.0.*"`, `reason="confirmed"`)
	wantMessage(t, got[3], "deleted", created.ID)

	if got[0].ResourceID == nil || *got[0].ResourceID != created.ID || got[0].RepositoryID == nil || *got[0].RepositoryID != "r-px" {
		t.Errorf("audit target = resource %v repo %v", got[0].ResourceID, got[0].RepositoryID)
	}
}

// Nothing a request is refused for is stored or audited.
func TestCurationRuleValidation(t *testing.T) {
	rows := recordAudit(t)

	long := strings.Repeat("a", curation.MaxPackagePattern+1)
	cases := []struct {
		name string
		body string
	}{
		{"bad action", `{"action":"hide","package_pattern":"x"}`},
		{"missing action", `{"package_pattern":"x"}`},
		{"missing package", `{"action":"block"}`},
		{"blank package", `{"action":"block","package_pattern":"  "}`},
		{"package with space", `{"action":"block","package_pattern":"a b"}`},
		{"package too long", `{"action":"block","package_pattern":"` + long + `"}`},
		{
			"version too long",
			`{"action":"block","package_pattern":"x","version_pattern":"` + strings.Repeat("1", curation.MaxVersionPattern+1) + `"}`,
		},
		{"reason too long", `{"action":"block","package_pattern":"x","reason":"` + strings.Repeat("r", curation.MaxReason+1) + `"}`},
		{"malformed json", `{"action":`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newCurationDB()

			if rw := curationRequest(t, db, nil, http.MethodPost, rulesPath, tc.body); rw.Code != http.StatusBadRequest {
				t.Errorf("status %d, want 400: %s", rw.Code, rw.Body.String())
			}

			if len(db.rules) != 0 {
				t.Errorf("rejected rule was stored: %+v", db.rules)
			}
		})
	}

	t.Run("update to an invalid rule", func(t *testing.T) {
		db := newCurationDB()
		curationRequest(t, db, nil, http.MethodPost, rulesPath, blockBody)

		for _, body := range []string{`{"action":"hide"}`, `{"package_pattern":""}`} {
			if rw := curationRequest(t, db, nil, http.MethodPatch, firstRuleP, body); rw.Code != http.StatusBadRequest {
				t.Errorf("%s: status %d, want 400", body, rw.Code)
			}
		}

		if db.rules[0].Action != "block" || db.rules[0].Package != "left-pad" {
			t.Errorf("rule changed by a rejected update: %+v", db.rules[0])
		}
	})

	wantAudit(t, rows(), audit.ActionCurationRuleCreated)
}

func TestCurationRuleErrors(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(db *curationMockDB)
		method string
		path   string
		body   string
		want   int
	}{
		{"hosted repository", nil, http.MethodPost, "/api/v1/repositories/hosted/curation-rules", blockBody, http.StatusBadRequest},
		{"group repository", nil, http.MethodGet, "/api/v1/repositories/grp/curation-rules", "", http.StatusBadRequest},
		{"unknown repository", nil, http.MethodGet, "/api/v1/repositories/nope/curation-rules", "", http.StatusNotFound},
		{"update a rule of another repository", func(db *curationMockDB) {
			db.rules = []curation.Rule{{ID: missingID, RepositoryID: "r-other", Action: "block", Package: "x"}}
		}, http.MethodPatch, rulesPath + "/" + missingID, `{"reason":"x"}`, http.StatusNotFound},
		{"delete a rule of another repository", func(db *curationMockDB) {
			db.rules = []curation.Rule{{ID: missingID, RepositoryID: "r-other", Action: "block", Package: "x"}}
		}, http.MethodDelete, rulesPath + "/" + missingID, "", http.StatusNotFound},
		{"update an unknown rule", nil, http.MethodPatch, rulesPath + "/" + missingID, `{"reason":"x"}`, http.StatusNotFound},
		{"delete an unknown rule", nil, http.MethodDelete, rulesPath + "/" + missingID, "", http.StatusNotFound},
		{"update with a non-uuid id", nil, http.MethodPatch, rulesPath + "/not-a-uuid", `{"reason":"x"}`, http.StatusNotFound},
		{"delete with a non-uuid id", nil, http.MethodDelete, rulesPath + "/0", "", http.StatusNotFound},
		{"rule limit", func(db *curationMockDB) { db.maxRules = 0 }, http.MethodPost, rulesPath, blockBody, http.StatusConflict},
		{"list failure", func(db *curationMockDB) { db.listErr = errors.New("pq: connection refused") },
			http.MethodGet, rulesPath, "", http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newCurationDB()
			if tc.setup != nil {
				tc.setup(db)
			}

			before := len(db.rules)

			rw := curationRequest(t, db, nil, tc.method, tc.path, tc.body)
			if rw.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rw.Code, tc.want, rw.Body.String())
			}

			if strings.Contains(rw.Body.String(), "connection refused") {
				t.Errorf("database error leaked: %s", rw.Body.String())
			}

			if len(db.rules) != before {
				t.Errorf("rules changed: %d -> %d", before, len(db.rules))
			}
		})
	}
}

func TestCurationAvailability(t *testing.T) {
	now := time.Now()
	young := now.Add(-2 * time.Hour)
	old := now.Add(-400 * time.Hour)
	published := fakePublished{
		"left-pad@1.3.0": old, "fresh@1.0.0": young, "vetted@1.0.0": young, "unlisted@2.0.0": old,
	}

	px := func(mode string, minAge int, rules ...curation.Rule) func(db *curationMockDB) {
		return func(db *curationMockDB) {
			db.repos["px"].CurationMode, db.repos["px"].MinAgeHours = mode, minAge
			for i := range rules {
				rules[i].RepositoryID, rules[i].ID = "r-px", fmt.Sprintf("rule-%d", i)
			}

			db.rules = rules
		}
	}
	block := curation.Rule{Action: curation.ActionBlock, Package: "left-pad", Version: "1.*", Reason: "malware"}
	allow := curation.Rule{Action: curation.ActionAllow, Package: "vetted"}

	cases := []struct {
		name        string
		setup       func(db *curationMockDB)
		repo        string
		pkg, ver    string
		wantCode    int
		available   bool
		wantSources []string
		wantMinAge  string
		wantDetail  string
	}{
		{name: "denylist block", setup: px(curation.ModeDenylist, 0, block), repo: "px", pkg: "left-pad", ver: "1.3.0",
			wantCode: 200, wantSources: []string{"curation"}, wantMinAge: "off", wantDetail: "malware"},
		{name: "denylist miss", setup: px(curation.ModeDenylist, 0, block), repo: "px", pkg: "left-pad", ver: "2.0.0",
			wantCode: 200, available: true, wantMinAge: "off"},
		{name: "allowlist miss", setup: px(curation.ModeAllowlist, 0, allow), repo: "px", pkg: "unlisted", ver: "2.0.0",
			wantCode: 200, wantSources: []string{"curation"}, wantMinAge: "off", wantDetail: "allowlist"},
		{name: "allowlist hit", setup: px(curation.ModeAllowlist, 0, allow), repo: "px", pkg: "vetted", ver: "1.0.0",
			wantCode: 200, available: true, wantMinAge: "off"},
		{name: "mode off ignores rules", setup: px(curation.ModeOff, 0, block), repo: "px", pkg: "left-pad", ver: "1.3.0",
			wantCode: 200, available: true, wantMinAge: "off"},
		{name: "too young", setup: px(curation.ModeOff, 72), repo: "px", pkg: "fresh", ver: "1.0.0",
			wantCode: 200, wantSources: []string{"min_age"}, wantMinAge: "checked"},
		{name: "old enough", setup: px(curation.ModeOff, 72), repo: "px", pkg: "left-pad", ver: "1.3.0",
			wantCode: 200, available: true, wantMinAge: "checked"},
		{name: "allow exempts the minimum age", setup: px(curation.ModeAllowlist, 72, allow), repo: "px", pkg: "vetted", ver: "1.0.0",
			wantCode: 200, available: true, wantMinAge: "checked"},
		{name: "block beats the age", setup: px(curation.ModeDenylist, 72, block), repo: "px", pkg: "left-pad", ver: "1.3.0",
			wantCode: 200, wantSources: []string{"curation"}, wantMinAge: "checked"},
		{name: "undated version with a minimum age", setup: px(curation.ModeOff, 72), repo: "px", pkg: "mystery", ver: "1.0.0",
			wantCode: 200, available: true, wantMinAge: "unknown"},
		{name: "blocked repository", setup: func(db *curationMockDB) { db.repos["px"].Blocked = true }, repo: "px", pkg: "a", ver: "1",
			wantCode: 200, wantSources: []string{"repository_blocked"}, wantMinAge: "off"},
		{
			name:        "quarantined hosted version",
			setup:       func(db *curationMockDB) { db.holds.Quarantined = true },
			repo:        "hosted",
			pkg:         "a",
			ver:         "1",
			wantCode:    200,
			wantSources: []string{"quarantine"},
			wantMinAge:  "off",
		},
		{name: "license blocked and quarantined", setup: func(db *curationMockDB) {
			db.holds = models.VersionHolds{Quarantined: true, LicenseBlocked: true}
		}, repo: "hosted", pkg: "a", ver: "1", wantCode: 200, wantSources: []string{"quarantine", "license_policy"}, wantMinAge: "off"},
		{name: "clean hosted version", repo: "hosted", pkg: "a", ver: "1", wantCode: 200, available: true, wantMinAge: "off"},
		{name: "blocked hosted repository", setup: func(db *curationMockDB) { db.repos["hosted"].Blocked = true }, repo: "hosted",
			pkg: "a", ver: "1", wantCode: 200, wantSources: []string{"repository_blocked"}, wantMinAge: "off"},
		{name: "group has no verdict", repo: "grp", pkg: "a", ver: "1", wantCode: 400},
		{name: "unknown repository", repo: "nope", pkg: "a", ver: "1", wantCode: 404},
		{name: "missing package", repo: "px", ver: "1", wantCode: 400},
		{name: "missing version", repo: "px", pkg: "a", wantCode: 400},
		{name: "oversized package", repo: "px", pkg: strings.Repeat("a", maxAvailabilityParam+1), ver: "1", wantCode: 400},
		{name: "holds failure", setup: func(db *curationMockDB) { db.holdsErr = errors.New("pq: boom") }, repo: "hosted",
			pkg: "a", ver: "1", wantCode: 500},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newCurationDB()
			if tc.setup != nil {
				tc.setup(db)
			}

			q := fmt.Sprintf("/api/v1/repositories/%s/availability?package=%s&version=%s", tc.repo, urlQuery(tc.pkg), urlQuery(tc.ver))

			rw := curationRequest(t, db, published, http.MethodGet, q, "")
			if rw.Code != tc.wantCode {
				t.Fatalf("status %d, want %d: %s", rw.Code, tc.wantCode, rw.Body.String())
			}

			if tc.wantCode != http.StatusOK {
				return
			}

			got := decodeResp[responses.AvailabilityResponse](t, rw)
			if got.Available != tc.available || got.MinAge != tc.wantMinAge {
				t.Errorf("available = %v min_age = %q, want %v %q", got.Available, got.MinAge, tc.available, tc.wantMinAge)
			}

			var sources []string
			for _, r := range got.Reasons {
				sources = append(sources, r.Source)
			}

			if strings.Join(sources, ",") != strings.Join(tc.wantSources, ",") {
				t.Errorf("reason sources = %v, want %v", sources, tc.wantSources)
			}

			if tc.wantDetail != "" && !strings.Contains(got.Reasons[0].Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to contain %q", got.Reasons[0].Detail, tc.wantDetail)
			}
		})
	}
}

func urlQuery(s string) string {
	return strings.NewReplacer("@", "%40", "/", "%2F").Replace(s)
}

// A hold carries what the console shows: the rule that blocks, the moment a
// minimum-age hold ends and the publish time the answer was based on.
func TestCurationAvailabilityDetails(t *testing.T) {
	published := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	db := newCurationDB()
	db.repos["px"].CurationMode, db.repos["px"].MinAgeHours = curation.ModeDenylist, 24
	db.rules = []curation.Rule{{ID: missingID, RepositoryID: "r-px", Action: curation.ActionBlock, Package: "bad", Version: "*"}}

	src := fakePublished{"young@1.0.0": published, "bad@1.0.0": published}

	young := decodeResp[responses.AvailabilityResponse](t,
		curationRequest(t, db, src, http.MethodGet, "/api/v1/repositories/px/availability?package=young&version=1.0.0", ""))
	if want := published.Add(24 * time.Hour).Format(time.RFC3339); len(young.Reasons) != 1 || young.Reasons[0].Until != want ||
		young.Reasons[0].RuleID != "" || young.PublishedAt != published.Format(time.RFC3339) {
		t.Errorf("young = %+v, want one min_age reason until %s published %s", young, want, published.Format(time.RFC3339))
	}

	bad := decodeResp[responses.AvailabilityResponse](t,
		curationRequest(t, db, src, http.MethodGet, "/api/v1/repositories/px/availability?package=bad&version=1.0.0", ""))
	if len(bad.Reasons) != 1 || bad.Reasons[0].RuleID != missingID || bad.Reasons[0].Until != "" {
		t.Errorf("bad = %+v, want one curation reason naming rule %s", bad, missingID)
	}
}

// Without a publish time source the minimum age cannot be checked, and a failing
// rule read is a 500, never an answer computed without the rules.
func TestCurationAvailabilityWithoutSourceOrRules(t *testing.T) {
	db := newCurationDB()
	db.repos["px"].MinAgeHours = 24

	got := decodeResp[responses.AvailabilityResponse](t,
		curationRequest(t, db, nil, http.MethodGet, "/api/v1/repositories/px/availability?package=a&version=1", ""))
	if !got.Available || got.MinAge != "unknown" {
		t.Errorf("no source: %+v, want available with min_age unknown", got)
	}

	db.listErr = errors.New("pq: down")
	if rw := curationRequest(t, db, nil, http.MethodGet, "/api/v1/repositories/px/availability?package=a&version=1", ""); rw.Code !=
		http.StatusInternalServerError {
		t.Errorf("rules read failure: status %d, want 500", rw.Code)
	}
}
