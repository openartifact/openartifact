// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package branding

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateProductName(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"Acme Artifacts", "Acme Artifacts", true},
		{"  padded  ", "padded", true},
		{"Äpfel & Birnen", "Äpfel & Birnen", true},
		{strings.Repeat("x", 64), strings.Repeat("x", 64), true},
		{strings.Repeat("é", 64), strings.Repeat("é", 64), true},
		{"", "", false},
		{"   ", "", false},
		{strings.Repeat("x", 65), "", false},
		{"bad\x00name", "", false},
		{"two\nlines", "", false},
		{"rtl\u202eoverride", "", false},
		{"zero\u200bwidth", "", false},
		{"bad\xffutf8", "", false},
	}

	for _, tt := range tests {
		got, err := ValidateProductName(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("ValidateProductName(%q) = %q, %v; want %q ok=%v", tt.in, got, err, tt.want, tt.ok)
		}

		if err != nil && !errors.Is(err, ErrInvalid) {
			t.Errorf("error does not wrap ErrInvalid: %v", err)
		}
	}
}

func TestValidateColor(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"#4F46E5", "#4f46e5", true},
		{" #00ff00 ", "#00ff00", true},
		{"", "", true},
		{"#fff", "", false},
		{"4f46e5", "", false},
		{"#4f46e5ff", "", false},
		{"#gggggg", "", false},
		{"red", "", false},
		{"rgb(1,2,3)", "", false},
		{"#4f46e5;background:url(x)", "", false},
	}

	for _, tt := range tests {
		got, err := ValidateColor(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("ValidateColor(%q) = %q, %v; want %q ok=%v", tt.in, got, err, tt.want, tt.ok)
		}
	}
}

func TestValidateLinks(t *testing.T) {
	tests := []struct {
		name    string
		fn      func(string) (string, error)
		in      string
		want    string
		wantErr bool
	}{
		{"support https", ValidateSupportURL, "https://support.example.com/help", "https://support.example.com/help", false},
		{"support http", ValidateSupportURL, "http://intranet/help", "http://intranet/help", false},
		{"support mailto", ValidateSupportURL, "mailto:it@example.com", "mailto:it@example.com", false},
		{"support empty hides", ValidateSupportURL, "", "", false},
		{"support javascript", ValidateSupportURL, "javascript:alert(1)", "", true},
		{"support data", ValidateSupportURL, "data:text/html,x", "", true},
		{"support relative", ValidateSupportURL, "/help", "", true},
		{"support schemeless", ValidateSupportURL, "example.com/help", "", true},
		{"support credentials", ValidateSupportURL, "https://user:pw@example.com/", "", true},
		{"support no host", ValidateSupportURL, "https:///x", "", true},
		{"support whitespace", ValidateSupportURL, "https://example.com/a b", "", true},
		{"support newline", ValidateSupportURL, "https://example.com/\nX: y", "", true},
		{"support too long", ValidateSupportURL, "https://example.com/" + strings.Repeat("a", 2100), "", true},
		{"source https", ValidateSourceURL, "https://git.example.com/acme/oa", "https://git.example.com/acme/oa", false},
		{"source empty is default", ValidateSourceURL, "", DefaultSourceURL, false},
		{"source mailto rejected", ValidateSourceURL, "mailto:a@b.c", "", true},
		{"source javascript rejected", ValidateSourceURL, "javascript:alert(1)", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.fn(tt.in)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("got %q, %v; want %q wantErr=%v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestValidateLoginMessage(t *testing.T) {
	if got, err := ValidateLoginMessage("Welcome\r\n\r\n**Acme** [help](https://x.example)\ttab"); err != nil || strings.Contains(got, "\r") {
		t.Errorf("valid message rejected or CR kept: %q, %v", got, err)
	}

	for name, in := range map[string]string{
		"too long":     strings.Repeat("a", maxLoginMessageLen+1),
		"nul":          "a\x00b",
		"escape":       "a\x1bb",
		"bidi":         "a\u202eb",
		"invalid utf8": "a\xffb",
	} {
		if _, err := ValidateLoginMessage(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestConfig(t *testing.T) {
	d, err := Config{}.Defaults()
	if err != nil || d.ProductName != DefaultProductName || d.SourceURL != DefaultSourceURL || len(d.Images) != 0 {
		t.Fatalf("empty config defaults = %+v, %v", d, err)
	}

	for name, c := range map[string]Config{
		"name":    {ProductName: strings.Repeat("x", 100)},
		"colour":  {PrimaryColor: "blue"},
		"message": {LoginMessage: "a\x00"},
		"support": {SupportURL: "ftp://x"},
		"source":  {SourceURL: "javascript:x"},
	} {
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "branding.") {
			t.Errorf("%s: Validate() = %v, want a branding.* error", name, err)
		}
	}

	if _, err := (Config{LogoLight: "/nonexistent/logo.png"}).Defaults(); err == nil || !strings.Contains(err.Error(), "branding.logoLight") {
		t.Errorf("missing logo file: %v", err)
	}
}
