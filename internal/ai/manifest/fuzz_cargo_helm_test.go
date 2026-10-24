// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package manifest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

const cargoHelmFuzzLimit = 4096

type cargoHelmFuzzMember struct{ name, body string }

// cargoHelmFuzzTarball builds a gzip+tar archive of regular files.
func cargoHelmFuzzTarball(members ...cargoHelmFuzzMember) []byte {
	var buf bytes.Buffer

	gw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	tw := tar.NewWriter(gw)

	for _, m := range members {
		_ = tw.WriteHeader(&tar.Header{Name: m.name, Mode: 0o644, Size: int64(len(m.body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(m.body))
	}

	_ = tw.Close()
	_ = gw.Close()

	return buf.Bytes()
}

// FuzzExtractCargoHelm reads the manifests of a .crate or a chart .tgz from
// arbitrary archive bytes: the result is clean text under a known label within
// the member limit, a lenient read never fails as ambiguous, and a strict one
// never silently picks between two manifests.
func FuzzExtractCargoHelm(f *testing.F) {
	crate := cargoHelmFuzzTarball(cargoHelmFuzzMember{"demo-0.1.0/Cargo.toml", "[package]\nname = \"demo\"\nlicense = \"MIT\"\n"})
	locked := cargoHelmFuzzTarball(
		cargoHelmFuzzMember{"demo-0.1.0/Cargo.toml", "[package]\nname = \"demo\"\n"},
		cargoHelmFuzzMember{"demo-0.1.0/Cargo.lock", "version = 3\n"})
	twice := cargoHelmFuzzTarball(
		cargoHelmFuzzMember{"demo-0.1.0/Cargo.toml", "[package]\nname = \"a\"\n"},
		cargoHelmFuzzMember{"./demo-0.1.0/Cargo.toml", "[package]\nname = \"b\"\n"})
	chart := cargoHelmFuzzTarball(
		cargoHelmFuzzMember{"demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 1.0.0\n"},
		cargoHelmFuzzMember{"demo/requirements.yaml", "dependencies: []\n"},
		cargoHelmFuzzMember{"demo/Chart.lock", "digest: x\n"},
		cargoHelmFuzzMember{"demo/charts/sub/Chart.yaml", "name: sub\n"})

	f.Add(crate, "demo-0.1.0.crate", false, true, false)
	f.Add(locked, "demo-0.1.0.crate", true, true, false)
	f.Add(twice, "demo-0.1.0.crate", false, true, false)
	f.Add(twice, "demo-0.1.0.crate", false, false, false)
	f.Add(chart, "demo-1.0.0.tgz", true, true, true)
	f.Add(chart, "demo-1.0.0.tar.gz", false, false, true)
	f.Add(cargoHelmFuzzTarball(cargoHelmFuzzMember{"/abs/Cargo.toml", "x"}, cargoHelmFuzzMember{"../Cargo.toml", "x"}), "demo.crate", false, true, false)
	f.Add(cargoHelmFuzzTarball(cargoHelmFuzzMember{"demo/Cargo.toml", "name = \"\x00\xff\"\n" + strings.Repeat("x", cargoHelmFuzzLimit)}), "demo.crate", false, true, false)
	f.Add([]byte{0x1f, 0x8b}, "demo.crate", false, false, false)
	f.Add([]byte{}, "", false, false, true)

	f.Fuzz(func(t *testing.T, data []byte, filename string, deps, strict, helm bool) {
		src := Source{
			Format:      "cargo",
			Filename:    filename,
			Open:        func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
			MemberLimit: cargoHelmFuzzLimit,
			Strict:      strict,
			Deps:        deps,
		}

		labels := []string{"Cargo.toml", "Cargo.lock"}

		if helm {
			src.Format = "helm"
			labels = []string{"Chart.yaml", "requirements.yaml", "Chart.lock"}
		}

		if !deps {
			labels = slices.DeleteFunc(labels, func(l string) bool { return l == "Cargo.lock" || l == "Chart.lock" })
		}

		parts, err := ExtractParts(context.Background(), src)
		if err != nil {
			cargoHelmFuzzCheckError(t, src, err)

			return
		}

		if len(parts) == 0 {
			t.Fatal("ExtractParts succeeded without a part")
		}

		for _, p := range parts {
			if !slices.Contains(labels, p.Label) {
				t.Fatalf("unexpected %s part label %q", src.Format, p.Label)
			}

			if !utf8.ValidString(p.Body) || strings.Contains(p.Body, "\x00") || strings.TrimSpace(p.Body) == "" {
				t.Fatalf("part %q is not clean non-empty text: %q", p.Label, p.Body)
			}

			// The member limit, with every invalid byte possibly widened to U+FFFD.
			if len(p.Body) > 3*cargoHelmFuzzLimit {
				t.Fatalf("part %q is %d bytes, over the member limit", p.Label, len(p.Body))
			}
		}

		text, err := Extract(context.Background(), src)
		if err != nil {
			t.Fatalf("ExtractParts succeeded but Extract failed: %v", err)
		}

		if len(text) > MaxBytes || !utf8.ValidString(text) || strings.Contains(text, "\x00") {
			t.Fatalf("Extract returned %d bytes of unclean text", len(text))
		}
	})
}

func cargoHelmFuzzCheckError(t *testing.T, src Source, err error) {
	t.Helper()

	switch {
	case errors.Is(err, ErrUnsupported):
		if Supported(src.Format, src.Filename) {
			t.Fatalf("ExtractParts(%q) = ErrUnsupported but Supported is true", src.Filename)
		}
	case errors.Is(err, ErrAmbiguous):
		if !src.Strict {
			t.Fatal("a lenient read must not fail as ambiguous")
		}

		// Only a cargo manifest is unique: chart manifests are never ambiguous.
		if src.Format != "cargo" {
			t.Fatalf("a %s archive cannot be ambiguous", src.Format)
		}
	case errors.Is(err, ErrTooLarge), errors.Is(err, ErrNotFound):
	default:
		if !Supported(src.Format, src.Filename) {
			t.Fatalf("ExtractParts(%q) = %v for an unsupported file name", src.Filename, err)
		}
	}
}
