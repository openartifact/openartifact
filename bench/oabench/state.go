// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"os"
)

// state is what `seed` leaves behind for `load`.
type state struct {
	URL         string `json:"url"`
	Token       string `json:"token"`
	MavenRepo   string `json:"maven_repo"`
	NPMRepo     string `json:"npm_repo"`
	DebRepo     string `json:"deb_repo"`
	RPMRepo     string `json:"rpm_repo"`
	DockerRepo  string `json:"docker_repo"`
	MavenCount  int    `json:"maven_count"`
	NPMCount    int    `json:"npm_count"`
	DebCount    int    `json:"deb_count"`
	BigJar      string `json:"big_jar"`
	DockerBlob  string `json:"docker_blob"`
	DockerImage string `json:"docker_image"`
	NPMTarball  string `json:"npm_tarball"`
}

func (s *state) save(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, b, 0o600)
}

func loadState(path string) (*state, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var s state

	return &s, json.Unmarshal(b, &s)
}
