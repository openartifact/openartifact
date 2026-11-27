// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package p2_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/p2"
	"github.com/openartifact/openartifact/pkg/p2/p2test"
)

func rewrite(t *testing.T, doc string, opts p2.Options) (string, error) {
	t.Helper()

	var out bytes.Buffer

	err := p2.RewriteXML(&out, strings.NewReader(doc), opts)

	return out.String(), err
}

func TestRewriteStripsMirrorsAndStats(t *testing.T) {
	t.Parallel()

	got, err := rewrite(t, p2test.Artifacts, p2.Options{})
	if err != nil {
		t.Fatal(err)
	}

	want := strings.Replace(p2test.Artifacts,
		"    <property name='p2.mirrorsURL' value='https://example.org/download.php?file=/rel&amp;format=xml'/>\n"+
			"    <property name='p2.statsURI' value='https://example.org/stats/rel'/>\n", "\n\n", 1)
	want = strings.Replace(want, "<properties size='4'>", "<properties size='2'>", 1)

	// Whitespace around a dropped property is kept; compare without the blank lines it leaves.
	if squash(got) != squash(want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}

	if strings.Contains(got, "download.php") || strings.Contains(got, "stats/rel") {
		t.Error("the mirror or statistics URL survived")
	}

	// The artifact-level property of the same name is not a repository property.
	if !strings.Contains(got, "kept: this is an artifact property") {
		t.Error("an artifact property was removed")
	}

	if !strings.Contains(got, "<?xml version='1.0' encoding='UTF-8'?>\n<?artifactRepository version='1.1.0'?>") {
		t.Error("the prolog changed")
	}

	if !strings.Contains(got, "<rule filter='(&amp; (classifier=osgi.bundle))' output='${repoUrl}/plugins/${id}_${version}.jar'/>") {
		t.Error("the download rule changed")
	}
}

func squash(s string) string {
	var b strings.Builder

	for line := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(line) != "" {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}

	return b.String()
}

func TestRewriteWithoutStrippedPropertiesIsIdentity(t *testing.T) {
	t.Parallel()

	doc := "<repository><properties size='1'><property name='p2.timestamp' value='1'/></properties>" +
		"<!-- keep &me --><artifacts size='0'/></repository>\n"

	got, err := rewrite(t, doc, p2.Options{})
	if err != nil || got != doc {
		t.Fatalf("got %q, %v; want the input back", got, err)
	}
}

func TestRewriteNonSelfClosingProperty(t *testing.T) {
	t.Parallel()

	doc := "<repository><properties size='2'><property name='p2.statsURI' value='x'>text<b/></property>" +
		"<property name='a' value='b'></property></properties></repository>"

	got, err := rewrite(t, doc, p2.Options{})
	if err != nil {
		t.Fatal(err)
	}

	want := "<repository><properties size='1'><property name='a' value='b'></property></properties></repository>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRewriteRelocatesChildren(t *testing.T) {
	t.Parallel()

	r, err := p2.NewRelocator("https://updates.example.org/releases/2024-12",
		"https://updates.example.org/releases/2024-12/compositeArtifacts.xml", "compositeArtifacts.xml")
	if err != nil {
		t.Fatal(err)
	}

	got, err := rewrite(t, p2test.Composite, p2.Options{Relocate: r.Relocate})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"<child location='202412041000' />",
		"<child location='_host/technology/epp/2024-12/'/>",
		"<child location='202412041000'/>",
		"<child location='https://other.example.net/repo/'/>",
		"<children size='4'>",
		"<properties size='1'>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	if strings.Contains(got, "mirrors") || strings.Contains(got, "updates.example.org") {
		t.Errorf("a mirror URL or an absolute child on the upstream host survived:\n%s", got)
	}
}

func TestRewriteRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		doc  string
		want error
	}{
		{"html page", "<html><body>Not found</body></html>", p2.ErrNotRepository},
		{"doctype", "<!DOCTYPE repository [<!ENTITY a 'b'>]><repository/>", p2.ErrDirective},
		{"doctype inside", "<repository><!DOCTYPE x></repository>", p2.ErrDirective},
		{"two roots", "<repository/><repository/>", p2.ErrNotRepository},
		{"text after the root", "<repository/>junk", p2.ErrNotRepository},
		{"empty", "", p2.ErrNotRepository},
		{"truncated", "<repository><properties size='1'>", p2.ErrTruncated},
		{"too deep", "<repository>" + strings.Repeat("<a>", 100), p2.ErrTooDeep},
		{"huge token", "<repository><!--" + strings.Repeat("x", 9<<20) + "--></repository>", p2.ErrTokenTooLarge},
	}

	for _, tt := range tests {
		_, err := rewrite(t, tt.doc, p2.Options{})
		if !errors.Is(err, tt.want) {
			t.Errorf("%s: got %v, want %v", tt.name, err, tt.want)
		}
	}

	if _, err := rewrite(t, "<repository>&unknown;</repository>", p2.Options{}); err == nil {
		t.Error("an undefined entity must fail, not expand")
	}

	if _, err := rewrite(t, "<?xml version='1.0' encoding='UTF-16'?><repository/>", p2.Options{}); err == nil {
		t.Error("a non-UTF-8 document must be refused")
	}

	if _, err := rewrite(t, "<repository></x></repository>", p2.Options{}); err == nil {
		t.Error("a mismatched end tag must fail")
	}
}

// failingWriter fails every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestRewriteReportsWriteErrors(t *testing.T) {
	t.Parallel()

	if err := p2.RewriteXML(failingWriter{}, strings.NewReader(p2test.Artifacts), p2.Options{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("got %v, want the writer's error", err)
	}
}
