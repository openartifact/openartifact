// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"testing"

	formatrpm "github.com/openartifact/openartifact/internal/format/rpm"
)

func TestRepositoryHandler_ValidatePackage_Valid(t *testing.T) {
	rh := formatrpm.NewRepositoryHandler()
	rpmData := buildMinimalRPM()

	pkg, err := rh.ValidatePackage(context.Background(), bytes.NewReader(rpmData))
	if err != nil {
		t.Fatalf("ValidatePackage(valid) error: %v", err)
	}

	if pkg.Name != "rpmtest" {
		t.Errorf("Name = %q, want %q", pkg.Name, "rpmtest")
	}
}

func TestRepositoryHandler_ValidatePackage_Invalid(t *testing.T) {
	rh := formatrpm.NewRepositoryHandler()

	_, err := rh.ValidatePackage(context.Background(), bytes.NewReader([]byte("not an rpm")))
	if err == nil {
		t.Error("ValidatePackage(invalid) expected error, got nil")
	}
}

func TestRepositoryHandler_GetPackageInfo(t *testing.T) {
	rh := formatrpm.NewRepositoryHandler()
	rpmData := buildMinimalRPM()
	pkg, _ := rh.ValidatePackage(context.Background(), bytes.NewReader(rpmData))

	info := rh.GetPackageInfo(pkg)
	if info["name"] != "rpmtest" {
		t.Errorf("info[name] = %v, want rpmtest", info["name"])
	}

	nilInfo := rh.GetPackageInfo(nil)
	if len(nilInfo) != 0 {
		t.Errorf("GetPackageInfo(nil) = %v, want empty map", nilInfo)
	}
}
