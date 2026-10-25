// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

type groupDB struct {
	databasetest.BaseMockDB

	links []*models.RepositoryGroupMember
	repos map[string]*models.Repository
}

func (d *groupDB) ListGroupMemberRepositories(context.Context, string) ([]*models.Repository, error) {
	repos := make([]*models.Repository, 0, len(d.links))

	for _, l := range d.links {
		repos = append(repos, d.repos[l.MemberRepoID])
	}

	return repos, nil
}

// memberReply is what a fake member answers for a path.
type memberReply struct {
	status int
	body   string
}

type fixture struct {
	h       *Handler
	grp     *models.Repository
	replies map[string]map[string]memberReply // member name -> path -> reply
	denied  map[string]bool                   // member name -> caller may not read
	asked   []string                          // members asked, in order
}

func newFixture(tb testing.TB, format string, members ...*models.Repository) *fixture {
	tb.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		tb.Fatal(err)
	}

	db := &groupDB{repos: map[string]*models.Repository{}}

	for i, m := range members {
		m.ID = "id-" + m.Name
		db.repos[m.ID] = m
		db.links = append(db.links, &models.RepositoryGroupMember{MemberRepoID: m.ID, Priority: i})
	}

	return &fixture{
		h: NewHandler(log, db),
		// The merge tests predate hosted wins and pin the union behavior;
		// hostedwins_test.go turns it off.
		grp:     &models.Repository{ID: "grp", Name: "all", Format: format, RepoType: "group", MergeProxyVersions: true},
		replies: map[string]map[string]memberReply{},
		denied:  map[string]bool{},
	}
}

func member(name, format, repoType string) *models.Repository {
	return &models.Repository{Name: name, Format: format, RepoType: repoType}
}

func (f *fixture) backend() Backend {
	return Backend{
		Authorize: func(_ *http.Request, m *models.Repository, _ string) bool { return !f.denied[m.Name] },
		ServeMember: func(w http.ResponseWriter, _ *http.Request, m *models.Repository, p string) {
			f.asked = append(f.asked, m.Name)

			rep, ok := f.replies[m.Name][p]
			if !ok {
				http.NotFound(w, nil)
				return
			}

			w.WriteHeader(rep.status)
			_, _ = io.WriteString(w, rep.body)
		},
	}
}

func (f *fixture) get(method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, "/", nil)
	w := httptest.NewRecorder()
	f.h.Serve(w, req, f.grp, path, f.backend())

	return w
}

func (f *fixture) set(memberName, path string, status int, body string) {
	if f.replies[memberName] == nil {
		f.replies[memberName] = map[string]memberReply{}
	}

	f.replies[memberName][path] = memberReply{status, body}
}

func TestServe_FirstMemberInOrderWins(t *testing.T) {
	f := newFixture(t, "raw", member("a", "raw", "hosted"), member("b", "raw", "proxy"))
	f.set("a", "f.txt", 200, "from-a")
	f.set("b", "f.txt", 200, "from-b")
	f.set("b", "only-b.txt", 200, "only-b")

	if w := f.get(http.MethodGet, "f.txt"); w.Body.String() != "from-a" {
		t.Errorf("f.txt = %q, want from-a", w.Body.String())
	}

	if w := f.get(http.MethodGet, "only-b.txt"); w.Code != 200 || w.Body.String() != "only-b" {
		t.Errorf("only-b.txt = %d %q, want fall-through to b", w.Code, w.Body.String())
	}
}

func TestServe_StopsAtFirstHit(t *testing.T) {
	f := newFixture(t, "raw", member("a", "raw", "hosted"), member("b", "raw", "hosted"))
	f.set("a", "f", 200, "x")
	f.set("b", "f", 200, "y")

	f.get(http.MethodGet, "f")

	if len(f.asked) != 1 || f.asked[0] != "a" {
		t.Errorf("members asked = %v, want only [a]", f.asked)
	}
}

func TestServe_NoMemberHasFileIs404(t *testing.T) {
	f := newFixture(t, "raw", member("a", "raw", "hosted"), member("b", "raw", "proxy"))

	if w := f.get(http.MethodGet, "nope"); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestServe_FailingMemberIsSkippedButNotMistakenForAbsence(t *testing.T) {
	f := newFixture(t, "raw", member("a", "raw", "proxy"), member("b", "raw", "hosted"))
	f.set("a", "f", 502, "bad gateway")
	f.set("b", "f", 200, "from-b")

	if w := f.get(http.MethodGet, "f"); w.Code != 200 || w.Body.String() != "from-b" {
		t.Errorf("= %d %q, want the healthy member's answer", w.Code, w.Body.String())
	}

	if w := f.get(http.MethodGet, "missing"); w.Code != http.StatusNotFound {
		t.Errorf("missing with healthy 404s = %d, want 404", w.Code)
	}

	f.set("b", "g", 500, "boom")

	if w := f.get(http.MethodGet, "g"); w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "boom") {
		t.Errorf("all members failing = %d %q, want generic 502", w.Code, w.Body.String())
	}
}

func TestServe_PrivateMemberDoesNotLeakThroughGroup(t *testing.T) {
	f := newFixture(t, "raw", member("private", "raw", "hosted"), member("public", "raw", "hosted"))
	f.set("private", "secret.txt", 200, "TOP SECRET")
	f.set("private", "shared.txt", 200, "private-version")
	f.set("public", "shared.txt", 200, "public-version")
	f.denied["private"] = true

	if w := f.get(http.MethodGet, "secret.txt"); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "SECRET") {
		t.Errorf("secret.txt = %d %q, want a plain 404", w.Code, w.Body.String())
	}

	if w := f.get(http.MethodGet, "shared.txt"); w.Body.String() != "public-version" {
		t.Errorf("shared.txt = %q, want the readable member's version", w.Body.String())
	}

	for _, asked := range f.asked {
		if asked == "private" {
			t.Error("a member the caller cannot read was consulted")
		}
	}
}

func TestServe_WritesAreRefused(t *testing.T) {
	f := newFixture(t, "raw", member("a", "raw", "hosted"))

	for _, m := range []string{http.MethodPut, http.MethodPost, http.MethodDelete} {
		w := f.get(m, "f")
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s = %d Allow %q, want 405", m, w.Code, w.Header().Get("Allow"))
		}
	}

	if len(f.asked) != 0 {
		t.Errorf("members consulted for a write: %v", f.asked)
	}
}

func TestServe_IgnoresNestedGroupsMismatchedFormatsAndBlockedMembers(t *testing.T) {
	blocked := member("blocked", "raw", "proxy")
	blocked.Blocked = true

	f := newFixture(t, "raw", member("nested", "raw", "group"), member("deb", "deb", "hosted"), blocked, member("ok", "raw", "hosted"))
	for _, n := range []string{"nested", "deb", "blocked", "ok"} {
		f.set(n, "f", 200, n)
	}

	if w := f.get(http.MethodGet, "f"); w.Body.String() != "ok" {
		t.Errorf("f = %q, want ok", w.Body.String())
	}
}

const metaPath = "org/x/lib/maven-metadata.xml"

func versionsDoc(updated string, versions ...string) string {
	var b strings.Builder

	b.WriteString("<metadata><groupId>org.x</groupId><artifactId>lib</artifactId><versioning><versions>")

	for _, v := range versions {
		b.WriteString("<version>" + v + "</version>")
	}

	b.WriteString("</versions><lastUpdated>" + updated + "</lastUpdated></versioning></metadata>")

	return b.String()
}

func TestServe_MavenMetadataIsMergedAcrossMembers(t *testing.T) {
	f := newFixture(t, "maven", member("hosted", "maven", "hosted"), member("central", "maven", "proxy"))
	f.set("hosted", metaPath, 200, versionsDoc("20240101000000", "1.0", "1.1"))
	f.set("central", metaPath, 200, versionsDoc("20240201000000", "1.1", "2.0"))

	w := f.get(http.MethodGet, metaPath)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}

	body := w.Body.String()
	for _, v := range []string{"<version>1.0</version>", "<version>1.1</version>", "<version>2.0</version>", "<latest>2.0</latest>"} {
		if !strings.Contains(body, v) {
			t.Errorf("merged metadata lacks %s:\n%s", v, body)
		}
	}

	if len(f.asked) != 2 {
		t.Errorf("members asked = %v, want both", f.asked)
	}
}

func TestServe_MavenMetadataChecksumMatchesMergedBytes(t *testing.T) {
	f := newFixture(t, "maven", member("hosted", "maven", "hosted"), member("central", "maven", "proxy"))
	f.set("hosted", metaPath, 200, versionsDoc("1", "1.0"))
	f.set("central", metaPath, 200, versionsDoc("2", "2.0"))

	merged := f.get(http.MethodGet, metaPath).Body.Bytes()
	sum := sha1.Sum(merged)

	w := f.get(http.MethodGet, metaPath+".sha1")
	if got := strings.Fields(w.Body.String()); w.Code != 200 || len(got) == 0 || got[0] != hex.EncodeToString(sum[:]) {
		t.Errorf("sha1 = %d %q, want %x", w.Code, w.Body.String(), sum)
	}
}

func TestServe_MavenMetadataSingleContributorPassesThroughUnchanged(t *testing.T) {
	f := newFixture(t, "maven", member("hosted", "maven", "hosted"), member("central", "maven", "proxy"))
	doc := versionsDoc("1", "1.0")
	f.set("central", metaPath, 200, doc)

	if w := f.get(http.MethodGet, metaPath); w.Body.String() != doc {
		t.Errorf("body rewritten: %q", w.Body.String())
	}

	if w := f.get(http.MethodHead, metaPath); w.Code != 200 || w.Body.Len() != 0 {
		t.Errorf("HEAD = %d with %d body bytes", w.Code, w.Body.Len())
	}
}

func TestServe_MavenMetadataSkipsUnreadableAndFailedMembers(t *testing.T) {
	f := newFixture(t, "maven", member("private", "maven", "hosted"), member("flaky", "maven", "proxy"), member("ok", "maven", "hosted"))
	f.set("private", metaPath, 200, versionsDoc("1", "9.9-private"))
	f.set("flaky", metaPath, 502, "")
	f.set("ok", metaPath, 200, versionsDoc("1", "1.0"))
	f.denied["private"] = true

	w := f.get(http.MethodGet, metaPath)
	if w.Code != 200 || strings.Contains(w.Body.String(), "private") || !strings.Contains(w.Body.String(), "1.0") {
		t.Errorf("= %d %q", w.Code, w.Body.String())
	}
}

func TestServe_MavenArtifactsUseFirstHitNotMerge(t *testing.T) {
	f := newFixture(t, "maven", member("hosted", "maven", "hosted"), member("central", "maven", "proxy"))
	f.set("hosted", "org/x/lib/1.0/lib-1.0.jar", 200, "hosted-jar")
	f.set("central", "org/x/lib/1.0/lib-1.0.jar", 200, "central-jar")

	if w := f.get(http.MethodGet, "org/x/lib/1.0/lib-1.0.jar"); w.Body.String() != "hosted-jar" {
		t.Errorf("jar = %q", w.Body.String())
	}
}

func TestMavenMetadataPath(t *testing.T) {
	cases := map[string]bool{
		"g/a/maven-metadata.xml":        true,
		"g/a/maven-metadata.xml.sha1":   true,
		"g/a/maven-metadata.xml.sha512": true,
		"g/a/maven-metadata.xml.asc":    false,
		"g/a/1.0/a-1.0.jar":             false,
		"g/a/other-maven-metadata.xml":  false,
	}

	for p, want := range cases {
		if _, _, ok := mavenMetadataPath(p); ok != want {
			t.Errorf("mavenMetadataPath(%q) = %v, want %v", p, ok, want)
		}
	}
}

func TestServe_HitIsNotSniffableAsHTML(t *testing.T) {
	f := newFixture(t, "raw", member("a", "raw", "hosted"))
	f.set("a", "x.html", 200, "<script>alert(1)</script>")

	w := f.get(http.MethodGet, "x.html")

	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}

	if got := w.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream default", got)
	}
}
