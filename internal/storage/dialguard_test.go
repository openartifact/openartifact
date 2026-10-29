// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/netutil"
)

func TestValidAzureAccountName(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		want bool
	}{
		{"acct", true},
		{"abc", true},
		{"devstoreaccount1", true},
		{"abcdefghijklmnopqrstuvwx", true}, // 24
		{"ab", false},
		{"abcdefghijklmnopqrstuvwxy", false}, // 25
		{"", false},
		{"Acct", false},
		{"my-account", false},
		{"my_account", false},
		{"a.b.c", false},
		{"169.254.169.254/x", false},
		{"evil.example.com#", false},
		{"acct\n", false},
	} {
		if got := ValidAzureAccountName(tc.name); got != tc.want {
			t.Errorf("ValidAzureAccountName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNewAzureBlobStoreRejectsBadAccountName(t *testing.T) {
	t.Parallel()

	for _, account := range []string{"169.254.169.254/x", "a", "Acct", "acct.evil.example", "acct#"} {
		_, err := NewAzureBlobStore(StorageConfig{AzureAccountName: account, AzureAccountKey: fakeAzureKey, AzureContainerName: "cont"})
		if !errors.Is(err, ErrAzureAccountNameInvalid) {
			t.Errorf("account %q: err = %v, want ErrAzureAccountNameInvalid", account, err)
		}
	}
}

// TestSafeDialRefusesPrivateEndpoints: a backend marked SafeDial never connects
// to a loopback endpoint (the policy of the admin save check, enforced at dial
// time so DNS rebinding cannot get around it) unless private upstreams are
// allowed; an operator-config backend dials as before.
func TestSafeDialRefusesPrivateEndpoints(t *testing.T) {
	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	backends := map[string]func(StorageConfig) (interface{ HealthCheck(context.Context) error }, error){
		"azure": func(c StorageConfig) (interface{ HealthCheck(context.Context) error }, error) {
			c.AzureAccountName, c.AzureAccountKey, c.AzureContainerName, c.AzureEndpoint = "acct", fakeAzureKey, "cont", srv.URL+"/acct"

			return NewAzureBlobStore(c)
		},
		"oci": func(c StorageConfig) (interface{ HealthCheck(context.Context) error }, error) {
			c.Endpoint, c.Bucket, c.OCIInsecure = srv.URL, "repo", true

			return NewOCIBlobStore(c)
		},
		"s3": func(c StorageConfig) (interface{ HealthCheck(context.Context) error }, error) {
			c.Endpoint, c.Bucket, c.Region, c.ForcePathStyle = srv.URL, "bucket", "us-east-1", true
			c.AccessKey, c.SecretKey = "k", "s"

			return NewS3BlobStore(c)
		},
	}

	cases := []struct {
		name         string
		safe         bool
		allowPrivate bool
		wantBlocked  bool
	}{
		{"guarded", true, false, true},
		{"guarded with private upstreams allowed", true, true, false},
		{"operator config", false, false, false},
	}

	for kind, build := range backends {
		for _, tc := range cases {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				t.Setenv(netutil.EnvAllowPrivate, map[bool]string{true: "true", false: "false"}[tc.allowPrivate])
				hits.Store(0)

				store, err := build(StorageConfig{SafeDial: tc.safe})
				if err != nil {
					t.Fatal(err)
				}

				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()

				err = store.HealthCheck(ctx)

				switch {
				case tc.wantBlocked:
					if err == nil || !strings.Contains(err.Error(), netutil.ErrBlockedAddress.Error()) {
						t.Fatalf("err = %v, want %q", err, netutil.ErrBlockedAddress)
					}

					if n := hits.Load(); n != 0 {
						t.Fatalf("the loopback server was contacted %d times", n)
					}
				case hits.Load() == 0:
					t.Fatalf("the endpoint was never contacted (err = %v)", err)
				}
			})
		}
	}
}
