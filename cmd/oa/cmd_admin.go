// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ═══════════════════════════════════════════════════════════════════════════════
// USER (admin)
// ═══════════════════════════════════════════════════════════════════════════════

func handleUser(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa user <list|create|show|update|delete>")
	}

	switch args[0] {
	case "list":
		users := fetchAllPages(client, "/api/v1/users", "data", "per_page", 200)
		printTable(users, []string{"ID", "USERNAME", "EMAIL", "DISABLED"}, []string{"id", "username", "email", "disabled"}, outFmt)
	case "create":
		userCreate(client, args[1:])
	case "show":
		if len(args) < 2 {
			dief("Usage: oa user show <id>")
		}

		showGeneric(client, "/api/v1/users/"+escape(args[1]), outFmt)
	case "update":
		if len(args) < 2 {
			dief("Usage: oa user update <id> [--email E] [--full-name N] [--disabled[=false]]")
		}

		userUpdate(client, args[1], args[2:])
	case "delete":
		if len(args) < 2 {
			dief("Usage: oa user delete <id>")
		}

		deleteResource(client, "/api/v1/users/"+escape(args[1]), fmt.Sprintf("User '%s' deleted.", args[1]))
	default:
		dief("Unknown user subcommand: %s", args[0])
	}
}

// deleteResource issues a DELETE and prints msg on success.
func deleteResource(client *Client, path, msg string) {
	resp, err := client.do("DELETE", path, nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp, http.StatusOK, http.StatusNoContent)
	fmt.Println(msg)
}

// postEmpty issues a body-less POST and prints msg on success.
func postEmpty(client *Client, path, msg string, expected ...int) {
	resp, err := client.do("POST", path, nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp, expected...)
	fmt.Println(msg)
}

// userCreate creates an account. The server has no admin flag on create:
// administrator rights come from a permission-target assignment.
func userCreate(client *Client, args []string) {
	fs := flag.NewFlagSet("user create", flag.ExitOnError)
	username := fs.String("username", "", "Username")
	email := fs.String("email", "", "Email")
	passwordStdin := fs.Bool("password-stdin", false, "Read the password from stdin")
	legacyPassword := fs.String("password", "", "removed; use --password-stdin")
	fullName := fs.String("full-name", "", "Full name")
	_ = fs.Parse(args)

	if *username == "" || *email == "" {
		dief("--username and --email are required")
	}

	if *legacyPassword != "" {
		dief("%v", errPasswordFlag)
	}

	var password string
	if *passwordStdin {
		password = readPasswordStdin()
	} else {
		password = readPassword("Password: ")
	}

	body := map[string]any{
		"username": *username,
		"email":    *email,
		"password": password,
	}
	if *fullName != "" {
		body["full_name"] = *fullName
	}

	sendJSON(client, "POST", "/api/v1/users", body, http.StatusCreated)
	fmt.Printf("User '%s' created.\n", *username)
}

func userUpdate(client *Client, id string, args []string) {
	fs := flag.NewFlagSet("user update", flag.ExitOnError)
	email := fs.String("email", "", "Email")
	fullName := fs.String("full-name", "", "Full name")
	disabled := fs.Bool("disabled", false, "Disable the user (--disabled=false to re-enable)")
	_ = fs.Parse(args)

	set := setFlags(fs)
	body := map[string]any{}

	if set["email"] {
		body["email"] = *email
	}

	if set["full-name"] {
		body["full_name"] = *fullName
	}

	if set["disabled"] {
		body["disabled"] = *disabled
	}

	if len(body) == 0 {
		dief("nothing to update: pass --email, --full-name and/or --disabled[=false]")
	}

	sendJSON(client, "PATCH", "/api/v1/users/"+escape(id), body, http.StatusOK)
	fmt.Printf("User '%s' updated.\n", id)
}

// ═══════════════════════════════════════════════════════════════════════════════
// GROUP (admin)
// ═══════════════════════════════════════════════════════════════════════════════

func handleGroup(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa group <list|create|show|delete|members|add-member|rm-member>")
	}

	switch args[0] {
	case "list":
		listGeneric(client, "/api/v1/groups", "data", []string{"ID", "NAME", "DESCRIPTION"}, []string{"id", "name", "description"}, outFmt)
	case "create":
		if len(args) < 2 {
			dief("Usage: oa group create <name> [--description D]")
		}

		fs := flag.NewFlagSet("group create", flag.ExitOnError)
		desc := fs.String("description", "", "Description")
		_ = fs.Parse(args[2:])

		body := map[string]any{"name": args[1]}
		if *desc != "" {
			body["description"] = *desc
		}

		sendJSON(client, "POST", "/api/v1/groups", body, http.StatusCreated)
		fmt.Printf("Group '%s' created.\n", args[1])
	case "show":
		if len(args) < 2 {
			dief("Usage: oa group show <id>")
		}

		showGeneric(client, "/api/v1/groups/"+escape(args[1]), outFmt)
	case "delete":
		if len(args) < 2 {
			dief("Usage: oa group delete <id>")
		}

		deleteResource(client, "/api/v1/groups/"+escape(args[1]), fmt.Sprintf("Group '%s' deleted.", args[1]))
	case "members":
		if len(args) < 2 {
			dief("Usage: oa group members <id>")
		}

		listGeneric(client, "/api/v1/groups/"+escape(args[1])+"/members", "data",
			[]string{"USER_ID", "USERNAME", "EMAIL"}, []string{"id", "username", "email"}, outFmt)
	case "add-member":
		if len(args) < 3 {
			dief("Usage: oa group add-member <id> <user-id>")
		}

		postEmpty(client, "/api/v1/groups/"+escape(args[1])+"/members/"+escape(args[2]), "Member added.",
			http.StatusOK, http.StatusNoContent, http.StatusCreated)
	case "rm-member":
		if len(args) < 3 {
			dief("Usage: oa group rm-member <id> <user-id>")
		}

		deleteResource(client, "/api/v1/groups/"+escape(args[1])+"/members/"+escape(args[2]), "Member removed.")
	default:
		dief("Unknown group subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ORG
// ═══════════════════════════════════════════════════════════════════════════════

func handleOrg(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa org <list|create|show|delete|members|add-member|rm-member>")
	}

	switch args[0] {
	case "list":
		listGeneric(client, "/api/v1/orgs", "organizations", []string{"NAME", "DISPLAY_NAME"}, []string{"name", "display_name"}, outFmt)
	case "create":
		if len(args) < 2 {
			dief("Usage: oa org create <name> [--display-name N] [--description D]")
		}

		fs := flag.NewFlagSet("org create", flag.ExitOnError)
		displayName := fs.String("display-name", "", "Display name")
		desc := fs.String("description", "", "Description")
		_ = fs.Parse(args[2:])

		sendJSON(client, "POST", "/api/v1/orgs", map[string]any{
			"name":         args[1],
			"display_name": *displayName,
			"description":  *desc,
		}, http.StatusCreated)
		fmt.Printf("Organization '%s' created.\n", args[1])
	case "show":
		if len(args) < 2 {
			dief("Usage: oa org show <name>")
		}

		showGeneric(client, "/api/v1/orgs/"+escape(args[1]), outFmt)
	case "delete":
		if len(args) < 2 {
			dief("Usage: oa org delete <name>")
		}

		deleteResource(client, "/api/v1/orgs/"+escape(args[1]), fmt.Sprintf("Organization '%s' deleted.", args[1]))
	case "members":
		if len(args) < 2 {
			dief("Usage: oa org members <name>")
		}

		listGeneric(client, "/api/v1/orgs/"+escape(args[1])+"/members", "members",
			[]string{"USER_ID", "ROLE"}, []string{"user_id", "role"}, outFmt)
	case "add-member":
		if len(args) < 3 {
			dief("Usage: oa org add-member <name> <user-id> [--role member|owner]")
		}

		fs := flag.NewFlagSet("org add-member", flag.ExitOnError)
		role := fs.String("role", "member", "Role: member|owner")
		_ = fs.Parse(args[3:])

		sendJSON(client, "POST", "/api/v1/orgs/"+escape(args[1])+"/members", map[string]string{
			"user_id": args[2],
			"role":    *role,
		}, http.StatusCreated)
		fmt.Println("Member added.")
	case "rm-member":
		if len(args) < 3 {
			dief("Usage: oa org rm-member <name> <user-id>")
		}

		deleteResource(client, "/api/v1/orgs/"+escape(args[1])+"/members/"+escape(args[2]), "Member removed.")
	default:
		dief("Unknown org subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TOKEN (admin)
// ═══════════════════════════════════════════════════════════════════════════════

func handleToken(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa token <list|revoke>")
	}

	switch args[0] {
	case "list":
		tokens := fetchAllPages(client, "/api/v1/admin/tokens", "data", "per_page", 100)
		printTable(tokens, []string{"ID", "NAME", "KIND", "USER_ID", "CREATED"},
			[]string{"id", "name", "kind", "user_id", "created_at"}, outFmt)
	case "revoke":
		if len(args) < 2 {
			dief("Usage: oa token revoke <id>")
		}

		deleteResource(client, "/api/v1/admin/tokens/"+escape(args[1]), "Token revoked.")
	default:
		dief("Unknown token subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// GPG
// ═══════════════════════════════════════════════════════════════════════════════

func handleGPG(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa gpg <list|import|generate|delete>")
	}

	switch args[0] {
	case "list":
		listGeneric(client, "/api/v1/admin/gpg/keys", "keys", []string{"FINGERPRINT", "NAME", "EMAIL"}, []string{"fingerprint", "name", "email"}, outFmt)
	case "import":
		fs := flag.NewFlagSet("gpg import", flag.ExitOnError)
		keyFile := fs.String("key", "", "Path to armored GPG key file")
		_ = fs.Parse(args[1:])

		if *keyFile == "" {
			dief("--key is required")
		}

		data, err := os.ReadFile(*keyFile)
		if err != nil {
			dief("reading key file: %v", err)
		}

		sendJSON(client, "POST", "/api/v1/admin/gpg/keys", map[string]string{"armored_key": string(data)}, http.StatusCreated)
		fmt.Println("GPG key imported.")
	case "generate":
		fs := flag.NewFlagSet("gpg generate", flag.ExitOnError)
		name := fs.String("name", "", "Name")
		email := fs.String("email", "", "Email")
		bits := fs.Int("bits", 4096, "Key size in bits")
		_ = fs.Parse(args[1:])

		if *name == "" || *email == "" {
			dief("--name and --email are required")
		}

		result := sendJSON(client, "POST", "/api/v1/admin/gpg/keys/generate", map[string]any{
			"name":  *name,
			"email": *email,
			"bits":  *bits,
		}, http.StatusCreated)

		if outFmt == "json" {
			printJSON(result)
			return
		}

		fmt.Printf("GPG key generated: %v\n", result["fingerprint"])
	case "delete":
		if len(args) < 2 {
			dief("Usage: oa gpg delete <fingerprint>")
		}

		deleteResource(client, "/api/v1/admin/gpg/keys/"+escape(args[1]), "GPG key deleted.")
	default:
		dief("Unknown gpg subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// STORAGE BACKEND (admin)
// ═══════════════════════════════════════════════════════════════════════════════

// secretConfigMarkers are substrings (after dropping '_' and '-', lowercased)
// that mark a storage config key as a credential. The server encrypts these at
// rest; on a command line they would sit in the process list.
//
//nolint:gochecknoglobals // immutable lookup table
var secretConfigMarkers = []string{"secret", "password", "key", "token", "cred", "connstr", "sas"}

// findSecretConfigKey returns the first credential-looking key in a decoded
// config (searching nested objects), or "".
func findSecretConfigKey(v any) string {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			norm := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(k))
			for _, m := range secretConfigMarkers {
				if strings.Contains(norm, m) {
					return k
				}
			}

			if found := findSecretConfigKey(val); found != "" {
				return found
			}
		}
	case []any:
		for _, val := range t {
			if found := findSecretConfigKey(val); found != "" {
				return found
			}
		}
	}

	return ""
}

// storageConfig builds the config object for storage create/update. Config
// that holds credentials must come from --config-file (or "-" for stdin);
// inline --config is accepted only for non-secret settings. It returns nil when
// neither flag was given.
func storageConfig(inline, file string) map[string]any {
	if inline != "" && file != "" {
		dief("use either --config or --config-file, not both")
	}

	var raw []byte

	switch {
	case file == "-":
		data, err := io.ReadAll(stdinReader)
		if err != nil {
			dief("reading config from stdin: %v", err)
		}

		raw = data
	case file != "":
		data, err := os.ReadFile(file)
		if err != nil {
			dief("reading --config-file: %v", err)
		}

		raw = data
	case inline != "":
		raw = []byte(inline)
	default:
		return nil
	}

	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		dief("config is not a valid JSON object: %v", err)
	}

	if file == "" {
		if key := findSecretConfigKey(cfg); key != "" {
			dief("refusing inline --config: %q looks like a credential and would be visible in the process list; put the config in a file and use --config-file (or --config-file - to read stdin)", key)
		}
	}

	return cfg
}

func handleStorage(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa storage <list|create|show|update|delete|test>")
	}

	switch args[0] {
	case "list":
		listGeneric(client, "/api/v1/admin/storage", "items",
			[]string{"NAME", "KIND", "DEFAULT", "STATUS"}, []string{"name", "kind", "is_default", "status"}, outFmt)
	case "create":
		fs := flag.NewFlagSet("storage create", flag.ExitOnError)
		name := fs.String("name", "", "Backend name")
		sType := fs.String("type", "filesystem", "Backend type: filesystem|s3|gcs|azure|garage|seaweedfs|oci")
		config := fs.String("config", "", "Inline JSON config (non-secret settings only)")
		configFile := fs.String("config-file", "", "File with the JSON config, or - for stdin (use this for credentials)")
		isDefault := fs.Bool("default", false, "Set as default")
		_ = fs.Parse(args[1:])

		if *name == "" {
			dief("--name is required")
		}

		cfg := storageConfig(*config, *configFile)
		if cfg == nil {
			cfg = map[string]any{}
		}

		sendJSON(client, "POST", "/api/v1/admin/storage", map[string]any{
			"name":       *name,
			"kind":       *sType,
			"config":     cfg,
			"is_default": *isDefault,
		}, http.StatusCreated)
		fmt.Printf("Storage backend '%s' created.\n", *name)
	case "show":
		if len(args) < 2 {
			dief("Usage: oa storage show <name>")
		}

		showGeneric(client, "/api/v1/admin/storage/"+escape(args[1]), outFmt)
	case "update":
		if len(args) < 2 {
			dief("Usage: oa storage update <name> [--config JSON | --config-file F] [--default[=false]]")
		}

		storageUpdate(client, args[1], args[2:])
	case "delete":
		if len(args) < 2 {
			dief("Usage: oa storage delete <name>")
		}

		deleteResource(client, "/api/v1/admin/storage/"+escape(args[1]), fmt.Sprintf("Storage backend '%s' deleted.", args[1]))
	case "test":
		if len(args) < 2 {
			dief("Usage: oa storage test <name>")
		}

		storageTest(client, args[1])
	default:
		dief("Unknown storage subcommand: %s", args[0])
	}
}

func storageUpdate(client *Client, name string, args []string) {
	fs := flag.NewFlagSet("storage update", flag.ExitOnError)
	config := fs.String("config", "", "Inline JSON config (non-secret settings only)")
	configFile := fs.String("config-file", "", "File with the JSON config, or - for stdin (use this for credentials)")
	isDefault := fs.Bool("default", false, "Set as default (--default=false to unset)")
	_ = fs.Parse(args)

	set := setFlags(fs)
	body := map[string]any{}

	if set["default"] {
		body["is_default"] = *isDefault
	}

	if cfg := storageConfig(*config, *configFile); cfg != nil {
		body["config"] = cfg
	}

	if len(body) == 0 {
		dief("nothing to update: pass --config, --config-file and/or --default[=false]")
	}

	sendJSON(client, "PATCH", "/api/v1/admin/storage/"+escape(name), body, http.StatusOK)
	fmt.Printf("Storage backend '%s' updated.\n", name)
}

// storageTest runs the server-side connectivity test. The server answers 200
// with {"ok": false, ...} when the backend is unreachable, so the body decides.
func storageTest(client *Client, name string) {
	var result struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}

	resp, err := client.do("POST", "/api/v1/admin/storage/"+escape(name)+"/test", nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)
	decodeJSON(resp, &result)

	if !result.OK {
		dief("storage backend test failed: %s (%s)", result.Message, result.Error)
	}

	fmt.Println("Storage backend test passed.")
}

// ═══════════════════════════════════════════════════════════════════════════════
// BACKUP (admin)
// ═══════════════════════════════════════════════════════════════════════════════

func handleBackup(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa backup <list|create|show|download|restore>")
	}

	switch args[0] {
	case "list":
		listGeneric(client, "/api/v1/admin/backup", "backups",
			[]string{"ID", "TYPE", "STATUS", "SIZE", "CREATED"}, []string{"id", "type", "status", "size_bytes", "created_at"}, outFmt)
	case "create":
		fs := flag.NewFlagSet("backup create", flag.ExitOnError)
		bType := fs.String("type", "full", "Backup type: full|metadata-only")
		_ = fs.Parse(args[1:])

		result := sendJSON(client, "POST", "/api/v1/admin/backup", map[string]string{"type": *bType}, http.StatusAccepted)

		if outFmt == "json" {
			printJSON(result)
			return
		}

		fmt.Printf("Backup created: %v\n", result["id"])
	case "show":
		if len(args) < 2 {
			dief("Usage: oa backup show <id>")
		}

		showGeneric(client, "/api/v1/admin/backup/"+escape(args[1]), outFmt)
	case "download":
		if len(args) < 3 {
			dief("Usage: oa backup download <id> <dest>")
		}

		downloadFile(client, "/api/v1/admin/backup/"+escape(args[1])+"/download", args[2])
	case "restore":
		if len(args) < 2 {
			dief("Usage: oa backup restore <archive-file>")
		}

		backupRestore(client, args[1])
	default:
		dief("Unknown backup subcommand: %s", args[0])
	}
}

// backupRestore uploads a backup archive (as produced by `backup download`);
// the server restores from the uploaded file, not from a stored job id.
func backupRestore(client *Client, archive string) {
	file, err := os.Open(archive)
	if err != nil {
		dief("opening archive: %v", err)
	}
	defer func() { _ = file.Close() }()

	body, contentType := multipartStream(file, filepath.Base(archive))
	defer func() { _ = body.Close() }()

	resp, err := client.do("POST", "/api/v1/admin/restore", body, contentType)
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp, http.StatusAccepted)
	fmt.Println("Restore initiated.")
}

// ═══════════════════════════════════════════════════════════════════════════════
// AUDIT (admin)
// ═══════════════════════════════════════════════════════════════════════════════

// auditPageSize is the largest page the audit endpoint serves.
const auditPageSize = 100

func handleAudit(args []string, client *Client, outFmt string) {
	if len(args) >= 1 && args[0] != "list" {
		dief("Unknown audit subcommand: %s", args[0])
	}

	fs := flag.NewFlagSet("audit list", flag.ExitOnError)
	limit := fs.Int("limit", 50, "Max entries")
	action := fs.String("action", "", "Filter by action")
	actor := fs.String("actor", "", "Filter by actor")

	if len(args) > 0 {
		_ = fs.Parse(args[1:])
	} else {
		_ = fs.Parse(nil)
	}

	if *limit < 1 {
		dief("--limit must be positive")
	}

	var entries []map[string]any

	// One fixed page size: the server computes the offset from page*per_page.
	perPage := min(*limit, auditPageSize)

	for page := 1; len(entries) < *limit; page++ {
		q := url.Values{}
		q.Set("per_page", strconv.Itoa(perPage))
		q.Set("page", strconv.Itoa(page))

		if *action != "" {
			q.Set("action", *action)
		}

		if *actor != "" {
			q.Set("actor", *actor)
		}

		var result struct {
			Data    []map[string]any `json:"data"`
			HasMore bool             `json:"has_more"`
		}

		getJSON(client, "/api/v1/admin/audit?"+q.Encode(), &result)

		entries = append(entries, result.Data...)

		if !result.HasMore || len(result.Data) == 0 {
			break
		}
	}

	if len(entries) > *limit {
		entries = entries[:*limit]
	}

	printTable(entries, []string{"TIME", "ACTOR", "ACTION", "TYPE", "TARGET"},
		[]string{"timestamp", "actor", "action", "resource_type", "target"}, outFmt)
}
