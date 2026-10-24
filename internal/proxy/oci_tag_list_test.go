// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"encoding/json"
	"testing"
)

// TestRewriteTagList: the upstream's repository name is replaced by the
// client's and the rest of the listing is kept; a listing that is not a JSON
// object (null decodes into a nil map without error) is refused instead of
// panicking on the assignment.
func TestRewriteTagList(t *testing.T) {
	t.Parallel()

	out, err := rewriteTagList([]byte(`{"name":"library/alpine","tags":["3.19","latest"],"extra":1}`), "alpine")
	if err != nil {
		t.Fatalf("rewriteTagList: %v", err)
	}

	var got struct {
		Name  string   `json:"name"`
		Tags  []string `json:"tags"`
		Extra int      `json:"extra"`
	}

	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("result %q: %v", out, err)
	}

	if got.Name != "alpine" || len(got.Tags) != 2 || got.Extra != 1 {
		t.Errorf("rewriteTagList = %q", out)
	}

	for _, body := range []string{"null", " null ", "[]", `"x"`, "5", ""} {
		if out, err := rewriteTagList([]byte(body), "alpine"); err == nil {
			t.Errorf("rewriteTagList(%q) = %q, want an error", body, out)
		}
	}
}
