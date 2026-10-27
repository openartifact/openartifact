// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ociRegistry is an in-process registry implementing the blob subset of the
// OCI distribution API: monolithic uploads, HEAD/GET (with Range through
// http.ServeContent), DELETE and the /v2/ ping. Behaviour switches model the
// registries seen in the wild.
type ociRegistry struct {
	*httptest.Server

	mu    sync.Mutex
	blobs map[string][]byte

	// ignoreRange makes GET answer 200 with the full body.
	ignoreRange bool
	// requireAuth rejects requests without these basic credentials.
	user, pass string
	// failStatus, when non-zero, answers every blob request with it.
	failStatus int
	// uploadStatus overrides the status of the upload initiation.
	uploadStatus int
	// location, when set, is the absolute upload URL handed out.
	location string
	// noLocation omits the Location header on upload initiation.
	noLocation bool
	// deleteStatus overrides the status of DELETE.
	deleteStatus int
	// pingStatus overrides the status of GET /v2/.
	pingStatus int
	// authSeen records the Authorization header of each request by path.
	authSeen map[string]string
}

func newOCIRegistry(t *testing.T) *ociRegistry {
	t.Helper()

	reg := &ociRegistry{blobs: map[string][]byte{}, authSeen: map[string]string{}}

	reg.Server = httptest.NewServer(http.HandlerFunc(reg.serve))
	t.Cleanup(reg.Close)

	return reg
}

func (r *ociRegistry) has(sha string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, ok := r.blobs[sha]

	return ok
}

func (r *ociRegistry) seed(sha string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.blobs[sha] = data
}

func (r *ociRegistry) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.authSeen[req.Method+" "+req.URL.Path] = req.Header.Get("Authorization")

	if r.user != "" {
		if u, p, ok := req.BasicAuth(); !ok || u != r.user || p != r.pass {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}
	}

	if req.URL.Path == "/v2/" {
		if r.pingStatus != 0 {
			w.WriteHeader(r.pingStatus)

			return
		}

		w.WriteHeader(http.StatusOK)

		return
	}

	if r.failStatus != 0 {
		w.WriteHeader(r.failStatus)
		_, _ = w.Write([]byte("registry exploded"))

		return
	}

	switch {
	case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/blobs/uploads/"):
		if r.uploadStatus != 0 {
			w.WriteHeader(r.uploadStatus)

			return
		}

		switch {
		case r.noLocation:
		case r.location != "":
			w.Header().Set("Location", r.location)
		default:
			w.Header().Set("Location", req.URL.Path+"session")
		}

		w.WriteHeader(http.StatusAccepted)
	case req.Method == http.MethodPut && strings.HasSuffix(req.URL.Path, "/blobs/uploads/session"):
		body, _ := io.ReadAll(req.Body)
		r.blobs[strings.TrimPrefix(req.URL.Query().Get("digest"), "sha256:")] = body
		w.WriteHeader(http.StatusCreated)
	case strings.Contains(req.URL.Path, "/blobs/sha256:"):
		r.serveBlob(w, req)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (r *ociRegistry) serveBlob(w http.ResponseWriter, req *http.Request) {
	sha := req.URL.Path[strings.LastIndex(req.URL.Path, ":")+1:]

	body, ok := r.blobs[sha]
	if !ok {
		w.WriteHeader(http.StatusNotFound)

		return
	}

	switch req.Method {
	case http.MethodDelete:
		if r.deleteStatus != 0 {
			w.WriteHeader(r.deleteStatus)

			return
		}

		delete(r.blobs, sha)
		w.WriteHeader(http.StatusAccepted)
	case http.MethodHead:
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
	default:
		if r.ignoreRange {
			req.Header.Del("Range")
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, req, "", time.Time{}, bytes.NewReader(body))
	}
}

func newOCIStore(t *testing.T, reg *ociRegistry, mutate func(*StorageConfig)) *OCIBlobStore {
	t.Helper()

	cfg := StorageConfig{Type: "oci", Endpoint: reg.URL, Bucket: "oa/blobs", OCIInsecure: true, StagingPath: t.TempDir()}
	if mutate != nil {
		mutate(&cfg)
	}

	store, err := NewOCIBlobStore(cfg)
	if err != nil {
		t.Fatal(err)
	}

	return store
}

func TestNewOCIBlobStoreValidatesConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  StorageConfig
		want error
	}{
		{"missing endpoint", StorageConfig{Bucket: "r"}, ErrOCIEndpointRequired},
		{"missing repository", StorageConfig{Endpoint: "http://r"}, ErrOCIBucketRequired},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := NewOCIBlobStore(tc.cfg); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestOCIRoundTrip(t *testing.T) {
	t.Parallel()

	reg := newOCIRegistry(t)
	store := newOCIStore(t, reg, nil)
	ctx := context.Background()

	data := []byte("oci blob payload")
	sha := sha256Of(data)

	id, err := store.Put(ctx, bytes.NewReader(data), map[string]string{"sha256": sha})
	if err != nil || id != sha {
		t.Fatalf("Put = %q, %v", id, err)
	}

	rc, err := store.Get(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}

	if got := readAll(t, rc); !bytes.Equal(got, data) {
		t.Fatalf("Get = %q", got)
	}

	size, err := store.GetSize(ctx, sha)
	if err != nil || size != int64(len(data)) {
		t.Fatalf("GetSize = %d, %v", size, err)
	}

	md, err := store.GetMetadata(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}

	if md["sha256"] != sha || md["content-length"] != strconv.Itoa(len(data)) || md["content-type"] != "application/octet-stream" {
		t.Fatalf("GetMetadata = %v", md)
	}

	if key, ok, err := store.ExistsBySHA256(ctx, sha); err != nil || !ok || key != sha {
		t.Fatalf("ExistsBySHA256 = %q, %v, %v", key, ok, err)
	}

	if err := store.Delete(ctx, sha); err != nil {
		t.Fatal(err)
	}

	if reg.has(sha) {
		t.Fatal("Delete left the blob in the registry")
	}

	if key, ok, err := store.ExistsBySHA256(ctx, sha); err != nil || ok || key != "" {
		t.Fatalf("ExistsBySHA256 after delete = %q, %v, %v", key, ok, err)
	}
}

func TestOCIPutRequiresMatchingSHA256(t *testing.T) {
	t.Parallel()

	reg := newOCIRegistry(t)
	store := newOCIStore(t, reg, nil)
	ctx := context.Background()
	data := []byte("payload")

	tests := []struct {
		name string
		meta map[string]string
		want error
	}{
		{"no metadata", nil, ErrOCIPutRequiresSHA256},
		{"empty digest", map[string]string{"sha256": ""}, ErrOCIPutRequiresSHA256},
		{"malformed digest", map[string]string{"sha256": "../etc/passwd"}, ErrInvalidBlobID},
		{"digest of other bytes", map[string]string{"sha256": sha256Of([]byte("other"))}, ErrDigestMismatch},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := store.Put(ctx, bytes.NewReader(data), tc.meta); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}

			if len(reg.blobs) != 0 {
				t.Fatalf("rejected Put reached the registry: %d blobs", len(reg.blobs))
			}
		})
	}
}

func TestOCIPutSkipsBlobTheRegistryHas(t *testing.T) {
	t.Parallel()

	reg := newOCIRegistry(t)
	store := newOCIStore(t, reg, nil)
	data := []byte("already there")
	sha := sha256Of(data)

	reg.seed(sha, []byte("registry copy"))
	reg.uploadStatus = http.StatusInternalServerError // any upload attempt would fail

	if id, err := store.Put(context.Background(), bytes.NewReader(data), map[string]string{"sha256": sha}); err != nil || id != sha {
		t.Fatalf("Put of an existing blob = %q, %v", id, err)
	}
}

func TestOCIPutUploadFailures(t *testing.T) {
	t.Parallel()

	data := []byte("payload")
	meta := func() map[string]string { return map[string]string{"sha256": sha256Of(data)} }

	tests := []struct {
		name  string
		setup func(*ociRegistry)
		want  string
	}{
		{"initiate refused", func(r *ociRegistry) { r.uploadStatus = http.StatusForbidden }, "initiate upload: status 403"},
		{"no location", func(r *ociRegistry) { r.noLocation = true }, ErrOCIMissingLocationHeader.Error()},
		{"head fails", func(r *ociRegistry) { r.failStatus = http.StatusBadGateway }, "unexpected status 502"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reg := newOCIRegistry(t)
			tc.setup(reg)
			store := newOCIStore(t, reg, nil)

			_, err := store.Put(context.Background(), bytes.NewReader(data), meta())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestOCIPutEmptyBlob(t *testing.T) {
	t.Parallel()

	reg := newOCIRegistry(t)
	store := newOCIStore(t, reg, nil)
	sha := sha256Of(nil)

	if _, err := store.Put(context.Background(), bytes.NewReader(nil), map[string]string{"sha256": sha}); err != nil {
		t.Fatal(err)
	}

	if size, err := store.GetSize(context.Background(), sha); err != nil || size != 0 {
		t.Fatalf("GetSize of the empty blob = %d, %v", size, err)
	}
}

func TestOCIRejectsInvalidBlobIDs(t *testing.T) {
	reg := newOCIRegistry(t)
	store := newOCIStore(t, reg, nil)
	ctx := context.Background()

	t.Run("ids", func(t *testing.T) {
		for _, id := range []string{"", "short", "../../x", strings.Repeat("g", 64)} {
			t.Run(id, func(t *testing.T) {
				if _, err := store.Get(ctx, id); !errors.Is(err, ErrInvalidBlobID) {
					t.Errorf("Get err = %v", err)
				}

				if _, err := store.GetFrom(ctx, id, 3); !errors.Is(err, ErrInvalidBlobID) {
					t.Errorf("GetFrom err = %v", err)
				}

				if err := store.Delete(ctx, id); !errors.Is(err, ErrInvalidBlobID) {
					t.Errorf("Delete err = %v", err)
				}

				if _, err := store.Exists(ctx, id); !errors.Is(err, ErrInvalidBlobID) {
					t.Errorf("Exists err = %v", err)
				}

				if _, err := store.GetMetadata(ctx, id); !errors.Is(err, ErrInvalidBlobID) {
					t.Errorf("GetMetadata err = %v", err)
				}

				if _, err := store.GetSize(ctx, id); !errors.Is(err, ErrInvalidBlobID) {
					t.Errorf("GetSize err = %v", err)
				}

				if _, _, err := store.ExistsBySHA256(ctx, id); !errors.Is(err, ErrInvalidBlobID) {
					t.Errorf("ExistsBySHA256 err = %v", err)
				}
			})
		}
	})

	if len(reg.authSeen) != 0 {
		t.Fatalf("invalid ids reached the registry: %v", reg.authSeen)
	}
}

func TestOCIMissingBlobMapsToNotFound(t *testing.T) {
	t.Parallel()

	reg := newOCIRegistry(t)
	store := newOCIStore(t, reg, nil)
	ctx := context.Background()
	sha := sha256Of([]byte("never stored"))

	if _, err := store.Get(ctx, sha); !errors.Is(err, ErrBlobNotFound) {
		t.Errorf("Get err = %v", err)
	}

	if _, err := store.GetFrom(ctx, sha, 2); !errors.Is(err, ErrBlobNotFound) {
		t.Errorf("GetFrom err = %v", err)
	}

	if _, err := store.GetSize(ctx, sha); !errors.Is(err, ErrBlobNotFound) {
		t.Errorf("GetSize err = %v", err)
	}

	if _, err := store.GetMetadata(ctx, sha); !errors.Is(err, ErrBlobNotFound) {
		t.Errorf("GetMetadata err = %v", err)
	}

	if ok, err := store.Exists(ctx, sha); err != nil || ok {
		t.Errorf("Exists = %v, %v", ok, err)
	}

	// Deleting a missing blob is idempotent.
	if err := store.Delete(ctx, sha); err != nil {
		t.Errorf("Delete of a missing blob = %v", err)
	}
}

func TestOCIGetFrom(t *testing.T) {
	t.Parallel()

	data := []byte("0123456789")
	sha := sha256Of(data)

	tests := []struct {
		name        string
		ignoreRange bool
		offset      int64
		want        string
		wantErr     error
	}{
		{"zero is a plain get", false, 0, "0123456789", nil},
		{"ranged", false, 4, "456789", nil},
		{"registry ignoring range", true, 4, "456789", nil},
		{"offset at the end", false, 10, "", ErrOffsetOutOfRange},
		{"offset past the end", false, 99, "", ErrOffsetOutOfRange},
		{"ignored range past the end", true, 99, "", ErrOffsetOutOfRange},
		{"negative offset", false, -1, "", ErrOffsetOutOfRange},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reg := newOCIRegistry(t)
			reg.seed(sha, data)
			reg.ignoreRange = tc.ignoreRange
			store := newOCIStore(t, reg, nil)

			rc, err := store.GetFrom(context.Background(), sha, tc.offset)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if got := string(readAll(t, rc)); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOCIUnexpectedStatusesAreErrors(t *testing.T) {
	t.Parallel()

	reg := newOCIRegistry(t)
	store := newOCIStore(t, reg, nil)
	ctx := context.Background()
	data := []byte("x")
	sha := sha256Of(data)

	reg.seed(sha, data)
	reg.failStatus = http.StatusInternalServerError

	check := func(name string, err error) {
		t.Helper()

		if err == nil || errors.Is(err, ErrBlobNotFound) || !strings.Contains(err.Error(), "500") {
			t.Errorf("%s err = %v, want a 500 error that is not ErrBlobNotFound", name, err)
		}
	}

	_, err := store.Get(ctx, sha)
	check("Get", err)

	_, err = store.GetFrom(ctx, sha, 1)
	check("GetFrom", err)

	check("Delete", store.Delete(ctx, sha))

	_, err = store.Exists(ctx, sha)
	check("Exists", err)

	_, err = store.GetMetadata(ctx, sha)
	check("GetMetadata", err)

	_, err = store.GetSize(ctx, sha)
	check("GetSize", err)
}

func TestOCIDeleteStatuses(t *testing.T) {
	t.Parallel()

	data := []byte("x")
	sha := sha256Of(data)

	tests := []struct {
		status  int
		wantErr bool
	}{
		{http.StatusAccepted, false},
		{http.StatusOK, false},
		{http.StatusMethodNotAllowed, true}, // registry with deletion disabled
		{http.StatusForbidden, true},
	}

	for _, tc := range tests {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			t.Parallel()

			reg := newOCIRegistry(t)
			reg.seed(sha, data)
			reg.deleteStatus = tc.status
			store := newOCIStore(t, reg, nil)

			err := store.Delete(context.Background(), sha)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestOCIPresignIsUnsupported(t *testing.T) {
	t.Parallel()

	store := newOCIStore(t, newOCIRegistry(t), nil)

	if _, err := store.PresignURL(context.Background(), sha256Of([]byte("x")), time.Minute); !errors.Is(err, ErrPresignNotSupported) {
		t.Fatalf("err = %v, want ErrPresignNotSupported", err)
	}
}

func TestOCIHealthCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"healthy", http.StatusOK, false},
		{"reachable but auth required", http.StatusUnauthorized, false},
		{"server error", http.StatusInternalServerError, true},
		{"not a registry", http.StatusNotFound, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reg := newOCIRegistry(t)
			reg.pingStatus = tc.status
			store := newOCIStore(t, reg, nil)

			if err := store.HealthCheck(context.Background()); (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}

	t.Run("unreachable", func(t *testing.T) {
		t.Parallel()

		reg := newOCIRegistry(t)
		store := newOCIStore(t, reg, nil)
		reg.Close()

		if err := store.HealthCheck(context.Background()); err == nil {
			t.Fatal("HealthCheck of a closed registry succeeded")
		}
	})
}

func TestOCISendsCredentialsOnlyToTheRegistryHost(t *testing.T) {
	t.Parallel()

	data := []byte("authenticated")
	sha := sha256Of(data)

	// A foreign host the registry's upload Location points at.
	var foreignAuth string

	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignAuth = r.Header.Get("Authorization")

		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(foreign.Close)

	reg := newOCIRegistry(t)
	reg.user, reg.pass = "robot", "s3cret"

	store := newOCIStore(t, reg, func(c *StorageConfig) { c.OCIUsername, c.OCIPassword = "robot", "s3cret" })

	if _, err := store.Put(context.Background(), bytes.NewReader(data), map[string]string{"sha256": sha}); err != nil {
		t.Fatalf("authenticated Put: %v", err)
	}

	if !strings.HasPrefix(reg.authSeen["POST /v2/oa/blobs/blobs/uploads/"], "Basic ") {
		t.Fatalf("registry requests carried no basic credentials: %v", reg.authSeen)
	}

	anon := newOCIStore(t, reg, nil)
	if _, err := anon.Get(context.Background(), sha); err == nil {
		t.Fatal("registry requiring credentials served an anonymous Get")
	}

	// A registry redirecting the upload to another host must not receive the credentials.
	other := sha256Of([]byte("second"))
	reg.location = foreign.URL + "/upload/session"

	if _, err := store.Put(context.Background(), bytes.NewReader([]byte("second")), map[string]string{"sha256": other}); err != nil {
		t.Fatalf("Put via foreign location: %v", err)
	}

	if foreignAuth != "" {
		t.Fatalf("credentials leaked to the foreign host: %q", foreignAuth)
	}
}

func TestOCISameHost(t *testing.T) {
	t.Parallel()

	store, err := NewOCIBlobStore(StorageConfig{Endpoint: "https://Registry.Example.com/", Bucket: "r"})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		url  string
		want bool
	}{
		{"https://registry.example.com/v2/r/blobs/uploads/x", true},
		{"https://REGISTRY.EXAMPLE.COM/other", true},
		{"http://registry.example.com/v2/", false},
		{"https://registry.example.com:8443/v2/", false},
		{"https://evil.example.net/v2/", false},
		{"https://registry.example.com.evil.net/v2/", false},
	}

	for _, tc := range tests {
		u, _ := url.Parse(tc.url)
		if got := store.sameHost(u); got != tc.want {
			t.Errorf("sameHost(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}

	broken := &OCIBlobStore{base: "http://[::1"}
	if broken.sameHost(&url.URL{Scheme: "http", Host: "x"}) {
		t.Error("sameHost with an unparsable base reported a match")
	}
}
