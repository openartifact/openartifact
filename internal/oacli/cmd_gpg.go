// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const gpgKeysPath = "/api/v1/admin/gpg/keys"

// gpgAlgorithms are the key algorithms `gpg generate` offers (source: the GPG
// key generation API, which internal/oacli must not import).
//
//nolint:gochecknoglobals // immutable lookup table
var gpgAlgorithms = []string{"ed25519", "rsa4096"}

// gpgActivateFlags registers --activate/--no-activate and returns a func that
// yields the tri-state (nil when neither flag was given).
func gpgActivateFlags(fs *pflag.FlagSet) func() *bool {
	on := fs.Bool("activate", false, "Make the key the active signing key")
	off := fs.Bool("no-activate", false, "Do not activate the key")

	return func() *bool {
		if *on && *off {
			dief("--activate and --no-activate are mutually exclusive")
		}

		if !*on && !*off {
			return nil
		}

		v := *on

		return &v
	}
}

// gpgKeys completes a key reference by name.
func (a *app) gpgKeys() completer {
	return a.listNames(gpgKeysPath, "keys", "name")
}

func newGPGCmd(a *app) *cobra.Command {
	return parentCmd("gpg", "Manage GPG signing keys (admin)",
		&cobra.Command{
			Use:   "list",
			Short: "List GPG keys",
			Args:  usageArgs(0, 0),
			Run:   func(*cobra.Command, []string) { gpgList(a.client, a.outFmt) },
		},
		gpgImportCmd(a),
		gpgGenerateCmd(a),
		gpgDeleteCmd(a),
		gpgActivateCmd(a),
		gpgDeactivateCmd(a),
		gpgExportCmd(a),
	)
}

// gpgFetchKeys returns the key list and the can_store_keys capability.
func gpgFetchKeys(client *Client) ([]map[string]any, bool) {
	resp, err := client.do("GET", gpgKeysPath, nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)

	body, _ := io.ReadAll(resp.Body)

	items, err := listItems(body, "keys")
	if err != nil {
		dief("%v", err)
	}

	can := true

	var meta struct {
		CanStore *bool `json:"can_store_keys"`
	}

	if json.Unmarshal(body, &meta) == nil && meta.CanStore != nil {
		can = *meta.CanStore
	}

	return items, can
}

func gpgActiveLabel(k map[string]any) string {
	var parts []string

	if g, _ := k["active_global"].(bool); g {
		parts = append(parts, "global")
	}

	if repos, ok := k["repositories"].([]any); ok {
		for _, r := range repos {
			parts = append(parts, fmt.Sprint(r))
		}
	}

	if len(parts) == 0 {
		return "-"
	}

	return strings.Join(parts, ",")
}

func gpgList(client *Client, outFmt string) {
	keys, can := gpgFetchKeys(client)
	if !can {
		fmt.Fprintln(
			os.Stderr,
			"Warning: server cannot store keys (OPENARTIFACT_SECRET_KEY is not set); import and generate are unavailable.",
		)
	}

	if outFmt == "json" {
		printTable(keys, nil, nil, outFmt)

		return
	}

	rows := make([]map[string]any, 0, len(keys))

	for _, k := range keys {
		row := maps.Clone(k)

		row["active"] = gpgActiveLabel(k)
		rows = append(rows, row)
	}

	printTable(rows, []string{"ID", "NAME", "FINGERPRINT", "ALGORITHM", "SOURCE", "ACTIVE"},
		[]string{"id", "name", "fingerprint", "algorithm", "source", "active"}, outFmt)
}

func gpgPrintResult(result map[string]any, outFmt, verb string) {
	if outFmt == "json" {
		printJSON(result)

		return
	}

	fmt.Printf("GPG key %s: %v\n", verb, result["fingerprint"])
}

func gpgImportCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import --key <file|->",
		Short: "Import a private key (passphrase: stdin or env OA_GPG_PASSPHRASE)",
		Args:  usageArgs(0, 0),
	}

	keyFile := cmd.Flags().String("key", "", "Armored private key file, or - for stdin")
	name := cmd.Flags().String("name", "", "Key name")
	passStdin := cmd.Flags().Bool("passphrase-stdin", false, "Read the key passphrase from stdin (env: OA_GPG_PASSPHRASE)")
	activate := gpgActivateFlags(cmd.Flags())

	flagFiles(cmd, "key")

	cmd.Run = func(*cobra.Command, []string) {
		if *keyFile == "" {
			dief("--key is required")
		}

		if *passStdin && *keyFile == "-" {
			dief("--passphrase-stdin cannot be combined with --key -")
		}

		var (
			data []byte
			err  error
		)

		if *keyFile == "-" {
			data, err = io.ReadAll(stdinReader)
		} else {
			data, err = os.ReadFile(*keyFile)
		}

		if err != nil {
			dief("reading key: %v", err)
		}

		passphrase := os.Getenv("OA_GPG_PASSPHRASE")
		if *passStdin {
			line, rerr := readLine(stdinReader)
			if rerr != nil && line == "" {
				dief("reading passphrase from stdin: %v", rerr)
			}

			passphrase = line
		}

		body := map[string]any{"name": *name, "private_key": string(data), "passphrase": passphrase}
		if act := activate(); act != nil {
			body["activate"] = *act
		}

		gpgPrintResult(sendJSON(a.client, "POST", gpgKeysPath, body, http.StatusCreated), a.outFmt, "imported")
	}

	return cmd
}

func gpgGenerateCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate a signing key",
		Args:  usageArgs(0, 0),
	}

	name := cmd.Flags().String("name", "", "Key name")
	algorithm := cmd.Flags().String("algorithm", "ed25519", "Algorithm: ed25519|rsa4096")
	userName := cmd.Flags().String("user-name", "", "User ID name")
	email := cmd.Flags().String("email", "", "User ID email")
	comment := cmd.Flags().String("comment", "", "User ID comment")
	activate := gpgActivateFlags(cmd.Flags())

	flagEnum(cmd, "algorithm", gpgAlgorithms...)

	cmd.Run = func(*cobra.Command, []string) {
		body := map[string]any{
			"name": *name, "algorithm": *algorithm,
			"user_name": *userName, "email": *email, "comment": *comment,
		}
		if act := activate(); act != nil {
			body["activate"] = *act
		}

		gpgPrintResult(sendJSON(a.client, "POST", gpgKeysPath+"/generate", body, http.StatusCreated), a.outFmt, "generated")
	}

	return cmd
}

// gpgResolve maps an id, fingerprint or exact name to the key id.
func gpgResolve(client *Client, ref string) string {
	keys, _ := gpgFetchKeys(client)

	var ids []string

	for _, k := range keys {
		id, _ := k["id"].(string)
		fp, _ := k["fingerprint"].(string)
		nm, _ := k["name"].(string)

		if id == ref || strings.EqualFold(fp, ref) {
			return id
		}

		if nm == ref {
			ids = append(ids, id)
		}
	}

	switch len(ids) {
	case 1:
		return ids[0]
	case 0:
		dief("no GPG key matches %q", ref)
	default:
		dief("%q matches %d keys; use the id or fingerprint", ref, len(ids))
	}

	return ""
}

func gpgDeleteCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "delete <key>",
		Short:             "Delete a key (id, name or fingerprint)",
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(a.gpgKeys()),
	}

	force := cmd.Flags().Bool("force", false, "Delete even if the key is active or assigned")

	cmd.Run = func(_ *cobra.Command, args []string) {
		path := gpgKeysPath + "/" + escape(gpgResolve(a.client, args[0]))
		if *force {
			path += "?force=true"
		}

		resp, err := a.client.do("DELETE", path, nil, "")
		if err != nil {
			dief("%v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode == http.StatusConflict && !*force {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
			if bytes.Contains(body, []byte("key_in_use")) {
				dief("the key is active or assigned to a repository; deactivate it first or run again with --force")
			}

			resp.Body = io.NopCloser(bytes.NewReader(body))
		}

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Println("GPG key deleted.")
	}

	return cmd
}

func gpgActivateCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "activate <key>",
		Short:             "Make a key the signer (global or per deb/rpm repository)",
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(a.gpgKeys()),
	}

	repo := cmd.Flags().String("repository", "", "Repository to activate for (default: global)")

	flagValues(cmd, "repository", a.repoNames())

	cmd.Run = func(_ *cobra.Command, args []string) {
		body := map[string]any{}
		if *repo != "" {
			body["repository"] = *repo
		}

		result := sendJSON(a.client, "PUT", gpgKeysPath+"/"+escape(gpgResolve(a.client, args[0]))+"/activate", body, http.StatusOK)
		gpgPrintResult(result, a.outFmt, "activated")
	}

	return cmd
}

func gpgDeactivateCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "deactivate <key>",
		Short:             "Stop using a key for signing",
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(a.gpgKeys()),
	}

	repo := cmd.Flags().String("repository", "", "Repository to deactivate for (default: global)")

	flagValues(cmd, "repository", a.repoNames())

	cmd.Run = func(_ *cobra.Command, args []string) {
		path := gpgKeysPath + "/" + escape(gpgResolve(a.client, args[0])) + "/activate"
		if *repo != "" {
			path += "?repository=" + escape(*repo)
		}

		deleteResource(a.client, path, "GPG key deactivated.")
	}

	return cmd
}

func gpgExportCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "export <key>",
		Short:             "Export the public key",
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(a.gpgKeys()),
	}

	output := cmd.Flags().String("output", "", "Write the public key to FILE (default: stdout)")

	flagFiles(cmd, "output")

	cmd.Run = func(_ *cobra.Command, args []string) {
		resp, err := a.client.do("GET", gpgKeysPath+"/"+escape(gpgResolve(a.client, args[0]))+"/export", nil, "")
		if err != nil {
			dief("%v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		checkOK(resp)

		data, err := io.ReadAll(resp.Body)
		if err != nil {
			dief("reading export: %v", err)
		}

		if *output == "" {
			_, _ = os.Stdout.Write(data)

			return
		}

		//nolint:gosec // public key, world-readable by design
		if err := os.WriteFile(*output, data, 0o644); err != nil {
			dief("writing %s: %v", *output, err)
		}

		fmt.Printf("Public key written to %s\n", *output)
	}

	return cmd
}
