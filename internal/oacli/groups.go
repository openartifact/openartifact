// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import "github.com/spf13/cobra"

// helpGroup is one heading of `oa --help`: the top-level commands under it.
type helpGroup struct {
	id, title string
	commands  []string
}

// helpGroups orders the top-level commands in the help and in docs/reference/cli.md.
func helpGroups() []helpGroup {
	return []helpGroup{
		{"auth", "Auth", []string{"auth"}},
		{"repos", "Repositories and packages", []string{"repo", "trash", "artifact", "search", "props", "quota", "cleanup", "delta"}},
		{"admin", "Administration", []string{"user", "group", "project", "token", "gpg", "storage", "backup", "audit", "system"}},
		{"security", "Security and integrations", []string{"webhook", "sbom", "vex", "dtrack", "vuln", "license", "secret"}},
		{"ci", "CI and delivery", []string{"upload", "bundle", "promote"}},
		{"config", "Configuration and migration", []string{"apply", "diff", "export", "import"}},
		{"utility", "Utility", []string{"version", "completion"}},
	}
}

// addCommandGroups adds every command group of the CLI to root.
func addCommandGroups(root *cobra.Command, a *app) {
	root.AddCommand(
		newAuthCmd(a),
		newRepoCmd(a),
		newTrashCmd(a),
		newArtifactCmd(a),
		newSearchCmd(a),
		newPropsCmd(a),
		newQuotaCmd(a),
		newCleanupCmd(a),
		newDeltaCmd(a),
		newUserCmd(a),
		newGroupCmd(a),
		newProjectCmd(a),
		newTokenCmd(a),
		newGPGCmd(a),
		newStorageCmd(a),
		newBackupCmd(a),
		newAuditCmd(a),
		newSystemCmd(a),
		newWebhookCmd(a),
		newSBOMCmd(a),
		newVEXCmd(a),
		newDTrackCmd(a),
		newVulnCmd(a),
		newLicenseCmd(a),
		newSecretCmd(a),
		newUploadCmd(a),
		newBundleCmd(a),
		newPromoteCmd(a),
		newApplyCmd(a),
		newDiffCmd(a),
		newExportCmd(a),
		newImportCmd(a),
	)

	for _, g := range helpGroups() {
		root.AddGroup(&cobra.Group{ID: g.id, Title: g.title + ":"})

		for _, name := range g.commands {
			if c := findChild(root, name); c != nil {
				c.GroupID = g.id
			}
		}
	}

	root.SetHelpCommandGroupID("utility")
}
