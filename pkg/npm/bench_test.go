// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"math/rand/v2"
	"testing"
)

const benchPackageJSON = `{
  "name": "@scope/example",
  "version": "1.2.3",
  "description": "An example package",
  "main": "index.js",
  "scripts": {"test": "jest", "build": "tsc"},
  "dependencies": {"lodash": "^4.17.21", "express": "^4.18.2", "zod": "^3.22.0"},
  "devDependencies": {"typescript": "^5.3.0", "jest": "^29.7.0"},
  "engines": {"node": ">=18"},
  "license": "MIT"
}`

// benchNPMTarball builds an npm tarball with package.json first and size
// bytes of incompressible payload after it.
func benchNPMTarball(b *testing.B, size int) []byte {
	b.Helper()

	payload := make([]byte, size)
	rng := rand.New(rand.NewPCG(1, 2))

	for i := range payload {
		payload[i] = byte(rng.Uint32())
	}

	var buf bytes.Buffer

	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for _, f := range []struct {
		name string
		data []byte
	}{
		{"package/package.json", []byte(benchPackageJSON)},
		{"package/dist/index.js", payload},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.data))}); err != nil {
			b.Fatal(err)
		}

		if _, err := tw.Write(f.data); err != nil {
			b.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		b.Fatal(err)
	}

	if err := gw.Close(); err != nil {
		b.Fatal(err)
	}

	return buf.Bytes()
}

func BenchmarkParseTarball(b *testing.B) {
	for _, size := range []int{4 << 10, 1 << 20, 16 << 20} {
		data := benchNPMTarball(b, size)

		b.Run(fmt.Sprintf("payload=%dKiB", size>>10), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))

			for b.Loop() {
				if _, _, _, err := ParseTarball(bytes.NewReader(data)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkParseTarballBuffered runs the previous buffer-everything
// implementation (test reference) for comparison with BenchmarkParseTarball.
func BenchmarkParseTarballBuffered(b *testing.B) {
	for _, size := range []int{4 << 10, 1 << 20, 16 << 20} {
		data := benchNPMTarball(b, size)

		b.Run(fmt.Sprintf("payload=%dKiB", size>>10), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))

			for b.Loop() {
				if _, _, _, err := parseTarballBuffered(bytes.NewReader(data)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkComputeIntegrity(b *testing.B) {
	for _, size := range []int{4 << 10, 1 << 20, 16 << 20} {
		data := benchNPMTarball(b, size)

		b.Run(fmt.Sprintf("payload=%dKiB", size>>10), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))

			for b.Loop() {
				if _, _, _, _, err := ComputeIntegrity(bytes.NewReader(data)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkValidateSemver(b *testing.B) {
	b.ReportAllocs()

	for b.Loop() {
		if err := ValidateSemver("1.2.3-beta.4+build.5"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkValidatePackageName(b *testing.B) {
	for _, name := range []string{"lodash", "@scope/example-package"} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				if err := ValidatePackageName(name); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
