// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/config"
)

func TestProbeHealth(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		w.WriteHeader(status)
	}))
	defer srv.Close()

	addr := srv.Listener.Addr().String()

	if err := probeHealth("http", addr, "", time.Second); err != nil {
		t.Fatalf("200 should be healthy: %v", err)
	}

	status = http.StatusServiceUnavailable
	if err := probeHealth("http", addr, "", time.Second); err == nil {
		t.Fatal("503 should be unhealthy")
	}
}

// closedAddr returns a loopback address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig

	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	addr := l.Addr().String()
	_ = l.Close()

	return addr
}

func TestProbeHealthConnectionRefused(t *testing.T) {
	addr := closedAddr(t)

	if err := probeHealth("http", addr, "", time.Second); err == nil {
		t.Fatal("closed port should be unhealthy")
	}
}

func TestHealthcheckFlagUsesConfiguredPort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SERVER_PORT", port)
	t.Setenv("OPENARTIFACT_CONFIG", "")

	if err := run([]string{"-healthcheck"}); err != nil {
		t.Fatalf("healthcheck against live server: %v", err)
	}

	_, dead, _ := net.SplitHostPort(closedAddr(t))

	t.Setenv("SERVER_PORT", dead)

	if err := run([]string{"-healthcheck"}); err == nil {
		t.Fatal("expected failure when nothing listens")
	}
}

func writeServerCert(t *testing.T, cert *x509.Certificate) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "server.pem")
	pemData := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})

	if err := os.WriteFile(path, pemData, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestProbeHealthVerifiesServerCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	addr := srv.Listener.Addr().String()

	if err := probeHealth("https", addr, writeServerCert(t, srv.Certificate()), 2*time.Second); err != nil {
		t.Fatalf("probe with the server's own certificate: %v", err)
	}

	// Mint a certificate that is not the one the server presents.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: "other"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"other.example"},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	otherCert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	if err := probeHealth("https", addr, writeServerCert(t, otherCert), 2*time.Second); err == nil {
		t.Fatal("a different certificate on the port must fail the probe")
	}

	if err := probeHealth("https", addr, "", 2*time.Second); err == nil {
		t.Fatal("an untrusted self-signed certificate must fail without a pinned certificate")
	}

	if err := probeHealth("http", addr, "", 2*time.Second); err == nil {
		t.Fatal("plain http against a TLS listener must fail")
	}
}

func TestHealthTarget(t *testing.T) {
	tests := []struct {
		name       string
		host       string
		tls        bool
		wantScheme string
		wantAddr   string
	}{
		{"wildcard v4", "0.0.0.0", false, "http", "127.0.0.1:8080"},
		{"wildcard v6", "::", false, "http", "127.0.0.1:8080"},
		{"empty", "", false, "http", "127.0.0.1:8080"},
		{"concrete v4", "10.1.2.3", true, "https", "10.1.2.3:8080"},
		{"concrete v6", "fd00::5", false, "http", "[fd00::5]:8080"},
		{"bracketed wildcard", "[::]", true, "https", "127.0.0.1:8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme, addr := healthTarget(config.ServerConfig{Host: tt.host, Port: 8080, TLSEnabled: tt.tls})
			if scheme != tt.wantScheme || addr != tt.wantAddr {
				t.Fatalf("got %s %s, want %s %s", scheme, addr, tt.wantScheme, tt.wantAddr)
			}
		})
	}
}
