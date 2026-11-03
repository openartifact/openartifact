// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// projectRoles are the roles a project member can hold; a group grant cannot
// be owner (source: the project API's role validation, which cmd/oa must not import).
//
//nolint:gochecknoglobals // immutable lookup table
var projectRoles = []string{"viewer", "developer", "maintainer", "owner"}

//nolint:gochecknoglobals // immutable lookup table
var projectGroupRoles = []string{"viewer", "developer", "maintainer"}

// storageKinds are the storage backend types the server accepts (source:
// validStorageKinds in internal/api/admin_storage.go, which cmd/oa must not import).
//
//nolint:gochecknoglobals // immutable lookup table
var storageKinds = []string{"filesystem", "s3", "gcs", "azure", "garage", "seaweedfs", "oci"}

// adminListCmd is `list`: a table of the items under key of GET path.
func adminListCmd(a *app, short, path, key string, headers, fields []string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: short,
		Args:  usageArgs(0, 0),
		Run: func(*cobra.Command, []string) {
			listGeneric(a.client, path, key, headers, fields, a.outFmt)
		},
	}
}

// adminShowCmd is `show <arg>`: the key/value view of GET base/<arg>.
func adminShowCmd(a *app, short, arg, base string, comp completer) *cobra.Command {
	return &cobra.Command{
		Use:               "show <" + arg + ">",
		Short:             short,
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(comp),
		Run: func(_ *cobra.Command, args []string) {
			showGeneric(a.client, base+escape(args[0]), a.outFmt)
		},
	}
}

// adminDeleteCmd is `delete <arg>`: DELETE base/<arg>, then "<label> '<arg>' deleted.".
func adminDeleteCmd(a *app, short, arg, base, label string, comp completer) *cobra.Command {
	return &cobra.Command{
		Use:               "delete <" + arg + ">",
		Short:             short,
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(comp),
		Run: func(_ *cobra.Command, args []string) {
			deleteResource(a.client, base+escape(args[0]), fmt.Sprintf("%s '%s' deleted.", label, args[0]))
		},
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// USER (admin)
// ═══════════════════════════════════════════════════════════════════════════════

// userIDs completes user ids.
func (a *app) userIDs() completer {
	return a.listNames("/api/v1/users", "data", "id")
}

// groupIDs completes group ids.
func (a *app) groupIDs() completer {
	return a.listNames("/api/v1/groups", "data", "id")
}

// projectMemberIDs completes the user ids of the members of the project named first.
func (a *app) projectMemberIDs() completer {
	return a.listNamesAt(func(args []string) string {
		return "/api/v1/projects/" + escape(args[0]) + "/members"
	}, "members", "user_id")
}

// groupMemberIDs completes the user ids of the members of the group named first.
func (a *app) groupMemberIDs() completer {
	return a.listNamesAt(func(args []string) string {
		return "/api/v1/groups/" + escape(args[0]) + "/members"
	}, "data", "id")
}

func newUserCmd(a *app) *cobra.Command {
	return parentCmd("user", "Manage users (admin)",
		&cobra.Command{
			Use:   "list",
			Short: "List users",
			Args:  usageArgs(0, 0),
			Run: func(*cobra.Command, []string) {
				users := fetchAllPages(a.client, "/api/v1/users", "data", "per_page", 200)
				printTable(users, []string{"ID", "USERNAME", "EMAIL", "DISABLED"},
					[]string{"id", "username", "email", "disabled"}, a.outFmt)
			},
		},
		userCreateCmd(a),
		adminShowCmd(a, "Show a user", "id", "/api/v1/users/", a.userIDs()),
		userUpdateCmd(a),
		adminDeleteCmd(a, "Delete a user", "id", "/api/v1/users/", "User", a.userIDs()),
	)
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

// userCreateCmd creates an account. The server has no admin flag on create:
// administrator rights come from a permission-target assignment.
func userCreateCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create --username U --email E",
		Short: "Create a user (password prompted without echo)",
		Args:  usageArgs(0, 0),
	}

	username := cmd.Flags().String("username", "", "Username")
	email := cmd.Flags().String("email", "", "Email")
	passwordStdin := cmd.Flags().Bool("password-stdin", false, "Read the password from stdin")
	legacyPassword := cmd.Flags().String("password", "", "removed; use --password-stdin")
	fullName := cmd.Flags().String("full-name", "", "Full name")

	mustRegister(cmd.Flags().MarkHidden("password"))

	cmd.Run = func(*cobra.Command, []string) {
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

		sendJSON(a.client, "POST", "/api/v1/users", body, http.StatusCreated)
		fmt.Printf("User '%s' created.\n", *username)
	}

	return cmd
}

func userUpdateCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "update <id>",
		Short:             "Update a user (only the flags you pass are sent)",
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(a.userIDs()),
	}

	email := cmd.Flags().String("email", "", "Email")
	fullName := cmd.Flags().String("full-name", "", "Full name")
	disabled := cmd.Flags().Bool("disabled", false, "Disable the user (--disabled=false to re-enable)")

	cmd.Run = func(cmd *cobra.Command, args []string) {
		set := cmd.Flags().Changed
		body := map[string]any{}

		if set("email") {
			body["email"] = *email
		}

		if set("full-name") {
			body["full_name"] = *fullName
		}

		if set("disabled") {
			body["disabled"] = *disabled
		}

		if len(body) == 0 {
			dief("nothing to update: pass --email, --full-name and/or --disabled[=false]")
		}

		sendJSON(a.client, "PATCH", "/api/v1/users/"+escape(args[0]), body, http.StatusOK)
		fmt.Printf("User '%s' updated.\n", args[0])
	}

	return cmd
}

// ═══════════════════════════════════════════════════════════════════════════════
// GROUP (admin)
// ═══════════════════════════════════════════════════════════════════════════════

func newGroupCmd(a *app) *cobra.Command {
	const base = "/api/v1/groups/"

	create := &cobra.Command{
		Use:               "create <name>",
		Short:             "Create a group",
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(nothing),
	}
	desc := create.Flags().String("description", "", "Description")
	create.Run = func(_ *cobra.Command, args []string) {
		body := map[string]any{"name": args[0]}
		if *desc != "" {
			body["description"] = *desc
		}

		sendJSON(a.client, "POST", "/api/v1/groups", body, http.StatusCreated)
		fmt.Printf("Group '%s' created.\n", args[0])
	}

	return parentCmd("group", "Manage groups (admin)",
		adminListCmd(a, "List groups", "/api/v1/groups", "data",
			[]string{"ID", "NAME", "DESCRIPTION"}, []string{"id", "name", "description"}),
		create,
		adminShowCmd(a, "Show a group", "id", base, a.groupIDs()),
		adminDeleteCmd(a, "Delete a group", "id", base, "Group", a.groupIDs()),
		&cobra.Command{
			Use:               "members <id>",
			Short:             "List group members",
			Args:              usageArgs(1, 1),
			ValidArgsFunction: positional(a.groupIDs()),
			Run: func(_ *cobra.Command, args []string) {
				listGeneric(a.client, base+escape(args[0])+"/members", "data",
					[]string{"USER_ID", "USERNAME", "EMAIL"}, []string{"id", "username", "email"}, a.outFmt)
			},
		},
		&cobra.Command{
			Use:               "add-member <id> <user-id>",
			Short:             "Add a member to a group",
			Args:              usageArgs(2, 2),
			ValidArgsFunction: positional(a.groupIDs(), a.userIDs()),
			Run: func(_ *cobra.Command, args []string) {
				postEmpty(a.client, base+escape(args[0])+"/members/"+escape(args[1]), "Member added.",
					http.StatusOK, http.StatusNoContent, http.StatusCreated)
			},
		},
		&cobra.Command{
			Use:               "rm-member <id> <user-id>",
			Short:             "Remove a member from a group",
			Args:              usageArgs(2, 2),
			ValidArgsFunction: positional(a.groupIDs(), a.groupMemberIDs()),
			Run: func(_ *cobra.Command, args []string) {
				deleteResource(a.client, base+escape(args[0])+"/members/"+escape(args[1]), "Member removed.")
			},
		},
	)
}

// ═══════════════════════════════════════════════════════════════════════════════
// PROJECT
// ═══════════════════════════════════════════════════════════════════════════════

// projectNames completes project names.
func (a *app) projectNames() completer {
	return a.listNames("/api/v1/projects", "projects", "name")
}

func newProjectCmd(a *app) *cobra.Command {
	const base = "/api/v1/projects/"

	// The response of show carries my_role (the caller's effective role), which
	// the generic key/value output prints with the other fields.
	show := adminShowCmd(a, "Show a project (includes my_role)", "name", base, a.projectNames())

	setRole := &cobra.Command{
		Use:               "set-role <name> <user-id> <role>",
		Short:             "Change a member's role",
		Args:              usageArgs(3, 3),
		ValidArgsFunction: positional(a.projectNames(), a.projectMemberIDs(), enum(projectRoles...)),
		Run: func(_ *cobra.Command, args []string) {
			sendJSON(a.client, "PATCH", base+escape(args[0])+"/members/"+escape(args[1]),
				map[string]string{"role": args[2]}, http.StatusOK)
			fmt.Println("Member role updated.")
		},
	}

	addGroup := &cobra.Command{
		Use:               "add-group <name> <group-id|group-name>",
		Short:             "Grant a group access to a project",
		Args:              usageArgs(2, 2),
		ValidArgsFunction: positional(a.projectNames(), a.listNames("/api/v1/groups", "data", "name")),
	}
	groupRole := addGroup.Flags().String("role", "viewer", "Role: viewer|developer|maintainer")
	flagEnum(addGroup, "role", projectGroupRoles...)

	addGroup.Run = func(_ *cobra.Command, args []string) {
		sendJSON(a.client, "PUT", base+escape(args[0])+"/groups/"+escape(args[1]),
			map[string]string{"role": *groupRole}, http.StatusOK)
		fmt.Println("Group added.")
	}

	return parentCmd("project", "Manage projects",
		adminListCmd(a, "List projects", "/api/v1/projects", "projects",
			[]string{"NAME", "DISPLAY_NAME", "MY_ROLE", "REPOSITORIES"},
			[]string{"name", "display_name", "my_role", "repository_count"}),
		projectCreateCmd(a),
		show,
		projectUpdateCmd(a),
		adminDeleteCmd(a, "Delete a project", "name", base, "Project", a.projectNames()),
		&cobra.Command{
			Use:               "members <name>",
			Short:             "List project members",
			Args:              usageArgs(1, 1),
			ValidArgsFunction: positional(a.projectNames()),
			Run: func(_ *cobra.Command, args []string) {
				listGeneric(a.client, base+escape(args[0])+"/members", "members",
					[]string{"USERNAME", "USER_ID", "ROLE"}, []string{"username", "user_id", "role"}, a.outFmt)
			},
		},
		projectAddMemberCmd(a),
		setRole,
		&cobra.Command{
			Use:               "rm-member <name> <user-id>",
			Short:             "Remove a member from a project",
			Args:              usageArgs(2, 2),
			ValidArgsFunction: positional(a.projectNames(), a.projectMemberIDs()),
			Run: func(_ *cobra.Command, args []string) {
				deleteResource(a.client, base+escape(args[0])+"/members/"+escape(args[1]), "Member removed.")
			},
		},
		&cobra.Command{
			Use:               "groups <name>",
			Short:             "List the group grants of a project",
			Args:              usageArgs(1, 1),
			ValidArgsFunction: positional(a.projectNames()),
			Run: func(_ *cobra.Command, args []string) {
				listGeneric(a.client, base+escape(args[0])+"/groups", "groups",
					[]string{"GROUP_NAME", "GROUP_ID", "ROLE"}, []string{"group_name", "group_id", "role"}, a.outFmt)
			},
		},
		addGroup,
		&cobra.Command{
			Use:               "rm-group <name> <group-id|group-name>",
			Short:             "Remove a group grant from a project",
			Args:              usageArgs(2, 2),
			ValidArgsFunction: positional(a.projectNames(), a.listNames("/api/v1/groups", "data", "name")),
			Run: func(_ *cobra.Command, args []string) {
				deleteResource(a.client, base+escape(args[0])+"/groups/"+escape(args[1]), "Group removed.")
			},
		},
	)
}

// projectCreateCmd creates a project. Global admins may also pick the owner;
// when the server lets every user create projects (projects.self_create) the
// caller becomes the owner and a body with --owner is rejected with 403.
func projectCreateCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "create <name>",
		Short:             "Create a project",
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(nothing),
	}

	displayName := cmd.Flags().String("display-name", "", "Display name")
	desc := cmd.Flags().String("description", "", "Description")
	owner := cmd.Flags().String("owner", "", "Owner username (global admins only; default: you)")

	cmd.Run = func(cmd *cobra.Command, args []string) {
		body := map[string]any{
			"name":         args[0],
			"display_name": *displayName,
			"description":  *desc,
		}

		if cmd.Flags().Changed("owner") {
			body["owner"] = *owner
		}

		sendJSON(a.client, "POST", "/api/v1/projects", body, http.StatusCreated)
		fmt.Printf("Project '%s' created.\n", args[0])
	}

	return cmd
}

// projectUpdateCmd sends only the flags the user set. display-name and
// description need the owner role; the policy flags need a global admin.
func projectUpdateCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Update a project (only the flags you pass are sent)",
		Long: "Update a project. --display-name and --description need the owner role; the policy flags " +
			"(--allowed-formats, --allowed-repo-types, --repo-name-prefix, --allow-public-repos, " +
			"--max-repositories, --token-max-ttl-days) need a global admin.",
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(a.projectNames()),
	}

	fl := cmd.Flags()
	displayName := fl.String("display-name", "", "Display name (owner)")
	desc := fl.String("description", "", "Description (owner)")
	formats := fl.String("allowed-formats", "", "Comma-separated formats; empty = all (global admin)")
	repoTypes := fl.String("allowed-repo-types", "", "Comma-separated repo types: hosted,proxy,group (global admin)")
	prefix := fl.String("repo-name-prefix", "", "Required repository name prefix (global admin)")
	public := fl.Bool("allow-public-repos", false, "Allow public repositories (global admin)")
	maxRepos := fl.Int("max-repositories", 0, "Maximum repositories, 0 = unlimited (global admin)")
	ttl := fl.Int("token-max-ttl-days", 0, "Maximum token lifetime in days, 0 = no cap (global admin)")

	cmd.Run = func(cmd *cobra.Command, args []string) {
		set := cmd.Flags().Changed
		body := map[string]any{}

		for name, v := range map[string]string{
			"display-name": *displayName, "description": *desc, "repo-name-prefix": *prefix,
		} {
			if set(name) {
				body[strings.ReplaceAll(name, "-", "_")] = v
			}
		}

		if set("allowed-formats") {
			body["allowed_formats"] = splitCSV(*formats)
		}

		if set("allowed-repo-types") {
			body["allowed_repo_types"] = splitCSV(*repoTypes)
		}

		if set("allow-public-repos") {
			body["allow_public_repos"] = *public
		}

		if set("max-repositories") {
			body["max_repositories"] = *maxRepos
		}

		if set("token-max-ttl-days") {
			body["token_max_ttl_days"] = *ttl
		}

		if len(body) == 0 {
			dief("nothing to update: pass --display-name, --description or a policy flag (see 'oa project update')")
		}

		sendJSON(a.client, "PATCH", "/api/v1/projects/"+escape(args[0]), body, http.StatusOK)
		fmt.Printf("Project '%s' updated.\n", args[0])
	}

	return cmd
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

// projectAddMemberCmd adds a user by id (positional) or by exact --username.
func projectAddMemberCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "add-member <name> (<user-id> | --username U)",
		Short:             "Add a member to a project",
		Args:              usageArgs(1, 2),
		ValidArgsFunction: positional(a.projectNames(), a.userIDs()),
	}

	role := cmd.Flags().String("role", "viewer", "Role: viewer|developer|maintainer|owner")
	username := cmd.Flags().String("username", "", "Add by exact username instead of user id")

	flagEnum(cmd, "role", projectRoles...)

	cmd.Run = func(_ *cobra.Command, args []string) {
		userID := ""
		if len(args) > 1 {
			userID = args[1]
		}

		if (userID == "") == (*username == "") {
			dief("Usage: oa project add-member <name> (<user-id> | --username U) [--role viewer|developer|maintainer|owner]")
		}

		body := map[string]string{"role": *role}
		if userID != "" {
			body["user_id"] = userID
		} else {
			body["username"] = *username
		}

		sendJSON(a.client, "POST", "/api/v1/projects/"+escape(args[0])+"/members", body, http.StatusCreated)
		fmt.Println("Member added.")
	}

	return cmd
}

// ═══════════════════════════════════════════════════════════════════════════════
// TOKEN (admin)
// ═══════════════════════════════════════════════════════════════════════════════

func newTokenCmd(a *app) *cobra.Command {
	return parentCmd("token", "Manage all API tokens (admin)",
		&cobra.Command{
			Use:   "list",
			Short: "List all tokens",
			Args:  usageArgs(0, 0),
			Run: func(*cobra.Command, []string) {
				tokens := fetchAllPages(a.client, "/api/v1/admin/tokens", "data", "per_page", 100)
				printTable(tokens, []string{"ID", "NAME", "KIND", "USER_ID", "CREATED"},
					[]string{"id", "name", "kind", "user_id", "created_at"}, a.outFmt)
			},
		},
		&cobra.Command{
			Use:               "revoke <id>",
			Short:             "Revoke any token",
			Args:              usageArgs(1, 1),
			ValidArgsFunction: positional(a.listNames("/api/v1/admin/tokens", "data", "id")),
			Run: func(_ *cobra.Command, args []string) {
				deleteResource(a.client, "/api/v1/admin/tokens/"+escape(args[0]), "Token revoked.")
			},
		},
	)
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
			dief(
				"refusing inline --config: %q looks like a credential and would be visible in the process list; "+
					"put the config in a file and use --config-file (or --config-file - to read stdin)",
				key,
			)
		}
	}

	return cfg
}

// storageNames completes storage backend names.
func (a *app) storageNames() completer {
	return a.listNames("/api/v1/admin/storage", "items", "name")
}

func newStorageCmd(a *app) *cobra.Command {
	const base = "/api/v1/admin/storage/"

	create := &cobra.Command{
		Use:   "create --name N",
		Short: "Create a storage backend (credentials via --config-file, - = stdin)",
		Args:  usageArgs(0, 0),
	}
	name := create.Flags().String("name", "", "Backend name")
	kind := create.Flags().String("type", "filesystem", "Backend type: "+strings.Join(storageKinds, "|"))
	config, configFile := storageConfigFlags(create)

	flagEnum(create, "type", storageKinds...)

	create.Run = func(*cobra.Command, []string) {
		if *name == "" {
			dief("--name is required")
		}

		cfg := storageConfig(*config, *configFile)
		if cfg == nil {
			cfg = map[string]any{}
		}

		sendJSON(a.client, "POST", "/api/v1/admin/storage", map[string]any{
			"name":   *name,
			"kind":   *kind,
			"config": cfg,
		}, http.StatusCreated)
		fmt.Printf("Storage backend '%s' created.\n", *name)
	}

	update := &cobra.Command{
		Use:               "update <name>",
		Short:             "Update a storage backend",
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(a.storageNames()),
	}
	uConfig, uConfigFile := storageConfigFlags(update)

	update.Run = func(_ *cobra.Command, args []string) {
		body := map[string]any{}

		if cfg := storageConfig(*uConfig, *uConfigFile); cfg != nil {
			body["config"] = cfg
		}

		if len(body) == 0 {
			dief("nothing to update: pass --config or --config-file")
		}

		sendJSON(a.client, "PATCH", base+escape(args[0]), body, http.StatusOK)
		fmt.Printf("Storage backend '%s' updated.\n", args[0])
	}

	return parentCmd("storage", "Manage storage backends (admin)",
		adminListCmd(a, "List storage backends", "/api/v1/admin/storage", "items",
			[]string{"NAME", "KIND", "STATUS"}, []string{"name", "kind", "status"}),
		create,
		adminShowCmd(a, "Show a storage backend", "name", base, a.storageNames()),
		update,
		adminDeleteCmd(a, "Delete a storage backend", "name", base, "Storage backend", a.storageNames()),
		&cobra.Command{
			Use:               "test <name>",
			Short:             "Test a storage backend",
			Args:              usageArgs(1, 1),
			ValidArgsFunction: positional(a.storageNames()),
			Run:               func(_ *cobra.Command, args []string) { storageTest(a.client, args[0]) },
		},
	)
}

// storageConfigFlags declares --config and --config-file of storage create/update.
func storageConfigFlags(cmd *cobra.Command) (*string, *string) {
	config := cmd.Flags().String("config", "", "Inline JSON config (non-secret settings only)")
	configFile := cmd.Flags().String("config-file", "", "File with the JSON config, or - for stdin (use this for credentials)")

	flagFiles(cmd, "config-file")

	return config, configFile
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

func newBackupCmd(a *app) *cobra.Command {
	const base = "/api/v1/admin/backup/"

	backupIDs := a.listNames("/api/v1/admin/backup", "backups", "id")

	create := &cobra.Command{
		Use:   "create",
		Short: "Create a backup",
		Args:  usageArgs(0, 0),
	}
	bType := create.Flags().String("type", "full", "Backup type: full|metadata-only")

	flagEnum(create, "type", "full", "metadata-only")

	create.Run = func(*cobra.Command, []string) {
		result := sendJSON(a.client, "POST", "/api/v1/admin/backup", map[string]string{"type": *bType}, http.StatusAccepted)

		if a.outFmt == "json" {
			printJSON(result)
			return
		}

		fmt.Printf("Backup created: %v\n", result["id"])
	}

	return parentCmd("backup", "Back up and restore the server (admin)",
		adminListCmd(a, "List backups", "/api/v1/admin/backup", "backups",
			[]string{"ID", "TYPE", "STATUS", "SIZE", "CREATED"}, []string{"id", "type", "status", "size_bytes", "created_at"}),
		create,
		adminShowCmd(a, "Show a backup", "id", base, backupIDs),
		&cobra.Command{
			Use:               "download <id> <dest>",
			Short:             "Download a backup archive",
			Args:              usageArgs(2, 2),
			ValidArgsFunction: positional(backupIDs, files),
			Run: func(_ *cobra.Command, args []string) {
				downloadFile(a.client, base+escape(args[0])+"/download", args[1])
			},
		},
		&cobra.Command{
			Use:               "restore <archive-file>",
			Short:             "Restore from a downloaded backup archive",
			Args:              usageArgs(1, 1),
			ValidArgsFunction: positional(files),
			Run:               func(_ *cobra.Command, args []string) { backupRestore(a.client, args[0]) },
		},
	)
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

func newAuditCmd(a *app) *cobra.Command {
	return parentCmd("audit", "Read and export the audit log (admin)", auditListCmd(a), auditExportCmd(a))
}

func auditListCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List audit entries",
		Args:  usageArgs(0, 0),
	}

	limit := cmd.Flags().Int("limit", 50, "Max entries")
	action := cmd.Flags().String("action", "", "Filter by action")
	actor := cmd.Flags().String("actor", "", "Filter by actor")

	cmd.Run = func(*cobra.Command, []string) {
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

			getJSON(a.client, "/api/v1/admin/audit?"+q.Encode(), &result)

			entries = append(entries, result.Data...)

			if !result.HasMore || len(result.Data) == 0 {
				break
			}
		}

		if len(entries) > *limit {
			entries = entries[:*limit]
		}

		printTable(entries, []string{"TIME", "ACTOR", "ACTION", "TYPE", "TARGET"},
			[]string{"timestamp", "actor", "action", "resource_type", "target"}, a.outFmt)
	}

	return cmd
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

// auditExportCmd streams the server's audit export to a file (written
// atomically, so an interrupted transfer leaves nothing behind) or to stdout.
// Its own --format (jsonl|csv) shadows the global output format after the
// command name.
func auditExportCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the audit log (default: jsonl to stdout)",
		Long: "Export the audit log as jsonl (default) or csv, to stdout or to the --output file.\n\n" +
			"After the command name, --format is the export format; the global output format is not used " +
			"by this command.",
		Args: usageArgs(0, 0),
	}

	format := cmd.Flags().String("format", "jsonl", "Export format: jsonl|csv")
	since := cmd.Flags().String("since", "", "Only entries at or after this time (RFC 3339, YYYY-MM-DD or an age like 24h, 7d)")
	until := cmd.Flags().String("until", "", "Only entries before this time (same forms)")
	actor := cmd.Flags().String("actor", "", "Filter by actor")
	action := cmd.Flags().String("action", "", "Filter by action")
	output := cmd.Flags().StringP("output", "o", "", "Write to this file instead of stdout")

	flagEnum(cmd, "format", "jsonl", "csv")
	flagFiles(cmd, "output")

	cmd.Run = func(*cobra.Command, []string) {
		auditExport(a.client, *format, *since, *until, *actor, *action, *output)
	}

	return cmd
}

func auditExport(client *Client, format, since, until, actor, action, output string) {
	q, err := auditExportQuery(format, since, until, actor, action, time.Now())
	if err != nil {
		dief("%v", err)
	}

	resp, err := client.do(http.MethodGet, "/api/v1/admin/audit/export?"+q.Encode(), nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)

	if output == "" || output == "-" {
		if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
			dief("writing export: %v", err)
		}

		return
	}

	if fi, err := os.Stat(output); err == nil && !fi.Mode().IsRegular() {
		// A device or pipe (/dev/null, /dev/stdout, a FIFO): no temp file and rename.
		f, err := os.OpenFile(output, os.O_WRONLY, 0)
		if err != nil {
			dief("%v", err)
		}

		if _, err := io.Copy(f, resp.Body); err != nil {
			_ = f.Close()

			dief("writing export: %v", err)
		}

		if err := f.Close(); err != nil {
			dief("writing export: %v", err)
		}

		return
	}

	n, err := downloadTo(resp, output)
	if err != nil {
		dief("%v", err)
	}

	fmt.Fprintf(os.Stderr, "Exported %d bytes to '%s'.\n", n, output)
}
