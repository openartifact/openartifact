// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"bytes"
	"testing"

	pkgrpm "github.com/M0Rf30/pkgformat/rpm"
)

func TestCreateTestRPM_RoundTrip(t *testing.T) {
	rpmBytes := CreateTestRPM("testpkg", "2.0.0", "1.el9", "x86_64")
	if len(rpmBytes) == 0 {
		t.Fatal("CreateTestRPM returned empty bytes")
	}

	pkg, err := pkgrpm.ReadPackage(bytes.NewReader(rpmBytes))
	if err != nil {
		t.Fatalf("ReadPackage failed: %v", err)
	}

	if pkg.Name != "testpkg" {
		t.Errorf("Name = %q, want %q", pkg.Name, "testpkg")
	}
	if pkg.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q", pkg.Version, "2.0.0")
	}
	if pkg.Release != "1.el9" {
		t.Errorf("Release = %q, want %q", pkg.Release, "1.el9")
	}
	if pkg.Arch != "x86_64" {
		t.Errorf("Arch = %q, want %q", pkg.Arch, "x86_64")
	}
}
