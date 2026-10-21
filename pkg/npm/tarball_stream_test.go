// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"reflect"
	"testing"
	"testing/iotest"
)

// parseTarballBuffered is the previous buffer-everything implementation,
// kept as the behavioural reference for the streaming ParseTarball.
func parseTarballBuffered(r io.Reader) (map[string]any, string, string, error) {
	raw, err := readAllLimited(r, maxTarballSize, ErrTarballTooLarge)
	if err != nil {
		return nil, "", "", err
	}

	h512 := sha512.Sum512(raw)
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(h512[:])
	h1 := sha1.Sum(raw)
	shasum := hex.EncodeToString(h1[:])

	m, err := extractPackageJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, "", "", err
	}

	return m, integrity, shasum, nil
}

type tarEntry struct {
	name string
	data []byte
}

func tarballOf(t *testing.T, entries []tarEntry) []byte {
	t.Helper()

	var buf bytes.Buffer

	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.data))}); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func noisePayload(n int) []byte {
	p := make([]byte, n)
	rng := rand.New(rand.NewPCG(7, 9))

	for i := range p {
		p[i] = byte(rng.Uint32())
	}

	return p
}

func TestParseTarball_StreamingEquivalentToBuffered(t *testing.T) {
	pj := []byte(`{"name":"x","version":"1.0.0","dependencies":{"a":"^1"}}`)
	noise := noisePayload(300 << 10)
	valid := tarballOf(t, []tarEntry{{"package/package.json", pj}, {"package/big.bin", noise}})

	cases := map[string][]byte{
		"package.json first":   valid,
		"package.json last":    tarballOf(t, []tarEntry{{"package/big.bin", noise}, {"package/package.json", pj}}),
		"missing package.json": tarballOf(t, []tarEntry{{"package/index.js", []byte("x")}}),
		"wrong prefix":         tarballOf(t, []tarEntry{{"foo/package.json", pj}}),
		"invalid json":         tarballOf(t, []tarEntry{{"package/package.json", []byte("{nope")}}),
		"oversized json":       tarballOf(t, []tarEntry{{"package/package.json", bytes.Repeat([]byte(" "), maxPackageJSONSize+1)}}),
		"trailing garbage":     append(append([]byte{}, valid...), noise[:4096]...),
		"not gzip":             []byte("this is not a tarball"),
		"empty":                {},
		"truncated":            valid[:len(valid)/2],
	}

	readers := map[string]func([]byte) io.Reader{
		"bytes":    func(b []byte) io.Reader { return bytes.NewReader(b) },
		"one byte": func(b []byte) io.Reader { return iotest.OneByteReader(bytes.NewReader(b)) },
		"data+eof": func(b []byte) io.Reader { return iotest.DataErrReader(bytes.NewReader(b)) },
	}

	for name, data := range cases {
		for rname, mk := range readers {
			t.Run(name+"/"+rname, func(t *testing.T) {
				wantM, wantI, wantS, wantErr := parseTarballBuffered(bytes.NewReader(data))
				gotM, gotI, gotS, gotErr := ParseTarball(mk(data))

				if (wantErr == nil) != (gotErr == nil) {
					t.Fatalf("error mismatch: buffered=%v streaming=%v", wantErr, gotErr)
				}

				if wantErr != nil {
					if wantErr.Error() != gotErr.Error() {
						t.Fatalf("error text: buffered=%q streaming=%q", wantErr, gotErr)
					}

					return
				}

				if !reflect.DeepEqual(wantM, gotM) || wantI != gotI || wantS != gotS {
					t.Fatalf("result mismatch: buffered=(%v,%s,%s) streaming=(%v,%s,%s)", wantM, wantI, wantS, gotM, gotI, gotS)
				}
			})
		}
	}
}

func TestParseTarball_ReadErrorSurfaces(t *testing.T) {
	data := tarballOf(t, []tarEntry{{"package/package.json", []byte(`{"name":"x"}`)}, {"package/b", noisePayload(64 << 10)}})
	boom := errors.New("boom")

	r := io.MultiReader(bytes.NewReader(data), iotest.ErrReader(boom))

	if _, _, _, err := ParseTarball(r); !errors.Is(err, boom) {
		t.Fatalf("want read error to surface, got %v", err)
	}
}

func TestLimitedReader(t *testing.T) {
	tests := []struct {
		name     string
		size     int
		limit    int64
		exceeded bool
	}{
		{"under", 10, 16, false},
		{"exact", 16, 16, false},
		{"one over", 17, 16, true},
		{"way over", 1000, 16, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lr := &limitedReader{r: bytes.NewReader(make([]byte, tc.size)), n: tc.limit}

			n, err := io.Copy(io.Discard, lr)

			if tc.exceeded {
				if !errors.Is(err, ErrTarballTooLarge) || !lr.exceeded {
					t.Fatalf("want ErrTarballTooLarge, got %v", err)
				}

				if n != tc.limit {
					t.Fatalf("delivered %d bytes, want exactly the limit %d", n, tc.limit)
				}

				return
			}

			if err != nil || lr.exceeded || n != int64(tc.size) {
				t.Fatalf("got n=%d err=%v exceeded=%v", n, err, lr.exceeded)
			}
		})
	}
}
