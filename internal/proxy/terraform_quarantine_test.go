// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/format/terraform"
)

// TestTerraformTicketRedemptionRechecksQuarantine: a ticket the proxy minted
// before a quarantine stops serving at redemption (the keys are the ones the
// mint checked), the checksum file and signature follow their platform's zip,
// and a download of another artifact is still served.
func TestTerraformTicketRedemptionRechecksQuarantine(t *testing.T) {
	f := newTFFake(t)

	modLink := f.get("demo/aws/1.0.0/download")
	if modLink.Code != http.StatusNoContent {
		t.Fatalf("mint module: %d", modLink.Code)
	}

	moduleFile := ticketFile(t, modLink.Header().Get("X-Terraform-Get"))

	if f.get("random/3.5.1/download/linux/amd64").Code != http.StatusOK {
		t.Fatal("package document failed")
	}

	module := tfModuleRedeem(moduleFile)
	zip := tfProviderDownload(terraform.DownloadProviderZip, tfZipFile)
	sums := tfProviderDownload(terraform.DownloadProviderSums, tfSumsFile)
	sig := tfProviderDownload(terraform.DownloadProviderSumsSig, tfSigFile)

	for _, d := range []terraform.Download{module, zip, sums, sig} {
		if c := f.redeem(d).Code; c != http.StatusOK {
			t.Fatalf("redeem %s before the quarantine = %d, want 200", d.File, c)
		}
	}

	blocked := func(key string, refused, served []terraform.Download) {
		t.Helper()

		f.fixture.svc.db = &quarantineDB{memDB: f.fixture.db, blocked: key}

		for _, d := range refused {
			if c := f.redeem(d).Code; c != http.StatusForbidden {
				t.Errorf("quarantined %q: redeem %s = %d, want 403", key, d.File, c)
			}
		}

		for _, d := range served {
			if c := f.redeem(d).Code; c != http.StatusOK {
				t.Errorf("quarantined %q: redeem %s = %d, want 200", key, d.File, c)
			}
		}
	}

	blocked(tfModuleArchiveKey("demo", "aws", "1.0.0", moduleFile), []terraform.Download{module}, []terraform.Download{zip, sums, sig})
	zipKey := tfZipKey("random", "3.5.1", "linux_amd64", tfZipFile)
	blocked(zipKey, []terraform.Download{zip, sums, sig}, []terraform.Download{module})

	// A scan of the zip is recorded under its cache entry's row id.
	entry, err := f.fixture.db.GetProxyCacheEntry(context.Background(), f.fixture.repo.ID, zipKey)
	if err != nil || entry == nil {
		t.Fatalf("cache entry of the zip: %v, %v", entry, err)
	}

	blocked(entry.ID, []terraform.Download{zip, sums, sig}, []terraform.Download{module})
}
