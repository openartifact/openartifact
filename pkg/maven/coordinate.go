// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

type Coordinate struct {
	GroupID    string
	ArtifactID string
	Version    string
	Classifier string
	Extension  string
}
