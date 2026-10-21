// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openartifact/openartifact/pkg/logger"
)

func newServerTestLogger(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	return log
}

// writeSelfSignedCert writes a throwaway certificate and key for 127.0.0.1.
func writeSelfSignedCert(t *testing.T) (string, string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")

	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}

	return certPath, keyPath
}

// TestStartFailsFast covers the startup errors Start must return itself. They
// used to surface (if at all) as a log line from the serve goroutine while the
// process carried on without serving.
func TestStartFailsFast(t *testing.T) {
	occupied, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = occupied.Close() }()

	t.Run("port in use", func(t *testing.T) {
		s := NewHTTPServer(occupied.Addr().String(), newServerTestLogger(t))
		if err := s.Start(context.Background()); err == nil {
			_ = s.Stop(context.Background())

			t.Fatal("Start on an occupied port succeeded")
		}
	})

	t.Run("missing TLS key pair", func(t *testing.T) {
		s := NewHTTPServer("127.0.0.1:0", newServerTestLogger(t))
		s.EnableTLS("/nonexistent/tls.crt", "/nonexistent/tls.key")

		if err := s.Start(context.Background()); err == nil {
			_ = s.Stop(context.Background())

			t.Fatal("Start with unreadable TLS files succeeded")
		}
	})
}

func TestStartServesTLS(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t)

	probe, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	addr := probe.Addr().String()
	_ = probe.Close()

	s := NewHTTPServer(addr, newServerTestLogger(t))
	s.EnableTLS(certPath, keyPath)
	s.GetRouter().Get("/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() { _ = s.Stop(context.Background()) })

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+addr+"/ping", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET over TLS: %v", err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}

	select {
	case err := <-s.ServeErrors():
		t.Errorf("unexpected serve error: %v", err)
	default:
	}
}
