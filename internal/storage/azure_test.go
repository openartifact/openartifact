// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// azureReply is the outcome of a plain HTTP request made by a test client.
type azureReply struct {
	status int
	header http.Header
	body   []byte
}

// send issues a request the way a browser or CLI holding a presigned URL would:
// no Authorization header, only what the URL and the given headers carry.
func (f *fakeAzure) send(t *testing.T, method, rawURL string, headers map[string]string, body []byte) azureReply {
	t.Helper()

	return sendHTTP(t, f.server.Client(), method, rawURL, headers, body)
}

func sendHTTP(t *testing.T, client *http.Client, method, rawURL string, headers map[string]string, body []byte) azureReply {
	t.Helper()

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, rawURL, rdr)
	if err != nil {
		t.Fatal(err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return azureReply{status: resp.StatusCode, header: resp.Header, body: got}
}

// upload sends body to a presigned upload with exactly the headers it asks for.
func (f *fakeAzure) upload(t *testing.T, put PresignedPut, body []byte) azureReply {
	t.Helper()

	return f.send(t, http.MethodPut, put.URL, put.Headers, body)
}

// configure changes the fake while it is serving.
func (f *fakeAzure) configure(fn func(*fakeAzure)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	fn(f)
}

func parseURL(t *testing.T, raw string) *url.URL {
	t.Helper()

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	return u
}

// withQuery returns raw with its query changed by mutate.
func withQuery(t *testing.T, raw string, mutate func(url.Values)) string {
	t.Helper()

	u := parseURL(t, raw)
	q := u.Query()
	mutate(q)
	u.RawQuery = q.Encode()

	return u.String()
}

func readAll(t *testing.T, rc io.ReadCloser) []byte {
	t.Helper()

	defer func() { _ = rc.Close() }()

	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// stageObject uploads content as a staging object through its presigned URL
// and returns its id and version.
func stageObject(t *testing.T, f *fakeAzure, s *AzureBlobStore, content []byte) (string, string) {
	t.Helper()

	id := uuid.NewString()

	put, err := s.PresignStagingPut(t.Context(), id, int64(len(content)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if r := f.upload(t, put, content); r.status != http.StatusCreated {
		t.Fatalf("upload = %d %s", r.status, r.body)
	}

	obj, err := s.StatStaging(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}

	return id, obj.Version
}

// azureLayouts are the endpoint shapes the store is exercised against.
func azureLayouts() map[string]fakeAzureOptions {
	return map[string]fakeAzureOptions{
		"public cloud style": {},
		"path style":         {account: "devstoreaccount1", prefix: "/devstoreaccount1"},
		"https":              {tls: true},
	}
}

func TestNewAzureBlobStoreEndpoint(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, endpoint, wantBase string
	}{
		{"public cloud by default", "", "https://acct.blob.core.windows.net/cont"},
		{"sovereign cloud", "https://acct.blob.core.chinacloudapi.cn/", "https://acct.blob.core.chinacloudapi.cn/cont"},
		{"path-style emulator", "http://127.0.0.1:10000/acct", "http://127.0.0.1:10000/acct/cont"},
		{"padding is trimmed", " http://azurite:10000/acct// ", "http://azurite:10000/acct/cont"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s, err := NewAzureBlobStore(StorageConfig{
				AzureAccountName: "acct", AzureAccountKey: fakeAzureKey, AzureContainerName: "cont", AzureEndpoint: tc.endpoint,
			})
			if err != nil {
				t.Fatal(err)
			}

			if s.baseURL != tc.wantBase {
				t.Errorf("baseURL = %q, want %q", s.baseURL, tc.wantBase)
			}
		})
	}
}

func TestNewAzureBlobStoreRejectsBadConfig(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		endpoint string
		key      string
		want     error
	}{
		{"not http", "ftp://host/acct", fakeAzureKey, ErrAzureEndpointInvalid},
		{"no scheme", "acct.blob.core.windows.net", fakeAzureKey, ErrAzureEndpointInvalid},
		{"no host", "http://", fakeAzureKey, ErrAzureEndpointInvalid},
		{"query", "https://host/acct?sas=1", fakeAzureKey, ErrAzureEndpointInvalid},
		{"fragment", "https://host/acct#f", fakeAzureKey, ErrAzureEndpointInvalid},
		{"key is not base64", "", "not base64!", ErrAzureAccountKeyInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewAzureBlobStore(StorageConfig{
				AzureAccountName: "acct", AzureAccountKey: tc.key, AzureContainerName: "cont", AzureEndpoint: tc.endpoint,
			})
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAzureSharedKeyStringToSign(t *testing.T) {
	t.Parallel()

	newStore := func(t *testing.T, account, endpoint, container string) *AzureBlobStore {
		t.Helper()

		s, err := NewAzureBlobStore(StorageConfig{
			AzureAccountName: account, AzureAccountKey: fakeAzureKey, AzureContainerName: container, AzureEndpoint: endpoint,
		})
		if err != nil {
			t.Fatal(err)
		}

		return s
	}

	newReq := func(t *testing.T, method, target string, body io.Reader, headers map[string]string) *http.Request {
		t.Helper()

		req, err := http.NewRequestWithContext(t.Context(), method, target, body)
		if err != nil {
			t.Fatal(err)
		}

		for k, v := range headers {
			req.Header.Set(k, v)
		}

		return req
	}

	t.Run("Get Container Metadata example of the Shared Key docs", func(t *testing.T) {
		t.Parallel()

		s := newStore(t, "myaccount", "", "mycontainer")
		req := newReq(t, http.MethodGet, "https://myaccount.blob.core.windows.net/mycontainer?restype=container&comp=metadata&timeout=20", nil,
			map[string]string{"X-Ms-Date": "Fri, 26 Jun 2015 23:39:12 GMT", "X-Ms-Version": "2015-02-21"})

		got, err := s.sharedKeyStringToSign(req)
		if err != nil {
			t.Fatal(err)
		}

		want := "GET" + strings.Repeat("\n", 12) +
			"x-ms-date:Fri, 26 Jun 2015 23:39:12 GMT\nx-ms-version:2015-02-21\n" +
			"/myaccount/mycontainer\ncomp:metadata\nrestype:container\ntimeout:20"
		if got != want {
			t.Errorf("string to sign =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("standard headers keep their positions", func(t *testing.T) {
		t.Parallel()

		s := newStore(t, "myaccount", "", "mycontainer")
		req := newReq(t, http.MethodPut, "https://myaccount.blob.core.windows.net/mycontainer/blobs/ab/cd/x", strings.NewReader("hello"), map[string]string{
			"Content-Type": "text/plain", "If-Match": `"0x1"`,
			"X-Ms-Blob-Type": "BlockBlob", "X-Ms-Meta-B": "2", "X-Ms-Meta-A": "1",
			"X-Ms-Date": "Fri, 26 Jun 2015 23:39:12 GMT", "X-Ms-Version": azureAPIVersion,
		})

		got, err := s.sharedKeyStringToSign(req)
		if err != nil {
			t.Fatal(err)
		}

		want := "PUT\n\n\n5\n\ntext/plain\n\n\n\"0x1\"\n\n\n\n" +
			"x-ms-blob-type:BlockBlob\nx-ms-date:Fri, 26 Jun 2015 23:39:12 GMT\nx-ms-meta-a:1\nx-ms-meta-b:2\nx-ms-version:" + azureAPIVersion + "\n" +
			"/myaccount/mycontainer/blobs/ab/cd/x"
		if got != want {
			t.Errorf("string to sign =\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("a path-style endpoint repeats the account", func(t *testing.T) {
		t.Parallel()

		s := newStore(t, "devstoreaccount1", "http://127.0.0.1:10000/devstoreaccount1", "cont")
		req := newReq(t, http.MethodGet, s.baseURL+"?Include=snapshots&comp=list&include=metadata&restype=container", nil,
			map[string]string{"X-Ms-Date": "Fri, 26 Jun 2015 23:39:12 GMT"})

		got, err := s.sharedKeyStringToSign(req)
		if err != nil {
			t.Fatal(err)
		}

		wantResource := "/devstoreaccount1/devstoreaccount1/cont\ncomp:list\ninclude:metadata,snapshots\nrestype:container"
		if !strings.HasSuffix(got, "\n"+wantResource) {
			t.Errorf("string to sign = %q, want it to end with %q", got, wantResource)
		}
	})

	t.Run("x-ms-date is required", func(t *testing.T) {
		t.Parallel()

		s := newStore(t, "myaccount", "", "mycontainer")

		if _, err := s.sharedKeyStringToSign(newReq(t, http.MethodGet, s.baseURL, nil, nil)); !errors.Is(err, ErrAzureMSDateHeaderRequired) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestAzureRequestsAreAuthorized(t *testing.T) {
	t.Parallel()

	for name, opts := range azureLayouts() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, s := newAzureFixture(t, opts)
			ctx := t.Context()

			content := []byte("authorized bytes")
			sha := sha256Hex(content)

			if err := s.HealthCheck(ctx); err != nil {
				t.Fatalf("HealthCheck: %v", err)
			}

			if _, err := s.Put(ctx, bytes.NewReader(content), map[string]string{"sha256": sha, "format": "raw"}); err != nil {
				t.Fatalf("Put: %v", err)
			}

			if ok, err := s.Exists(ctx, sha); err != nil || !ok {
				t.Fatalf("Exists = %v, %v", ok, err)
			}

			if size, err := s.GetSize(ctx, sha); err != nil || size != int64(len(content)) {
				t.Fatalf("GetSize = %d, %v", size, err)
			}

			if meta, err := s.GetMetadata(ctx, sha); err != nil || meta["Format"] != "raw" {
				t.Fatalf("GetMetadata = %v, %v", meta, err)
			}

			rc, err := s.Get(ctx, sha)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}

			if got := readAll(t, rc); !bytes.Equal(got, content) {
				t.Fatalf("Get = %q", got)
			}

			if ids, _, err := s.List(ctx, "", 10); err != nil || len(ids) != 1 || ids[0] != sha {
				t.Fatalf("List = %v, %v", ids, err)
			}

			if err := s.Delete(ctx, sha); err != nil {
				t.Fatalf("Delete: %v", err)
			}

			if ok, err := s.Exists(ctx, sha); err != nil || ok {
				t.Fatalf("Exists after Delete = %v, %v", ok, err)
			}
		})
	}
}

func TestAzureRejectsRequestsSignedWithAnotherKey(t *testing.T) {
	t.Parallel()

	_, s := newAzureFixture(t, fakeAzureOptions{})
	other := *s
	other.accountKey = []byte("another account key")

	err := other.HealthCheck(t.Context())
	if !errors.Is(err, ErrAzureUnexpectedStatus) || !strings.Contains(err.Error(), "403") {
		t.Errorf("err = %v, want a 403", err)
	}
}

func TestAzurePresignGet(t *testing.T) {
	t.Parallel()

	for name, opts := range azureLayouts() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f, s := newAzureFixture(t, opts)
			ctx := t.Context()

			t0 := time.Date(2030, 1, 2, 3, 4, 5, 987654321, time.UTC)
			f.freeze(t0)

			content := []byte("download me")
			sha := sha256Hex(content)

			if _, err := s.Put(ctx, bytes.NewReader(content), map[string]string{"sha256": sha}); err != nil {
				t.Fatal(err)
			}

			raw, err := s.PresignGet(ctx, sha, 0, PresignGetOptions{ContentDisposition: `attachment; filename="a b.txt"`, ContentType: "text/plain"})
			if err != nil {
				t.Fatal(err)
			}

			u := parseURL(t, raw)
			q := u.Query()

			if want := opts.prefix + "/cont/blobs/" + sha[:2] + "/" + sha[2:4] + "/" + sha; u.Path != want {
				t.Errorf("path = %q, want %q", u.Path, want)
			}

			for k, want := range map[string]string{
				"sv": "2020-10-02", "sr": "b", "sp": "r", "se": "2030-01-02T03:09:05Z",
				"rscd": `attachment; filename="a b.txt"`, "rsct": "text/plain",
			} {
				if got := q.Get(k); got != want {
					t.Errorf("%s = %q, want %q", k, got, want)
				}
			}

			if got, want := q.Get("spr"), map[bool]string{true: "https", false: ""}[opts.tls]; got != want {
				t.Errorf("spr = %q, want %q", got, want)
			}

			if q.Has("st") || q.Get("sig") == "" {
				t.Errorf("query = %v", q)
			}

			r := f.send(t, http.MethodGet, raw, nil, nil)
			if r.status != http.StatusOK || !bytes.Equal(r.body, content) {
				t.Fatalf("GET = %d %q", r.status, r.body)
			}

			if got := r.header.Get("Content-Disposition"); got != `attachment; filename="a b.txt"` {
				t.Errorf("Content-Disposition = %q", got)
			}

			if got := r.header.Get("Content-Type"); got != "text/plain" {
				t.Errorf("Content-Type = %q", got)
			}
		})
	}
}

func TestAzurePresignGetWithoutOverrides(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	f.freeze(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))

	content := []byte("plain download")
	sha := sha256Hex(content)

	if _, err := s.Put(t.Context(), bytes.NewReader(content), map[string]string{"sha256": sha}); err != nil {
		t.Fatal(err)
	}

	raw, err := s.PresignURL(t.Context(), sha, 0)
	if err != nil {
		t.Fatal(err)
	}

	if q := parseURL(t, raw).Query(); q.Has("rscd") || q.Has("rsct") {
		t.Errorf("query = %v, want no response overrides", q)
	}

	if r := f.send(t, http.MethodGet, raw, nil, nil); r.status != http.StatusOK || !bytes.Equal(r.body, content) {
		t.Errorf("GET = %d %q", r.status, r.body)
	}
}

func TestAzurePresignGetClampsTTL(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	t0 := time.Date(2030, 1, 2, 3, 4, 5, 987654321, time.UTC)
	f.freeze(t0)

	s.presignTTL = 20 * time.Minute
	sha := sha256Hex([]byte("x"))

	for _, tc := range []struct {
		name string
		ttl  time.Duration
		want string
	}{
		{"unset uses the configured TTL", 0, "2030-01-02T03:24:05Z"},
		{"too short is raised to a minute", 10 * time.Second, "2030-01-02T03:05:05Z"},
		{"in range is kept", 90 * time.Second, "2030-01-02T03:05:35Z"},
		{"too long is cut to an hour", 48 * time.Hour, "2030-01-02T04:04:05Z"},
	} {
		raw, err := s.PresignGet(t.Context(), sha, tc.ttl, PresignGetOptions{})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}

		if got := parseURL(t, raw).Query().Get("se"); got != tc.want {
			t.Errorf("%s: se = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestAzurePresignGetRejectsInvalidID(t *testing.T) {
	t.Parallel()

	_, s := newAzureFixture(t, fakeAzureOptions{})

	if _, err := s.PresignGet(t.Context(), "../etc/passwd", time.Minute, PresignGetOptions{}); err == nil {
		t.Error("PresignGet accepted an invalid blob id")
	}
}

func TestAzurePresignedURLCannotBeAltered(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()
	f.freeze(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))

	content, other := []byte("signed"), []byte("another blob")
	sha, otherSHA := sha256Hex(content), sha256Hex(other)

	for _, c := range [][]byte{content, other} {
		if _, err := s.Put(ctx, bytes.NewReader(c), map[string]string{"sha256": sha256Hex(c)}); err != nil {
			t.Fatal(err)
		}
	}

	raw, err := s.PresignGet(ctx, sha, time.Minute*10, PresignGetOptions{ContentType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}

	otherBlobURL := strings.Replace(raw, sha, otherSHA, 1)
	otherBlobURL = strings.Replace(otherBlobURL, "blobs/"+sha[:2]+"/"+sha[2:4], "blobs/"+otherSHA[:2]+"/"+otherSHA[2:4], 1)

	for _, tc := range []struct {
		name   string
		method string
		url    string
	}{
		{"more permissions", http.MethodGet, withQuery(t, raw, func(q url.Values) { q.Set("sp", "rw") })},
		{"another response type", http.MethodGet, withQuery(t, raw, func(q url.Values) { q.Set("rsct", "text/html") })},
		{"an added response header", http.MethodGet, withQuery(t, raw, func(q url.Values) { q.Set("rscd", "inline") })},
		{"a dropped response header", http.MethodGet, withQuery(t, raw, func(q url.Values) { q.Del("rsct") })},
		{"a later expiry", http.MethodGet, withQuery(t, raw, func(q url.Values) { q.Set("se", "2031-01-01T00:00:00Z") })},
		{"another blob", http.MethodGet, otherBlobURL},
		{"writing with a read SAS", http.MethodPut, raw},
		{"deleting with a read SAS", http.MethodDelete, raw},
	} {
		if r := f.send(t, tc.method, tc.url, map[string]string{"x-ms-blob-type": "BlockBlob"}, nil); r.status != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", tc.name, r.status)
		}
	}

	if r := f.send(t, http.MethodGet, raw, nil, nil); r.status != http.StatusOK {
		t.Fatalf("the untouched URL: status = %d", r.status)
	}
}

func TestAzurePresignedURLExpires(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	f.freeze(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))

	content := []byte("short lived")
	sha := sha256Hex(content)

	if _, err := s.Put(t.Context(), bytes.NewReader(content), map[string]string{"sha256": sha}); err != nil {
		t.Fatal(err)
	}

	raw, err := s.PresignGet(t.Context(), sha, 5*time.Minute, PresignGetOptions{})
	if err != nil {
		t.Fatal(err)
	}

	f.advance(5*time.Minute - time.Second)

	if r := f.send(t, http.MethodGet, raw, nil, nil); r.status != http.StatusOK {
		t.Fatalf("a second before expiry: status = %d", r.status)
	}

	f.advance(time.Second)

	if r := f.send(t, http.MethodGet, raw, nil, nil); r.status != http.StatusForbidden {
		t.Errorf("at expiry: status = %d, want 403", r.status)
	}
}

func TestAzureStagingUpload(t *testing.T) {
	t.Parallel()

	for name, opts := range azureLayouts() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f, s := newAzureFixture(t, opts)
			ctx := t.Context()
			f.freeze(time.Date(2030, 1, 2, 3, 4, 5, 500000000, time.UTC))

			if !s.StagingSupported() {
				t.Fatal("StagingSupported = false")
			}

			id := uuid.NewString()
			content := []byte("staged bytes")

			put, err := s.PresignStagingPut(ctx, id, int64(len(content)), 0)
			if err != nil {
				t.Fatal(err)
			}

			u := parseURL(t, put.URL)
			if want := opts.prefix + "/cont/staging/" + id; u.Path != want {
				t.Errorf("path = %q, want %q", u.Path, want)
			}

			if got := u.Query().Get("sp"); got != "cw" {
				t.Errorf("sp = %q, want cw", got)
			}

			if !maps.Equal(put.Headers, map[string]string{"x-ms-blob-type": "BlockBlob"}) {
				t.Errorf("Headers = %v", put.Headers)
			}

			if want := time.Date(2030, 1, 2, 3, 5, 5, 0, time.UTC); !put.Expires.Equal(want) {
				t.Errorf("Expires = %s, want %s (the TTL is raised to a minute)", put.Expires, want)
			}

			if _, err := s.StatStaging(ctx, id); !errors.Is(err, ErrStagingNotFound) {
				t.Fatalf("StatStaging before the upload: %v", err)
			}

			if r := f.upload(t, put, content); r.status != http.StatusCreated {
				t.Fatalf("upload = %d %s", r.status, r.body)
			}

			obj, err := s.StatStaging(ctx, id)
			if err != nil {
				t.Fatal(err)
			}

			if obj.Size != int64(len(content)) || obj.Version != f.blob("staging/"+id).etag {
				t.Errorf("StatStaging = %+v", obj)
			}

			rc, err := s.OpenStaging(ctx, id, obj.Version)
			if err != nil {
				t.Fatal(err)
			}

			if got := readAll(t, rc); !bytes.Equal(got, content) {
				t.Errorf("OpenStaging = %q", got)
			}
		})
	}
}

func TestAzureStagingUploadExpires(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	f.freeze(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))

	put, err := s.PresignStagingPut(t.Context(), uuid.NewString(), 1, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	f.advance(30 * time.Minute)

	if r := f.upload(t, put, []byte("x")); r.status != http.StatusForbidden {
		t.Errorf("late upload: status = %d, want 403", r.status)
	}
}

func TestAzureStagingRejectsForeignIDs(t *testing.T) {
	t.Parallel()

	_, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()
	sha := sha256Hex([]byte("x"))

	for _, id := range []string{"", "not-a-uuid", "../blobs/ab/cd/" + sha, uuid.NewString() + "/x"} {
		if _, err := s.PresignStagingPut(ctx, id, 1, time.Minute); err == nil {
			t.Errorf("PresignStagingPut(%q) succeeded", id)
		}

		if _, err := s.StatStaging(ctx, id); err == nil {
			t.Errorf("StatStaging(%q) succeeded", id)
		}

		if _, err := s.OpenStaging(ctx, id, `"v"`); err == nil {
			t.Errorf("OpenStaging(%q) succeeded", id)
		}

		if err := s.PromoteStaging(ctx, id, sha, `"v"`, nil); err == nil {
			t.Errorf("PromoteStaging(%q) succeeded", id)
		}

		if err := s.DeleteStaging(ctx, id); err == nil {
			t.Errorf("DeleteStaging(%q) succeeded", id)
		}

		if _, err := s.InitiateMultipart(ctx, id, 1, 1); err == nil {
			t.Errorf("InitiateMultipart(%q) succeeded", id)
		}
	}
}

func TestAzureStagingObjectChangedAfterVerification(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()

	good, evil := []byte("verified bytes"), []byte("swapped bytes!")
	sha := sha256Hex(good)

	id, version := stageObject(t, f, s, good)

	// The upload URL is still valid: the client swaps the object after the check.
	put, err := s.PresignStagingPut(ctx, id, int64(len(evil)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if r := f.upload(t, put, evil); r.status != http.StatusCreated {
		t.Fatalf("overwrite = %d", r.status)
	}

	if _, err := s.OpenStaging(ctx, id, version); !errors.Is(err, ErrStagingChanged) {
		t.Errorf("OpenStaging(old version) = %v, want ErrStagingChanged", err)
	}

	if err := s.PromoteStaging(ctx, id, sha, version, nil); !errors.Is(err, ErrStagingChanged) {
		t.Errorf("PromoteStaging(old version) = %v, want ErrStagingChanged", err)
	}

	if f.blob(azureBlobKey(sha)) != nil {
		t.Error("unverified bytes were published under the content-addressed name")
	}

	for _, call := range []struct {
		name string
		fn   func(version string) error
	}{
		{"OpenStaging", func(v string) error { _, err := s.OpenStaging(ctx, id, v); return err }},
		{"PromoteStaging", func(v string) error { return s.PromoteStaging(ctx, id, sha, v, nil) }},
	} {
		if err := call.fn(""); !errors.Is(err, ErrStagingChanged) {
			t.Errorf("%s without a version = %v, want ErrStagingChanged", call.name, err)
		}
	}
}

func TestAzureStagingMissingObject(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()
	id := uuid.NewString()
	sha := sha256Hex([]byte("never staged"))

	if _, err := s.OpenStaging(ctx, id, `"v"`); !errors.Is(err, ErrStagingNotFound) {
		t.Errorf("OpenStaging = %v, want ErrStagingNotFound", err)
	}

	if err := s.PromoteStaging(ctx, id, sha, `"v"`, nil); !errors.Is(err, ErrStagingNotFound) {
		t.Errorf("PromoteStaging = %v, want ErrStagingNotFound", err)
	}

	if f.blob(azureBlobKey(sha)) != nil {
		t.Error("a failed promotion left a blob behind")
	}

	if err := s.DeleteStaging(ctx, id); err != nil {
		t.Errorf("DeleteStaging of a missing object = %v", err)
	}
}

func TestAzureDeleteStaging(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()

	id, _ := stageObject(t, f, s, []byte("to delete"))

	for range 2 {
		if err := s.DeleteStaging(ctx, id); err != nil {
			t.Fatalf("DeleteStaging: %v", err)
		}
	}

	if _, err := s.StatStaging(ctx, id); !errors.Is(err, ErrStagingNotFound) {
		t.Errorf("StatStaging after delete = %v, want ErrStagingNotFound", err)
	}
}

func TestAzurePromoteStagingWritesWhatPutWrites(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()

	promoted, put := []byte("promoted content"), []byte("put content")
	sha, putSHA := sha256Hex(promoted), sha256Hex(put)

	meta := func(sha string) map[string]string {
		return map[string]string{"sha256": sha, "format": "raw", "path": "dir/file.txt"}
	}

	id, version := stageObject(t, f, s, promoted)

	if err := s.PromoteStaging(ctx, id, sha, version, meta(sha)); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Put(ctx, bytes.NewReader(put), meta(putSHA)); err != nil {
		t.Fatal(err)
	}

	rc, err := s.Get(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}

	if got := readAll(t, rc); !bytes.Equal(got, promoted) {
		t.Errorf("promoted content = %q", got)
	}

	a, b := f.blob(azureBlobKey(sha)), f.blob(azureBlobKey(putSHA))
	if a.contentType != b.contentType || a.contentEncoding != b.contentEncoding || !maps.Equal(a.meta, b.meta) {
		t.Errorf("promoted blob = {%q %q %v}, Put blob = {%q %q %v}",
			a.contentType, a.contentEncoding, a.meta, b.contentType, b.contentEncoding, b.meta)
	}

	if want := map[string]string{"format": "raw", "path": "dir/file.txt"}; !maps.Equal(a.meta, want) {
		t.Errorf("metadata = %v, want %v", a.meta, want)
	}

	if _, err := s.StatStaging(ctx, id); err != nil {
		t.Errorf("promotion removed the staging object: %v", err)
	}
}

func TestAzurePromoteStagingIsIdempotent(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()

	content := []byte("promote twice")
	sha := sha256Hex(content)
	id, version := stageObject(t, f, s, content)

	for range 2 {
		if err := s.PromoteStaging(ctx, id, sha, version, map[string]string{"sha256": sha}); err != nil {
			t.Fatal(err)
		}
	}

	if n := f.opCount("copy"); n != 1 {
		t.Errorf("copies = %d, want 1", n)
	}
}

func TestAzurePromoteStagingLeavesExistingBlobAlone(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	ctx := t.Context()

	content := []byte("already stored")
	sha := sha256Hex(content)

	if _, err := s.Put(ctx, bytes.NewReader(content), map[string]string{"sha256": sha}); err != nil {
		t.Fatal(err)
	}

	before := f.blob(azureBlobKey(sha))
	id, version := stageObject(t, f, s, content)

	if err := s.PromoteStaging(ctx, id, sha, version, map[string]string{"sha256": sha, "format": "raw"}); err != nil {
		t.Fatal(err)
	}

	if n := f.opCount("copy"); n != 0 {
		t.Errorf("copies = %d, want 0", n)
	}

	if after := f.blob(azureBlobKey(sha)); after.etag != before.etag {
		t.Error("an existing blob was rewritten")
	}
}

func TestAzurePromoteStagingResetsUploaderControlledState(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		metadata map[string]string
		want     map[string]string
	}{
		{"without metadata", map[string]string{"sha256": "ignored"}, map[string]string{}},
		{"with metadata", map[string]string{"format": "raw"}, map[string]string{"format": "raw"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f, s := newAzureFixture(t, fakeAzureOptions{})
			ctx := t.Context()

			content := []byte("hostile properties")
			sha := sha256Hex(content)

			put, err := s.PresignStagingPut(ctx, uuid.NewString(), int64(len(content)), time.Hour)
			if err != nil {
				t.Fatal(err)
			}

			// The uploader chooses properties and metadata of the staging blob.
			put.Headers["x-ms-blob-content-type"] = "text/html"
			put.Headers["x-ms-blob-content-encoding"] = "gzip"
			put.Headers["x-ms-meta-evil"] = "1"

			if r := f.upload(t, put, content); r.status != http.StatusCreated {
				t.Fatalf("upload = %d", r.status)
			}

			id := strings.TrimPrefix(parseURL(t, put.URL).Path, "/cont/staging/")

			obj, err := s.StatStaging(ctx, id)
			if err != nil {
				t.Fatal(err)
			}

			if err := s.PromoteStaging(ctx, id, sha, obj.Version, tc.metadata); err != nil {
				t.Fatal(err)
			}

			b := f.blob(azureBlobKey(sha))
			if b.contentType != "application/octet-stream" || b.contentEncoding != "" || !maps.Equal(b.meta, tc.want) {
				t.Errorf("blob = {%q %q %v}, want {application/octet-stream \"\" %v}", b.contentType, b.contentEncoding, b.meta, tc.want)
			}
		})
	}
}

func TestAzurePromoteStagingAsyncCopy(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		fails    bool
		wantErr  error
		wantBlob bool
	}{
		{"pending copy finishes", false, nil, true},
		{"failed copy leaves nothing", true, ErrAzureCopyFailed, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f, s := newAzureFixture(t, fakeAzureOptions{})
			ctx := t.Context()

			content := []byte("copied in the background")
			sha := sha256Hex(content)
			id, version := stageObject(t, f, s, content)

			f.configure(func(f *fakeAzure) { f.copyPolls, f.copyFails = 3, tc.fails })

			err := s.PromoteStaging(ctx, id, sha, version, map[string]string{"sha256": sha})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}

			b := f.blob(azureBlobKey(sha))
			if (b != nil) != tc.wantBlob {
				t.Fatalf("blob present = %v, want %v", b != nil, tc.wantBlob)
			}

			if b != nil && (b.copyStatus != "success" || b.contentType != "application/octet-stream" || !bytes.Equal(b.data, content)) {
				t.Errorf("blob = %+v", b)
			}
		})
	}
}

func TestAzurePromoteStagingCanceledWhileCopying(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})

	content := []byte("never finishes")
	sha := sha256Hex(content)
	id, version := stageObject(t, f, s, content)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	polls := 0

	f.configure(func(f *fakeAzure) {
		f.copyPolls = 1 << 20
		f.onPendingHead = func() {
			if polls++; polls == 2 {
				cancel()
			}
		}
	})

	if err := s.PromoteStaging(ctx, id, sha, version, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	if f.blob(azureBlobKey(sha)) != nil {
		t.Error("an abandoned copy was left behind")
	}
}

func TestAzurePromoteStagingWithLeftoverCopy(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		leftover   func(*fakeAzureBlob)
		wantCopies int
	}{
		{"a copy still pending is awaited", func(b *fakeAzureBlob) { b.copyStatus, b.copyPolls = "pending", 2 }, 0},
		{"a failed copy is redone", func(b *fakeAzureBlob) { b.copyStatus, b.copyFailure = "failed", "boom" }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f, s := newAzureFixture(t, fakeAzureOptions{})

			content := []byte("the real content")
			sha := sha256Hex(content)
			id, version := stageObject(t, f, s, content)

			f.seed(azureBlobKey(sha), content[:3], tc.leftover)

			if err := s.PromoteStaging(t.Context(), id, sha, version, map[string]string{"sha256": sha}); err != nil {
				t.Fatal(err)
			}

			if n := f.opCount("copy"); n != tc.wantCopies {
				t.Errorf("copies = %d, want %d", n, tc.wantCopies)
			}

			if tc.wantCopies > 0 {
				if b := f.blob(azureBlobKey(sha)); !bytes.Equal(b.data, content) || b.copyStatus != "success" {
					t.Errorf("blob = %+v", b)
				}
			}
		})
	}
}

func TestAzurePresignStagingPutSingleRequestLimit(t *testing.T) {
	t.Parallel()

	_, s := newAzureFixture(t, fakeAzureOptions{})
	id := uuid.NewString()

	if _, err := s.PresignStagingPut(t.Context(), id, azureMaxPutBlob, time.Minute); err != nil {
		t.Errorf("the largest Put Blob was refused: %v", err)
	}

	if _, err := s.PresignStagingPut(t.Context(), id, azureMaxPutBlob+1, time.Minute); !errors.Is(err, ErrSingleUploadTooLarge) {
		t.Errorf("err = %v, want ErrSingleUploadTooLarge", err)
	}
}

func TestAzureWalkStaging(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	f.freeze(now)
	f.configure(func(f *fakeAzure) { f.pageSize = 2 })

	old, fresh := now.Add(-48*time.Hour), now.Add(-time.Minute)
	stale := map[string]bool{}

	for i := range 5 {
		id := uuid.NewString()
		stale[id] = true

		f.seed("staging/"+id, []byte{byte(i)}, func(b *fakeAzureBlob) { b.modified = old })
	}

	freshID := uuid.NewString()
	f.seed("staging/"+freshID, []byte("fresh"), func(b *fakeAzureBlob) { b.modified = fresh })

	// Neither the content-addressed blobs nor stray names are staging objects.
	f.seed("staging/not-a-uuid", []byte("x"), func(b *fakeAzureBlob) { b.modified = old })
	f.seed("staging/"+uuid.NewString()+"/nested", []byte("x"), func(b *fakeAzureBlob) { b.modified = old })
	f.seed(azureBlobKey(sha256Hex([]byte("blob"))), []byte("blob"), func(b *fakeAzureBlob) { b.modified = old })

	got := map[string]bool{}
	batches := 0

	err := s.WalkStaging(t.Context(), now.Add(-24*time.Hour), func(ids []string) error {
		batches++

		if len(ids) == 0 || len(ids) > 2 {
			t.Errorf("batch of %d ids", len(ids))
		}

		for _, id := range ids {
			got[id] = true
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != len(stale) || batches < 3 {
		t.Errorf("walk found %d ids in %d batches, want %d ids in at least 3 batches", len(got), batches, len(stale))
	}

	for id := range stale {
		if !got[id] {
			t.Errorf("stale object %s not reported", id)
		}
	}

	if got[freshID] {
		t.Error("an object younger than the cutoff was reported")
	}
}

func TestAzureWalkStagingStopsOnCallbackError(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	f.freeze(now)
	f.configure(func(f *fakeAzure) { f.pageSize = 1 })

	for range 3 {
		f.seed("staging/"+uuid.NewString(), []byte("x"), func(b *fakeAzureBlob) { b.modified = now.Add(-time.Hour) })
	}

	stop := errors.New("stop")
	calls := 0

	err := s.WalkStaging(t.Context(), now, func([]string) error {
		calls++
		return stop
	})
	if !errors.Is(err, stop) || calls != 1 {
		t.Errorf("err = %v after %d calls, want the callback's error after 1", err, calls)
	}
}

func TestAzureStagingIsNotListedAsBlobs(t *testing.T) {
	t.Parallel()

	f, s := newAzureFixture(t, fakeAzureOptions{})

	stageObject(t, f, s, []byte("staged only"))

	content := []byte("stored")
	if _, err := s.Put(t.Context(), bytes.NewReader(content), map[string]string{"sha256": sha256Hex(content)}); err != nil {
		t.Fatal(err)
	}

	ids, _, err := s.List(t.Context(), "", 100)
	if err != nil {
		t.Fatal(err)
	}

	if len(ids) != 1 || ids[0] != sha256Hex(content) {
		t.Errorf("List = %v", ids)
	}
}
