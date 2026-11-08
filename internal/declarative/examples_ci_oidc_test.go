// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import "testing"

// The shipped CI OIDC example is documentation users copy: it must stay valid.
func TestCIOIDCExampleLoads(t *testing.T) {
	objs, err := Load(DefaultRegistry(), LoadOptions{Paths: []string{"../../examples/declarative/ci-oidc.yaml"}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	policies := map[string]bool{}

	for _, o := range objs {
		if o.Kind == "OIDCTrustPolicy" {
			policies[o.Name] = true
		}
	}

	for _, want := range []string{"github-release", "tekton-team-a", "tekton-edge-cluster"} {
		if !policies[want] {
			t.Errorf("policy %q is missing from the example (have %v)", want, policies)
		}
	}
}
