// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"net/http"

	"github.com/spf13/cobra"
)

// newDTrackCmd implements `oa dtrack`: the Dependency-Track integration of a
// repository (docs/security/dependency-track.md). Global settings, including the
// API key, are managed in the admin UI or through PUT /api/v1/admin/dependency-track.
func newDTrackCmd(a *app) *cobra.Command {
	repos := []completer{a.repoNames()}

	push := leafCmd("push <repo>", "Push an artifact to Dependency-Track", 1, 1, repos, nil)
	artifactID := push.Flags().String("artifact-id", "", "Artifact (name@version) to push")
	push.Run = func(_ *cobra.Command, args []string) {
		if *artifactID == "" {
			dief("--artifact-id is required")
		}

		result := sendJSON(a.client, http.MethodPost, repoPath(args[0], "dependency-track/push"),
			map[string]string{"artifact_id": *artifactID}, http.StatusAccepted)
		printScanResult(result, a.outFmt, "Push of %v queued (%v).", "artifact_id", "state")
	}

	return parentCmd("dtrack", "Dependency-Track integration of a repository",
		leafCmd("show <repo>", "Show the Dependency-Track settings of a repository", 1, 1, repos, func(_ *cobra.Command, args []string) {
			showGeneric(a.client, repoPath(args[0], "dependency-track"), a.outFmt)
		}),
		leafCmd("test <repo>", "Test the connection to Dependency-Track", 1, 1, repos, func(_ *cobra.Command, args []string) {
			result := sendJSON(a.client, http.MethodPost, repoPath(args[0], "dependency-track/test"), nil)
			printScanResult(result, a.outFmt, "Dependency-Track %v reachable.", "version")
		}),
		push,
		leafCmd("pushes <repo>", "List the artifacts pushed to Dependency-Track", 1, 1, repos, func(_ *cobra.Command, args []string) {
			listGeneric(a.client, repoPath(args[0], "dependency-track/pushes"), "pushes",
				[]string{"ARTIFACT", "STATE", "PROJECT", "VERSION", "COMPONENTS", "VEX APPLIED", "UPDATED"},
				[]string{"artifact_id", "state", "project_name", "project_version", "components", "vex_applied", "updated_at"}, a.outFmt)
		}),
	)
}
