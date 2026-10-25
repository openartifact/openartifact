// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/repotype"
)

func unitStampUUID(last byte) uuid.UUID {
	var u uuid.UUID

	u[15] = last

	return u
}

func TestStampCompareOrdering(t *testing.T) {
	t.Parallel()

	lo, hi := unitStampUUID(1), unitStampUUID(2)

	tests := []struct {
		name string
		a, b Stamp
		want int
	}{
		{"equal", Stamp{10, 1, lo}, Stamp{10, 1, lo}, 0},
		{"wall before", Stamp{9, 5, hi}, Stamp{10, 0, lo}, -1},
		{"wall after", Stamp{11, 0, lo}, Stamp{10, 9, hi}, 1},
		{"counter before", Stamp{10, 1, hi}, Stamp{10, 2, lo}, -1},
		{"counter after", Stamp{10, 3, lo}, Stamp{10, 2, hi}, 1},
		{"origin before", Stamp{10, 1, lo}, Stamp{10, 1, hi}, -1},
		{"origin after", Stamp{10, 1, hi}, Stamp{10, 1, lo}, 1},
		{"negative counter", Stamp{10, -1, hi}, Stamp{10, 0, lo}, -1},
		{"zero vs set", Stamp{}, Stamp{1, 0, uuid.Nil}, -1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.a.Compare(tc.b); got != tc.want {
				t.Fatalf("%v.Compare(%v) = %d, want %d", tc.a, tc.b, got, tc.want)
			}

			if got := tc.b.Compare(tc.a); got != -tc.want {
				t.Fatalf("%v.Compare(%v) = %d, want %d (antisymmetry)", tc.b, tc.a, got, -tc.want)
			}
		})
	}
}

func TestStampStringIsZero(t *testing.T) {
	t.Parallel()

	if !(Stamp{}).IsZero() {
		t.Fatal("zero Stamp is not IsZero")
	}

	for _, s := range []Stamp{{Wall: 1}, {Counter: 1}, {Origin: unitStampUUID(1)}} {
		if s.IsZero() {
			t.Fatalf("%+v reports IsZero", s)
		}
	}

	origin := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	s := Stamp{Wall: 1700000000123456, Counter: 7, Origin: origin}

	want := "1700000000123456.7@11111111-2222-3333-4444-555555555555"
	if got := s.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}

	// The textual form round-trips back to the same stamp.
	var (
		wall    int64
		counter int32
		org     string
	)

	rest := strings.Replace(s.String(), "@", " ", 1)
	rest = strings.Replace(rest, ".", " ", 1)

	if _, err := fmt.Sscanf(rest, "%d %d %s", &wall, &counter, &org); err != nil {
		t.Fatalf("parse %q: %v", s.String(), err)
	}

	back := Stamp{Wall: wall, Counter: counter, Origin: uuid.MustParse(org)}
	if back.Compare(s) != 0 || back != s {
		t.Fatalf("round trip = %+v, want %+v", back, s)
	}
}

func TestBackoffGrowthAndCap(t *testing.T) {
	t.Parallel()

	const (
		poll     = time.Minute
		maxDelay = 10 * time.Minute
	)

	within := func(d, nominal time.Duration) bool {
		return d >= time.Duration(float64(nominal)*0.79) && d <= time.Duration(float64(nominal)*1.21)
	}

	// The first retry waits baseBackoff, then it doubles until the cap.
	nominal := baseBackoff

	for n := range 12 {
		want := min(nominal, maxDelay)

		for range 50 {
			if got := Backoff(n, poll, maxDelay); !within(got, want) {
				t.Fatalf("Backoff(%d) = %v, want %v +-20%%", n, got, want)
			}
		}

		nominal *= 2
	}
}

func TestBackoffNonPositiveAttempt(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, -1, -100} {
		for range 50 {
			got := Backoff(n, time.Minute, time.Hour)
			if got < time.Duration(float64(baseBackoff)*0.79) || got > time.Duration(float64(baseBackoff)*1.21) {
				t.Fatalf("Backoff(%d) = %v, want about the base %v", n, got, baseBackoff)
			}
		}
	}
}

func TestBackoffBaseFollowsShortPoll(t *testing.T) {
	t.Parallel()

	// A poll interval below the base lowers the base; a non-positive one falls
	// back to a second.
	tests := []struct {
		name string
		poll time.Duration
		want time.Duration
	}{
		{"short poll", time.Second, time.Second},
		{"zero poll", 0, time.Second},
		{"negative poll", -time.Second, time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := Backoff(0, tc.poll, time.Hour)
			if got < time.Duration(float64(tc.want)*0.79) || got > time.Duration(float64(tc.want)*1.21) {
				t.Fatalf("Backoff(0, %v) = %v, want about %v", tc.poll, got, tc.want)
			}
		})
	}
}

func TestBackoffNeverExceedsCapWithJitter(t *testing.T) {
	t.Parallel()

	const maxDelay = 30 * time.Second

	for n := 0; n < 200; n += 7 {
		for range 20 {
			if got := Backoff(n, time.Minute, maxDelay); got > time.Duration(float64(maxDelay)*1.21) {
				t.Fatalf("Backoff(%d) = %v exceeds cap %v plus jitter", n, got, maxDelay)
			}
		}
	}
}

func TestValidatePeerURLScheme(t *testing.T) {
	t.Setenv(netutil.EnvAllowPrivate, "")

	ctx := context.Background()

	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"https public ip", "https://8.8.8.8", false},
		{"https public ip with port and path", "https://8.8.8.8:8443/oa", false},
		{"http refused", "http://8.8.8.8", true},
		{"ftp refused", "ftp://8.8.8.8", true},
		{"no scheme", "8.8.8.8", true},
		{"empty", "", true},
		{"no host", "https://", true},
		{"userinfo", "https://user:pw@8.8.8.8", true},
		{"user only", "https://user@8.8.8.8", true},
		{"query", "https://8.8.8.8/?a=b", true},
		{"fragment", "https://8.8.8.8/#frag", true},
		{"unparseable", "https://8.8.8.8:notaport", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePeerURL(ctx, tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidatePeerURL(%q) = %v, wantErr %v", tc.raw, err, tc.wantErr)
			}

			if err != nil && !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidatePeerURL(%q) error %v does not wrap ErrInvalid", tc.raw, err)
			}
		})
	}
}

func TestValidatePeerURLHTTPNeedsAllowPrivate(t *testing.T) {
	ctx := context.Background()

	t.Setenv(netutil.EnvAllowPrivate, "true")

	if err := ValidatePeerURL(ctx, "http://8.8.8.8"); err != nil {
		t.Fatalf("http with private upstreams allowed: %v", err)
	}

	// Credentials, query and fragment stay refused regardless of the switch.
	for _, raw := range []string{"http://u:p@8.8.8.8", "http://8.8.8.8/?a=1", "http://8.8.8.8/#x"} {
		if err := ValidatePeerURL(ctx, raw); err == nil {
			t.Fatalf("ValidatePeerURL(%q) succeeded", raw)
		}
	}

	t.Setenv(netutil.EnvAllowPrivate, "false")

	err := ValidatePeerURL(ctx, "http://8.8.8.8")
	if err == nil || !errors.Is(err, errHTTPSRequired) {
		t.Fatalf("http with private upstreams disallowed = %v, want errHTTPSRequired", err)
	}
}

func TestValidatePeerURLBlockedAddresses(t *testing.T) {
	ctx := context.Background()

	blocked := []string{
		"https://127.0.0.1",
		"https://127.0.0.1:8443",
		"https://[::1]",
		"https://10.1.2.3",
		"https://172.16.0.1",
		"https://192.168.1.1",
		"https://169.254.169.254",
		"https://0.0.0.0",
	}

	t.Run("private not allowed", func(t *testing.T) {
		t.Setenv(netutil.EnvAllowPrivate, "")

		for _, raw := range blocked {
			err := ValidatePeerURL(ctx, raw)
			if err == nil || !errors.Is(err, ErrInvalid) {
				t.Errorf("ValidatePeerURL(%q) = %v, want ErrInvalid", raw, err)
			}
		}
	})

	t.Run("private allowed keeps metadata blocked", func(t *testing.T) {
		t.Setenv(netutil.EnvAllowPrivate, "true")

		for _, raw := range []string{"https://127.0.0.1", "https://10.1.2.3", "https://192.168.1.1"} {
			if err := ValidatePeerURL(ctx, raw); err != nil {
				t.Errorf("ValidatePeerURL(%q) with private allowed = %v", raw, err)
			}
		}

		for _, raw := range []string{"https://169.254.169.254", "https://0.0.0.0"} {
			if err := ValidatePeerURL(ctx, raw); err == nil {
				t.Errorf("ValidatePeerURL(%q) succeeded, metadata/unspecified must stay blocked", raw)
			}
		}
	})
}

func TestValidatePeerTLS(t *testing.T) {
	t.Parallel()

	certPEM, keyPEM := unitSelfSigned(t)

	tests := []struct {
		name           string
		ca, cert, key  string
		wantErr        bool
		wantErrContain string
	}{
		{"nothing", "", "", "", false, ""},
		{"valid CA", certPEM, "", "", false, ""},
		{"valid pair", "", certPEM, keyPEM, false, ""},
		{"CA and pair", certPEM, certPEM, keyPEM, false, ""},
		{"bad CA PEM", "not a pem", "", "", true, "CA"},
		{"CA with garbage block", "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n", "", "", true, "CA"},
		{"cert without key", "", certPEM, "", true, "certificate"},
		{"key without cert", "", "", keyPEM, true, "certificate"},
		{"garbage pair", "", "x", "y", true, "certificate"},
		{"mismatched halves", "", keyPEM, certPEM, true, "certificate"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := ValidatePeerTLS(tc.ca, tc.cert, tc.key)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidatePeerTLS = %v, wantErr %v", err, tc.wantErr)
			}

			if err == nil {
				return
			}

			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error %v does not wrap ErrInvalid", err)
			}

			if !strings.Contains(err.Error(), tc.wantErrContain) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErrContain)
			}
		})
	}
}

// unitTimeoutError is a net.Error.
type unitTimeoutError struct{}

func (unitTimeoutError) Error() string   { return "i/o timeout" }
func (unitTimeoutError) Timeout() bool   { return true }
func (unitTimeoutError) Temporary() bool { return true }

func TestIsPeerWideClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"invalid unit", fmt.Errorf("%w: bad data", ErrInvalid), false},
		{"stale", ErrStale, false},
		{"quarantined", ErrQuarantined, false},
		{"defer error", deferred("missing_dep", errors.New("dep absent")), false},
		{"defer error wrapping net error", deferred("x", unitTimeoutError{}), true},
		{"context canceled", context.Canceled, true},
		{"deadline", context.DeadlineExceeded, true},
		{"wrapped canceled", fmt.Errorf("pull: %w", context.Canceled), true},
		{"eof", io.EOF, true},
		{"unexpected eof", fmt.Errorf("read: %w", io.ErrUnexpectedEOF), true},
		{"net error", unitTimeoutError{}, true},
		{"wrapped net error", fmt.Errorf("fetch: %w", &net.OpError{Op: "dial", Err: errors.New("refused")}), true},
		{"blocked address", fmt.Errorf("dial: %w", netutil.ErrBlockedAddress), true},
		{"http 401", &HTTPError{Status: http.StatusUnauthorized}, true},
		{"http 403", &HTTPError{Status: http.StatusForbidden}, true},
		{"http 429", &HTTPError{Status: http.StatusTooManyRequests}, true},
		{"http 408", &HTTPError{Status: http.StatusRequestTimeout}, true},
		{"http 500", &HTTPError{Status: http.StatusInternalServerError}, true},
		{"http 502", &HTTPError{Status: http.StatusBadGateway}, true},
		{"http 503 wrapped", fmt.Errorf("get: %w", &HTTPError{Status: http.StatusServiceUnavailable}), true},
		{"http 404", &HTTPError{Status: http.StatusNotFound}, false},
		{"http 400", &HTTPError{Status: http.StatusBadRequest}, false},
		{"http 410", &HTTPError{Status: http.StatusGone}, false},
		{"http 423", &HTTPError{Status: http.StatusLocked}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := isPeerWide(tc.err); got != tc.want {
				t.Fatalf("isPeerWide(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestHTTPErrorFatal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status int
		want   bool
	}{
		{http.StatusUnauthorized, true},
		{http.StatusForbidden, true},
		{http.StatusOK, false},
		{http.StatusBadRequest, false},
		{http.StatusNotFound, false},
		{http.StatusLocked, false},
		{http.StatusTooManyRequests, false},
		{http.StatusInternalServerError, false},
		{http.StatusBadGateway, false},
	}

	for _, tc := range tests {
		e := &HTTPError{Status: tc.status, Body: "x"}
		if got := e.Fatal(); got != tc.want {
			t.Errorf("HTTPError{%d}.Fatal() = %v, want %v", tc.status, got, tc.want)
		}
	}

	e := &HTTPError{Status: 418, Body: "teapot"}
	if msg := e.Error(); !strings.Contains(msg, "418") || !strings.Contains(msg, "teapot") {
		t.Fatalf("Error() = %q, want status and body", msg)
	}
}

// registeredFormats lists the formats with a registered codec, sorted.
func registeredFormats() []string { return slices.Sorted(maps.Keys(codecs)) }

func TestRegistryCoversFederationSupportedFormats(t *testing.T) {
	t.Parallel()

	// Every format repotype advertises has a codec, and every codec's format
	// is advertised: the repository API and the engine agree.
	known := []string{
		"raw", "maven", "npm", "docker", "helm", "pypi", "cargo", "go", "nuget",
		// formats federation does not serve
		"deb", "rpm", "apt", "yum", "conan", "terraform", "generic", "", "RAW", "unknown",
	}

	registered := registeredFormats()

	for _, f := range registered {
		if !repotype.FederationSupported(f) {
			t.Errorf("codec %q is registered but repotype.FederationSupported(%q) is false", f, f)
		}

		c, err := CodecFor(f)
		if err != nil || c == nil || c.Format() != f {
			t.Errorf("CodecFor(%q) = %v, %v", f, c, err)
		}
	}

	for _, f := range known {
		supported := repotype.FederationSupported(f)
		_, err := CodecFor(f)

		switch {
		case supported && err != nil:
			t.Errorf("repotype.FederationSupported(%q) is true but CodecFor fails: %v", f, err)
		case !supported && err == nil:
			t.Errorf("CodecFor(%q) succeeds but repotype.FederationSupported is false", f)
		case !supported && !errors.Is(err, ErrUnsupportedFormat):
			t.Errorf("CodecFor(%q) error %v is not ErrUnsupportedFormat", f, err)
		}

		if supported && !slices.Contains(registered, f) {
			t.Errorf("format %q supported but absent from the registered codecs %v", f, registered)
		}
	}
}

func TestRegistryFederationFormatsListsExactlyTheCodecs(t *testing.T) {
	t.Parallel()

	listed := strings.Split(repotype.FederationFormats(), ", ")
	slices.Sort(listed)

	registered := registeredFormats()
	if !slices.Equal(listed, registered) {
		t.Fatalf("repotype.FederationFormats() lists %v, registered codecs are %v", listed, registered)
	}

	for _, f := range listed {
		if !repotype.FederationSupported(f) {
			t.Errorf("FederationFormats lists %q but FederationSupported is false", f)
		}
	}
}

// unitSelfSigned returns a throwaway self-signed certificate and its private
// key, PEM encoded.
func unitSelfSigned(t *testing.T) (string, string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "peer.test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}
