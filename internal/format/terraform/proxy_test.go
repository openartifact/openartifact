// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// stubProxy records the download a proxy ticket was redeemed for.
type stubProxy struct {
	repo *models.Repository
	got  Download
	n    int
}

func (s *stubProxy) ServeTerraformDownload(w http.ResponseWriter, _ *http.Request, repo *models.Repository, d Download) {
	s.repo, s.got = repo, d
	s.n++

	w.WriteHeader(http.StatusNoContent)
}

func TestMinterTicketsAreRedeemedByTheHostedRoute(t *testing.T) {
	e := newEnv(t)
	e.db.Repo.ID = "repo-proxy"
	e.db.Repo.RepoType = "proxy"

	stub := &stubProxy{}
	e.h.WithProxy(stub)

	m := NewMinter(registryHost+"/", []byte(testSecret))
	if !m.Enabled() {
		t.Fatal("minter with a secret is not enabled")
	}

	for _, tc := range []struct {
		name string
		kind DownloadKind
		a, b string
		c    string
		file string
	}{
		{"module", DownloadModule, "vpc", "aws", "1.2.3", "vpc-aws-1.2.3.zip"},
		{"provider zip", DownloadProviderZip, "random", "3.5.1", "linux_amd64", "terraform-provider-random_3.5.1_linux_amd64.zip"},
		{"sums carry the platform", DownloadProviderSums, "random", "3.5.1", "linux_amd64", "terraform-provider-random_3.5.1_SHA256SUMS"},
		{"signature carries the platform", DownloadProviderSumsSig, "random", "3.5.1", "darwin_arm64", "terraform-provider-random_3.5.1_SHA256SUMS.72D7468F.sig"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub.n = 0
			link := m.URL("repo-proxy", tc.kind, tc.a, tc.b, tc.c, tc.file)

			w := e.do(http.MethodGet, ticketPathOf(t, link), nil)
			if w.Code != http.StatusNoContent || stub.n != 1 {
				t.Fatalf("status %d, proxy calls %d: %s", w.Code, stub.n, w.Body)
			}

			want := Download{Kind: tc.kind, A: tc.a, B: tc.b, C: tc.c, File: tc.file}
			if stub.got != want || stub.repo.ID != "repo-proxy" {
				t.Fatalf("redeemed %+v for repo %q, want %+v", stub.got, stub.repo.ID, want)
			}
		})
	}

	t.Run("a ticket of another kind of file names a bad platform", func(t *testing.T) {
		stub.n = 0
		link := m.URL("repo-proxy", DownloadProviderSums, "random", "3.5.1", "Linux", "x")

		if w := e.do(http.MethodGet, ticketPathOf(t, link), nil); w.Code != http.StatusForbidden || stub.n != 0 {
			t.Fatalf("status %d, proxy calls %d, want 403 and none", w.Code, stub.n)
		}
	})

	t.Run("an expired ticket is refused", func(t *testing.T) {
		stub.n = 0
		link := m.URL("repo-proxy", DownloadModule, "vpc", "aws", "1.2.3", "vpc-aws-1.2.3.zip")
		e.now = time.Now().Add(ticketTTL + time.Minute)

		if w := e.do(http.MethodGet, ticketPathOf(t, link), nil); w.Code != http.StatusForbidden || stub.n != 0 {
			t.Fatalf("status %d, proxy calls %d, want 403 and none", w.Code, stub.n)
		}
	})
}

func TestProxyTicketWithoutProxyServiceIsNotFound(t *testing.T) {
	e := newEnv(t)
	e.db.Repo.ID = "repo-proxy"
	e.db.Repo.RepoType = "proxy"

	link := NewMinter(registryHost, []byte(testSecret)).URL("repo-proxy", DownloadModule, "vpc", "aws", "1.2.3", "vpc-aws-1.2.3.zip")
	e.now = time.Now()

	if w := e.do(http.MethodGet, ticketPathOf(t, link), nil); w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404: %s", w.Code, w.Body)
	}
}

func TestMinterWithoutSecretMintsNothing(t *testing.T) {
	m := NewMinter(registryHost, nil)
	if m.Enabled() || m.URL("r", DownloadModule, "a", "b", "1.0.0", "f.zip") != "" {
		t.Fatal("a minter without a secret minted a URL")
	}
}

func TestMinterURLShape(t *testing.T) {
	m := NewMinter(registryHost+"/", []byte(testSecret))
	link := m.URL("repo-1", DownloadModule, "vpc", "aws", "1.2.3", "vpc-aws-1.2.3.zip")

	if !strings.HasPrefix(link, registryHost+ticketPath) || !strings.HasSuffix(link, "/vpc-aws-1.2.3.zip") {
		t.Fatalf("unexpected ticket URL %q", link)
	}
}
