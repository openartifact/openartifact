// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/nix"
)

// nixStoreHash is a valid store path hash (32 nixbase32 characters).
const nixStoreHash = "0123456789abcdfghijklmnpqrsvwxyz"

const nixNarinfoPath = nixStoreHash + ".narinfo"

// fakeNixKeys answers the key of every repository it is asked about.
type fakeNixKeys struct {
	key   *nix.SecretKey
	err   error
	asked []string
}

func (k *fakeNixKeys) SecretKeyForRepo(_ context.Context, repo *models.Repository) (*nix.SecretKey, error) {
	k.asked = append(k.asked, repo.Name)

	return k.key, k.err
}

func newNixKey(t *testing.T, name string) *nix.SecretKey {
	t.Helper()

	key, err := nix.GenerateKey(name)
	if err != nil {
		t.Fatal(err)
	}

	return key
}

// nixNarinfo renders a narinfo of the test path signed by the given keys.
func nixNarinfo(t *testing.T, signers ...*nix.SecretKey) string {
	t.Helper()

	var narHash, fileHash nix.SHA256

	narHash[0], fileHash[0] = 1, 2

	n := &nix.NarInfo{
		StorePath:   nix.StoreDir + "/" + nixStoreHash + "-hello-2.12",
		URL:         "nar/" + fileHash.Base32() + ".nar.xz",
		Compression: "xz",
		FileHash:    fileHash.String(),
		FileSize:    1000,
		NarHash:     narHash.String(),
		NarSize:     4000,
		References:  []string{nixStoreHash + "-hello-2.12"},
	}

	for _, key := range signers {
		sig, err := key.Sign(n)
		if err != nil {
			t.Fatal(err)
		}

		n.Sigs = append(n.Sigs, sig)
	}

	return n.String()
}

// verifiesUnder reports whether sig is a valid signature of n by publicKey ("name:base64").
func verifiesUnder(t *testing.T, publicKey, sig string, n *nix.NarInfo) bool {
	t.Helper()

	pkName, pkB64, _ := strings.Cut(publicKey, ":")
	sigName, sigB64, _ := strings.Cut(sig, ":")

	pub, err := base64.StdEncoding.DecodeString(pkB64)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatal(err)
	}

	fp, err := n.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}

	return pkName == sigName && ed25519.Verify(pub, []byte(fp), raw)
}

// nixFixture is a nix group over fake members; members are asked through a
// backend that records the requests they got.
type nixFixture struct {
	*fixture

	keys *fakeNixKeys
	seen []*http.Request
}

func newNix(t *testing.T, key *nix.SecretKey, members ...*models.Repository) *nixFixture {
	t.Helper()

	f := &nixFixture{fixture: newFixture(t, "nix", members...), keys: &fakeNixKeys{key: key}}
	f.h.WithNixKeys(f.keys)
	f.grp.Public = true

	return f
}

func (f *nixFixture) do(method, path string, hdr http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, "/", nil)
	inner := f.backend()

	maps.Copy(req.Header, hdr)

	be := Backend{
		Authorize: inner.Authorize,
		ServeMember: func(w http.ResponseWriter, r *http.Request, m *models.Repository, p string) {
			f.seen = append(f.seen, r)
			inner.ServeMember(w, r, m, p)
		},
	}

	w := httptest.NewRecorder()
	f.h.Serve(w, req, f.grp, path, be)

	return w
}

func (f *nixFixture) get(path string) *httptest.ResponseRecorder {
	return f.do(http.MethodGet, path, nil)
}

func parseNarinfo(t *testing.T, w *httptest.ResponseRecorder) *nix.NarInfo {
	t.Helper()

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}

	n, err := nix.ParseNarInfo(w.Body.Bytes())
	if err != nil {
		t.Fatalf("response is not a narinfo: %v\n%s", err, w.Body)
	}

	return n
}

func TestNixGroupSignsAHostedNarinfoWithItsOwnKey(t *testing.T) {
	memberKey, groupKey := newNixKey(t, "member-1"), newNixKey(t, "group-1")
	f := newNix(t, groupKey, member("hosted", "nix", "hosted"))
	f.set("hosted", nixNarinfoPath, http.StatusOK, nixNarinfo(t, memberKey))

	w := f.get(nixNarinfoPath)
	n := parseNarinfo(t, w)

	if w.Header().Get("Content-Type") != "text/x-nix-narinfo" {
		t.Errorf("Content-Type = %q", w.Header().Get("Content-Type"))
	}

	if len(n.Sigs) != 2 {
		t.Fatalf("signatures = %v, want the member's and the group's", n.Sigs)
	}

	if !verifiesUnder(t, memberKey.PublicKey(), n.Sigs[0], n) {
		t.Error("the member's own signature must stay and verify")
	}

	if !verifiesUnder(t, groupKey.PublicKey(), n.Sigs[1], n) {
		t.Error("the group's signature must verify under the group's public key")
	}

	if !slices.Equal(f.keys.asked, []string{"all"}) {
		t.Errorf("key asked for %v, want the group itself", f.keys.asked)
	}
}

func TestNixGroupReplacesItsOwnEarlierSignature(t *testing.T) {
	groupKey := newNixKey(t, "group-1")
	f := newNix(t, groupKey, member("hosted", "nix", "hosted"))
	// The member relays a signature by a key that has the group key's name.
	f.set("hosted", nixNarinfoPath, http.StatusOK, nixNarinfo(t, newNixKey(t, "group-1")))

	n := parseNarinfo(t, f.get(nixNarinfoPath))

	if len(n.Sigs) != 1 || !verifiesUnder(t, groupKey.PublicKey(), n.Sigs[0], n) {
		t.Errorf("signatures = %v, want exactly the group's current one", n.Sigs)
	}
}

func TestNixGroupServesAProxyNarinfoUntouched(t *testing.T) {
	upstream := newNixKey(t, "cache.example.org-1")
	// Not in the form String renders: field order and an unknown field must survive.
	body := "URL: nar/x.nar.xz\n" + strings.TrimPrefix(nixNarinfo(t, upstream), "StorePath: ") + "Unknown: kept\n"
	body = "StorePath: " + body

	f := newNix(t, newNixKey(t, "group-1"), member("up", "nix", "proxy"))
	f.set("up", nixNarinfoPath, http.StatusOK, body)

	w := f.get(nixNarinfoPath)
	if w.Code != http.StatusOK || w.Body.String() != body {
		t.Fatalf("proxy narinfo = %d\n%s\nwant it byte-identical:\n%s", w.Code, w.Body, body)
	}

	if len(f.keys.asked) != 0 {
		t.Errorf("the group loaded its key (%v) for a narinfo it does not vouch for", f.keys.asked)
	}
}

func TestNixGroupAsksHostedMembersFirst(t *testing.T) {
	for _, path := range []string{nixNarinfoPath, "nar/file.nar.xz", "debuginfo/abc"} {
		f := newNix(t, nil, member("up", "nix", "proxy"), member("mine", "nix", "hosted"))
		f.set("up", path, http.StatusOK, "from-proxy")
		f.set("mine", path, http.StatusOK, nixNarinfo(t))

		w := f.get(path)
		if w.Code != http.StatusOK || w.Body.String() == "from-proxy" {
			t.Errorf("%s: answered from the proxy: %d %q", path, w.Code, w.Body)
		}

		if !slices.Equal(f.asked, []string{"mine"}) {
			t.Errorf("%s: members asked = %v, want only the hosted one", path, f.asked)
		}
	}
}

func TestNixGroupPassesMembersOver(t *testing.T) {
	tests := map[string]struct {
		hosted, proxy int
		want          int
		wantBody      string
	}{
		"hosted 404 then the proxy has it":    {http.StatusNotFound, http.StatusOK, http.StatusOK, "from-proxy"},
		"hosted 500 then the proxy has it":    {http.StatusInternalServerError, http.StatusOK, http.StatusOK, "from-proxy"},
		"nobody has it":                       {http.StatusNotFound, http.StatusNotFound, http.StatusNotFound, ""},
		"a failing proxy hides the absence":   {http.StatusNotFound, http.StatusBadGateway, http.StatusBadGateway, ""},
		"a failing hosted member as well":     {http.StatusServiceUnavailable, http.StatusNotFound, http.StatusBadGateway, ""},
		"a quarantined narinfo is the answer": {http.StatusForbidden, http.StatusOK, http.StatusForbidden, "quarantined"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newNix(t, nil, member("mine", "nix", "hosted"), member("up", "nix", "proxy"))
			f.set("mine", nixNarinfoPath, tc.hosted, "quarantined")
			f.set("up", nixNarinfoPath, tc.proxy, "from-proxy")

			w := f.get(nixNarinfoPath)
			if w.Code != tc.want || (tc.wantBody != "" && w.Body.String() != tc.wantBody) {
				t.Errorf("status %d body %q, want %d %q", w.Code, w.Body, tc.want, tc.wantBody)
			}
		})
	}
}

func TestNixGroupHeadAsksMembersWithGet(t *testing.T) {
	f := newNix(t, newNixKey(t, "group-1"), member("mine", "nix", "hosted"))
	f.set("mine", nixNarinfoPath, http.StatusOK, nixNarinfo(t))

	hdr := http.Header{"If-None-Match": {`"stale"`}, "If-Modified-Since": {"Mon, 02 Jan 2006 15:04:05 GMT"}}
	w := f.do(http.MethodHead, nixNarinfoPath, hdr)

	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("HEAD narinfo = %d with %d body bytes", w.Code, w.Body.Len())
	}

	get := f.get(nixNarinfoPath)
	if w.Header().Get("Content-Length") != get.Header().Get("Content-Length") || w.Header().Get("Content-Length") == "0" {
		t.Errorf("HEAD Content-Length %q, GET %q", w.Header().Get("Content-Length"), get.Header().Get("Content-Length"))
	}

	if m := f.seen[0]; m.Method != http.MethodGet || m.Header.Get("If-None-Match") != "" || m.Header.Get("If-Modified-Since") != "" {
		t.Errorf("the member was asked with %s and headers %v", m.Method, m.Header)
	}
}

func TestNixGroupHeadOfAProxyNarinfo(t *testing.T) {
	f := newNix(t, nil, member("up", "nix", "proxy"))
	f.set("up", nixNarinfoPath, http.StatusOK, nixNarinfo(t))

	if w := f.do(http.MethodHead, nixNarinfoPath, nil); w.Code != http.StatusOK {
		t.Errorf("HEAD = %d", w.Code)
	}
}

func TestNixGroupNarinfoRevalidates(t *testing.T) {
	f := newNix(t, newNixKey(t, "group-1"), member("mine", "nix", "hosted"))
	f.set("mine", nixNarinfoPath, http.StatusOK, nixNarinfo(t))

	first := f.get(nixNarinfoPath)
	etag := first.Header().Get("ETag")

	if etag == "" || first.Header().Get("Cache-Control") == "" {
		t.Fatalf("ETag %q, Cache-Control %q", etag, first.Header().Get("Cache-Control"))
	}

	if w := f.do(http.MethodGet, nixNarinfoPath, http.Header{"If-None-Match": {etag}}); w.Code != http.StatusNotModified {
		t.Errorf("revalidation = %d, want 304", w.Code)
	}
}

func TestNixGroupWithoutAKeyServesTheMemberNarinfoAsIs(t *testing.T) {
	body := nixNarinfo(t, newNixKey(t, "member-1"))

	for name, keys := range map[string]*fakeNixKeys{"no secret key": {}, "no provider": nil} {
		t.Run(name, func(t *testing.T) {
			f := newNix(t, nil, member("mine", "nix", "hosted"))
			if keys == nil {
				f.h.nixKeys = nil
			}

			f.set("mine", nixNarinfoPath, http.StatusOK, body)

			if w := f.get(nixNarinfoPath); w.Code != http.StatusOK || w.Body.String() != body {
				t.Errorf("narinfo = %d\n%s\nwant it unchanged:\n%s", w.Code, w.Body, body)
			}
		})
	}
}

func TestNixGroupKeyFailureIsNotAnUnsignedAnswer(t *testing.T) {
	f := newNix(t, nil, member("mine", "nix", "hosted"), member("up", "nix", "proxy"))
	f.keys.err = errors.New("database down")
	f.set("mine", nixNarinfoPath, http.StatusOK, nixNarinfo(t))
	f.set("up", nixNarinfoPath, http.StatusOK, "from-proxy")

	if w := f.get(nixNarinfoPath); w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "database") {
		t.Errorf("narinfo with a failing key = %d %q, want a plain 500", w.Code, w.Body)
	}
}

func TestNixGroupAnswersCacheInfoAndPublicKeyItself(t *testing.T) {
	groupKey := newNixKey(t, "group-1")
	f := newNix(t, groupKey, member("up", "nix", "proxy"))

	info := f.get("nix-cache-info")
	if info.Code != http.StatusOK || !strings.Contains(info.Body.String(), "StoreDir: /nix/store\n") ||
		!strings.Contains(info.Body.String(), "WantMassQuery: 1\n") || !strings.Contains(info.Body.String(), "Priority: 30\n") {
		t.Errorf("nix-cache-info = %d %q", info.Code, info.Body)
	}

	if key := f.get("public.key"); key.Code != http.StatusOK || key.Body.String() != groupKey.PublicKey()+"\n" {
		t.Errorf("public.key = %d %q, want %q", key.Code, key.Body, groupKey.PublicKey())
	}

	if head := f.do(http.MethodHead, "public.key", nil); head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Errorf("HEAD public.key = %d with %d body bytes", head.Code, head.Body.Len())
	}

	if len(f.asked) != 0 {
		t.Errorf("members asked for the group's own documents: %v", f.asked)
	}
}

func TestNixGroupPublicKeyNeedsAKey(t *testing.T) {
	f := newNix(t, nil, member("up", "nix", "proxy"))

	if w := f.get("public.key"); w.Code != http.StatusNotFound {
		t.Errorf("public.key without a key = %d, want 404", w.Code)
	}

	f.keys.err = errors.New("database down")

	if w := f.get("public.key"); w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "database") {
		t.Errorf("public.key with a failing key = %d %q", w.Code, w.Body)
	}
}

func TestNixGroupSkipsMembersTheCallerCannotRead(t *testing.T) {
	f := newNix(t, newNixKey(t, "group-1"), member("secret", "nix", "hosted"), member("up", "nix", "proxy"))
	f.denied["secret"] = true
	f.set("secret", nixNarinfoPath, http.StatusOK, nixNarinfo(t))
	f.set("up", nixNarinfoPath, http.StatusOK, "from-proxy")

	if w := f.get(nixNarinfoPath); w.Body.String() != "from-proxy" || slices.Contains(f.asked, "secret") {
		t.Errorf("answer %q, members asked %v", w.Body, f.asked)
	}

	f.denied["up"] = true

	for _, path := range []string{nixNarinfoPath, "nix-cache-info", "public.key"} {
		if w := f.get(path); w.Code != http.StatusNotFound {
			t.Errorf("%s with no readable member = %d, want 404", path, w.Code)
		}
	}
}

func TestNixGroupIsReadOnlyAndBlockable(t *testing.T) {
	f := newNix(t, nil, member("mine", "nix", "hosted"))
	f.set("mine", nixNarinfoPath, http.StatusOK, nixNarinfo(t))

	for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodDelete} {
		if w := f.do(method, nixNarinfoPath, nil); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", method, w.Code)
		}
	}

	f.grp.Blocked = true

	for _, path := range []string{nixNarinfoPath, "nix-cache-info", "public.key", "nar/f.nar"} {
		if w := f.get(path); w.Code != http.StatusForbidden {
			t.Errorf("%s of a blocked group = %d, want 403", path, w.Code)
		}
	}

	if len(f.asked) != 0 {
		t.Errorf("a blocked or read-only group asked members: %v", f.asked)
	}
}
