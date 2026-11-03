// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/spf13/cobra"
)

// vexStatuses and vexJustifications are the values the server accepts for a
// VEX decision (internal/api/vex.go, OpenVEX); hardcoded because internal/oacli must
// not import server packages.
//
//nolint:gochecknoglobals // immutable lists
var (
	vexStatuses       = []string{"affected", "not_affected", "fixed", "under_investigation"}
	vexJustifications = []string{
		"component_not_present",
		"vulnerable_code_not_present",
		"vulnerable_code_not_in_execute_path",
		"vulnerable_code_cannot_be_controlled_by_adversary",
		"inline_mitigations_already_exist",
	}
)

// newVEXCmd implements `oa vex`: VEX documents built from the scan results and
// the admin VEX decisions of a repository.
func newVEXCmd(a *app) *cobra.Command {
	repos := []completer{a.repoNames()}

	list := leafCmd("list <repo>", "List recorded VEX decisions", 1, 1, repos, nil)
	listArtifact := list.Flags().String("artifact-id", "", "Only this artifact")
	list.Run = func(_ *cobra.Command, args []string) {
		path := repoPath(args[0], "vex/statements")
		if *listArtifact != "" {
			path += "?artifact_id=" + url.QueryEscape(*listArtifact)
		}

		listGeneric(a.client, path, "statements", []string{"ID", "ARTIFACT", "VULNERABILITY", "STATUS", "JUSTIFICATION", "UPDATED"},
			[]string{"id", "artifact_id", "vuln_id", "status", "justification", "updated_at"}, a.outFmt)
	}

	return parentCmd(
		"vex",
		"VEX documents and decisions",
		vexGetCmd(a),
		list,
		vexSetCmd(a),
		leafCmd(
			"delete <repo> <id>",
			"Delete a VEX decision",
			2,
			2,
			a.repoScanIDs("vex/statements", "statements"),
			func(_ *cobra.Command, args []string) {
				deleteResource(a.client, repoPath(args[0], "vex/statements/"+escape(args[1])), "VEX decision deleted.")
			},
		),
	)
}

// vexGetCmd defines its own --format (the VEX format), which shadows the global
// output format after the command name.
func vexGetCmd(a *app) *cobra.Command {
	cmd := leafCmd("get <repo>", "Get the VEX document of an artifact or the whole repository", 1, 1, []completer{a.repoNames()}, nil)
	cmd.Long = "Get the VEX document of an artifact or the whole repository; it goes to stdout without --output.\n\n" +
		"After the command name, --format is the VEX format; to choose the output format " +
		"put the global flag before the command: oa --format json vex get REPO."

	fl := cmd.Flags()
	artifactID := fl.String("artifact-id", "", "Only this artifact (default: every scanned artifact of the repository)")
	format := fl.String("format", "openvex", "VEX format: openvex|cyclonedx")
	spec := fl.String("spec-version", "", "CycloneDX spec version (1.4|1.5|1.6)")
	output := fl.String("output", "", "Write the document to this file instead of stdout")
	attest := fl.String(
		"attest",
		"",
		"Return a signed in-toto attestation instead: true (DSSE envelope) or bundle (Sigstore bundle, for cosign)",
	)

	flagEnum(cmd, "format", "openvex", "cyclonedx")
	flagEnum(cmd, "spec-version", "1.4", "1.5", "1.6")
	flagEnum(cmd, "attest", attestValues...)
	flagFiles(cmd, "output")

	cmd.Run = func(_ *cobra.Command, args []string) {
		vexGet(
			a.client,
			repoPath(args[0], "vex"),
			url.Values{"format": {*format}, "spec_version": {*spec}, "artifact_id": {*artifactID}, "attest": {*attest}},
			*output,
		)
	}

	return cmd
}

func vexSetCmd(a *app) *cobra.Command {
	cmd := leafCmd("set <repo>", "Record a VEX decision (not_affected requires --justification)", 1, 1, []completer{a.repoNames()}, nil)

	fl := cmd.Flags()
	artifactID := fl.String("artifact-id", "", "Artifact (name@version)")
	vuln := fl.String("vuln", "", "Vulnerability id (CVE-..., GHSA-...)")
	status := fl.String("status", "", "affected|not_affected|fixed|under_investigation")
	justification := fl.String("justification", "", "Required for not_affected: component_not_present|vulnerable_code_not_present|"+
		"vulnerable_code_not_in_execute_path|vulnerable_code_cannot_be_controlled_by_adversary|inline_mitigations_already_exist")
	impact := fl.String("impact", "", "Impact statement (why the artifact is not affected)")
	action := fl.String("action", "", "Action statement (what consumers should do)")

	flagEnum(cmd, "status", vexStatuses...)
	flagEnum(cmd, "justification", vexJustifications...)

	cmd.Run = func(_ *cobra.Command, args []string) {
		if *artifactID == "" || *vuln == "" || *status == "" {
			dief("--artifact-id, --vuln and --status are required")
		}

		result := sendJSON(a.client, "PUT", repoPath(args[0], "vex/statements"), map[string]string{
			"artifact_id": *artifactID, "vuln_id": *vuln, "status": *status,
			"justification": *justification, "impact_statement": *impact, "action_statement": *action,
		})
		printScanResult(result, a.outFmt, "VEX decision %v recorded (%v).", "id", "status")
	}

	return cmd
}

// vexGet streams the VEX document to stdout or to output.
func vexGet(client *Client, base string, q url.Values, output string) {
	for k, v := range q {
		if len(v) == 0 || v[0] == "" {
			q.Del(k)
		}
	}

	path := base
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	resp, err := client.do(http.MethodGet, path, nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)

	if output == "" {
		if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
			dief("%v", err)
		}

		fmt.Println()

		return
	}

	n, err := downloadTo(resp, output)
	if err != nil {
		dief("%v", err)
	}

	fmt.Printf("Wrote %d bytes to '%s'.\n", n, output)
}
