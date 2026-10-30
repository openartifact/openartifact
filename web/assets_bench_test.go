// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

// benchDist is a UI-sized dist: 90 compressible chunks of ~14 KB (1.3 MB plain),
// either plain (a build without scripts/precompress.js) or gzipped by the build.
func benchDist(precompressed bool) fs.FS {
	fsys := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><html><body>" + strings.Repeat("<div>x</div>", 100) + "</body></html>")}}

	for i := range 90 {
		body := []byte(strings.Repeat(fmt.Sprintf("export const v%d = () => 'OpenArtifact %d';\n", i, i), 300))
		name := fmt.Sprintf("assets/chunk%d-AbCdEfGh%d.js", i, i)

		if !precompressed {
			fsys[name] = &fstest.MapFile{Data: body}

			continue
		}

		var buf bytes.Buffer

		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(body)
		_ = zw.Close()

		fsys[name+".gz"] = &fstest.MapFile{Data: buf.Bytes()}
	}

	return fsys
}

// BenchmarkNewSPAHandler is the startup cost of the embedded UI.
func BenchmarkNewSPAHandler(b *testing.B) {
	for _, pre := range []bool{false, true} {
		fsys := benchDist(pre)

		b.Run(fmt.Sprintf("precompressed=%v", pre), func(b *testing.B) {
			b.ReportAllocs()

			for range b.N {
				_ = newSPAHandler(fsys, nil)
			}
		})
	}
}
