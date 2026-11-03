// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The contract tests run every command against a recording fake server that
// matches requests with the real chi router (realRouter). This test, which sorts
// last in the package and so runs after them, checks that every (method, path)
// they saw is in the published OpenAPI document: a CLI that calls an endpoint
// the API reference does not describe has drifted from the API.

var (
	apiCallsMu sync.Mutex          //nolint:gochecknoglobals // test recorder shared by the contract servers
	apiCalls   = map[string]bool{} //nolint:gochecknoglobals // see apiCallsMu
)

// noteAPICall records a request the CLI made, as "METHOD /route/{pattern}".
func noteAPICall(method, pattern string) {
	apiCallsMu.Lock()
	defer apiCallsMu.Unlock()

	apiCalls[method+" "+pattern] = true
}

// minRecordedCalls is far below what the full contract suite records (well over
// a hundred routes); a run narrowed with -run records few and is skipped.
const minRecordedCalls = 60

// shape turns a route into its parameter-free form: every {param} and the
// trailing wildcard become {}, so {name} and {id} spellings do not matter.
func shape(path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") || s == "*" {
			segs[i] = "{}"
		}
	}

	return strings.Join(segs, "/")
}

func TestCLICallsAreInTheOpenAPIDocument(t *testing.T) {
	apiCallsMu.Lock()
	calls := make([]string, 0, len(apiCalls))
	for c := range apiCalls {
		calls = append(calls, c)
	}
	apiCallsMu.Unlock()

	if len(calls) < minRecordedCalls {
		t.Skipf("only %d CLI routes recorded: run the whole package so the contract tests record them", len(calls))
	}

	raw, err := os.ReadFile("../../internal/api/openapi.json")
	if err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}

	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}

	documented := map[string]bool{}

	for path, ops := range doc.Paths {
		for method := range ops {
			documented[strings.ToUpper(method)+" "+shape(path)] = true
		}
	}

	slices.Sort(calls)

	for _, c := range calls {
		method, path, _ := strings.Cut(c, " ")

		// The path-addressed upload endpoints (/repos/FORMAT/NAME/...) are the
		// format protocols, not part of the REST API document.
		if strings.HasPrefix(path, "/repos/") {
			continue
		}

		if !documented[method+" "+shape(path)] {
			t.Errorf("the CLI calls %s, which is not in internal/api/openapi.json", c)
		}
	}
}
