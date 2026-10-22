// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package netutil

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func redirectReq(t *testing.T, raw string, hdr bool) *http.Request {
	t.Helper()

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	r := &http.Request{URL: u, Header: http.Header{}}
	if hdr {
		r.Header.Set("Authorization", "Bearer x")
		r.Header.Set("Proxy-Authorization", "Basic x")
		r.Header.Set("Cookie", "a=b")
	}

	return r
}

func TestCheckRedirectCredentials(t *testing.T) {
	cr := SafeHTTPClient(time.Second).CheckRedirect

	cases := []struct {
		name  string
		chain []string // initial request, then each hop
		keep  bool
	}{
		{"same origin", []string{"https://a/x", "https://a/y"}, true},
		{"scheme downgrade same host", []string{"https://a/x", "http://a/y"}, false},
		{"scheme upgrade same host", []string{"http://a/x", "https://a/y"}, false},
		{"other host", []string{"https://a/x", "https://b/y"}, false},
		{"other port", []string{"https://a/x", "https://a:8443/y"}, false},
		{"away and back", []string{"https://a/x", "https://b/y", "https://a/z"}, false},
		{"downgrade and back", []string{"https://a/x", "http://a/y", "https://a/z"}, false},
		{"downgrade then stay", []string{"https://a/x", "http://a/y", "http://a/z"}, false},
	}

	for _, tc := range cases {
		var via []*http.Request

		var req *http.Request

		for i, raw := range tc.chain {
			req = redirectReq(t, raw, i == 0)
			if i > 0 {
				// Go copies the initial headers onto every hop.
				req.Header = via[0].Header.Clone()
				if err := cr(req, via); err != nil {
					t.Fatalf("%s: %v", tc.name, err)
				}
			}

			via = append(via, req)
		}

		got := req.Header.Get("Authorization") != ""
		if got != tc.keep {
			t.Errorf("%s: Authorization kept = %v, want %v", tc.name, got, tc.keep)
		}

		if !tc.keep && (req.Header.Get("Proxy-Authorization") != "" || req.Header.Get("Cookie") != "") {
			t.Errorf("%s: Proxy-Authorization/Cookie not stripped", tc.name)
		}
	}
}

// TestSafeClientRedirectDowngrade drives a real redirect from a TLS server to
// a plain-HTTP server on the same host.
func TestSafeClientRedirectDowngrade(t *testing.T) {
	t.Setenv(EnvAllowPrivate, "true")

	seen := make(chan string, 1)
	plain := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
	}))
	defer plain.Close()

	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer secure.Close()

	c := SafeHTTPClient(5 * time.Second)
	c.Transport.(*http.Transport).TLSClientConfig = secure.Client().Transport.(*http.Transport).TLSClientConfig

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, secure.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Authorization", "Bearer secret")

	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if got := <-seen; got != "" {
		t.Errorf("Authorization leaked over scheme downgrade: %q", got)
	}
}
