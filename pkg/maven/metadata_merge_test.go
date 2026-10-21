// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func artifactMeta(updated string, versions ...string) string {
	var b strings.Builder

	b.WriteString("<metadata><groupId>org.x</groupId><artifactId>lib</artifactId><versioning>")
	b.WriteString("<latest>" + versions[len(versions)-1] + "</latest><versions>")

	for _, v := range versions {
		b.WriteString("<version>" + v + "</version>")
	}

	b.WriteString("</versions><lastUpdated>" + updated + "</lastUpdated></versioning></metadata>")

	return b.String()
}

func mustParse(t *testing.T, doc string) *Metadata {
	t.Helper()

	m, err := ParseMetadata([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	return m
}

func TestMergeMetadata_UnionsVersionsInMavenOrder(t *testing.T) {
	a := mustParse(t, artifactMeta("20240101000000", "1.0", "1.2", "1.10"))
	b := mustParse(t, artifactMeta("20240301000000", "1.1", "1.2", "2.0-SNAPSHOT"))

	merged, err := MergeMetadata(a, b)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"1.0", "1.1", "1.2", "1.10", "2.0-SNAPSHOT"}
	if !slices.Equal(merged.Versioning.Versions, want) {
		t.Errorf("versions = %v, want %v", merged.Versioning.Versions, want)
	}

	if merged.Versioning.Latest != "2.0-SNAPSHOT" {
		t.Errorf("latest = %q, want 2.0-SNAPSHOT", merged.Versioning.Latest)
	}

	if merged.Versioning.Release != "1.10" {
		t.Errorf("release = %q, want highest non-snapshot 1.10", merged.Versioning.Release)
	}

	if merged.Versioning.LastUpdated != "20240301000000" {
		t.Errorf("lastUpdated = %q, want the newest", merged.Versioning.LastUpdated)
	}

	if merged.GroupID != "org.x" || merged.ArtifactID != "lib" {
		t.Errorf("coordinates lost: %q:%q", merged.GroupID, merged.ArtifactID)
	}
}

func TestMergeMetadata_RoundTripsThroughXML(t *testing.T) {
	merged, err := MergeMetadata(mustParse(t, artifactMeta("1", "1.0")), mustParse(t, artifactMeta("2", "1.1")))
	if err != nil {
		t.Fatal(err)
	}

	out, err := merged.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(string(out), "<?xml") {
		t.Errorf("missing XML declaration: %s", out)
	}

	again := mustParse(t, string(out))
	if !slices.Equal(again.Versioning.Versions, []string{"1.0", "1.1"}) {
		t.Errorf("round trip versions = %v", again.Versioning.Versions)
	}
}

func TestMergeMetadata_UnionsPluginsFirstWins(t *testing.T) {
	a := mustParse(t, `<metadata><plugins><plugin><name>A</name><prefix>a</prefix><artifactId>a-plugin</artifactId></plugin></plugins></metadata>`)
	b := mustParse(t, `<metadata><plugins>
		<plugin><name>A2</name><prefix>a</prefix><artifactId>other</artifactId></plugin>
		<plugin><name>B</name><prefix>b</prefix><artifactId>b-plugin</artifactId></plugin></plugins></metadata>`)

	merged, err := MergeMetadata(a, b)
	if err != nil {
		t.Fatal(err)
	}

	if len(merged.Plugins) != 2 || merged.Plugins[0].ArtifactID != "a-plugin" || merged.Plugins[1].Prefix != "b" {
		t.Errorf("plugins = %+v", merged.Plugins)
	}
}

func TestMergeMetadata_SnapshotLevelTakesNewestDocument(t *testing.T) {
	older := mustParse(t, `<metadata><groupId>g</groupId><artifactId>a</artifactId><version>1.0-SNAPSHOT</version>
		<versioning><snapshot><timestamp>20240101.000000</timestamp><buildNumber>1</buildNumber></snapshot>
		<lastUpdated>20240101000000</lastUpdated></versioning></metadata>`)
	newer := mustParse(t, `<metadata><groupId>g</groupId><artifactId>a</artifactId><version>1.0-SNAPSHOT</version>
		<versioning><snapshot><timestamp>20240201.000000</timestamp><buildNumber>7</buildNumber></snapshot>
		<lastUpdated>20240201000000</lastUpdated></versioning></metadata>`)

	merged, err := MergeMetadata(older, newer)
	if err != nil {
		t.Fatal(err)
	}

	if merged.Versioning.Snapshot.BuildNumber != 7 {
		t.Errorf("build number = %d, want the newest document's 7", merged.Versioning.Snapshot.BuildNumber)
	}
}

func TestParseMetadata_RejectsOtherDocuments(t *testing.T) {
	for _, doc := range []string{"", "not xml", "<project/>"} {
		if _, err := ParseMetadata([]byte(doc)); err == nil {
			t.Errorf("ParseMetadata(%q) succeeded", doc)
		}
	}

	if _, err := MergeMetadata(); !errors.Is(err, ErrNoMetadata) {
		t.Errorf("empty merge error = %v", err)
	}
}
