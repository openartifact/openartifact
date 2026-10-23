// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"strings"
)

const gpgKeysPath = "/api/v1/admin/gpg/keys"

// gpgActivateFlags registers --activate/--no-activate and returns a func that
// yields the tri-state (nil when neither flag was given).
func gpgActivateFlags(fs *flag.FlagSet) func() *bool {
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

func handleGPG(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa gpg <list|import|generate|delete|activate|deactivate|export>")
	}

	rest := args[1:]

	switch args[0] {
	case "list":
		gpgList(client, outFmt)
	case "import":
		gpgImport(rest, client, outFmt)
	case "generate":
		gpgGenerate(rest, client, outFmt)
	case "delete":
		gpgDelete(rest, client)
	case "activate":
		gpgActivate(rest, client, outFmt)
	case "deactivate":
		gpgDeactivate(rest, client)
	case "export":
		gpgExport(rest, client)
	default:
		dief("Unknown gpg subcommand: %s", args[0])
	}
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
		fmt.Fprintln(os.Stderr, "Warning: server cannot store keys (OPENARTIFACT_SECRET_KEY is not set); import and generate are unavailable.")
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

func gpgImport(args []string, client *Client, outFmt string) {
	fs := flag.NewFlagSet("gpg import", flag.ExitOnError)
	keyFile := fs.String("key", "", "Armored private key file, or - for stdin")
	name := fs.String("name", "", "Key name")
	passStdin := fs.Bool("passphrase-stdin", false, "Read the key passphrase from stdin (env: OA_GPG_PASSPHRASE)")
	activate := gpgActivateFlags(fs)
	_ = fs.Parse(args)

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
	if a := activate(); a != nil {
		body["activate"] = *a
	}

	gpgPrintResult(sendJSON(client, "POST", gpgKeysPath, body, http.StatusCreated), outFmt, "imported")
}

func gpgGenerate(args []string, client *Client, outFmt string) {
	fs := flag.NewFlagSet("gpg generate", flag.ExitOnError)
	name := fs.String("name", "", "Key name")
	algorithm := fs.String("algorithm", "ed25519", "Algorithm: ed25519|rsa4096")
	userName := fs.String("user-name", "", "User ID name")
	email := fs.String("email", "", "User ID email")
	comment := fs.String("comment", "", "User ID comment")
	activate := gpgActivateFlags(fs)
	_ = fs.Parse(args)

	body := map[string]any{
		"name": *name, "algorithm": *algorithm,
		"user_name": *userName, "email": *email, "comment": *comment,
	}
	if a := activate(); a != nil {
		body["activate"] = *a
	}

	gpgPrintResult(sendJSON(client, "POST", gpgKeysPath+"/generate", body, http.StatusCreated), outFmt, "generated")
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

// gpgKeyArg parses "<key> [flags]" with the key before or after the flags.
func gpgKeyArg(fs *flag.FlagSet, args []string, usage string) string {
	var ref string

	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		ref, args = args[0], args[1:]
	}

	_ = fs.Parse(args)

	if ref == "" && fs.NArg() > 0 {
		ref = fs.Arg(0)
	}

	if ref == "" {
		dief("Usage: %s", usage)
	}

	return ref
}

func gpgDelete(args []string, client *Client) {
	fs := flag.NewFlagSet("gpg delete", flag.ExitOnError)
	force := fs.Bool("force", false, "Delete even if the key is active or assigned")
	ref := gpgKeyArg(fs, args, "oa gpg delete <id|name|fingerprint> [--force]")

	path := gpgKeysPath + "/" + escape(gpgResolve(client, ref))
	if *force {
		path += "?force=true"
	}

	resp, err := client.do("DELETE", path, nil, "")
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

func gpgActivate(args []string, client *Client, outFmt string) {
	fs := flag.NewFlagSet("gpg activate", flag.ExitOnError)
	repo := fs.String("repository", "", "Repository to activate for (default: global)")
	ref := gpgKeyArg(fs, args, "oa gpg activate <key> [--repository NAME]")

	body := map[string]any{}
	if *repo != "" {
		body["repository"] = *repo
	}

	result := sendJSON(client, "PUT", gpgKeysPath+"/"+escape(gpgResolve(client, ref))+"/activate", body, http.StatusOK)
	gpgPrintResult(result, outFmt, "activated")
}

func gpgDeactivate(args []string, client *Client) {
	fs := flag.NewFlagSet("gpg deactivate", flag.ExitOnError)
	repo := fs.String("repository", "", "Repository to deactivate for (default: global)")
	ref := gpgKeyArg(fs, args, "oa gpg deactivate <key> [--repository NAME]")

	path := gpgKeysPath + "/" + escape(gpgResolve(client, ref)) + "/activate"
	if *repo != "" {
		path += "?repository=" + escape(*repo)
	}

	deleteResource(client, path, "GPG key deactivated.")
}

func gpgExport(args []string, client *Client) {
	fs := flag.NewFlagSet("gpg export", flag.ExitOnError)
	output := fs.String("output", "", "Write the public key to FILE (default: stdout)")
	ref := gpgKeyArg(fs, args, "oa gpg export <key> [--output FILE]")

	resp, err := client.do("GET", gpgKeysPath+"/"+escape(gpgResolve(client, ref))+"/export", nil, "")
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
