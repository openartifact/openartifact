// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"testing"
)

func TestRepositoryPath_RoundTrip(t *testing.T) {
	c := Coordinate{
		GroupID:    "com.example",
		ArtifactID: "my-app",
		Version:    "1.0.0",
		Classifier: "",
		Extension:  "jar",
	}
	path := RepositoryPath(c)

	groupPath, artifactID, version, filename, err := ParseRepositoryPath(path)
	if err != nil {
		t.Fatalf("ParseRepositoryPath error: %v", err)
	}

	if groupPath != "com/example" {
		t.Errorf("groupPath = %q, want %q", groupPath, "com/example")
	}

	if artifactID != "my-app" {
		t.Errorf("artifactID = %q, want %q", artifactID, "my-app")
	}

	if version != "1.0.0" {
		t.Errorf("version = %q, want %q", version, "1.0.0")
	}

	if filename != "my-app-1.0.0.jar" {
		t.Errorf("filename = %q, want %q", filename, "my-app-1.0.0.jar")
	}
}
