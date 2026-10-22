// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"encoding/json"
	"time"
)

type NuGetPackage struct {
	ID                       string
	RepositoryID             string
	PackageID                string
	Version                  string
	Title                    string
	Description              string
	Summary                  string
	Authors                  string
	Owners                   string
	Tags                     string
	ProjectURL               string
	LicenseURL               string
	IconURL                  string
	RequireLicenseAcceptance bool
	IsPrerelease             bool
	MinClientVersion         string
	// DependencyGroups is the registration "dependencyGroups" JSON of the
	// package. nil means not recorded yet (derive it from the nuspec); an empty
	// array means the package has no dependencies.
	DependencyGroups     json.RawMessage
	BlobKey              string
	PackageHash          string
	PackageHashAlgorithm string
	PackageSize          int64
	DownloadCount        int
	CreatedAt            time.Time
	UpdatedAt            time.Time
}
