// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	testID  = "6f1f4a9e-9d0b-4f55-8f8e-0c2f0a6d3b11"
	testRow = `{"id":"` + testID + `","path":"a/b.txt"}`
)

func rawTable(t *testing.T) *Table {
	t.Helper()

	tbl, ok := Lookup("raw_artifacts")
	if !ok {
		t.Fatal("raw_artifacts is not journaled")
	}

	return tbl
}

func sha(b []byte) string {
	s := sha256.Sum256(b)

	return hex.EncodeToString(s[:])
}

func TestSidecarRoundTrip(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 600, time.UTC)

	data, id, err := EncodeSidecar(rawTable(t), testID, 68, at, []byte(testRow))
	if err != nil {
		t.Fatal(err)
	}

	if id != sha(data) || !LooksLikeSidecar(data) || !strings.HasPrefix(string(data), `{"openartifact_sidecar":1,`) {
		t.Fatalf("encoded sidecar = %s (id %s)", data, id)
	}

	sc, tbl, err := DecodeSidecar(id, data)
	if err != nil {
		t.Fatal(err)
	}

	if tbl.Name != "raw_artifacts" || sc.Key != testID || !sc.WrittenAt.Equal(at) || sc.Schema != 68 || string(sc.Row) != testRow {
		t.Fatalf("decoded = %+v table %s", sc, tbl.Name)
	}
}

func TestDecodeSidecarRejects(t *testing.T) {
	good, goodID, err := EncodeSidecar(rawTable(t), testID, 68, time.Now(), []byte(testRow))
	if err != nil {
		t.Fatal(err)
	}

	mk := func(doc string) ([]byte, string) { return []byte(doc), sha([]byte(doc)) }

	tests := []struct {
		name string
		data []byte
		id   string
		want error
	}{
		{"wrong id", good, strings.Repeat("0", 64), ErrSidecarDigest},
		{"truncated", good[:len(good)-3], sha(good[:len(good)-3]), ErrSidecarFormat},
		{"not a sidecar", []byte(`{"hello":1}`), sha([]byte(`{"hello":1}`)), ErrSidecarFormat},
		{"oversize", make([]byte, MaxSidecarBytes+1), goodID, ErrSidecarTooLarge},
	}

	for name, doc := range map[string]struct {
		doc  string
		want error
	}{
		"future version": {`{"openartifact_sidecar":2,"table":"raw_artifacts","key":"` + testID + `","row":` + testRow + `}`, ErrSidecarVersion},
		"unknown table":  {`{"openartifact_sidecar":1,"table":"users","key":"` + testID + `","row":` + testRow + `}`, ErrSidecarTable},
		"key mismatch":   {`{"openartifact_sidecar":1,"table":"raw_artifacts","key":"` + strings.Replace(testID, "6f", "7f", 1) + `","row":` + testRow + `}`, ErrSidecarKey},
		"bad key shape":  {`{"openartifact_sidecar":1,"table":"raw_artifacts","key":"not-a-uuid","row":{"id":"not-a-uuid"}}`, ErrSidecarKey},
		"row not object": {`{"openartifact_sidecar":1,"table":"raw_artifacts","key":"` + testID + `","row":[1]}`, ErrSidecarRow},
	} {
		d, id := mk(doc.doc)
		tests = append(tests, struct {
			name string
			data []byte
			id   string
			want error
		}{name, d, id, doc.want})
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := DecodeSidecar(tc.id, tc.data); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestSidecarMetadataMarker(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]string
		want bool
	}{
		{"as written", SidecarMetadata("raw_artifacts", "x"), true},
		{"s3 lower-cased", map[string]string{"oakind": "sidecar"}, true},
		{"azure canonicalised", map[string]string{"Oakind": "sidecar"}, true},
		{"other kind", map[string]string{"oakind": "blob"}, false},
		{"none", nil, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSidecarMeta(tc.meta); got != tc.want {
				t.Fatalf("IsSidecarMeta(%v) = %v", tc.meta, got)
			}
		})
	}
}

func TestSplitKey(t *testing.T) {
	composite, ok := Lookup("docker_manifest_refs")
	if !ok {
		t.Fatal("docker_manifest_refs missing")
	}

	tests := []struct {
		name  string
		table *Table
		key   string
		ok    bool
	}{
		{"uuid", rawTable(t), testID, true},
		{"upper-case uuid", rawTable(t), strings.ToUpper(testID), false},
		{"uuid with suffix", rawTable(t), testID + "x", false},
		{"composite", composite, `["` + testID + `","manifest","sha256:abc"]`, true},
		{"composite wrong arity", composite, `["` + testID + `"]`, false},
		{"composite bad uuid", composite, `["zz","manifest","sha256:abc"]`, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := tc.table.SplitKey(tc.key); ok != tc.ok {
				t.Fatalf("SplitKey(%q) ok = %v", tc.key, ok)
			}
		})
	}
}

func TestRegistryShape(t *testing.T) {
	seen := map[string]bool{}

	for _, tbl := range Tables {
		if seen[tbl.Name] {
			t.Errorf("table %s listed twice", tbl.Name)
		}

		seen[tbl.Name] = true

		if _, dup := NotJournaled[tbl.Name]; dup {
			t.Errorf("table %s is both journaled and exempt", tbl.Name)
		}

		if len(tbl.Key) == 0 {
			t.Errorf("table %s has no key", tbl.Name)
		}
	}

	for name, why := range NotJournaled {
		if why == "" {
			t.Errorf("exempt table %s has no reason", name)
		}
	}
}

func TestReportExitCode(t *testing.T) {
	tests := []struct {
		name string
		rep  *Report
		want int
	}{
		{"clean", &Report{}, ExitOK},
		{"unrecoverable only", &Report{UnrecoverableCount: 2, MissingBlobCount: 2}, ExitIncomplete},
		{"failure", &Report{FailureCount: 1}, ExitFailed},
		{"invalid sidecar", &Report{SidecarsInvalid: 1}, ExitFailed},
		{"failure wins", &Report{FailureCount: 1, UnrecoverableCount: 1}, ExitFailed},
		{"unsupported sidecars are not errors", &Report{SidecarsUnsupported: 5}, ExitOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rep.ExitCode(); got != tc.want {
				t.Fatalf("ExitCode = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestIDSet(t *testing.T) {
	a, b, c := strings.Repeat("0a", 32), strings.Repeat("5b", 32), strings.Repeat("ff", 32)

	// Listed out of order: finish sorts.
	s := idSet{sorted: true}
	for _, id := range []string{b, a, c, "not-hex"} {
		s.add(id)
	}

	s.finish()

	if s.len() != 3 || !s.has(a) || !s.has(b) || !s.has(c) || s.has(strings.Repeat("11", 32)) || s.has("zz") {
		t.Fatalf("idSet wrong: len=%d", s.len())
	}
}
