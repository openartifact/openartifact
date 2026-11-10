// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"

	"github.com/M0Rf30/pkgformat/rpm/pack"
	"github.com/M0Rf30/pkgformat/rpm/tag"
)

func buildMinimalRPM() []byte {
	return buildRPM("rpmtest", "")
}

// buildMinimalRPMWithLicense is buildMinimalRPM with the License tag set to
// license; an empty license leaves it empty.
func buildMinimalRPMWithLicense(license string) []byte {
	return buildRPM("rpmtest", license)
}

// buildRPM writes a valid rpm package without files.
func buildRPM(name, license string) []byte {
	return writeRPM(name, license, nil)
}

// buildUnsignableRPM parses as an rpm but has no payload digest to sign over.
func buildUnsignableRPM() []byte {
	return writeRPM("rpmtest", "", func(r *pack.RPM) {
		r.AddCustomTag(tag.PAYLOADDIGESTALGO, pack.EntryInt32(nil))
	})
}

func writeRPM(name, license string, tweak func(*pack.RPM)) []byte {
	r, err := pack.NewRPM(pack.RPMMetaData{
		Name: name, Version: "1.0", Release: "1.el9", Arch: "x86_64",
		Summary: "Test package", Description: "A test", Licence: license,
	})
	if err != nil {
		panic(err)
	}

	if tweak != nil {
		tweak(r)
	}

	var buf bytes.Buffer
	if err := r.Write(&buf); err != nil {
		panic(err)
	}

	return buf.Bytes()
}
