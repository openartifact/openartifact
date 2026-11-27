// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gitlfs_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/gitlfs"
)

const oid = "4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393"

func TestValidOID(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		oid:                                      true,
		strings.Repeat("0", 64):                  true,
		strings.ToUpper(oid):                     false,
		oid[:63]:                                 false,
		oid + "0":                                false,
		"":                                       false,
		oid[:63] + "g":                           false,
		"../" + oid[3:]:                          false,
		strings.Repeat("a", 32) + "/" + oid[:31]: false,
	}

	for in, want := range tests {
		if got := gitlfs.ValidOID(in); got != want {
			t.Errorf("ValidOID(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestObjectPath(t *testing.T) {
	t.Parallel()

	if got, want := gitlfs.ObjectPath(oid), "objects/4d/7a/"+oid; got != want {
		t.Errorf("ObjectPath = %q, want %q", got, want)
	}
}

func TestParseBatch(t *testing.T) {
	t.Parallel()

	req, err := gitlfs.ParseBatch([]byte(`{"operation":"upload","transfers":["basic"],"ref":{"name":"refs/heads/main"},` +
		`"objects":[{"oid":"` + oid + `","size":12}],"hash_algo":"sha256"}`))
	if err != nil {
		t.Fatal(err)
	}

	if req.Operation != gitlfs.OperationUpload || len(req.Objects) != 1 || req.Objects[0].OID != oid || req.Objects[0].Size != 12 ||
		req.Ref == nil || req.Ref.Name != "refs/heads/main" || req.HashAlgo != gitlfs.HashSHA256 {
		t.Errorf("parsed = %+v", req)
	}

	for _, bad := range []string{``, `[]`, `{"operation":1}`, `{"objects":[{"oid":1}]}`, `not json`} {
		if _, err := gitlfs.ParseBatch([]byte(bad)); !errors.Is(err, gitlfs.ErrInvalidBatch) {
			t.Errorf("ParseBatch(%q) error = %v, want ErrInvalidBatch", bad, err)
		}
	}
}

func TestPeekOperation(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		`{"operation":"download","objects":[]}`:                     "download",
		`{"objects":[],"operation":"upload"}`:                       "upload",
		`{"Operation":"download"}`:                                  "download",
		`{"operation":"download","operation":"upload"}`:             "upload",
		`{"operation":"upload","operation":"download"}`:             "download",
		`{"operation":"download"} trailing`:                         "",
		`{"operation":"download"`:                                   "",
		`{"operation":null}`:                                        "",
		``:                                                          "",
		`{"objects":[{"operation":"download"}]}`:                    "",
		`{"operation":"download","objects":[{"oid":1}],"size":"x"}`: "download",
	}

	for in, want := range tests {
		if got := gitlfs.PeekOperation([]byte(in)); got != want {
			t.Errorf("PeekOperation(%s) = %q, want %q", in, got, want)
		}
	}
}

// FuzzPeekOperation: whatever the body, a download verdict from the peek (which
// the middleware authorizes as a read) is the operation the handler parses.
func FuzzPeekOperation(f *testing.F) {
	f.Add([]byte(`{"operation":"download","objects":[]}`))
	f.Add([]byte(`{"operation":"download","operation":"upload"}`))
	f.Add([]byte(`{"OPERATION":"upload","objects":[{"oid":"x","size":1}]}`))
	f.Add([]byte(`{"operation":"download","objects":[` + strings.Repeat(`{"oid":"a","size":1},`, 3) + `{}]}`))

	f.Fuzz(func(t *testing.T, body []byte) {
		peek := gitlfs.PeekOperation(body)
		if peek != gitlfs.OperationDownload {
			return
		}

		req, err := gitlfs.ParseBatch(body)
		if err != nil {
			// A body that is a document for the peek but not for the handler is
			// refused with 400 there; it never reads or writes anything.
			return
		}

		if req.Operation != peek {
			t.Fatalf("peek %q, parsed %q for %q", peek, req.Operation, body)
		}
	})
}
