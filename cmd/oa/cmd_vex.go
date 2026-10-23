// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
)

// handleVEX implements `oa vex`: VEX documents built from the scan results and
// the admin VEX decisions of a repository.
func handleVEX(args []string, client *Client, outFmt string) {
	if len(args) < 2 {
		dief("Usage: oa vex <get|list|set|delete> <repo> [...]")
	}

	base := "/api/v1/repositories/" + escape(args[1]) + "/vex"

	switch args[0] {
	case "get":
		fs := flag.NewFlagSet("vex get", flag.ExitOnError)
		artifactID := fs.String("artifact-id", "", "Only this artifact (default: every scanned artifact of the repository)")
		format := fs.String("format", "openvex", "VEX format: openvex|cyclonedx")
		spec := fs.String("spec-version", "", "CycloneDX spec version (1.4|1.5|1.6)")
		output := fs.String("output", "", "Write the document to this file instead of stdout")
		attest := fs.String("attest", "", "Return a signed in-toto attestation instead: true (DSSE envelope) or bundle (Sigstore bundle, for cosign)")
		_ = fs.Parse(args[2:])

		vexGet(client, base, url.Values{"format": {*format}, "spec_version": {*spec}, "artifact_id": {*artifactID}, "attest": {*attest}}, *output)
	case "list":
		fs := flag.NewFlagSet("vex list", flag.ExitOnError)
		artifactID := fs.String("artifact-id", "", "Only this artifact")
		_ = fs.Parse(args[2:])

		path := base + "/statements"
		if *artifactID != "" {
			path += "?artifact_id=" + url.QueryEscape(*artifactID)
		}

		listGeneric(client, path, "statements", []string{"ID", "ARTIFACT", "VULNERABILITY", "STATUS", "JUSTIFICATION", "UPDATED"},
			[]string{"id", "artifact_id", "vuln_id", "status", "justification", "updated_at"}, outFmt)
	case "set":
		fs := flag.NewFlagSet("vex set", flag.ExitOnError)
		artifactID := fs.String("artifact-id", "", "Artifact (name@version)")
		vuln := fs.String("vuln", "", "Vulnerability id (CVE-..., GHSA-...)")
		status := fs.String("status", "", "affected|not_affected|fixed|under_investigation")
		justification := fs.String("justification", "", "Required for not_affected: component_not_present|vulnerable_code_not_present|"+
			"vulnerable_code_not_in_execute_path|vulnerable_code_cannot_be_controlled_by_adversary|inline_mitigations_already_exist")
		impact := fs.String("impact", "", "Impact statement (why the artifact is not affected)")
		action := fs.String("action", "", "Action statement (what consumers should do)")
		_ = fs.Parse(args[2:])

		if *artifactID == "" || *vuln == "" || *status == "" {
			dief("--artifact-id, --vuln and --status are required")
		}

		result := sendJSON(client, "PUT", base+"/statements", map[string]string{
			"artifact_id": *artifactID, "vuln_id": *vuln, "status": *status,
			"justification": *justification, "impact_statement": *impact, "action_statement": *action,
		})
		printScanResult(result, outFmt, "VEX decision %v recorded (%v).", "id", "status")
	case "delete":
		needArgs(args, 3, "Usage: oa vex delete <repo> <id>")
		deleteResource(client, base+"/statements/"+escape(args[2]), "VEX decision deleted.")
	default:
		dief("Unknown vex subcommand: %s", args[0])
	}
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
