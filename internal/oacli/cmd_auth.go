// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"

	"github.com/spf13/cobra"
)

func newAuthCmd(a *app) *cobra.Command {
	return parentCmd("auth", "Sign in, sign out and manage tokens",
		authLoginCmd(a),
		authLogoutCmd(a),
		&cobra.Command{
			Use:   "whoami",
			Short: "Show the signed-in user",
			Args:  usageArgs(0, 0),
			Run:   func(*cobra.Command, []string) { authWhoami(a.client, a.outFmt) },
		},
		authPasswdCmd(a),
		authTokenCmd(a),
	)
}

// authLoginCmd signs in. The login server is its own --server when given, else
// the resolved global one (--server before the command, OA_SERVER, config).
func authLoginCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in and save the credentials",
		Long: "Sign in with a username and password (prompted, or --password-stdin) and save an API token, " +
			"or leave the username blank to paste an existing API token.",
		Args:              usageArgs(0, 0),
		ValidArgsFunction: positional(),
	}

	serverURL := cmd.Flags().String("server", "", "Server URL (default: the global server)")
	username := cmd.Flags().String("username", "", "Username (required with --password-stdin)")
	passwordStdin := cmd.Flags().Bool("password-stdin", false, "Read the password from stdin")
	legacyPassword := cmd.Flags().String("password", "", "removed; use --password-stdin")

	mustRegister(cmd.Flags().MarkHidden("password"))

	cmd.Run = func(cmd *cobra.Command, _ []string) {
		if *legacyPassword != "" {
			dief("%v", errPasswordFlag)
		}

		server := a.client.baseURL
		if cmd.Flags().Changed("server") {
			server = *serverURL
		}

		authLogin(server, *username, *passwordStdin)
	}

	return cmd
}

func authLogin(server, username string, passwordStdin bool) {
	switch {
	case passwordStdin:
		if username == "" {
			dief("--username is required with --password-stdin")
		}
	case username == "":
		username = promptLine("Username (leave blank to use token): ")
	}

	var savedToken, savedTokenID string

	if username == "" {
		savedToken = readPassword("API token: ")
	} else {
		var password string
		if passwordStdin {
			password = readPasswordStdin()
		} else {
			password = readPassword("Password: ")
		}

		savedToken, savedTokenID = sessionLoginToken(newClient(server, ""), username, password, passwordStdin)
	}

	newCfg := &Config{Server: server, Token: savedToken, TokenID: savedTokenID}
	if err := saveConfig(newCfg); err != nil {
		dief("saving config: %v", err)
	}

	fmt.Println("Credentials saved.")
}

// authLogoutCmd clears the saved credentials. The server-side access token
// minted by `auth login` does not expire and API tokens cannot revoke tokens,
// so a real revocation needs a session: with --revoke the CLI signs in with
// --username/--password-stdin and revokes the saved token id.
func authLogoutCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Forget the saved credentials",
		Args:  usageArgs(0, 0),
	}

	revoke := cmd.Flags().Bool("revoke", false, "Also revoke the saved token on the server (needs --username and --password-stdin)")
	username := cmd.Flags().String("username", "", "Username (with --revoke)")
	passwordStdin := cmd.Flags().Bool("password-stdin", false, "Read the password from stdin (with --revoke)")

	cmd.Run = func(*cobra.Command, []string) { authLogout(a.cfg, *revoke, *username, *passwordStdin) }

	return cmd
}

func authLogout(cfg *Config, revoke bool, username string, passwordStdin bool) {
	tokenID := cfg.TokenID
	revoked := false

	if revoke {
		if tokenID == "" || username == "" || !passwordStdin {
			dief("--revoke needs a login saved by this version of oa, --username and --password-stdin")
		}

		c := newClient(cfg.Server, "")
		c.httpClient.Jar = newCookieJar()
		sessionSignIn(c, username, readPasswordStdin(), true)

		resp := sessionDo(c, http.MethodDelete, "/api/v1/auth/tokens/"+escape(tokenID), nil, http.StatusNoContent, http.StatusOK)
		_ = resp.Body.Close()
		revoked = true
	}

	cfg.Token = ""
	cfg.TokenID = ""

	if err := saveConfig(cfg); err != nil {
		dief("clearing credentials: %v", err)
	}

	fmt.Println("Logged out.")

	if !revoked && tokenID != "" {
		fmt.Printf(
			"The access token (id %s) is still valid on the server; revoke it in the web UI, "+
				"or run `oa auth logout --revoke --username U --password-stdin` before logging out.\n",
			tokenID,
		)
	}
}

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

// sessionLoginToken signs in with username/password and exchanges the browser
// session (cookie + CSRF token) for an API access token, which is what the CLI
// authenticates with afterwards. A bootstrap/reset account must change its
// password first; the server refuses to mint tokens until then, so the new
// password is collected here (second stdin line with --password-stdin).
func sessionLoginToken(c *Client, username, password string, fromStdin bool) (string, string) {
	c.httpClient.Jar = newCookieJar()

	sessionSignIn(c, username, password, fromStdin)

	return mintCLIToken(c)
}

func sessionSignIn(c *Client, username, password string, fromStdin bool) {
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
		fmt.Fprintln(os.Stderr, "This account must change its password before it can be used.")

		newPw := readNewPassword(fromStdin)
		changeResp := sessionDo(c, http.MethodPost, "/api/v1/auth/change-password", map[string]string{
			"current_password": password,
			"new_password":     newPw,
		}, http.StatusOK, http.StatusNoContent)
		_ = changeResp.Body.Close()

		// Changing the password invalidates the session; sign in again.
		again, err := c.doJSON("POST", "/api/v1/auth/login", map[string]string{
			"username": username,
			"password": newPw,
		})
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = again.Body.Close() }()

		checkOK(again, http.StatusOK)
	}
}

// mintCLIToken creates the long-lived "oa-cli" access token on the signed-in
// session and returns its secret and server-side id.
func mintCLIToken(c *Client) (string, string) {
	tokResp := sessionDo(c, http.MethodPost, "/api/v1/auth/tokens", map[string]string{
		"name": "oa-cli",
		"kind": "access",
	}, http.StatusCreated, http.StatusOK)

	defer func() { _ = tokResp.Body.Close() }()

	var result struct {
		Token string `json:"token"`
		ID    string `json:"id"`
	}
	decodeJSON(tokResp, &result)

	if result.Token == "" {
		dief("server did not return a token")
	}

	return result.Token, result.ID
}

func readNewPassword(fromStdin bool) string {
	if fromStdin {
		return readPasswordStdin()
	}

	newPw := readPassword("New password: ")
	if newPw != readPassword("Confirm new password: ") {
		dief("passwords do not match")
	}

	return newPw
}

// sessionDo issues a state-changing request on a cookie session (body nil for
// none), echoing the CSRF cookie in the X-CSRF-Token header as the server
// requires.
func sessionDo(c *Client, method, path string, body any, ok ...int) *http.Response {
	var rdr io.Reader

	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			dief("%v", err)
		}

		rdr = bytes.NewReader(data)
	}

	base, err := url.Parse(c.baseURL)
	if err != nil {
		dief("%v", err)
	}

	//nolint:noctx // CLI tool, no context available
	req, err := http.NewRequest(method, c.baseURL+path, rdr)
	if err != nil {
		dief("%v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	for _, ck := range c.httpClient.Jar.Cookies(base) {
		if ck.Name == "oa_csrf" {
			req.Header.Set("X-Csrf-Token", ck.Value)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		dief("%v", err)
	}

	checkOK(resp, ok...)

	return resp
}

func newCookieJar() *cookiejar.Jar {
	jar, err := cookiejar.New(nil)
	if err != nil {
		dief("%v", err)
	}

	return jar
}

func authWhoami(client *Client, outFmt string) {
	resp, err := client.do("GET", "/api/v1/auth/me", nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)

	var result map[string]any
	decodeJSON(resp, &result)

	if outFmt == "json" {
		printJSON(result)
		return
	}

	w := newTabWriter()

	for _, k := range []string{"id", "username", "email", "full_name", "is_admin", "last_login_at"} {
		if v, ok := result[k]; ok {
			_, _ = fmt.Fprintf(w, "%s\t%v\n", k, v)
		}
	}

	_ = w.Flush()
}

func authPasswdCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "passwd",
		Short: "Change your password",
		Args:  usageArgs(0, 0),
	}

	passwordStdin := cmd.Flags().Bool("password-stdin", false, "Read current then new password from two lines on stdin")
	legacy := []*string{
		cmd.Flags().String("password", "", "removed; use --password-stdin"),
		cmd.Flags().String("old-password", "", "removed; use --password-stdin"),
		cmd.Flags().String("new-password", "", "removed; use --password-stdin"),
	}

	for _, name := range []string{"password", "old-password", "new-password"} {
		mustRegister(cmd.Flags().MarkHidden(name))
	}

	cmd.Run = func(*cobra.Command, []string) {
		for _, v := range legacy {
			if *v != "" {
				dief("%v", errPasswordFlag)
			}
		}

		authPasswd(a.client, *passwordStdin)
	}

	return cmd
}

func authPasswd(client *Client, passwordStdin bool) {
	var current, newPw string

	if passwordStdin {
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

func authTokenCmd(a *app) *cobra.Command {
	list := &cobra.Command{
		Use:   "list",
		Short: "List your API tokens",
		Args:  usageArgs(0, 0),
		Run: func(*cobra.Command, []string) {
			listGeneric(a.client, "/api/v1/auth/tokens", "data",
				[]string{"ID", "NAME", "KIND", "EXPIRES", "CREATED"},
				[]string{"id", "name", "kind", "expires_at", "created_at"}, a.outFmt)
		},
	}

	create := &cobra.Command{
		Use:   "create --name <name>",
		Short: "Create an API token (signs in with the account password)",
		Long: "Create an API token. The server refuses to mint tokens for a caller that authenticates " +
			"with a token, so the account password is asked for (or read with --password-stdin).",
		Args:              usageArgs(0, 0),
		ValidArgsFunction: positional(),
	}

	name := create.Flags().String("name", "", "Token name")
	kind := create.Flags().String("kind", "access", "Token kind: access|deploy")
	username := create.Flags().String("username", "", "Account to sign in as (default: the saved token's owner)")
	passwordStdin := create.Flags().Bool("password-stdin", false, "Read the account password from stdin")

	flagEnum(create, "kind", "access", "deploy")

	create.Run = func(*cobra.Command, []string) {
		if *name == "" {
			dief("--name is required")
		}

		session := sessionFromFlags(a.client, *username, *passwordStdin)

		resp := sessionDo(
			session,
			http.MethodPost,
			"/api/v1/auth/tokens",
			map[string]string{"name": *name, "kind": *kind},
			http.StatusCreated,
		)
		defer func() { _ = resp.Body.Close() }()

		var result map[string]any
		decodeJSON(resp, &result)

		if a.outFmt == formatJSON {
			printJSON(result)
			return
		}

		fmt.Printf("Token created: %v\n", result["token"])
	}

	revoke := &cobra.Command{
		Use:               "revoke <id> [--username U] [--password-stdin]",
		Short:             "Revoke an API token (signs in with the account password)",
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(a.listNames("/api/v1/auth/tokens", "data", "id")),
	}

	revUser := revoke.Flags().String("username", "", "Account to sign in as (default: the saved token's owner)")
	revStdin := revoke.Flags().Bool("password-stdin", false, "Read the account password from stdin")

	revoke.Run = func(_ *cobra.Command, args []string) {
		session := sessionFromFlags(a.client, *revUser, *revStdin)

		resp := sessionDo(session, http.MethodDelete, "/api/v1/auth/tokens/"+escape(args[0]), nil, http.StatusNoContent)
		_ = resp.Body.Close()

		fmt.Println("Token revoked.")
	}

	return parentCmd("token", "Manage API tokens", list, create, revoke)
}
