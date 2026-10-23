// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"math/big"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewjam/saml"

	"github.com/openartifact/openartifact/internal/database/models"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.t = c.t.Add(d)
}

// fetchStub is an injectable metadata fetcher.
type fetchStub struct {
	mu    sync.Mutex
	body  string
	err   error
	calls int
}

func (f *fetchStub) set(body string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.body, f.err = body, err
}

func (f *fetchStub) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}

func (f *fetchStub) fetch(_ context.Context, _ string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++

	if f.err != nil {
		return nil, f.err
	}

	return []byte(f.body), nil
}

func newTestCert(t *testing.T, cn string) *x509.Certificate {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	return cert
}

// idpDoc renders IdP metadata that trusts certs; rootAttrs go on the
// EntityDescriptor (validUntil / cacheDuration).
func idpDoc(rootAttrs string, certs ...*x509.Certificate) string {
	var keys strings.Builder

	for _, c := range certs {
		keys.WriteString(`<KeyDescriptor use="signing"><KeyInfo xmlns="http://www.w3.org/2000/09/xmldsig#"><X509Data><X509Certificate>` +
			base64.StdEncoding.EncodeToString(c.Raw) + `</X509Certificate></X509Data></KeyInfo></KeyDescriptor>`)
	}

	return `<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example.com" ` + rootAttrs + `>` +
		`<IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">` + keys.String() +
		`<SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://idp.example.com/sso"/>` +
		`</IDPSSODescriptor></EntityDescriptor>`
}

func firstCertCN(t *testing.T, md *saml.EntityDescriptor) string {
	t.Helper()

	certs, err := idpSigningCerts(md)
	if err != nil {
		t.Fatalf("idpSigningCerts: %v", err)
	}

	return certs[0].Subject.CommonName
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

const testMetadataURL = "https://idp.example.com/metadata"

func newTestMetadataCache() (*idpMetadataCache, *fakeClock, *fetchStub) {
	clock := newFakeClock()
	stub := &fetchStub{}

	return newIDPMetadataCache(clock.now, stub.fetch), clock, stub
}

func TestIDPMetadataRefresh_ClampsConfiguredInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		seconds int
		want    time.Duration
	}{
		{0, time.Hour},
		{-5, time.Hour},
		{1, time.Minute},
		{59, time.Minute},
		{60, time.Minute},
		{120, 2 * time.Minute},
		{86400, 24 * time.Hour},
		{10_000_000, 24 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.seconds), func(t *testing.T) {
			t.Parallel()

			if got := IDPMetadataRefresh(models.SAMLConfig{IDPMetadataRefreshSeconds: tt.seconds}); got != tt.want {
				t.Errorf("IDPMetadataRefresh(%d) = %v, want %v", tt.seconds, got, tt.want)
			}
		})
	}
}

// A rolled-over IdP certificate must be picked up on its own after the refresh
// interval, without anybody editing the provider.
func TestIDPMetadataCache_PicksUpRotatedCertificateAfterInterval(t *testing.T) {
	t.Parallel()

	cache, clock, stub := newTestMetadataCache()
	stub.set(idpDoc("", newTestCert(t, "old")), nil)

	cfg := models.SAMLConfig{IDPMetadataURL: testMetadataURL, IDPMetadataRefreshSeconds: 600}

	md, err := cache.metadata(context.Background(), cfg, nil)
	if err != nil || firstCertCN(t, md) != "old" {
		t.Fatalf("first load: cn=%v err=%v", md, err)
	}

	stub.set(idpDoc("", newTestCert(t, "new")), nil)
	clock.advance(599 * time.Second)

	md, err = cache.metadata(context.Background(), cfg, nil)
	if err != nil || firstCertCN(t, md) != "old" || stub.callCount() != 1 {
		t.Fatalf("within the interval the cached copy is used: calls=%d err=%v", stub.callCount(), err)
	}

	clock.advance(2 * time.Second)

	md, err = cache.metadata(context.Background(), cfg, nil)
	if err != nil || firstCertCN(t, md) != "new" || stub.callCount() != 2 {
		t.Fatalf("after the interval the rotated cert must be served: calls=%d err=%v", stub.callCount(), err)
	}
}

func TestIDPMetadataCache_PerProviderIntervals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       models.SAMLConfig
		rootAttrs string
		// advance lists the clock steps between consecutive reads (the first read
		// happens at t0); wantCalls is the total fetch count afterwards.
		advance   []time.Duration
		wantCalls int
	}{
		{
			name:      "default is one hour",
			cfg:       models.SAMLConfig{},
			advance:   []time.Duration{59 * time.Minute, 2 * time.Minute},
			wantCalls: 2,
		},
		{
			name:      "configured interval below the floor is raised to 60s",
			cfg:       models.SAMLConfig{IDPMetadataRefreshSeconds: 5},
			advance:   []time.Duration{50 * time.Second, 20 * time.Second},
			wantCalls: 2,
		},
		{
			name:      "configured interval above the maximum is capped at 24h",
			cfg:       models.SAMLConfig{IDPMetadataRefreshSeconds: 90_000},
			advance:   []time.Duration{23 * time.Hour, 2 * time.Hour},
			wantCalls: 2,
		},
		{
			name:      "cacheDuration shorter than the interval wins",
			cfg:       models.SAMLConfig{},
			rootAttrs: `cacheDuration="PT5M"`,
			advance:   []time.Duration{4 * time.Minute, 2 * time.Minute},
			wantCalls: 2,
		},
		{
			name:      "cacheDuration longer than the interval does not extend it",
			cfg:       models.SAMLConfig{IDPMetadataRefreshSeconds: 120},
			rootAttrs: `cacheDuration="PT6H"`,
			advance:   []time.Duration{time.Minute, 2 * time.Minute},
			wantCalls: 2,
		},
		{
			name:      "cacheDuration below the 60s floor is raised to 60s",
			cfg:       models.SAMLConfig{},
			rootAttrs: `cacheDuration="PT5S"`,
			advance:   []time.Duration{30 * time.Second, 31 * time.Second},
			wantCalls: 2,
		},
		{
			name:      "cacheDuration below the floor is not refetched before 60s",
			cfg:       models.SAMLConfig{},
			rootAttrs: `cacheDuration="PT5S"`,
			advance:   []time.Duration{20 * time.Second, 20 * time.Second},
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cache, clock, stub := newTestMetadataCache()
			stub.set(idpDoc(tt.rootAttrs, newTestCert(t, "idp")), nil)

			cfg := tt.cfg
			cfg.IDPMetadataURL = testMetadataURL

			for i := range len(tt.advance) + 1 {
				if i > 0 {
					clock.advance(tt.advance[i-1])
				}

				if _, err := cache.metadata(context.Background(), cfg, nil); err != nil {
					t.Fatalf("read %d: %v", i, err)
				}
			}

			if got := stub.callCount(); got != tt.wantCalls {
				t.Errorf("fetch calls = %d, want %d", got, tt.wantCalls)
			}
		})
	}
}

func TestIDPMetadataCache_ValidUntil(t *testing.T) {
	t.Parallel()

	cfg := models.SAMLConfig{IDPMetadataURL: testMetadataURL}

	t.Run("a live document that is already expired is rejected", func(t *testing.T) {
		t.Parallel()

		cache, clock, stub := newTestMetadataCache()
		stub.set(idpDoc(`validUntil="`+rfc3339(clock.now().Add(-time.Minute))+`"`, newTestCert(t, "idp")), nil)

		_, err := cache.metadata(context.Background(), cfg, nil)
		if !errors.Is(err, ErrIDPMetadataUnavailable) || !errors.Is(err, errIDPMetadataExpired) {
			t.Fatalf("expected ErrIDPMetadataUnavailable wrapping the expiry, got %v", err)
		}
	})

	t.Run("validUntil bounds the copy even within the refresh interval", func(t *testing.T) {
		t.Parallel()

		cache, clock, stub := newTestMetadataCache()
		stub.set(idpDoc(`validUntil="`+rfc3339(clock.now().Add(10*time.Minute))+`"`, newTestCert(t, "idp")), nil)

		if _, err := cache.metadata(context.Background(), cfg, nil); err != nil {
			t.Fatal(err)
		}

		clock.advance(11 * time.Minute) // before the 1h refresh, after validUntil

		stub.set(idpDoc("", newTestCert(t, "renewed")), nil)

		md, err := cache.metadata(context.Background(), cfg, nil)
		if err != nil || firstCertCN(t, md) != "renewed" || stub.callCount() != 2 {
			t.Fatalf("an expired copy must be refetched: calls=%d err=%v", stub.callCount(), err)
		}
	})

	t.Run("stale-if-error never serves a copy past its validUntil", func(t *testing.T) {
		t.Parallel()

		cache, clock, stub := newTestMetadataCache()
		stub.set(idpDoc(`validUntil="`+rfc3339(clock.now().Add(2*time.Hour))+`"`, newTestCert(t, "idp")), nil)

		if _, err := cache.metadata(context.Background(), cfg, nil); err != nil {
			t.Fatal(err)
		}

		stub.set("", errors.New("idp down"))

		clock.advance(90 * time.Minute) // refresh due, copy still valid

		if _, err := cache.metadata(context.Background(), cfg, nil); err != nil {
			t.Fatalf("a valid stale copy must be served while the IdP is down: %v", err)
		}

		clock.advance(31 * time.Minute) // negative cache over, validUntil passed

		if _, err := cache.metadata(context.Background(), cfg, nil); !errors.Is(err, ErrIDPMetadataUnavailable) {
			t.Fatalf("a copy past validUntil must not be served, got %v", err)
		}
	})

	t.Run("static xml ignores validUntil and never refreshes", func(t *testing.T) {
		t.Parallel()

		cache, clock, stub := newTestMetadataCache()
		xmlCfg := models.SAMLConfig{IDPMetadataXML: idpDoc(`validUntil="`+rfc3339(clock.now().Add(-24*time.Hour))+`"`, newTestCert(t, "static"))}

		for range 2 {
			md, err := cache.metadata(context.Background(), xmlCfg, nil)
			if err != nil || firstCertCN(t, md) != "static" {
				t.Fatalf("static metadata: %v", err)
			}

			clock.advance(48 * time.Hour)
		}

		if stub.callCount() != 0 {
			t.Errorf("static xml must never be fetched, got %d calls", stub.callCount())
		}
	})
}

func TestIDPMetadataCache_StaleIfErrorIsBounded(t *testing.T) {
	t.Parallel()

	cache, clock, stub := newTestMetadataCache()
	stub.set(idpDoc("", newTestCert(t, "idp")), nil)

	cfg := models.SAMLConfig{IDPMetadataURL: testMetadataURL}

	if _, err := cache.metadata(context.Background(), cfg, nil); err != nil {
		t.Fatal(err)
	}

	stub.set("", errors.New("idp down"))

	clock.advance(2 * time.Hour)

	md, err := cache.metadata(context.Background(), cfg, nil)
	if err != nil || firstCertCN(t, md) != "idp" {
		t.Fatalf("stale copy must be served while the refresh fails: %v", err)
	}

	clock.advance(23 * time.Hour) // 25h after the last successful fetch

	if _, err := cache.metadata(context.Background(), cfg, nil); !errors.Is(err, ErrIDPMetadataUnavailable) {
		t.Fatalf("a copy older than 24h must not be served, got %v", err)
	}
}

func TestIDPMetadataCache_NegativeCaching(t *testing.T) {
	t.Parallel()

	cfg := models.SAMLConfig{IDPMetadataURL: testMetadataURL}
	down := errors.New("idp down")

	t.Run("without a copy failures fail fast and are retried after the backoff", func(t *testing.T) {
		t.Parallel()

		cache, clock, stub := newTestMetadataCache()
		stub.set("", down)

		for range 5 {
			if _, err := cache.metadata(context.Background(), cfg, nil); !errors.Is(err, ErrIDPMetadataUnavailable) {
				t.Fatalf("expected ErrIDPMetadataUnavailable, got %v", err)
			}
		}

		if stub.callCount() != 1 {
			t.Fatalf("failures within the backoff must not refetch, got %d calls", stub.callCount())
		}

		clock.advance(idpMetadataFailureBackoff + time.Second)
		stub.set(idpDoc("", newTestCert(t, "back")), nil)

		md, err := cache.metadata(context.Background(), cfg, nil)
		if err != nil || firstCertCN(t, md) != "back" || stub.callCount() != 2 {
			t.Fatalf("recovery after the backoff: calls=%d err=%v", stub.callCount(), err)
		}
	})

	t.Run("with a stale copy it is served without hammering the IdP", func(t *testing.T) {
		t.Parallel()

		cache, clock, stub := newTestMetadataCache()
		stub.set(idpDoc("", newTestCert(t, "idp")), nil)

		if _, err := cache.metadata(context.Background(), cfg, nil); err != nil {
			t.Fatal(err)
		}

		stub.set("", down)
		clock.advance(2 * time.Hour)

		for range 5 {
			if _, err := cache.metadata(context.Background(), cfg, nil); err != nil {
				t.Fatalf("stale copy expected: %v", err)
			}
		}

		if stub.callCount() != 2 { // initial + one failed refresh
			t.Fatalf("expected a single failed refresh inside the backoff, got %d calls", stub.callCount())
		}
	})
}

// Concurrent logins against an expired entry share one fetch.
func TestIDPMetadataCache_ConcurrentLoadsShareOneFetch(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32

	release := make(chan struct{})
	doc := idpDoc("", newTestCert(t, "idp"))
	cache := newIDPMetadataCache(time.Now, func(context.Context, string) ([]byte, error) {
		calls.Add(1)
		<-release

		return []byte(doc), nil
	})

	cfg := models.SAMLConfig{IDPMetadataURL: testMetadataURL}

	var wg sync.WaitGroup

	errs := make(chan error, 8)

	for range 8 {
		wg.Go(func() {
			_, err := cache.metadata(context.Background(), cfg, nil)
			errs <- err
		})
	}

	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no fetch started")
		}

		runtime.Gosched()
	}

	close(release)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("load: %v", err)
		}
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("fetches = %d, want 1", got)
	}
}

// A caller whose request was cancelled must not poison the shared fetch.
func TestIDPMetadataCache_CallerCancellationDoesNotFailTheFetch(t *testing.T) {
	t.Parallel()

	cache, _, stub := newTestMetadataCache()
	stub.set(idpDoc("", newTestCert(t, "idp")), nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := cache.metadata(ctx, models.SAMLConfig{IDPMetadataURL: testMetadataURL}, nil); err != nil {
		t.Fatalf("cancelled caller: %v", err)
	}
}

func TestIDPMetadataCache_NoSource(t *testing.T) {
	t.Parallel()

	cache, _, _ := newTestMetadataCache()
	if _, err := cache.metadata(context.Background(), models.SAMLConfig{}, nil); !errors.Is(err, errNoIDPMetadata) {
		t.Fatalf("expected errNoIDPMetadata, got %v", err)
	}
}

func TestIDPSigningCerts_OnlySigningUseAndRollover(t *testing.T) {
	t.Parallel()

	oldCert, newCert := newTestCert(t, "old"), newTestCert(t, "new")

	md := &saml.EntityDescriptor{IDPSSODescriptors: []saml.IDPSSODescriptor{{}}}
	kd := func(use string, c *x509.Certificate) saml.KeyDescriptor {
		k := saml.KeyDescriptor{Use: use}
		k.KeyInfo.X509Data.X509Certificates = []saml.X509Certificate{{Data: "\n " + base64.StdEncoding.EncodeToString(c.Raw) + "\n"}}

		return k
	}
	md.IDPSSODescriptors[0].KeyDescriptors = []saml.KeyDescriptor{
		kd("signing", oldCert), kd("encryption", newTestCert(t, "enc")), kd("", newCert),
	}

	certs, err := idpSigningCerts(md)
	if err != nil {
		t.Fatal(err)
	}

	if len(certs) != 2 || certs[0].Subject.CommonName != "old" || certs[1].Subject.CommonName != "new" {
		t.Errorf("expected [old new] (signing + unspecified use), got %d certs", len(certs))
	}

	if _, err := idpSigningCerts(&saml.EntityDescriptor{}); err == nil {
		t.Error("metadata without a signing certificate must be an error")
	}
}
