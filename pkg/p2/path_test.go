// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package p2

import "testing"

func TestParseDoc(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key  string
		want Doc
		ok   bool
	}{
		{"artifacts.xml", Doc{Name: "artifacts", Codec: CodecXML}, true},
		{"artifacts.jar", Doc{Name: "artifacts", Codec: CodecJar}, true},
		{"a/b/artifacts.xml.xz", Doc{Name: "artifacts", Codec: CodecXZ}, true},
		{"compositeContent.jar", Doc{Name: "compositeContent", Codec: CodecJar, Composite: true}, true},
		{"_host/x/compositeArtifacts.xml", Doc{Name: "compositeArtifacts", Codec: CodecXML, Composite: true}, true},
		{"content.xml.xz", Doc{}, false},
		{"content.jar", Doc{}, false},
		{"p2.index", Doc{}, false},
		{"plugins/artifacts.jar", Doc{Name: "artifacts", Codec: CodecJar}, true},
		{"artifacts.xml.sha256", Doc{}, false},
		{"myartifacts.jar", Doc{}, false},
	}

	for _, tt := range tests {
		got, ok := ParseDoc(tt.key)
		if ok != tt.ok || got != tt.want {
			t.Errorf("ParseDoc(%q) = %+v, %v; want %+v, %v", tt.key, got, ok, tt.want, tt.ok)
		}
	}
}

func TestIsMetadataAndArtifact(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"p2.index", "content.jar", "content.xml", "content.xml.xz", "artifacts.jar", "x/y/compositeContent.xml",
		"compositeArtifacts.xml.xz", "content.xml.xz.sha256", "sub/p2.index",
	} {
		if !IsMetadata(key) || IsArtifact(key) {
			t.Errorf("%q is metadata, not an artifact", key)
		}
	}

	for _, key := range []string{
		"plugins/a_1.0.0.jar", "features/a_1.0.0.jar", "binary/a_1.0.0", "r/plugins/a_1.0.0.jar.pack.gz",
		"_host/r/features/a.b_1.jar",
	} {
		if IsMetadata(key) || !IsArtifact(key) {
			t.Errorf("%q is an artifact, not metadata", key)
		}
	}

	for _, key := range []string{"readme.txt", "plugins", "plugins/", "myplugins/a.jar", "plugins/sub/a.jar.txt"} {
		if IsArtifact(key) {
			t.Errorf("%q is not an artifact", key)
		}
	}
}

func TestCoordinates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key, id, version string
		ok               bool
	}{
		{"plugins/org.eclipse.core.runtime_3.31.0.v20240101-1200.jar", "org.eclipse.core.runtime", "3.31.0.v20240101-1200", true},
		{"features/org.eclipse.jdt_3.19.0.v20240301.jar", "org.eclipse.jdt", "3.19.0.v20240301", true},
		{"site/plugins/com.acme.x_1.0.0.jar.pack.gz", "com.acme.x", "1.0.0", true},
		{"plugins/with_underscore_1.0.0.jar", "with_underscore", "1.0.0", true},
		{"plugins/a.b_2.jar", "a.b", "2", true},
		{"plugins/no-version.jar", "", "", false},
		{"plugins/a_x.jar", "", "", false},
		{"plugins/a_1.0.0.txt", "", "", false},
		{"binary/org.eclipse.equinox.executable_3.8.0", "", "", false},
		{"content.jar", "", "", false},
		{"plugins/_1.0.0.jar", "", "", false},
	}

	for _, tt := range tests {
		id, version, ok := Coordinates(tt.key)
		if id != tt.id || version != tt.version || ok != tt.ok {
			t.Errorf("Coordinates(%q) = %q, %q, %v; want %q, %q, %v", tt.key, id, version, ok, tt.id, tt.version, tt.ok)
		}
	}
}
