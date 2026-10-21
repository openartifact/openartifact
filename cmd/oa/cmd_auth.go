// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/http/cookiejar"
)

// The server refuses to change a password or mint/revoke tokens when the
// caller authenticates with an API token (a leaked token must not be able to
// take over the account), and the CLI always holds a token. These commands
// therefore sign in with the account password on a temporary cookie session,
// exactly like `oa auth login` does, and never store it.

// currentUsername resolves the signed-in user's name through the saved token.
func currentUsername(client *Client) string {
	var me struct {
		Username string `json:"username"`
	}

	getJSON(client, "/api/v1/auth/me", &me)

	if me.Username == "" {
		dief("server did not report the current username")
	}

	return me.Username
}

// openSession signs in with username/password and returns a client carrying
// the cookie session (CSRF-aware via sessionDo).
func openSession(client *Client, username, password string) *Client {
	c := newClient(client.baseURL, "")

	jar, err := cookiejar.New(nil)
	if err != nil {
		dief("%v", err)
	}

	c.httpClient.Jar = jar

	resp, err := c.doJSON("POST", "/api/v1/auth/login", map[string]string{
		"username": username,
		"password": password,
	})
	if err != nil {
		dief("%v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	checkOK(resp, http.StatusOK)

	var user struct {
		MustChangePassword bool `json:"must_change_password"`
	}
	decodeJSON(resp, &user)

	if user.MustChangePassword {
		dief("this account must change its password first: run 'oa auth login' (or 'oa auth passwd')")
	}

	return c
}

func authPasswd(args []string, client *Client) {
	fs := flag.NewFlagSet("auth passwd", flag.ExitOnError)
	passwordStdin := fs.Bool("password-stdin", false, "Read current then new password from two lines on stdin")
	legacyPassword := fs.String("password", "", "removed; use --password-stdin")
	legacyOld := fs.String("old-password", "", "removed; use --password-stdin")
	legacyNew := fs.String("new-password", "", "removed; use --password-stdin")
	_ = fs.Parse(args)

	if *legacyPassword != "" || *legacyOld != "" || *legacyNew != "" {
		dief("%v", errPasswordFlag)
	}

	var current, newPw string

	if *passwordStdin {
		current = readPasswordStdin()
		newPw = readPasswordStdin()
	} else {
		current = readPassword("Current password: ")
		newPw = readPassword("New password: ")

		confirm := readPassword("Confirm new password: ")
		if newPw != confirm {
			dief("passwords do not match")
		}
	}

	session := openSession(client, currentUsername(client), current)

	resp := sessionDo(session, http.MethodPost, "/api/v1/auth/change-password", map[string]string{
		"current_password": current,
		"new_password":     newPw,
	}, http.StatusOK, http.StatusNoContent)
	_ = resp.Body.Close()

	fmt.Println("Password changed.")
}

// sessionFromFlags signs in for a token command: the username defaults to the
// token's owner, the password is prompted (or read from stdin).
func sessionFromFlags(client *Client, username string, passwordStdin bool) *Client {
	if username == "" {
		username = currentUsername(client)
	}

	var password string
	if passwordStdin {
		password = readPasswordStdin()
	} else {
		password = readPassword("Password for " + username + ": ")
	}

	return openSession(client, username, password)
}

func authToken(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa auth token <list|create|revoke>")
	}

	switch args[0] {
	case "list":
		listGeneric(client, "/api/v1/auth/tokens", "data",
			[]string{"ID", "NAME", "KIND", "EXPIRES", "CREATED"},
			[]string{"id", "name", "kind", "expires_at", "created_at"}, outFmt)
	case "create":
		fs := flag.NewFlagSet("auth token create", flag.ExitOnError)
		name := fs.String("name", "", "Token name")
		kind := fs.String("kind", "access", "Token kind: access|deploy")
		username := fs.String("username", "", "Account to sign in as (default: the saved token's owner)")
		passwordStdin := fs.Bool("password-stdin", false, "Read the account password from stdin")
		_ = fs.Parse(args[1:])

		if *name == "" {
			dief("--name is required")
		}

		session := sessionFromFlags(client, *username, *passwordStdin)

		resp := sessionDo(session, http.MethodPost, "/api/v1/auth/tokens", map[string]string{"name": *name, "kind": *kind}, http.StatusCreated)

		defer func() { _ = resp.Body.Close() }()

		var result map[string]any
		decodeJSON(resp, &result)

		if outFmt == "json" {
			printJSON(result)
			return
		}

		fmt.Printf("Token created: %v\n", result["token"])
	case "revoke":
		if len(args) < 2 {
			dief("Usage: oa auth token revoke <id> [--username U] [--password-stdin]")
		}

		fs := flag.NewFlagSet("auth token revoke", flag.ExitOnError)
		username := fs.String("username", "", "Account to sign in as (default: the saved token's owner)")
		passwordStdin := fs.Bool("password-stdin", false, "Read the account password from stdin")
		_ = fs.Parse(args[2:])

		session := sessionFromFlags(client, *username, *passwordStdin)

		resp := sessionDo(session, http.MethodDelete, "/api/v1/auth/tokens/"+escape(args[1]), nil, http.StatusNoContent)
		_ = resp.Body.Close()

		fmt.Println("Token revoked.")
	default:
		dief("Unknown auth token subcommand: %s", args[0])
	}
}
