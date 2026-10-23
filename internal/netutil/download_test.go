// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package netutil

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
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

// A redirect to a host name is checked where the connection is made: the name
// below resolves to loopback, which the client must refuse to dial.
func TestDownloadClient_RedirectToNameResolvingToLoopbackIsRefusedAtDial(t *testing.T) {
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the redirect target must never be contacted")
		_, _ = io.WriteString(w, "secret")
	}))
	defer other.Close()

	_, port, err := net.SplitHostPort(other.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://localhost:"+port+"/", http.StatusFound)
	}))
	defer srv.Close()

	if _, err := fetch(t, downloadClient(t, srv), srv.URL); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want ErrBlockedAddress", err)
	}
}

// connectProxy is an HTTP proxy that tunnels every CONNECT to target, whatever
// host the client asked for, and records the hosts it was asked for.
type connectProxy struct {
	*httptest.Server

	mu    sync.Mutex
	hosts []string
}

func newConnectProxy(t *testing.T, target string) *connectProxy {
	t.Helper()

	p := &connectProxy{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}

		p.mu.Lock()
		p.hosts = append(p.hosts, r.Host)
		p.mu.Unlock()

		upstream, err := (&net.Dialer{}).DialContext(r.Context(), "tcp", target)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer upstream.Close()

		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()

		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")

		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(upstream, conn)
			close(done)
		}()
		_, _ = io.Copy(conn, upstream)
		<-done
	}))
	t.Cleanup(p.Close)

	return p
}

func (p *connectProxy) requested() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.hosts...)
}

// proxyClient sends everything except the trusted server through proxy.
func proxyClient(t *testing.T, trusted *httptest.Server, proxy *connectProxy) *http.Client {
	t.Helper()

	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}

	u, err := url.Parse(trusted.URL)
	if err != nil {
		t.Fatal(err)
	}

	c := newDownloadClient(5*time.Second, u.Host, func(req *http.Request) (*url.URL, error) {
		if req.URL.Host == u.Host {
			return nil, nil
		}

		return proxyURL, nil
	})

	pool := x509.NewCertPool()
	pool.AddCert(trusted.Certificate())

	c.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}

	return c
}

// Behind a proxy the proxy resolves host names, so a redirect to a name this
// process cannot resolve (example.com, which the proxy maps to the test
// server) must work, and the proxy, not the client, is what was dialled.
func TestDownloadClient_ProxyResolvesRedirectHost(t *testing.T) {
	final := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "from the proxy")
	}))
	defer final.Close()

	proxy := newConnectProxy(t, final.Listener.Addr().String())

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/asset", http.StatusFound)
	}))
	defer srv.Close()

	body, err := fetch(t, proxyClient(t, srv, proxy), srv.URL)
	if err != nil {
		t.Fatalf("a redirect served through a proxy must not need local DNS: %v", err)
	}

	if body != "from the proxy" {
		t.Errorf("body = %q", body)
	}

	if got := proxy.requested(); len(got) != 1 || got[0] != "example.com:443" {
		t.Errorf("proxy CONNECT hosts = %v, want [example.com:443]", got)
	}
}

// A literal private address is refused even when a proxy would carry it.
func TestDownloadClient_ProxyStillRefusesLiteralPrivateRedirect(t *testing.T) {
	proxy := newConnectProxy(t, "127.0.0.1:1")

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://169.254.169.254/latest", http.StatusFound)
	}))
	defer srv.Close()

	if _, err := fetch(t, proxyClient(t, srv, proxy), srv.URL); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want ErrBlockedAddress", err)
	}

	if got := proxy.requested(); len(got) != 0 {
		t.Errorf("the proxy was asked for %v", got)
	}
}
