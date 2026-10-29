// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import (
	"bytes"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"go.yaml.in/yaml/v3"
)

// wholeDocumentHelmIndex marshals index.yaml as one yaml document: the output
// renderHelmIndex must reproduce byte for byte.
func wholeDocumentHelmIndex(charts []*models.HelmChart, generated time.Time) ([]byte, error) {
	entries := make(map[string][]HelmChartEntry)
	for _, chart := range charts {
		entries[chart.Name] = append(entries[chart.Name], newHelmChartEntry(chart))
	}

	return yaml.Marshal(struct {
		APIVersion string                      `yaml:"apiVersion"`
		Generated  string                      `yaml:"generated"`
		Entries    map[string][]HelmChartEntry `yaml:"entries"`
	}{APIVersion: "v1", Generated: generated.UTC().Format(time.RFC3339), Entries: entries})
}

func TestRenderHelmIndexMatchesWholeDocument(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	longDescription := strings.Repeat("a very long description that the yaml emitter folds at eighty columns ", 6)

	chart := func(name, version string) *models.HelmChart {
		return &models.HelmChart{
			Name: name, Version: version, Digest: "abc", CreatedAt: created, Description: "chart " + name,
		}
	}

	full := chart("full", "1.0.0")
	full.AppVersion = "2.0"
	full.Home = "https://example.org/full"
	full.Sources = []string{"https://example.org/src", "https://example.org/src2"}
	full.Maintainers = []map[string]string{{"name": "Jane", "email": "jane@example.org"}, {"name": "Joe"}}
	full.Icon = "https://example.org/icon.png"
	full.APIVersion = "v2"
	full.Type = "application"
	full.Keywords = []string{"yes", "123", "null", "a: b"}
	full.Description = longDescription

	numericDigest := chart("digest", "1.0.0")
	numericDigest.Digest = "0000000000000000000000000000000000000000000000000000000000001234"
	numericDigest.AppVersion = "true"

	multiline := chart("multiline", "1.0.0")
	multiline.Description = "first line\nsecond line\n  indented\n"

	unicode := chart("ünïcode-图表", "1.0.0")
	unicode.Description = "héllo wörld ✓"

	manyVersions := make([]*models.HelmChart, 0, 40)
	for i := range 40 {
		manyVersions = append(manyVersions, chart("many", "1."+strconv.Itoa(i)+".0"))
	}

	tests := []struct {
		name   string
		charts []*models.HelmChart
	}{
		{"empty", nil},
		{"one chart", []*models.HelmChart{chart("a", "1.0.0")}},
		{"every field", []*models.HelmChart{full}},
		{"scalars that need quoting", []*models.HelmChart{
			numericDigest, chart("true", "1.0.0"), chart("null", "1.0.0"), chart("123", "1.0.0"),
		}},
		{"multi-line and unicode", []*models.HelmChart{multiline, unicode}},
		{"natural key order", []*models.HelmChart{
			chart("chart-10", "1.0.0"), chart("chart-2", "1.0.0"), chart("chart-1", "1.0.0"), chart("Chart-3", "1.0.0"),
			chart("a01", "1.0.0"), chart("a1", "1.0.0"), chart("a001", "1.0.0"), chart("a-b", "1.0.0"), chart("a.b", "1.0.0"),
			chart("1chart", "1.0.0"), chart("10chart", "1.0.0"), chart("2chart", "1.0.0"),
		}},
		{"key beyond the simple key limit", []*models.HelmChart{
			chart(strings.Repeat("k", 200), "1.0.0"), chart(strings.Repeat("k", 130), "1.0.0"), chart("short", "1.0.0"),
		}},
		{"versions of a name stay in input order", manyVersions},
		{"names interleaved", []*models.HelmChart{
			chart("b", "2.0.0"), chart("a", "1.0.0"), chart("b", "1.0.0"), chart("a", "2.0.0"),
		}},
		{"version needing escape", []*models.HelmChart{chart("a", "1.0.0+b#c?d")}},
	}

	generated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			want, err := wholeDocumentHelmIndex(tt.charts, generated)
			if err != nil {
				t.Fatal(err)
			}

			got, err := renderHelmIndex(tt.charts, generated)
			if err != nil {
				t.Fatal(err)
			}

			if string(got) != string(want) {
				t.Errorf("index.yaml differs from the whole-document marshal\n got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// TestRenderHelmIndexLargeMatchesWholeDocument covers a repository the size of
// the benchmark one.
func TestRenderHelmIndexLargeMatchesWholeDocument(t *testing.T) {
	t.Parallel()

	charts := benchCharts(3000)
	generated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	want, err := wholeDocumentHelmIndex(charts, generated)
	if err != nil {
		t.Fatal(err)
	}

	got, err := renderHelmIndex(charts, generated)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != string(want) {
		t.Error("index.yaml differs from the whole-document marshal")
	}
}

// TestPlainScalarIsWrittenPlainByYAML checks plainScalar against the library:
// every string it accepts is written verbatim, unquoted, on one line. The
// strings are all those up to four characters over an alphabet that covers the
// bool, null, number and timestamp spellings, and random longer ones.
func TestPlainScalarIsWrittenPlainByYAML(t *testing.T) {
	t.Parallel()

	const col = len("k: ")

	check := func(s string) {
		if !plainScalar(s, col) {
			return
		}

		out, err := yaml.Marshal(map[string]string{"k": s})
		if err != nil {
			t.Fatal(err)
		}

		if want := "k: " + s + "\n"; string(out) != want {
			t.Fatalf("plainScalar(%q) is true, yaml writes %q", s, out)
		}
	}

	const alphabet = "0123aeobxtzynfu:. -+_/#@%=~"

	var gen func(prefix string, n int)

	gen = func(prefix string, n int) {
		check(prefix)

		if n == 0 {
			return
		}

		for i := range len(alphabet) {
			gen(prefix+alphabet[i:i+1], n-1)
		}
	}

	gen("", 4)

	// Whole words: the spellings the resolver reads as something else.
	for _, s := range []string{
		"y", "Y", "yes", "Yes", "YES", "n", "no", "No", "on", "On", "off", "OFF", "true", "True", "TRUE", "false", "null",
		"Null", "NULL", "nan", "inf", "1", "1.0", "1e3", "1E3", "0x1F", "0o17", "0b101", "017", "1_000", "2001-12-14",
		"2001-12-14t21:59:43.10-05:00", "1:30", "190:20:30", "0b1f", "0b", "1.0.0", "1.0.0-rc.1", "12e5", "12e5f", "7f3a9c",
		"3e8a", "0e", "00", "1 2", "1 e5", "9e9 ",
	} {
		check(s)
	}

	const randomAlphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEXZ.-+_:/ @%=#~,?&"

	rng := rand.New(rand.NewPCG(1, 2))

	for range 200000 {
		b := make([]byte, 1+rng.IntN(90))
		for i := range b {
			b[i] = randomAlphabet[rng.IntN(len(randomAlphabet))]
		}

		check(string(b))
	}
}

// TestPlainKeyIsWrittenPlainByYAML is the same check for mapping keys.
func TestPlainKeyIsWrittenPlainByYAML(t *testing.T) {
	t.Parallel()

	const alphabet = "0123aeobxtzynfu.-_"

	var gen func(prefix string, n int)

	check := func(s string) {
		if !plainKey(s) {
			return
		}

		out, err := yaml.Marshal(map[string]int{s: 1})
		if err != nil {
			t.Fatal(err)
		}

		if want := s + ": 1\n"; string(out) != want {
			t.Fatalf("plainKey(%q) is true, yaml writes %q", s, out)
		}
	}

	gen = func(prefix string, n int) {
		check(prefix)

		if n == 0 {
			return
		}

		for i := range len(alphabet) {
			gen(prefix+alphabet[i:i+1], n-1)
		}
	}

	gen("", 4)

	for _, s := range []string{strings.Repeat("k", 128), strings.Repeat("k", 129), "true", "1e3", "chart-1.2", "0x1"} {
		check(s)
	}
}

// TestRenderHelmIndexRandomMatchesWholeDocument renders random charts, with
// values that are sometimes plain, sometimes quoted, folded or escaped, and
// compares them with the whole-document marshal.
func TestRenderHelmIndexRandomMatchesWholeDocument(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewPCG(7, 11))

	words := []string{
		"alpha", "beta", "1.2.3", "2.0", "true", "null", "v2", "https://example.org/a", "a: b", "# c", "x y", "-lead",
		"trail ", "é", "tab\t", "multi\nline", "", "0x10", "1e3", "12345", "a,b", "[x]", "q'uote", "d\"q", "50%", "a b c d e f g",
		strings.Repeat("long ", 30),
	}

	pick := func() string { return words[rng.IntN(len(words))] }

	pickList := func() []string {
		l := make([]string, rng.IntN(4))
		for i := range l {
			l[i] = pick()
		}

		return l
	}

	for round := range 300 {
		charts := make([]*models.HelmChart, 1+rng.IntN(12))
		for i := range charts {
			c := &models.HelmChart{
				Name:        words[rng.IntN(8)] + strconv.Itoa(rng.IntN(15)),
				Version:     pick(),
				AppVersion:  pick(),
				Description: pick() + " " + pick(),
				Digest:      chartDigest(rng.IntN(100)),
				Home:        pick(),
				Sources:     pickList(),
				Icon:        pick(),
				APIVersion:  pick(),
				Type:        pick(),
				Keywords:    pickList(),
				CreatedAt:   time.Unix(int64(rng.IntN(2000000000)), 0),
			}

			for range rng.IntN(3) {
				c.Maintainers = append(c.Maintainers, map[string]string{"name": pick(), "email": pick()})
			}

			charts[i] = c
		}

		generated := time.Unix(1700000000, 0)

		want, err := wholeDocumentHelmIndex(charts, generated)
		if err != nil {
			t.Fatal(err)
		}

		got, err := renderHelmIndex(charts, generated)
		if err != nil {
			t.Fatal(err)
		}

		if string(got) != string(want) {
			t.Fatalf("round %d: index.yaml differs from the whole-document marshal\n got:\n%s\nwant:\n%s", round, got, want)
		}
	}
}

// TestRenderHelmIndexWritesTypicalChartsDirectly keeps the direct writer on the
// path of ordinary charts: if it declined them, index.yaml would still be
// right but would be rendered through the yaml library again.
func TestRenderHelmIndexWritesTypicalChartsDirectly(t *testing.T) {
	t.Parallel()

	charts := benchCharts(100)

	for i := 0; i < len(charts); i += 10 {
		var buf bytes.Buffer

		if !writeGroupDirect(&buf, charts[i].Name, charts[i:i+10]) {
			t.Fatalf("chart %q was not written directly", charts[i].Name)
		}
	}
}
