// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package secrets

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// maxBody is the request body cap of the secret scan endpoint.
const maxBody = 1 << 20

// passwordHits returns text of n "password=<value>" matches joined by sep,
// each with a distinct value.
func passwordHits(n int, sep string) string {
	var b strings.Builder

	for i := range n {
		fmt.Fprintf(&b, "password=hunter%06d%s", i, sep)
	}

	return b.String()
}

func BenchmarkScanText_Hits(b *testing.B) {
	log := newTestScanner(b)

	for _, n := range []int{100, 1000, 5000, 20000} {
		for _, sep := range []struct{ name, s string }{{"lines", "\n"}, {"oneline", " "}} {
			text := passwordHits(n, sep.s)

			b.Run(fmt.Sprintf("%d/%s", n, sep.name), func(b *testing.B) {
				b.SetBytes(int64(len(text)))

				for b.Loop() {
					log.ScanText(text)
				}
			})
		}
	}
}

// A body that is nothing but matches, on one line, must scan in a fraction of a
// second: the quadratic hint code needed seconds for this 256 KiB and minutes
// for a body at the 1 MiB request cap.
func TestScanText_ManyHitsStayLinear(t *testing.T) {
	scanner := newTestScanner(t)

	for name, sep := range map[string]string{"one line": " ", "many lines": "\n"} {
		t.Run(name, func(t *testing.T) {
			text := passwordHits(maxBody/4/len("password=hunter000000 "), sep)

			start := time.Now()
			findings := scanner.ScanText(text)
			elapsed := time.Since(start)

			if elapsed > 2*time.Second {
				t.Fatalf("scanning %d bytes took %s", len(text), elapsed)
			}

			if len(findings) != MaxFindings {
				t.Fatalf("findings = %d, want the cap %d", len(findings), MaxFindings)
			}
		})
	}
}

func TestScanText_CapsFindings(t *testing.T) {
	scanner := newTestScanner(t)

	text := passwordHits(MaxFindings+500, "\n")

	findings := scanner.ScanText(text)
	if len(findings) != MaxFindings {
		t.Fatalf("findings = %d, want %d", len(findings), MaxFindings)
	}

	// Below the cap nothing is dropped.
	if got := len(scanner.ScanText(passwordHits(MaxFindings, "\n"))); got != MaxFindings {
		t.Fatalf("findings at the cap = %d, want %d", got, MaxFindings)
	}

	if got := len(scanner.ScanText(passwordHits(3, "\n"))); got != 3 {
		t.Fatalf("findings below the cap = %d, want 3", got)
	}
}

// Findings past the cap are not reported, but their text is still masked in the
// hints of the findings that are.
func TestScanText_HintsMaskSecretsBeyondTheCap(t *testing.T) {
	scanner := newTestScanner(t)

	npm := "npm_" + strings.Repeat("a", 36)
	// The npm token pattern sorts after the password one: the cap drops it.
	text := "password=hunter000000 " + npm + " \n" + passwordHits(MaxFindings, "\n")

	findings := scanner.ScanText(text)
	if len(findings) != MaxFindings {
		t.Fatalf("findings = %d, want %d", len(findings), MaxFindings)
	}

	for _, f := range findings {
		if f.Type == "npm_token" {
			t.Fatal("the npm finding should have been cut by the cap")
		}

		if strings.Contains(f.LineHint, npm) || strings.Contains(f.LineHint, "hunter") {
			t.Fatalf("hint leaks a secret: %q", f.LineHint)
		}
	}

	if want := "**** ****"; findings[0].LineHint != want {
		t.Fatalf("first hint = %q, want %q", findings[0].LineHint, want)
	}
}

// Adjacent and overlapping secrets render as one mask, not one per match.
func TestScanText_HintMergesAdjacentSecrets(t *testing.T) {
	scanner := newTestScanner(t)

	first, second := "npm_"+strings.Repeat("a", 36), "npm_"+strings.Repeat("b", 36)

	for name, text := range map[string]string{
		"adjacent":  first + second,
		"nested":    "api_key=" + first, // generic_api_key covers the npm token
		"separated": first + " " + second,
	} {
		findings := scanner.ScanText(text)
		if len(findings) != 2 {
			t.Fatalf("%s: findings = %d, want 2", name, len(findings))
		}

		want := "****"
		if name == "separated" {
			want = "**** ****"
		}

		for _, f := range findings {
			if f.LineHint != want {
				t.Errorf("%s: %s hint = %q, want %q", name, f.Type, f.LineHint, want)
			}
		}
	}
}

// A window that starts or ends inside a multibyte rune is widened to a rune
// boundary, and the hint stays valid UTF-8 whatever the offset.
func TestScanText_HintStaysValidUTF8AtEveryOffset(t *testing.T) {
	scanner := newTestScanner(t)

	for pad := range 12 {
		text := strings.Repeat("é", 20+pad) + strings.Repeat("a", pad%3) + "password=hunter000000" + strings.Repeat("😀", 20+pad)

		for _, f := range scanner.ScanText(text) {
			if !utf8.ValidString(f.LineHint) || !strings.Contains(f.LineHint, "****") || strings.Contains(f.LineHint, "hunter") {
				t.Fatalf("pad %d: hint %q", pad, f.LineHint)
			}
		}
	}
}
