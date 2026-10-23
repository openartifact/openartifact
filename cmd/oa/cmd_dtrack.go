// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"flag"
	"net/http"
)

// handleDTrack implements `oa dtrack`: the Dependency-Track integration of a
// repository (docs/security/dependency-track.md). Global settings, including the
// API key, are managed in the admin UI or through PUT /api/v1/admin/dependency-track.
func handleDTrack(args []string, client *Client, outFmt string) {
	if len(args) < 2 {
		dief("Usage: oa dtrack <show|test|push|pushes> <repo> [--artifact-id ID]")
	}

	base := "/api/v1/repositories/" + escape(args[1]) + "/dependency-track"

	switch args[0] {
	case "show":
		showGeneric(client, base, outFmt)
	case "test":
		result := sendJSON(client, http.MethodPost, base+"/test", nil)
		printScanResult(result, outFmt, "Dependency-Track %v reachable.", "version")
	case "push":
		fs := flag.NewFlagSet("dtrack push", flag.ExitOnError)
		artifactID := fs.String("artifact-id", "", "Artifact (name@version) to push")
		_ = fs.Parse(args[2:])

		if *artifactID == "" {
			dief("--artifact-id is required")
		}

		result := sendJSON(client, http.MethodPost, base+"/push", map[string]string{"artifact_id": *artifactID}, http.StatusAccepted)
		printScanResult(result, outFmt, "Push of %v queued (%v).", "artifact_id", "state")
	case "pushes":
		listGeneric(client, base+"/pushes", "pushes",
			[]string{"ARTIFACT", "STATE", "PROJECT", "VERSION", "COMPONENTS", "VEX APPLIED", "UPDATED"},
			[]string{"artifact_id", "state", "project_name", "project_version", "components", "vex_applied", "updated_at"}, outFmt)
	default:
		dief("Unknown dtrack subcommand: %s", args[0])
	}
}
