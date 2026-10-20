// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"errors"
	"strings"
	"testing"
)

func TestValidatePackageName_Valid(t *testing.T) {
	cases := []string{
		"react",
		"lodash.merge",
		"@scope/name",
		"@types/node",
		"foo-bar",
		"foo_bar",
	}
	for _, name := range cases {
		if err := ValidatePackageName(name); err != nil {
			t.Errorf("ValidatePackageName(%q) unexpected error: %v", name, err)
		}
	}
}

func TestValidatePackageName_RejectUppercase(t *testing.T) {
	err := ValidatePackageName("React")
	if err == nil {
		t.Fatal("expected error for uppercase name")
	}

	if !errors.Is(err, ErrInvalidPackageName) {
		t.Fatalf("expected ErrInvalidPackageName, got %v", err)
	}
}

func TestValidatePackageName_RejectLeadingDot(t *testing.T) {
	err := ValidatePackageName(".foo")
	if err == nil {
		t.Fatal("expected error for leading dot")
	}

	if !errors.Is(err, ErrInvalidPackageName) {
		t.Fatalf("expected ErrInvalidPackageName, got %v", err)
	}
}

func TestValidatePackageName_RejectLeadingUnderscore(t *testing.T) {
	err := ValidatePackageName("_foo")
	if err == nil {
		t.Fatal("expected error for leading underscore")
	}

	if !errors.Is(err, ErrInvalidPackageName) {
		t.Fatalf("expected ErrInvalidPackageName, got %v", err)
	}
}

func TestValidatePackageName_RejectCoreModule(t *testing.T) {
	for _, core := range []string{"http", "fs", "path", "os", "crypto"} {
		err := ValidatePackageName(core)
		if err == nil {
			t.Errorf("expected error for core module %q", core)
		}

		if !errors.Is(err, ErrInvalidPackageName) {
			t.Errorf("expected ErrInvalidPackageName for %q, got %v", core, err)
		}
	}
}

func TestValidatePackageName_RejectOverlong(t *testing.T) {
	name := strings.Repeat("a", 215)

	err := ValidatePackageName(name)
	if err == nil {
		t.Fatal("expected error for 215-char name")
	}

	if !errors.Is(err, ErrInvalidPackageName) {
		t.Fatalf("expected ErrInvalidPackageName, got %v", err)
	}
}

func TestValidatePackageName_RejectEmpty(t *testing.T) {
	err := ValidatePackageName("")
	if err == nil {
		t.Fatal("expected error for empty name")
	}

	if !errors.Is(err, ErrInvalidPackageName) {
		t.Fatalf("expected ErrInvalidPackageName, got %v", err)
	}
}

func TestValidatePackageName_RejectSpace(t *testing.T) {
	err := ValidatePackageName("foo bar")
	if err == nil {
		t.Fatal("expected error for name with space")
	}
}

func TestValidatePackageName_RejectEmptyScope(t *testing.T) {
	err := ValidatePackageName("@/name")
	if err == nil {
		t.Fatal("expected error for empty scope")
	}

	if !errors.Is(err, ErrInvalidPackageName) {
		t.Fatalf("expected ErrInvalidPackageName, got %v", err)
	}
}

func TestValidatePackageName_RejectEmptyAfterScope(t *testing.T) {
	err := ValidatePackageName("@scope/")
	if err == nil {
		t.Fatal("expected error for empty package after scope")
	}

	if !errors.Is(err, ErrInvalidPackageName) {
		t.Fatalf("expected ErrInvalidPackageName, got %v", err)
	}
}

func TestValidatePackageName_RejectUppercaseScope(t *testing.T) {
	err := ValidatePackageName("@SCOPE/name")
	if err == nil {
		t.Fatal("expected error for uppercase scope")
	}

	if !errors.Is(err, ErrInvalidPackageName) {
		t.Fatalf("expected ErrInvalidPackageName, got %v", err)
	}
}

func TestValidatePackageName_RejectsSlashInUnscoped(t *testing.T) {
	for _, n := range []string{"a/-/b", "foo/../x", "a/b"} {
		if err := ValidatePackageName(n); err == nil {
			t.Errorf("ValidatePackageName(%q) = nil, want error", n)
		}
	}
}
