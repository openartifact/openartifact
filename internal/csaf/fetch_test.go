// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package csaf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// provider is an httptest CSAF provider serving files from memory.
type provider struct {
	*httptest.Server

	mu        sync.Mutex
	files     map[string][]byte
	requested []string
}

func newProvider(t *testing.T) *provider {
	t.Helper()

	p := &provider{files: map[string][]byte{}}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()

		p.requested = append(p.requested, r.URL.Path)

		body, ok := p.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}

		_, _ = w.Write(body)
	}))
	t.Cleanup(p.Close)

	return p
}

func (p *provider) set(path string, body []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.files[path] = body
}

func (p *provider) wasRequested(path string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return slices.Contains(p.requested, path)
}

// testKey is a throwaway OpenPGP key generated for the test.
type testKey struct {
	entity *openpgp.Entity
	public string // armored
}

func newTestKey(t *testing.T, name string) testKey {
	t.Helper()

	e, err := openpgp.NewEntity(name, "", "psirt@acme.example", &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA})
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer

	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := e.Serialize(w); err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	return testKey{entity: e, public: buf.String()}
}

func (k testKey) fingerprint() string {
	return strings.ToUpper(hex.EncodeToString(k.entity.PrimaryKey.Fingerprint))
}

func (k testKey) sign(t *testing.T, body []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&buf, k.entity, bytes.NewReader(body), nil); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func sha256File(body []byte, name string) []byte {
	s := sha256.Sum256(body)
	return []byte(hex.EncodeToString(s[:]) + "  " + name + "\n")
}

func sha512File(body []byte, name string) []byte {
	s := sha512.Sum512(body)
	return []byte(hex.EncodeToString(s[:]) + "  " + name + "\n")
}

func rolieEntryJSON(base, name, updated string, hash, sig bool) string {
	links := `{"rel":"self","href":"` + base + `/docs/` + name + `"}`
	if hash {
		links += `,{"rel":"hash","href":"` + base + `/docs/` + name + `.sha256"}`
	}

	if sig {
		links += `,{"rel":"signature","href":"` + base + `/docs/` + name + `.asc"}`
	}

	return `{"id":"` + name + `","updated":"` + updated + `","link":[` + links + `],` +
		`"content":{"type":"application/json","src":"` + base + `/docs/` + name + `"}}`
}

func ts(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}

	return &t
}

func fetch(t *testing.T, p *provider, src Source) *Result {
	t.Helper()

	res, err := NewFetcher(p.Client()).Fetch(context.Background(), src)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	return res
}

// TestFetchProviderMetadata walks a provider with a ROLIE feed and a directory
// distribution, checking hashes and signatures against the metadata's key.
func TestFetchProviderMetadata(t *testing.T) {
	p := newProvider(t)
	acme, other := newTestKey(t, "Acme"), newTestKey(t, "Other")
	doc := readFixture(t)

	p.set("/keys/acme.asc", []byte(acme.public))
	p.set("/keys/other.asc", []byte(other.public))
	p.set("/provider-metadata.json", []byte(`{"metadata_version":"2.0","role":"csaf_trusted_provider",
		"public_openpgp_keys":[
			{"fingerprint":"`+acme.fingerprint()+`","url":"`+p.URL+`/keys/acme.asc"},
			{"fingerprint":"0000000000000000000000000000000000000000","url":"/keys/other.asc"}],
		"distributions":[{"rolie":{"feeds":[{"tlp_label":"WHITE","url":"`+p.URL+`/feed.json"}]}},{"directory_url":"`+p.URL+`/dir"}]}`))

	p.set("/feed.json", []byte(`{"feed":{"id":"acme-white","entry":[`+strings.Join([]string{
		rolieEntryJSON(p.URL, "good.json", "2024-05-03T00:00:00Z", true, true),
		rolieEntryJSON(p.URL, "badhash.json", "2024-05-04T00:00:00Z", true, true),
		rolieEntryJSON(p.URL, "tampered.json", "2024-05-05T00:00:00Z", false, true),
		rolieEntryJSON(p.URL, "unsigned.json", "2024-05-06T00:00:00Z", false, false),
		rolieEntryJSON(p.URL, "otherkey.json", "2024-05-07T00:00:00Z", false, true),
		rolieEntryJSON(p.URL, "old.json", "2024-01-01T00:00:00Z", false, false),
	}, ",")+`]}}`))

	for _, name := range []string{"good.json", "badhash.json", "tampered.json", "unsigned.json", "otherkey.json"} {
		p.set("/docs/"+name, doc)
	}

	p.set("/docs/good.json.sha256", sha256File(doc, "good.json"))
	p.set("/docs/good.json.asc", acme.sign(t, doc))
	p.set("/docs/badhash.json.sha256", sha256File([]byte("something else"), "badhash.json"))
	p.set("/docs/badhash.json.asc", acme.sign(t, doc))
	p.set("/docs/tampered.json", bytes.Replace(doc, []byte("never called"), []byte("always called"), 1))
	p.set("/docs/tampered.json.asc", acme.sign(t, doc))
	p.set("/docs/otherkey.json.asc", other.sign(t, doc))

	p.set("/dir/changes.csv", []byte("\"2024/dir.json\",\"2024-05-02T00:00:00Z\"\n\"2023/old-dir.json\",\"2023-12-01T00:00:00Z\"\n"))
	p.set("/dir/2024/dir.json", doc)
	p.set("/dir/2024/dir.json.sha512", sha512File(doc, "dir.json"))
	p.set("/dir/2024/dir.json.asc", acme.sign(t, doc))

	res := fetch(t, p, Source{URL: p.URL + "/provider-metadata.json", RequireSignature: true, Since: ts("2024-03-01T00:00:00Z")})

	var got []string
	for _, d := range res.Documents {
		got = append(got, strings.TrimPrefix(d.URL, p.URL))
	}

	if want := []string{"/dir/2024/dir.json", "/docs/good.json"}; !slices.Equal(got, want) {
		t.Errorf("documents = %v, want %v", got, want)
	}

	if len(res.Documents) > 0 && len(res.Documents[0].Statements) != 6 {
		t.Errorf("statements = %d, want 6", len(res.Documents[0].Statements))
	}

	reasons := map[string]string{}
	for _, r := range res.Rejected {
		reasons[strings.TrimPrefix(r.URL, p.URL)] = r.Reason
	}

	for doc, want := range map[string]string{
		"/docs/badhash.json":  "hash mismatch",
		"/docs/tampered.json": "invalid OpenPGP signature",
		"/docs/unsigned.json": "not signed",
		// The other key is listed with a fingerprint it does not have: not trusted.
		"/docs/otherkey.json": "invalid OpenPGP signature",
	} {
		if !strings.Contains(reasons[doc], want) {
			t.Errorf("%s rejected with %q, want %q", doc, reasons[doc], want)
		}
	}

	if len(res.Rejected) != 4 {
		t.Errorf("rejected = %+v", res.Rejected)
	}

	if res.Cursor == nil || !res.Cursor.Equal(*ts("2024-05-03T00:00:00Z")) {
		t.Errorf("cursor = %v, want the newest accepted change", res.Cursor)
	}

	if p.wasRequested("/docs/old.json") || p.wasRequested("/dir/2023/old-dir.json") {
		t.Error("documents not changed since the cursor were fetched")
	}
}

// TestFetchSingleDocument covers a source URL that is one document: Since,
// probed hash files, pinned keys and the signature policy.
func TestFetchSingleDocument(t *testing.T) {
	acme, other := newTestKey(t, "Acme"), newTestKey(t, "Other")
	doc := readFixture(t)

	cases := []struct {
		name    string
		src     Source
		sig     []byte
		hash    []byte
		docs    int
		reason  string
		noFetch bool
	}{
		{name: "unsigned, not required", docs: 1},
		{name: "unsigned, required", src: Source{RequireSignature: true}, reason: "not signed"},
		{name: "signed, no key, required", src: Source{RequireSignature: true}, sig: acme.sign(t, doc), reason: "no OpenPGP key"},
		{name: "signed, no key, not required", sig: acme.sign(t, doc), docs: 1},
		{name: "pinned key", src: Source{RequireSignature: true, PGPKeys: acme.public}, sig: acme.sign(t, doc), docs: 1},
		{name: "pinned other key", src: Source{PGPKeys: other.public}, sig: acme.sign(t, doc), reason: "invalid OpenPGP signature"},
		{name: "garbage signature", src: Source{PGPKeys: acme.public}, sig: []byte("not a signature"), reason: "invalid OpenPGP signature"},
		{name: "probed hash matches", hash: sha256File(doc, "vex.json"), docs: 1},
		{name: "probed hash mismatch", hash: sha256File([]byte("x"), "vex.json"), reason: "hash mismatch"},
		{name: "released before since", src: Source{Since: ts("2024-05-01T10:00:00Z")}, noFetch: true},
		{name: "released after since", src: Source{Since: ts("2024-04-30T00:00:00Z")}, docs: 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newProvider(t)
			p.set("/vex.json", doc)

			if c.sig != nil {
				p.set("/vex.json.asc", c.sig)
			}

			if c.hash != nil {
				p.set("/vex.json.sha256", c.hash)
			}

			c.src.URL = p.URL + "/vex.json"
			res := fetch(t, p, c.src)

			if len(res.Documents) != c.docs {
				t.Errorf("documents = %d, want %d (rejected %+v)", len(res.Documents), c.docs, res.Rejected)
			}

			if c.docs == 1 && (res.Cursor == nil || !res.Cursor.Equal(*ts("2024-05-01T10:00:00Z"))) {
				t.Errorf("cursor = %v, want the release date", res.Cursor)
			}

			switch {
			case c.reason != "" && (len(res.Rejected) != 1 || !strings.Contains(res.Rejected[0].Reason, c.reason)):
				t.Errorf("rejected = %+v, want %q", res.Rejected, c.reason)
			case c.reason == "" && len(res.Rejected) != 0:
				t.Errorf("rejected = %+v", res.Rejected)
			case c.noFetch && (p.wasRequested("/vex.json.asc") || res.Cursor != nil):
				t.Error("an unchanged document was processed")
			}
		})
	}
}

func TestFetchSourceFailures(t *testing.T) {
	p := newProvider(t)
	p.set("/pm.json", []byte(`{"metadata_version":"2.0","distributions":[{"rolie":{"feeds":[{"url":"`+p.URL+`/missing.json"}]}}]}`))
	p.set("/other.json", []byte(`{"hello":"world"}`))

	for path, want := range map[string]string{
		"/pm.json":    "ROLIE feed: not found",
		"/other.json": "not a CSAF provider metadata",
		"/nope.json":  "source: not found",
	} {
		_, err := NewFetcher(p.Client()).Fetch(context.Background(), Source{URL: p.URL + path})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error = %v, want %q", path, err, want)
		}
	}

	if _, err := NewFetcher(p.Client()).Fetch(context.Background(), Source{URL: p.URL + "/pm.json", PGPKeys: "garbage"}); err == nil {
		t.Error("unreadable pinned keys were accepted")
	}
}

func TestFetchRefusesHTTPSDowngrade(t *testing.T) {
	plain := newProvider(t)
	plain.set("/vex.json", readFixture(t))

	tlsSrv := httptest.NewTLSServer(http.RedirectHandler(plain.URL+"/vex.json", http.StatusFound))
	t.Cleanup(tlsSrv.Close)

	_, err := NewFetcher(tlsSrv.Client()).Fetch(context.Background(), Source{URL: tlsSrv.URL + "/vex.json"})
	if err == nil || !strings.Contains(err.Error(), "refusing to leave https") {
		t.Fatalf("error = %v, want the downgrade refused", err)
	}

	if plain.wasRequested("/vex.json") {
		t.Error("the http redirect target was fetched")
	}
}
