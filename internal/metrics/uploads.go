// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package metrics

import (
	"net/http"

	"github.com/openartifact/openartifact/internal/repotype"
)

// Upload format label values of openartifact_uploads_total: one per package
// format, lower case, a closed set (the repotype registry). Every code path that counts
// an upload goes through CountUpload with one of these.
const (
	FormatDeb       = "deb"
	FormatRPM       = "rpm"
	FormatNPM       = "npm"
	FormatMaven     = "maven"
	FormatHelm      = "helm"
	FormatRubyGems  = "rubygems"
	FormatDocker    = "docker"
	FormatPyPI      = "pypi"
	FormatCargo     = "cargo"
	FormatGo        = "go"
	FormatNuGet     = "nuget"
	FormatConda     = "conda"
	FormatConan     = "conan"
	FormatIvy       = "ivy"
	FormatSBT       = "sbt"
	FormatApk       = "apk"
	FormatRaw       = "raw"
	FormatPacman    = "pacman"
	FormatNix       = "nix"
	FormatTerraform = "terraform"
)

// Upload result label values of openartifact_uploads_total.
const (
	UploadSuccess = "success"
	UploadError   = "error"
)

// uploadFormatOther is the format label of an unknown format; CountUpload never
// lets a caller-supplied string widen the label set.
const uploadFormatOther = "other"

// UploadResult maps an HTTP status to the result label: any status below 400 is
// a success, everything else an error.
func UploadResult(status int) string {
	if status >= http.StatusBadRequest {
		return UploadError
	}

	return UploadSuccess
}

// CountUpload counts one finished hosted upload request. format is one of the
// Format* constants (anything else is recorded as "other"), result is
// UploadSuccess or UploadError. It is a no-op on a nil registry.
func (r *Registry) CountUpload(format, result string) {
	if r == nil {
		return
	}

	if !knownUploadFormat(format) {
		format = uploadFormatOther
	}

	if result != UploadSuccess {
		result = UploadError
	}

	r.UploadsTotal.WithLabelValues(format, result).Inc()
}

func knownUploadFormat(format string) bool {
	return repotype.Valid(format)
}
