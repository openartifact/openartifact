// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package netutil

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// downloadClient trusts the test server's certificate and treats it as the
// operator-configured host.
func downloadClient(t *testing.T, trusted *httptest.Server) *http.Client {
	t.Helper()

	u, err := url.Parse(trusted.URL)
	if err != nil {
		t.Fatal(err)
	}

	c := NewDownloadClient(5*time.Second, u.Host)

	pool := x509.NewCertPool()
	pool.AddCert(trusted.Certificate())

	c.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}

	return c
}

// fetch GETs rawURL and returns the body, closing it.
func fetch(t *testing.T, c *http.Client, rawURL string) (string, error) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)

	return string(body), err
}

func TestDownloadClient_ConfiguredHostMayBePrivate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}

		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	// The test server listens on loopback, which SafeHTTPClient would refuse.
	body, err := fetch(t, downloadClient(t, srv), srv.URL+"/start")
	if err != nil {
		t.Fatalf("a redirect back to the configured host must work: %v", err)
	}

	if body != "ok" {
		t.Errorf("body = %q", body)
	}
}

func TestDownloadClient_RedirectToPrivateRangeIsRefused(t *testing.T) {
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the redirect target must never be contacted")
		_, _ = io.WriteString(w, "secret")
	}))
	defer other.Close()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer srv.Close()

	_, err := fetch(t, downloadClient(t, srv), srv.URL)
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want ErrBlockedAddress", err)
	}
}

func TestDownloadClient_RedirectToHTTPIsRefused(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/plain", http.StatusFound)
	}))
	defer srv.Close()

	if _, err := fetch(t, downloadClient(t, srv), srv.URL); !errors.Is(err, errRedirectNotHTTPS) {
		t.Fatalf("err = %v, want errRedirectNotHTTPS", err)
	}
}

func TestDownloadClient_RedirectLoopIsCapped(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path+"x", http.StatusFound)
	}))
	defer srv.Close()

	if _, err := fetch(t, downloadClient(t, srv), srv.URL+"/"); !errors.Is(err, errTooManyRedirects) {
		t.Fatalf("err = %v, want errTooManyRedirects", err)
	}
}

func TestDownloadClient_HonoursProxyEnvironment(t *testing.T) {
	c := NewDownloadClient(time.Second, "mirror.example")

	if c.Transport.(*http.Transport).Proxy == nil {
		t.Fatal("proxy settings from the environment are ignored")
	}
}
