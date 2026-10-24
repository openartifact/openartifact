// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"strings"
	"testing"
)

func cargoFuzzCrate(files ...[2]string) []byte {
	var buf bytes.Buffer

	gw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	tw := tar.NewWriter(gw)

	for _, f := range files {
		_ = tw.WriteHeader(&tar.Header{Name: f[0], Mode: 0o644, Size: int64(len(f[1])), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(f[1]))
	}

	_ = tw.Close()
	_ = gw.Close()

	return buf.Bytes()
}

// cargoFuzzCheckResult asserts what every cargo license result guarantees.
func cargoFuzzCheckResult(t *testing.T, res Result) {
	t.Helper()

	if res.Empty() != (res.Source == "") {
		t.Fatalf("result %+v: Source must be set exactly when licenses were found", res)
	}

	if !res.Empty() && (res.Source != "Cargo.toml" || len(res.Licenses) != 1 || strings.TrimSpace(res.Licenses[0]) == "") {
		t.Fatalf("result %+v is not one Cargo.toml license", res)
	}
}

// FuzzReadCargoCrate reads the license of arbitrary .crate bytes the way a
// publish does: the result is a license of the crate's own Cargo.toml, or one
// of the documented errors; an unsupported file name is the only reason for
// ErrUnsupported.
func FuzzReadCargoCrate(f *testing.F) {
	toml := "[package]\nname = \"demo\"\nversion = \"0.1.0\"\nlicense = \"MIT/Apache-2.0\"\n"

	f.Add(cargoFuzzCrate([2]string{"demo-0.1.0/Cargo.toml", toml}), "demo-0.1.0.crate")
	f.Add(cargoFuzzCrate([2]string{"demo-0.1.0/Cargo.toml", "[package]\nname = \"demo\"\nlicense-file = \"LICENSE\"\n"}), "demo-0.1.0.crate")
	f.Add(cargoFuzzCrate([2]string{"demo-0.1.0/Cargo.toml", toml}, [2]string{"other/Cargo.toml", toml}), "demo-0.1.0.crate")
	f.Add(cargoFuzzCrate([2]string{"demo-0.1.0/Cargo.toml", "[package\n"}), "demo-0.1.0.crate")
	f.Add(cargoFuzzCrate([2]string{"demo-0.1.0/Cargo.toml", "[package]\nlicense = 5\n"}), "demo-0.1.0.crate")
	f.Add(cargoFuzzCrate([2]string{"demo-0.1.0/src/lib.rs", "fn main() {}"}), "demo-0.1.0.crate")
	f.Add(cargoFuzzCrate([2]string{"/Cargo.toml", toml}, [2]string{"../x/Cargo.toml", toml}), "demo-0.1.0.crate")
	f.Add(cargoFuzzCrate([2]string{"demo-0.1.0/Cargo.toml", toml}), "demo-0.1.0.tgz")
	f.Add([]byte("not a crate"), "demo-0.1.0.crate")
	f.Add([]byte{0x1f, 0x8b}, "demo-0.1.0.crate")
	f.Add([]byte{}, "")

	f.Fuzz(func(t *testing.T, data []byte, filename string) {
		res, err := ReadBytes(context.Background(), "cargo", filename, data)

		if !Supported("cargo", filename) {
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("ReadBytes(%q) = %v for an unsupported file name", filename, err)
			}

			return
		}

		if errors.Is(err, ErrUnsupported) {
			t.Fatalf("ReadBytes(%q) = ErrUnsupported but Supported is true", filename)
		}

		if err != nil {
			if !res.Empty() || res.Source != "" {
				t.Fatalf("ReadBytes returned %+v with error %v", res, err)
			}

			return
		}

		cargoFuzzCheckResult(t, res)
	})
}

// FuzzParseCargo reads the license of arbitrary Cargo.toml text: an error or a
// result of the Cargo.toml source, and the legacy "A/B" spelling is turned into
// an expression once, never twice.
func FuzzParseCargo(f *testing.F) {
	f.Add("[package]\nname = \"demo\"\nlicense = \"MIT/Apache-2.0\"\n")
	f.Add("[package]\nlicense = \"MIT OR Apache-2.0\"\n")
	f.Add("[package]\nlicense = \"(MIT/Apache-2.0)\"\n")
	f.Add("[package]\nlicense = \"  \"\n")
	f.Add("[package]\nlicense = [\"MIT\"]\n")
	f.Add("package.license = \"/\"\n")
	f.Add("[package]\nlicense.workspace = true\n")
	f.Add("[package")
	f.Add("")

	f.Fuzz(func(t *testing.T, text string) {
		res, err := parseCargo(text)
		if err != nil {
			if !res.Empty() {
				t.Fatalf("parseCargo returned %+v with error %v", res, err)
			}

			return
		}

		cargoFuzzCheckResult(t, res)

		if !res.Empty() {
			l := res.Licenses[0]
			if again := cargoExpression(l); again != l {
				t.Fatalf("cargoExpression is not idempotent: %q -> %q", l, again)
			}
		}
	})
}
