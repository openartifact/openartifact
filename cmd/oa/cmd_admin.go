// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
// PROJECT
// ═══════════════════════════════════════════════════════════════════════════════

func handleProject(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa project <list|create|show|update|delete|members|add-member|set-role|rm-member|groups|add-group|rm-group>")
	}

	switch args[0] {
	case "list":
		listGeneric(client, "/api/v1/projects", "projects",
			[]string{"NAME", "DISPLAY_NAME", "MY_ROLE", "REPOSITORIES"},
			[]string{"name", "display_name", "my_role", "repository_count"}, outFmt)
	case "create":
		projectCreate(client, args[1:])
	case "show":
		if len(args) < 2 {
			dief("Usage: oa project show <name>")
		}

		// The response carries my_role (the caller's effective role), which
		// the generic key/value output prints with the other fields.
		showGeneric(client, "/api/v1/projects/"+escape(args[1]), outFmt)
	case "update":
		projectUpdate(client, args[1:])
	case "delete":
		if len(args) < 2 {
			dief("Usage: oa project delete <name>")
		}

		deleteResource(client, "/api/v1/projects/"+escape(args[1]), fmt.Sprintf("Project '%s' deleted.", args[1]))
	case "members":
		if len(args) < 2 {
			dief("Usage: oa project members <name>")
		}

		listGeneric(client, "/api/v1/projects/"+escape(args[1])+"/members", "members",
			[]string{"USERNAME", "USER_ID", "ROLE"}, []string{"username", "user_id", "role"}, outFmt)
	case "add-member":
		projectAddMember(client, args[1:])
	case "set-role":
		if len(args) < 4 {
			dief("Usage: oa project set-role <name> <user-id> <viewer|developer|maintainer|owner>")
		}

		sendJSON(client, "PATCH", "/api/v1/projects/"+escape(args[1])+"/members/"+escape(args[2]),
			map[string]string{"role": args[3]}, http.StatusOK)
		fmt.Println("Member role updated.")
	case "rm-member":
		if len(args) < 3 {
			dief("Usage: oa project rm-member <name> <user-id>")
		}

		deleteResource(client, "/api/v1/projects/"+escape(args[1])+"/members/"+escape(args[2]), "Member removed.")
	case "groups":
		if len(args) < 2 {
			dief("Usage: oa project groups <name>")
		}

		listGeneric(client, "/api/v1/projects/"+escape(args[1])+"/groups", "groups",
			[]string{"GROUP_NAME", "GROUP_ID", "ROLE"}, []string{"group_name", "group_id", "role"}, outFmt)
	case "add-group":
		if len(args) < 3 {
			dief("Usage: oa project add-group <name> <group-id|group-name> [--role viewer|developer|maintainer]")
		}

		fs := flag.NewFlagSet("project add-group", flag.ExitOnError)
		role := fs.String("role", "viewer", "Role: viewer|developer|maintainer")
		_ = fs.Parse(args[3:])

		sendJSON(client, "PUT", "/api/v1/projects/"+escape(args[1])+"/groups/"+escape(args[2]), map[string]string{
			"role": *role,
		}, http.StatusOK)
		fmt.Println("Group added.")
	case "rm-group":
		if len(args) < 3 {
			dief("Usage: oa project rm-group <name> <group-id|group-name>")
		}

		deleteResource(client, "/api/v1/projects/"+escape(args[1])+"/groups/"+escape(args[2]), "Group removed.")
	default:
		dief("Unknown project subcommand: %s", args[0])
	}
}

// projectCreate creates a project. Global admins may also pick the owner;
// when the server lets every user create projects (projects.self_create) the
// caller becomes the owner and a body with --owner is rejected with 403.
func projectCreate(client *Client, args []string) {
	if len(args) < 1 {
		dief("Usage: oa project create <name> [--display-name N] [--description D] [--owner USERNAME]")
	}

	fs := flag.NewFlagSet("project create", flag.ExitOnError)
	displayName := fs.String("display-name", "", "Display name")
	desc := fs.String("description", "", "Description")
	owner := fs.String("owner", "", "Owner username (global admins only; default: you)")
	_ = fs.Parse(args[1:])

	body := map[string]any{
		"name":         args[0],
		"display_name": *displayName,
		"description":  *desc,
	}

	if setFlags(fs)["owner"] {
		body["owner"] = *owner
	}

	sendJSON(client, "POST", "/api/v1/projects", body, http.StatusCreated)
	fmt.Printf("Project '%s' created.\n", args[0])
}

// projectUpdate sends only the flags the user set. display-name and
// description need the owner role; the policy flags need a global admin.
func projectUpdate(client *Client, args []string) {
	const usage = "Usage: oa project update <name> [--display-name N] [--description D] " +
		"[--allowed-formats F,F] [--allowed-repo-types T,T] [--repo-name-prefix P] " +
		"[--allow-public-repos[=false]] [--max-repositories N] [--token-max-ttl-days N]"

	if len(args) < 1 {
		dief("%s", usage)
	}

	fs := flag.NewFlagSet("project update", flag.ExitOnError)
	displayName := fs.String("display-name", "", "Display name (owner)")
	desc := fs.String("description", "", "Description (owner)")
	formats := fs.String("allowed-formats", "", "Comma-separated formats; empty = all (global admin)")
	repoTypes := fs.String("allowed-repo-types", "", "Comma-separated repo types: hosted,proxy,group (global admin)")
	prefix := fs.String("repo-name-prefix", "", "Required repository name prefix (global admin)")
	public := fs.Bool("allow-public-repos", false, "Allow public repositories (global admin)")
	maxRepos := fs.Int("max-repositories", 0, "Maximum repositories, 0 = unlimited (global admin)")
	ttl := fs.Int("token-max-ttl-days", 0, "Maximum token lifetime in days, 0 = no cap (global admin)")
	_ = fs.Parse(args[1:])

	set := setFlags(fs)
	body := map[string]any{}

	for name, v := range map[string]string{
		"display-name": *displayName, "description": *desc, "repo-name-prefix": *prefix,
	} {
		if set[name] {
			body[strings.ReplaceAll(name, "-", "_")] = v
		}
	}

	if set["allowed-formats"] {
		body["allowed_formats"] = splitCSV(*formats)
	}

	if set["allowed-repo-types"] {
		body["allowed_repo_types"] = splitCSV(*repoTypes)
	}

	if set["allow-public-repos"] {
		body["allow_public_repos"] = *public
	}

	if set["max-repositories"] {
		body["max_repositories"] = *maxRepos
	}

	if set["token-max-ttl-days"] {
		body["token_max_ttl_days"] = *ttl
	}

	if len(body) == 0 {
		dief("nothing to update: pass --display-name, --description or a policy flag (see 'oa project update')")
	}

	sendJSON(client, "PATCH", "/api/v1/projects/"+escape(args[0]), body, http.StatusOK)
	fmt.Printf("Project '%s' updated.\n", args[0])
}

// splitCSV splits a comma-separated flag value; an empty value yields an empty
// (non-nil) slice so the JSON body carries [] rather than null.
func splitCSV(s string) []string {
	out := []string{}

	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}

	return out
}

// projectAddMember adds a user by id (positional) or by exact --username.
func projectAddMember(client *Client, args []string) {
	const usage = "Usage: oa project add-member <name> (<user-id> | --username U) [--role viewer|developer|maintainer|owner]"

	if len(args) < 1 {
		dief("%s", usage)
	}

	rest := args[1:]
	userID := ""

	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		userID, rest = rest[0], rest[1:]
	}

	fs := flag.NewFlagSet("project add-member", flag.ExitOnError)
	role := fs.String("role", "viewer", "Role: viewer|developer|maintainer|owner")
	username := fs.String("username", "", "Add by exact username instead of user id")
	_ = fs.Parse(rest)

	if (userID == "") == (*username == "") {
		dief("%s", usage)
	}

	body := map[string]string{"role": *role}
	if userID != "" {
		body["user_id"] = userID
	} else {
		body["username"] = *username
	}

	sendJSON(client, "POST", "/api/v1/projects/"+escape(args[0])+"/members", body, http.StatusCreated)
	fmt.Println("Member added.")
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
	if len(args) >= 1 && args[0] == "export" {
		handleAuditExport(args[1:], client)
		return
	}

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

// auditExportQuery builds the query string of GET /api/v1/admin/audit/export.
// since and until accept what the server accepts (RFC 3339 or YYYY-MM-DD, UTC)
// and also a relative age such as 24h or 7d, resolved against now.
func auditExportQuery(format, since, until, actor, action string, now time.Time) (url.Values, error) {
	if format != "jsonl" && format != "csv" {
		return nil, fmt.Errorf("%w, got %q", errAuditFormat, format)
	}

	q := url.Values{"format": {format}}

	for _, f := range []struct{ name, value string }{{"since", since}, {"until", until}} {
		if f.value == "" {
			continue
		}

		resolved, err := resolveAuditTime(f.value, now)
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", f.name, err)
		}

		q.Set(f.name, resolved)
	}

	if actor != "" {
		q.Set("actor", actor)
	}

	if action != "" {
		q.Set("action", action)
	}

	return q, nil
}

var errAuditFormat = errors.New("--format must be jsonl or csv")

var errAuditTime = errors.New("expected RFC 3339, YYYY-MM-DD or a relative age like 24h or 7d")

// resolveAuditTime turns a relative age (Go duration or <n>d) into an RFC 3339
// instant before now; absolute values are validated and passed through.
func resolveAuditTime(v string, now time.Time) (string, error) {
	if _, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return v, nil
	}

	if _, err := time.Parse(time.DateOnly, v); err == nil {
		return v, nil
	}

	age, err := parseAge(v)
	if err != nil {
		return "", errAuditTime
	}

	return now.Add(-age).UTC().Format(time.RFC3339), nil
}

func parseAge(v string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return 0, errAuditTime
		}

		return time.Duration(n) * 24 * time.Hour, nil
	}

	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, errAuditTime
	}

	return d, nil
}

// handleAuditExport streams the server's audit export to a file (written
// atomically, so an interrupted transfer leaves nothing behind) or to stdout.
func handleAuditExport(args []string, client *Client) {
	fs := flag.NewFlagSet("audit export", flag.ExitOnError)
	format := fs.String("format", "jsonl", "Export format: jsonl|csv")
	since := fs.String("since", "", "Only entries at or after this time (RFC 3339, YYYY-MM-DD or an age like 24h, 7d)")
	until := fs.String("until", "", "Only entries before this time (same forms)")
	actor := fs.String("actor", "", "Filter by actor")
	action := fs.String("action", "", "Filter by action")
	output := fs.String("o", "", "Write to this file instead of stdout")
	fs.StringVar(output, "output", "", "Write to this file instead of stdout")

	_ = fs.Parse(args)

	if fs.NArg() > 0 {
		dief("unexpected argument: %s", fs.Arg(0))
	}

	q, err := auditExportQuery(*format, *since, *until, *actor, *action, time.Now())
	if err != nil {
		dief("%v", err)
	}

	resp, err := client.do(http.MethodGet, "/api/v1/admin/audit/export?"+q.Encode(), nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)

	if *output == "" || *output == "-" {
		if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
			dief("writing export: %v", err)
		}

		return
	}

	n, err := downloadTo(resp, *output)
	if err != nil {
		dief("%v", err)
	}

	fmt.Fprintf(os.Stderr, "Exported %d bytes to '%s'.\n", n, *output)
}
