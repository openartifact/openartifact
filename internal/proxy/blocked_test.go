// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/blockreason"
	"github.com/openartifact/openartifact/internal/curation"
	"github.com/openartifact/openartifact/internal/quarantine"
)

// holdDB quarantines every artifact whose key ends in suffix, for reason.
type holdDB struct {
	*memDB

	suffix string
	reason quarantine.Reason
}

func (h *holdDB) IsArtifactQuarantined(_ context.Context, _ string, keys []string) (bool, error) {
	for _, k := range keys {
		if strings.HasSuffix(k, h.suffix) {
			return true, nil
		}
	}

	return false, nil
}

func (h *holdDB) ArtifactHoldReason(context.Context, string, []string) (quarantine.Reason, error) {
	return h.reason, nil
}

// blockedBody is the BLOCKED_BY_POLICY body.
type blockedBody struct {
	Error   string `json:"error"`
	Code    string `json:"code"`
	Policy  string `json:"policy"`
	Package string `json:"package"`
	Version string `json:"version"`
}

// wantBlocked checks that w is the refusal of policy; pkg and version are
// checked when not empty.
func wantBlocked(t *testing.T, w *httptest.ResponseRecorder, policy blockreason.Policy, pkg, version string) {
	t.Helper()

	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d (%s), want 403", w.Code, w.Body.String())
	}

	if got := w.Header().Get(blockreason.Header); got != string(policy) {
		t.Errorf("%s = %q, want %q", blockreason.Header, got, policy)
	}

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var b blockedBody
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, w.Body.String())
	}

	if b.Code != blockreason.Code || b.Policy != string(policy) || b.Error == "" {
		t.Errorf("body = %+v, want code %s policy %s and a message", b, blockreason.Code, policy)
	}

	if pkg != "" && b.Package != pkg {
		t.Errorf("package = %q, want %q", b.Package, pkg)
	}

	if version != "" && b.Version != version {
		t.Errorf("version = %q, want %q", b.Version, version)
	}
}

// TestBlockReason_NPM: every policy that refuses a tarball names itself.
func TestBlockReason_NPM(t *testing.T) {
	tests := []struct {
		name    string
		policy  blockreason.Policy
		tarball string
		setup   func(f *fixture)
		version string
	}{
		{
			name: "curation", policy: blockreason.Curation, tarball: "lib/-/lib-2.0.0.tgz", version: "2.0.0",
			setup: func(f *fixture) { f.useCuration(curation.ModeDenylist, blockRule("lib", "2.0.0")) },
		},
		{
			name: "min age", policy: blockreason.MinAge, tarball: "lib/-/lib-2.1.0-rc.1.tgz", version: "2.1.0-rc.1",
			setup: func(*fixture) {},
		},
		{
			name: "quarantine", policy: blockreason.Quarantine, tarball: "lib/-/lib-1.0.0.tgz", version: "1.0.0",
			setup: func(f *fixture) {
				f.svc.db = &holdDB{memDB: f.db, suffix: "lib-1.0.0.tgz", reason: quarantine.ReasonScan}
			},
		},
		{
			name: "license", policy: blockreason.License, tarball: "lib/-/lib-1.0.0.tgz", version: "1.0.0",
			setup: func(f *fixture) {
				f.svc.db = &holdDB{memDB: f.db, suffix: "lib-1.0.0.tgz", reason: quarantine.ReasonLicense}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _ := minAgeFixture(t, npmMinAgeGate)
			tt.setup(f)

			wantBlocked(t, f.do(http.MethodGet, tt.tarball), tt.policy, "lib", tt.version)

			// HEAD answers the same status and header.
			w := f.do(http.MethodHead, tt.tarball)
			if w.Code != http.StatusForbidden || w.Header().Get(blockreason.Header) != string(tt.policy) {
				t.Errorf("HEAD: status %d, header %q", w.Code, w.Header().Get(blockreason.Header))
			}
		})
	}
}

// TestBlockReason_PyPI: the same on a PyPI file download.
func TestBlockReason_PyPI(t *testing.T) {
	tests := []struct {
		name   string
		policy blockreason.Policy
		file   string
		setup  func(f *fixture)
		pkg    string
	}{
		{
			name: "curation", policy: blockreason.Curation, file: ageSdist231, pkg: "requests",
			setup: func(f *fixture) { f.useCuration(curation.ModeDenylist, blockRule("requests", "2.31.0")) },
		},
		{name: "min age", policy: blockreason.MinAge, file: ageSdist233, pkg: "requests", setup: func(*fixture) {}},
		{
			name: "quarantine", policy: blockreason.Quarantine, file: wheelName,
			setup: func(f *fixture) {
				f.svc.db = &holdDB{memDB: f.db, suffix: wheelName, reason: quarantine.ReasonScan}
			},
		},
		{
			name: "license", policy: blockreason.License, file: wheelName,
			setup: func(f *fixture) {
				f.svc.db = &holdDB{memDB: f.db, suffix: wheelName, reason: quarantine.ReasonLicense}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := agePyPIFixture(t, 24,
				ageFile{ageSdist231, 100 * hour},
				ageFile{wheelName, 100 * hour},
				ageFile{ageSdist233, hour},
			)
			tt.setup(f)

			wantBlocked(t, f.do(http.MethodGet, "files/requests/"+tt.file), tt.policy, tt.pkg, "")
		})
	}
}

// TestBlockReason_QuarantineWithoutReasonStore: a store that cannot tell a
// license hold from a scan reports a quarantine.
func TestBlockReason_QuarantineWithoutReasonStore(t *testing.T) {
	f, _ := minAgeFixture(t, 0)
	f.svc.db = &quarantineDB{memDB: f.db, blocked: "lib/-/lib-1.0.0.tgz"}

	wantBlocked(t, f.do(http.MethodGet, "lib/-/lib-1.0.0.tgz"), blockreason.Quarantine, "lib", "1.0.0")
}

// TestBlockReason_Docker: the registry API keeps its DENIED error and gains the
// header; the policy is in the detail.
func TestBlockReason_Docker(t *testing.T) {
	const manifest = "hub/library/alpine/manifests/3.19"

	tests := []struct {
		name   string
		policy blockreason.Policy
		setup  func(f *ociFixture)
	}{
		{
			name: "curation", policy: blockreason.Curation,
			setup: func(f *ociFixture) {
				f.svc.WithCuration(&dlRules{rules: []curation.Rule{dlRule(curation.ActionBlock, "library/alpine", "3.19")}})
				f.repo.CurationMode = curation.ModeDenylist
			},
		},
		{
			name: "quarantine", policy: blockreason.Quarantine,
			setup: func(f *ociFixture) {
				f.svc.db = &holdDB{memDB: f.db, suffix: "/manifests/3.19", reason: quarantine.ReasonScan}
			},
		},
		{
			name: "license", policy: blockreason.License,
			setup: func(f *ociFixture) {
				f.svc.db = &holdDB{memDB: f.db, suffix: "/manifests/3.19", reason: quarantine.ReasonLicense}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newOCIFixture(t)
			f.reg.addManifest("library/alpine", "3.19", alpineManifest)
			tt.setup(f)

			w := f.do(http.MethodGet, manifest)
			if w.Code != http.StatusForbidden || ociErrorCode(t, w) != "DENIED" {
				t.Fatalf("status %d %q, want 403 DENIED", w.Code, w.Body.String())
			}

			if got := w.Header().Get(blockreason.Header); got != string(tt.policy) {
				t.Errorf("%s = %q, want %q", blockreason.Header, got, tt.policy)
			}

			var body struct {
				Errors []struct {
					Detail map[string]string `json:"detail"`
				} `json:"errors"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}

			want := map[string]string{"code": blockreason.Code, "policy": string(tt.policy), "package": "library/alpine", "version": "3.19"}
			for k, v := range want {
				if got := body.Errors[0].Detail[k]; got != v {
					t.Errorf("detail[%s] = %q, want %q", k, got, v)
				}
			}

			if w := f.do(http.MethodHead, manifest); w.Header().Get(blockreason.Header) != string(tt.policy) {
				t.Errorf("HEAD: header %q", w.Header().Get(blockreason.Header))
			}
		})
	}
}
