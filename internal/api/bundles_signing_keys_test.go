// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/gpgkeys"
	"github.com/openartifact/openartifact/pkg/gpg"
)

const signingKeysPath = "/api/v1/bundles/signing-keys"

// listedSigningKeys decodes the ids of the signing-keys answer.
func listedSigningKeys(t *testing.T, env *signerEnv, who authzCaller) []string {
	t.Helper()

	rw := who.do(t, env.h, http.MethodGet, signingKeysPath, "")
	expectStatus(t, "signing keys of "+who.name, rw, http.StatusOK)

	var out struct {
		Keys []map[string]any `json:"keys"`
	}

	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rw.Body.String(), err)
	}

	if out.Keys == nil {
		t.Fatalf("keys must be an empty array, not null: %s", rw.Body.String())
	}

	ids := make([]string, 0, len(out.Keys))

	for _, k := range out.Keys {
		ids = append(ids, k["id"].(string))

		for field := range k {
			if strings.Contains(field, "private") || field == "repositories" || field == "created_by" {
				t.Errorf("signing key exposes %q", field)
			}
		}
	}

	return ids
}

func TestBundleSigningKeysListsExactlyThePermittedKeys(t *testing.T) {
	tests := []struct {
		name    string
		signers []string
		admin   bool
		// want lists "first" (env.key), "second" or nothing.
		want []string
	}{
		{"plain user sees no key", nil, false, nil},
		{"grant of another user", []string{"KEYID=somebody"}, false, nil},
		{"grant for a key that does not exist", []string{"00000000-0000-4000-8000-00000000ffff=ci"}, false, nil},
		{"granted by key id", []string{"KEYID=ci"}, false, []string{"first"}},
		{"granted by fingerprint", []string{"KEYFPR=ci"}, false, []string{"first"}},
		{"granted both keys", []string{"KEYID=ci", "KEY2ID=ci"}, false, []string{"first", "second"}},
		{"administrator sees every stored key but not the config key", nil, true, []string{"first", "second"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newSignerEnv(t)

			second, err := env.h.keys.(*gpgkeys.Service).Generate(context.Background(),
				gpgkeys.GenerateInput{Name: "second", Algorithm: gpg.AlgorithmED25519})
			if err != nil {
				t.Fatal(err)
			}

			grants := strings.NewReplacer("KEY2ID", second.ID).Replace(strings.Join(tc.signers, "\n"))
			env.h.WithBundleSigners(env.expand(strings.Split(grants, "\n")))

			who := env.ci
			if tc.admin {
				who = authzAdmin()
			}

			got := listedSigningKeys(t, env, who)

			byName := map[string]string{"first": env.key.ID, "second": second.ID}
			want := make([]string, 0, len(tc.want))

			for _, n := range tc.want {
				want = append(want, byName[n])
			}

			slices.Sort(got)
			slices.Sort(want)

			if !slices.Equal(got, want) {
				t.Fatalf("listed %v, want %v", got, want)
			}

			// What is listed is exactly what a seal accepts.
			for _, id := range got {
				expectStatus(t, "seal with listed key "+id, env.seal(t, who, id), http.StatusOK)
			}

			if !tc.admin {
				for _, id := range []string{env.key.ID, second.ID} {
					if !slices.Contains(got, id) {
						rw := env.seal(t, who, id)
						expectStatus(t, "seal with unlisted key "+id, rw, http.StatusForbidden)

						if !strings.Contains(rw.Body.String(), bundleSignerForbiddenCode) {
							t.Errorf("403 lacks code %q: %s", bundleSignerForbiddenCode, rw.Body.String())
						}
					}
				}
			}
		})
	}
}

func TestBundleSigningKeysNeedsAnAccount(t *testing.T) {
	env := newSignerEnv(t, "KEYID=ci")
	anon := authzCaller{name: "anon", p: &auth.Principal{IsAnonymous: true}}

	expectStatus(t, "anonymous", anon.do(t, env.h, http.MethodGet, signingKeysPath, ""), http.StatusUnauthorized)
}

func TestBundleSigningKeysWithoutKeyService(t *testing.T) {
	env := newSignerEnv(t)
	env.h.keys = nil

	if got := listedSigningKeys(t, env, authzAdmin()); len(got) != 0 {
		t.Fatalf("listed %v without a key service", got)
	}
}

func TestBundleConfigKeyIsRefusedWithTheSignerCode(t *testing.T) {
	env := newSignerEnv(t, "KEYID=ci")
	rw := env.seal(t, authzAdmin(), "config")

	expectStatus(t, "config key", rw, http.StatusForbidden)

	if !strings.Contains(rw.Body.String(), bundleSignerForbiddenCode) {
		t.Errorf("403 lacks code %q: %s", bundleSignerForbiddenCode, rw.Body.String())
	}
}
