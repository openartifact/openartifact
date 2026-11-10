// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"bytes"

	"github.com/M0Rf30/pkgformat/rpm/pack"
)

func CreateTestRPM(name, version, release, arch string) []byte {
	r, err := pack.NewRPM(pack.RPMMetaData{
		Name: name, Version: version, Release: release, Arch: arch,
		Summary: "Test package " + name, Description: "Integration test package",
		Licence: "MIT", Group: "Development/Tools", OS: "linux",
	})
	if err != nil {
		panic(err)
	}

	var buf bytes.Buffer
	if err := r.Write(&buf); err != nil {
		panic(err)
	}

	return buf.Bytes()
}
