// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import "github.com/openartifact/openartifact/internal/repotype"

// aiMaxNameLen caps artifact_name and artifact_version of an AI analysis
// trigger.
const aiMaxNameLen = 512

// ValidateAIArtifactIdentity checks the artifact_format, artifact_name and
// artifact_version a caller supplies when triggering an analysis (REST and MCP
// share it) and returns a message for the first problem, "" when acceptable.
// The format is one of the repository formats, which are also the ids package
// listings report. It is compiled into every build because the MCP server
// validates with it even when the AI handlers are left out (-tags noai).
func ValidateAIArtifactIdentity(format, name, version string) string {
	if format != "" && !repotype.Valid(format) {
		return "artifact_format must be one of: " + validFormatList()
	}

	if len(name) > aiMaxNameLen || len(version) > aiMaxNameLen {
		return "artifact_name/artifact_version too long"
	}

	return ""
}
