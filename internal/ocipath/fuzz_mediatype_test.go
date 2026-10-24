// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ocipath

import (
	"strings"
	"testing"
)

// FuzzSafeMediaType feeds arbitrary manifest and listing media types, which are
// uploader or upstream controlled, to the filter in front of the /v2/
// Content-Type header: whatever it lets through is one bare registry JSON type
// that stays unchanged when filtered again, and everything else is served as
// the inert fallback.
func FuzzSafeMediaType(f *testing.F) {
	for _, s := range []string{
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json; charset=utf-8",
		"application/vnd.docker.distribution.manifest.v1+prettyjws",
		"APPLICATION/JSON",
		"application/json;q=1",
		"text/html",
		"image/svg+xml",
		"application/vnd.x+json\r\nSet-Cookie: a=b",
		"application/vnd.x+json\n",
		"application/vnd..+json",
		"application/vnd.a+json,text/html",
		"",
		";",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		mt, ok := SafeMediaType(raw)
		served := ServedMediaType(raw)

		if !ok {
			if mt != "" || served != FallbackMediaType {
				t.Fatalf("rejected %q but got %q, served %q", raw, mt, served)
			}

			return
		}

		if served != mt {
			t.Fatalf("ServedMediaType(%q) = %q, SafeMediaType = %q", raw, served, mt)
		}

		if mt != "application/json" && !registryTypeRE.MatchString(mt) {
			t.Fatalf("accepted %q as %q, which is not a registry JSON type", raw, mt)
		}

		if strings.ContainsAny(mt, ";, \t\r\n\"\x00") || mt != strings.ToLower(mt) {
			t.Fatalf("accepted %q as %q, which is not a bare lowercase media type", raw, mt)
		}

		if again, ok := SafeMediaType(mt); !ok || again != mt {
			t.Fatalf("SafeMediaType(%q) = %q is not stable: %q, %v", raw, mt, again, ok)
		}
	})
}
