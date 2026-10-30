// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"net/http"
	"testing"
)

// TestTicketRedemptionRechecksQuarantine: a download link minted before a
// quarantine stops working at once instead of at its expiry, for the module
// archive, the provider zip and the checksum file and signature of the
// quarantined platform, while the links of a sibling version and of another
// platform of the same version keep serving.
func TestTicketRedemptionRechecksQuarantine(t *testing.T) {
	e := newEnv(t)
	e.h.WithSigner(signerResolver{newSigner(t)})

	for _, v := range []string{"1.0.0", "1.1.0"} {
		if w := e.putModule("vpc", "aws", v, moduleZip(t)); w.Code != http.StatusCreated {
			t.Fatalf("upload module %s: %d", v, w.Code)
		}

		for _, p := range [][2]string{{"linux", "amd64"}, {"darwin", "arm64"}} {
			if w := e.putProvider("aws", v, p[0], p[1], providerZip(t, "aws", v), ""); w.Code != http.StatusCreated {
				t.Fatalf("upload provider %s %s/%s: %d", v, p[0], p[1], w.Code)
			}
		}
	}

	moduleLink := func(v string) string {
		w := e.do(http.MethodGet, "/repos/myrepo/vpc/aws/"+v+"/download", nil)
		if w.Code != http.StatusNoContent {
			t.Fatalf("mint module %s: %d", v, w.Code)
		}

		return ticketPathOf(t, w.Header().Get("X-Terraform-Get"))
	}

	providerLinks := func(v, osName, arch string) [3]string {
		w := e.do(http.MethodGet, "/repos/myrepo/aws/"+v+"/download/"+osName+"/"+arch, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("mint provider %s %s/%s: %d", v, osName, arch, w.Code)
		}

		return [3]string{
			ticketPathOf(t, jsonField(t, w, "download_url")),
			ticketPathOf(t, jsonField(t, w, "shasums_url")),
			ticketPathOf(t, jsonField(t, w, "shasums_signature_url")),
		}
	}

	redeem := func(path string) int { return e.do(http.MethodGet, path, nil).Code }

	blockedModule := moduleLink("1.0.0")
	siblingModule := moduleLink("1.1.0")
	blocked := providerLinks("1.0.0", "linux", "amd64")
	otherPlatform := providerLinks("1.0.0", "darwin", "arm64")
	siblingVersion := providerLinks("1.1.0", "linux", "amd64")

	all := append([]string{blockedModule, siblingModule}, append(append(blocked[:], otherPlatform[:]...), siblingVersion[:]...)...)
	for _, path := range all {
		if c := redeem(path); c != http.StatusOK {
			t.Fatalf("GET %s before the quarantine = %d, want 200", path, c)
		}
	}

	var linux100 string

	for _, p := range e.db.provs {
		if p.Version == "1.0.0" && p.OS == "linux" {
			linux100 = p.ID
		}
	}

	e.db.Keys[e.db.mods[0].ID] = true
	e.db.Keys[linux100] = true

	for name, path := range map[string]string{
		"module archive": blockedModule, "provider zip": blocked[0], "provider checksums": blocked[1], "provider signature": blocked[2],
	} {
		if c := redeem(path); c != http.StatusForbidden {
			t.Errorf("%s of a quarantined version = %d, want 403", name, c)
		}
	}

	for name, path := range map[string]string{
		"sibling module version": siblingModule, "other platform zip": otherPlatform[0],
		"other platform checksums": otherPlatform[1], "other platform signature": otherPlatform[2],
		"sibling provider version": siblingVersion[0],
	} {
		if c := redeem(path); c != http.StatusOK {
			t.Errorf("%s = %d, want 200", name, c)
		}
	}
}
