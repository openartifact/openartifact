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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

// ─── Config ──────────────────────────────────────────────────────────────────

type Config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "oa", "config.json")
}

func loadConfig() (*Config, error) {
	data, err := os.ReadFile(configPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{Server: "http://localhost:8080"}, nil
		}

		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	if cfg.Server == "" {
		cfg.Server = "http://localhost:8080"
	}

	return &cfg, nil
}

func saveConfig(cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	return writeFileAtomic(configPath(), data, configDirMode, configFileMode)
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}

// ─── HTTP Client ─────────────────────────────────────────────────────────────

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func newClient(serverURL, token string) *Client {
	return &Client{
		baseURL:    strings.TrimSuffix(serverURL, "/"),
		token:      token,
		httpClient: newHTTPClient(),
	}
}

func (c *Client) do(method, path string, body io.Reader, contentType string) (*http.Response, error) {
	//nolint:noctx // CLI tool, no context available
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	return c.httpClient.Do(req)
}

func (c *Client) doJSON(method, path string, body any) (*http.Response, error) {
	var r io.Reader

	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}

		r = bytes.NewReader(data)
	}

	return c.do(method, path, r, "application/json")
}

// ─── Output helpers ───────────────────────────────────────────────────────────

func dief(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	os.Exit(1)
}

func printError(resp *http.Response) {
	body, _ := io.ReadAll(resp.Body)

	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Details string `json:"details"`
	}

	_ = json.Unmarshal(body, &e)

	msg := e.Message
	if msg == "" {
		msg = e.Error
	}

	if msg == "" {
		msg = e.Details
	}

	if msg == "" {
		msg = string(body)
	}

	fmt.Fprintf(os.Stderr, "Error (%d): %s\n", resp.StatusCode, msg)
}

func checkOK(resp *http.Response, expected ...int) {
	codes := expected
	if len(codes) == 0 {
		codes = []int{http.StatusOK}
	}

	if slices.Contains(codes, resp.StatusCode) {
		return
	}

	printError(resp)
	os.Exit(1)
}

func printJSON(v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling JSON: %v\n", err)
		return
	}

	fmt.Println(string(data))
}

func decodeJSON(resp *http.Response, v any) {
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		dief("decoding response: %v", err)
	}
}

func newTabWriter() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func main() {
	cfg, err := loadConfig()
	if err != nil {
		dief("loading config: %v", err)
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Parse global flags before the command
	globalFlags := flag.NewFlagSet("global", flag.ContinueOnError)
	globalFlags.SetOutput(io.Discard)
	serverURL := globalFlags.String("server", getEnv("OA_SERVER", cfg.Server), "")
	token := globalFlags.String("token", getEnv("OA_TOKEN", cfg.Token), "")
	outFmt := globalFlags.String("format", "table", "")
	_ = globalFlags.Parse(os.Args[1:])

	remaining := globalFlags.Args()
	if len(remaining) == 0 {
		printUsage()
		os.Exit(1)
	}

	command := remaining[0]
	args := remaining[1:]
	client := newClient(*serverURL, *token)

	switch command {
	// ── Auth ──────────────────────────────────────────────────────────────
	case "auth":
		handleAuth(args, client, cfg, *outFmt)

	// ── Repo ──────────────────────────────────────────────────────────────
	case "repo":
		handleRepo(args, client, *outFmt)

	// ── Artifact / package ────────────────────────────────────────────────
	case "artifact":
		handleArtifact(args, client, *outFmt)

	// ── Search ────────────────────────────────────────────────────────────
	case "search":
		handleSearch(args, client, *outFmt)

	// ── User (admin) ──────────────────────────────────────────────────────
	case "user":
		handleUser(args, client, *outFmt)

	// ── Group (admin) ─────────────────────────────────────────────────────
	case "group":
		handleGroup(args, client, *outFmt)

	// ── Org ───────────────────────────────────────────────────────────────
	case "org":
		handleOrg(args, client, *outFmt)

	// ── Token (admin) ─────────────────────────────────────────────────────
	case "token":
		handleToken(args, client, *outFmt)

	// ── GPG ───────────────────────────────────────────────────────────────
	case "gpg":
		handleGPG(args, client, *outFmt)

	// ── Storage backend (admin) ───────────────────────────────────────────
	case "storage":
		handleStorage(args, client, *outFmt)

	// ── Backup / restore (admin) ──────────────────────────────────────────
	case "backup":
		handleBackup(args, client, *outFmt)

	// ── Audit log (admin) ─────────────────────────────────────────────────
	case "audit":
		handleAudit(args, client, *outFmt)

	// ── Webhook ───────────────────────────────────────────────────────────
	case "webhook":
		handleWebhook(args, client, *outFmt)

	// ── SBOM ──────────────────────────────────────────────────────────────
	case "sbom":
		handleSBOM(args, client, *outFmt)

	// ── Vulnerability scan ────────────────────────────────────────────────
	case "vuln":
		handleVuln(args, client, *outFmt)

	// ── License scan ──────────────────────────────────────────────────────
	case "license":
		handleLicense(args, client, *outFmt)

	// ── Secret scan ───────────────────────────────────────────────────────
	case "secret":
		handleSecret(args, client, *outFmt)

	// ── Cleanup policies ──────────────────────────────────────────────────
	case "cleanup":
		handleCleanup(args, client, *outFmt)

	// ── Quota ─────────────────────────────────────────────────────────────
	case "quota":
		handleQuota(args, client, *outFmt)

	// ── Properties ────────────────────────────────────────────────────────
	case "props":
		handleProps(args, client, *outFmt)

	// ── Delta ─────────────────────────────────────────────────────────────
	case "delta":
		handleDelta(args, client, *outFmt)
	case "import":
		os.Exit(handleImport(args, client, *outFmt))

	// ── System ────────────────────────────────────────────────────────────
	case "system":
		handleSystem(args, client, *outFmt)

	// ── Utility ───────────────────────────────────────────────────────────
	case "version":
		fmt.Printf("oa version %s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}
}

// ─── Usage ────────────────────────────────────────────────────────────────────

func printUsage() {
	fmt.Fprintf(os.Stderr, "OpenArtifact CLI v%s\n\nUsage:\n  oa [--server URL] [--token TOKEN] [--format table|json] <command> [args]\n\nGlobal flags:\n  --server    Server URL  (default: http://localhost:8080, env: OA_SERVER)\n  --token     API token   (env: OA_TOKEN)\n  --format    Output format: table|json  (default: table)\n\nAuth:\n  auth login [--server URL] [--username U] [--password-stdin]\n                                     Login with username/password (prompted without echo) or token\n  auth logout                        Clear saved credentials\n  auth whoami                        Show current user\n  auth passwd [--password-stdin]     Change your password (stdin: current, then new, one per line)\n  auth token list                    List your personal API tokens\n  auth token create --name N         Create a personal API token\n  auth token revoke <id>             Revoke a personal API token\n\nRepositories:\n  repo list                          List repositories\n  repo create <name> --format <fmt>  Create repository\n  repo show <name>                   Show repository details\n  repo update <name> [flags]         Update repository\n  repo delete <name>                 Delete repository\n  repo packages <name>               List packages in repository\n  repo members <name>                List repository members\n\nArtifacts:\n  artifact list <repo>               List packages in repository\n  artifact upload <repo> <file>      Upload artifact (auto-detects deb/rpm)\n  artifact delete <repo> <id>        Delete artifact by ID\n\nSearch:\n  search <query> [--repo R] [--fmt F]  Search artifacts\n\nUsers (admin):\n  user list                          List users\n  user create --username U --email E [--password-stdin]  Create user (password prompted without echo)\n  show <id>                     Show user\n  update <id> [flags]           Update user\n  delete <id>                   Delete user\n\nGroups (admin):\n  group list                         List groups\n  group create <name>                Create group\n  show <id>                    Show group\n  delete <id>                  Delete group\n  members <id>                 List group members\n  group add-member <id> <user-id>    Add member to group\n  rm-member <id> <user-id>     Remove member from group\n\nOrganizations:\n  org list                           List organizations\n  org create <name>                  Create organization\n  org show <name>                    Show organization\n  org delete <name>                  Delete organization\n  org members <name>                 List org members\n  org add-member <name> <user-id>    Add member to org\n  rm-member <name> <user-id>     Remove member from org\n\nTokens (admin):\n  token list                         List all tokens\n  token revoke <id>                  Revoke any token\n\nGPG keys (admin):\n  gpg list                           List GPG keys\n  gpg import --key <file>            Import GPG key\n  gpg generate --name N --email E    Generate GPG key\n  gpg delete <fingerprint>           Delete GPG key\n\nStorage backends (admin):\n  storage list                       List storage backends\n  storage create [flags]             Create storage backend\n  storage show <name>                Show storage backend\n  storage update <name> [flags]      Update storage backend\n  storage delete <name>              Delete storage backend\n  storage test <name>                Test storage backend\n\nBackup & restore (admin):\n  backup list                        List backups\n  backup create [--type full|meta]   Create backup\n  show <id>                   Show backup\n  download <id> <dest>        Download backup\n  restore <id>                Restore from backup\n\nAudit log (admin):\n  audit list [--limit N] [--action A] [--user U]  List audit entries\n\nWebhooks:\n  webhook list                       List global webhooks\n  webhook create [flags]             Create webhook\n  show <id>                  Show webhook\n  update <id> [flags]        Update webhook\n  delete <id>                Delete webhook\n  test <id>                  Test webhook\n  deliveries <id>            List webhook deliveries\n  webhook repo list <repo>           List repo webhooks\n  webhook repo create <repo> [flags] Create repo webhook\n\nSBOM:\n  sbom list <repo>                   List SBOMs for repository\n  sbom generate <repo>               Generate SBOM for repository\n  sbom show <repo> <id>              Show SBOM\n  sbom download <repo> <id> <dest>   Download SBOM\n  sbom delete <repo> <id>            Delete SBOM\n\nVulnerability scanning:\n  vuln scan <repo>                   Scan repository for vulnerabilities\n  vuln list <repo>                   List vulnerability scans\n  vuln show <repo> <id>              Show vulnerability scan\n  vuln quarantine <repo> <id>        Quarantine vulnerable package\n  vuln delete <repo> <id>            Delete vulnerability scan\n\nLicense scanning:\n  license scan <repo>                Scan repository for license issues\n  license list <repo>                List license scans\n  license show <repo> <id>           Show license scan\n  license delete <repo> <id>         Delete license scan\n\nSecret scanning:\n  secret scan <repo>                 Scan repository for secrets\n  secret list <repo>                 List secret scans\n  secret show <repo> <id>            Show secret scan\n  secret quarantine <repo> <id>      Quarantine secret\n  delete <repo> <id>          Delete secret scan\n\nCleanup policies:\n  cleanup list <repo>                List cleanup policies\n  cleanup create <repo> [flags]      Create cleanup policy\n  cleanup show <repo> <id>           Show cleanup policy\n  cleanup update <repo> <id> [flags] Update cleanup policy\n  cleanup delete <repo> <id>         Delete cleanup policy\n  cleanup run <repo> <id>            Run cleanup policy now\n\nStorage quotas:\n  quota show <repo>                  Show repository quota\n  set <repo> --limit N --unit MB|GB|TB  Set quota\n  delete <repo>                Remove quota\n\nArtifact properties:\n  props list <repo> <artifact-id>    List artifact properties\n  props set <repo> <artifact-id> <key> <value>  Set property\n  props delete <repo> <artifact-id> <key>       Delete property\n  props search <repo> <key> <value>  Search artifacts by property\n\nMigration:\n  import nexus|artifactory --source-url URL --repo SRC[:DST]... [--dry-run] [--create-repos]\n                                     [--concurrency N] [--state-file F]  Import from Nexus / Artifactory\n\nPackage deltas:\n  delta list <repo> <pkg-id>         List available deltas for package\n  delta get <repo> <pkg-id> --from <sha256> <dest>  Download delta patch\n\nSystem:\n  system info                        Show system information\n\n  version                            Show CLI version\n  help                               Show this help\n\n", version)
}

// ═══════════════════════════════════════════════════════════════════════════════
// AUTH
// ═══════════════════════════════════════════════════════════════════════════════

func handleAuth(args []string, client *Client, cfg *Config, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa auth <login|logout|whoami|passwd|token>")
	}

	switch args[0] {
	case "login":
		authLogin(args[1:], cfg)
	case "logout":
		authLogout(cfg)
	case "whoami":
		authWhoami(client, outFmt)
	case "passwd":
		authPasswd(args[1:], client)
	case "token":
		authToken(args[1:], client, outFmt)
	default:
		dief("Unknown auth subcommand: %s", args[0])
	}
}

func authLogin(args []string, cfg *Config) {
	fs := flag.NewFlagSet("auth login", flag.ExitOnError)
	serverURL := fs.String("server", cfg.Server, "Server URL")
	usernameFlag := fs.String("username", "", "Username (required with --password-stdin)")
	passwordStdin := fs.Bool("password-stdin", false, "Read the password from stdin")
	legacyPassword := fs.String("password", "", "removed; use --password-stdin")
	_ = fs.Parse(args)

	if *legacyPassword != "" {
		dief("%v", errPasswordFlag)
	}

	username := *usernameFlag

	switch {
	case *passwordStdin:
		if username == "" {
			dief("--username is required with --password-stdin")
		}
	case username == "":
		username = promptLine("Username (leave blank to use token): ")
	}

	var savedToken string

	if username == "" {
		savedToken = readPassword("API token: ")
	} else {
		var password string
		if *passwordStdin {
			password = readPasswordStdin()
		} else {
			password = readPassword("Password: ")
		}
		c := newClient(*serverURL, "")

		resp, err := c.doJSON("POST", "/api/v1/auth/login", map[string]string{
			"username": username,
			"password": password,
		})
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK)

		var result struct {
			Token string `json:"token"`
		}
		decodeJSON(resp, &result)

		savedToken = result.Token
		if savedToken == "" {
			dief("server did not return a token")
		}
	}

	newCfg := &Config{Server: *serverURL, Token: savedToken}
	if err := saveConfig(newCfg); err != nil {
		dief("saving config: %v", err)
	}

	fmt.Println("Credentials saved.")
}

func authLogout(cfg *Config) {
	cfg.Token = ""
	if err := saveConfig(cfg); err != nil {
		dief("clearing credentials: %v", err)
	}

	fmt.Println("Logged out.")
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

	resp, err := client.doJSON("POST", "/api/v1/auth/change-password", map[string]string{
		"current_password": current,
		"new_password":     newPw,
	})
	if err != nil {
		dief("%v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	checkOK(resp, http.StatusOK, http.StatusNoContent)
	fmt.Println("Password changed.")
}

func authToken(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa auth token <list|create|revoke>")
	}

	switch args[0] {
	case "list":
		resp, err := client.do("GET", "/api/v1/auth/tokens", nil, "")
		if err != nil {
			dief("%v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		checkOK(resp)

		var result struct {
			Tokens []map[string]any `json:"tokens"`
		}
		decodeJSON(resp, &result)

		if outFmt == "json" {
			printJSON(result.Tokens)
			return
		}

		w := newTabWriter()

		_, _ = fmt.Fprintln(w, "ID\tNAME\tCREATED")
		for _, t := range result.Tokens {
			_, _ = fmt.Fprintf(w, "%v\t%v\t%v\n", t["id"], t["name"], t["created_at"])
		}

		_ = w.Flush()
	case "create":
		fs := flag.NewFlagSet("auth token create", flag.ExitOnError)
		name := fs.String("name", "", "Token name")
		_ = fs.Parse(args[1:])

		if *name == "" {
			dief("--name is required")
		}

		resp, err := client.doJSON("POST", "/api/v1/auth/tokens", map[string]string{"name": *name})
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusCreated, http.StatusOK)

		var result map[string]any
		decodeJSON(resp, &result)

		if outFmt == "json" {
			printJSON(result)
			return
		}

		fmt.Printf("Token created: %v\n", result["token"])
	case "revoke":
		if len(args) < 2 {
			dief("Usage: oa auth token revoke <id>")
		}

		resp, err := client.do("DELETE", "/api/v1/auth/tokens/"+args[1], nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Println("Token revoked.")
	default:
		dief("Unknown auth token subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// REPO
// ═══════════════════════════════════════════════════════════════════════════════

func handleRepo(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa repo <list|create|show|update|delete|packages|members>")
	}

	switch args[0] {
	case "list":
		repoList(client, outFmt)
	case "create":
		if len(args) < 2 {
			dief("Usage: oa repo create <name> [--format fmt] [--description D] [--public]")
		}

		repoCreate(client, args[1], args[2:], outFmt)
	case "show":
		if len(args) < 2 {
			dief("Usage: oa repo show <name>")
		}

		repoShow(client, args[1], outFmt)
	case "update":
		if len(args) < 2 {
			dief("Usage: oa repo update <name> [flags]")
		}

		repoUpdate(client, args[1], args[2:])
	case "delete":
		if len(args) < 2 {
			dief("Usage: oa repo delete <name>")
		}

		repoDelete(client, args[1])
	case "packages":
		if len(args) < 2 {
			dief("Usage: oa repo packages <name>")
		}

		repoPackages(client, args[1], outFmt)
	case "members":
		if len(args) < 2 {
			dief("Usage: oa repo members <name>")
		}

		repoMembers(client, args[1], outFmt)
	default:
		dief("Unknown repo subcommand: %s", args[0])
	}
}

func repoList(client *Client, outFmt string) {
	resp, err := client.do("GET", "/api/v1/repositories", nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)

	var result struct {
		Repositories []map[string]any `json:"repositories"`
	}
	decodeJSON(resp, &result)

	if outFmt == "json" {
		printJSON(result.Repositories)
		return
	}

	w := newTabWriter()

	_, _ = fmt.Fprintln(w, "NAME\tFORMAT\tDESCRIPTION\tPUBLIC")
	for _, r := range result.Repositories {
		_, _ = fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", r["name"], r["format"], r["description"], r["public"])
	}

	_ = w.Flush()
}

func repoCreate(client *Client, name string, args []string, outFmt string) {
	fs := flag.NewFlagSet("repo create", flag.ExitOnError)
	repoFmt := fs.String("format", "npm", "Repository format")
	desc := fs.String("description", "", "Description")
	public := fs.Bool("public", false, "Public repository")
	_ = fs.Parse(args)

	body := map[string]any{
		"name":        name,
		"format":      *repoFmt,
		"description": *desc,
		"public":      *public,
	}

	resp, err := client.doJSON("POST", "/api/v1/repositories", body)
	if err != nil {
		dief("%v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	checkOK(resp, http.StatusCreated, http.StatusOK)

	var result map[string]any
	decodeJSON(resp, &result)

	if outFmt == "json" {
		printJSON(result)
		return
	}

	fmt.Printf("Repository '%s' created.\n", name)
}

func repoShow(client *Client, name, outFmt string) {
	resp, err := client.do("GET", "/api/v1/repositories/"+name, nil, "")
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

	for _, k := range []string{"name", "format", "description", "public", "created_at", "updated_at"} {
		if v, ok := result[k]; ok {
			_, _ = fmt.Fprintf(w, "%s\t%v\n", k, v)
		}
	}

	_ = w.Flush()
}

func repoUpdate(client *Client, name string, args []string) {
	fs := flag.NewFlagSet("repo update", flag.ExitOnError)
	desc := fs.String("description", "", "Description")
	public := fs.Bool("public", false, "Public")
	_ = fs.Parse(args)

	body := map[string]any{}
	if *desc != "" {
		body["description"] = *desc
	}

	body["public"] = *public

	resp, err := client.doJSON("PATCH", "/api/v1/repositories/"+name, body)
	if err != nil {
		dief("%v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	checkOK(resp, http.StatusOK, http.StatusNoContent)
	fmt.Printf("Repository '%s' updated.\n", name)
}

func repoDelete(client *Client, name string) {
	resp, err := client.do("DELETE", "/api/v1/repositories/"+name, nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp, http.StatusOK, http.StatusNoContent)
	fmt.Printf("Repository '%s' deleted.\n", name)
}

func repoPackages(client *Client, name, outFmt string) {
	resp, err := client.do("GET", "/api/v1/repositories/"+name+"/packages", nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)

	var result struct {
		Packages []map[string]any `json:"packages"`
	}
	decodeJSON(resp, &result)

	if outFmt == "json" {
		printJSON(result.Packages)
		return
	}

	w := newTabWriter()

	_, _ = fmt.Fprintln(w, "ID\tNAME\tVERSION\tARCH\tSIZE")
	for _, p := range result.Packages {
		_, _ = fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n", p["id"], p["name"], p["version"], p["arch"], p["size"])
	}

	_ = w.Flush()
}

func repoMembers(client *Client, name, outFmt string) {
	resp, err := client.do("GET", "/api/v1/repositories/"+name+"/members", nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)

	var result []map[string]any
	decodeJSON(resp, &result)

	if outFmt == "json" {
		printJSON(result)
		return
	}

	w := newTabWriter()

	_, _ = fmt.Fprintln(w, "USER_ID\tROLE")
	for _, m := range result {
		_, _ = fmt.Fprintf(w, "%v\t%v\n", m["user_id"], m["role"])
	}

	_ = w.Flush()
}

// ═══════════════════════════════════════════════════════════════════════════════
// ARTIFACT
// ═══════════════════════════════════════════════════════════════════════════════

func handleArtifact(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa artifact <list|upload|delete>")
	}

	switch args[0] {
	case "list":
		if len(args) < 2 {
			dief("Usage: oa artifact list <repo>")
		}

		repoPackages(client, args[1], outFmt)
	case "upload":
		if len(args) < 3 {
			dief("Usage: oa artifact upload <repo> <file>")
		}

		artifactUpload(client, args[1], args[2])
	case "delete":
		if len(args) < 3 {
			dief("Usage: oa artifact delete <repo> <id>")
		}

		artifactDelete(client, args[1], args[2])
	default:
		dief("Unknown artifact subcommand: %s", args[0])
	}
}

func artifactUpload(client *Client, repo, filePath string) {
	file, err := os.Open(filePath)
	if err != nil {
		dief("opening file: %v", err)
	}
	defer func() { _ = file.Close() }()

	body, contentType := multipartStream(file, "file", filepath.Base(filePath))
	defer func() { _ = body.Close() }()

	// Choose endpoint based on file extension
	ext := strings.ToLower(filepath.Ext(filePath))

	var endpoint string

	switch ext {
	case ".deb":
		endpoint = "/api/v1/repositories/" + repo + "/debs"
	default:
		endpoint = "/api/v1/repositories/" + repo + "/packages"
	}

	resp, err := client.do("POST", endpoint, body, contentType)
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp, http.StatusCreated, http.StatusOK)
	fmt.Printf("Uploaded '%s' to '%s'.\n", filepath.Base(filePath), repo)
}

func artifactDelete(client *Client, repo, id string) {
	resp, err := client.do("DELETE", "/api/v1/repositories/"+repo+"/packages/"+id, nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp, http.StatusOK, http.StatusNoContent)
	fmt.Printf("Artifact '%s' deleted from '%s'.\n", id, repo)
}

// ═══════════════════════════════════════════════════════════════════════════════
// SEARCH
// ═══════════════════════════════════════════════════════════════════════════════

func handleSearch(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa search <query> [--repo R] [--fmt F]")
	}

	fs := flag.NewFlagSet("search", flag.ExitOnError)
	repoFilter := fs.String("repo", "", "Filter by repository")
	fmtFilter := fs.String("fmt", "", "Filter by format")
	limit := fs.Int("limit", 50, "Max results")
	_ = fs.Parse(args)

	query := strings.Join(fs.Args(), " ")
	if query == "" {
		dief("query is required")
	}

	q := url.Values{}
	q.Set("q", query)

	if *repoFilter != "" {
		q.Set("repo", *repoFilter)
	}

	if *fmtFilter != "" {
		q.Set("format", *fmtFilter)
	}

	q.Set("limit", strconv.Itoa(*limit))

	resp, err := client.do("GET", "/api/v1/search?"+q.Encode(), nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)

	var result struct {
		Results []map[string]any `json:"results"`
	}
	decodeJSON(resp, &result)

	if outFmt == "json" {
		printJSON(result.Results)
		return
	}

	w := newTabWriter()

	_, _ = fmt.Fprintln(w, "NAME\tREPO\tVERSION\tFORMAT")
	for _, r := range result.Results {
		_, _ = fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", r["name"], r["repository"], r["version"], r["format"])
	}

	_ = w.Flush()
}

// ═══════════════════════════════════════════════════════════════════════════════
// USER (admin)
// ═══════════════════════════════════════════════════════════════════════════════

func handleUser(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa user <list|create|show|update|delete>")
	}

	switch args[0] {
	case "list":
		listGeneric(client, "/api/v1/users", "users", []string{"ID", "USERNAME", "EMAIL", "DISABLED"}, []string{"id", "username", "email", "disabled"}, outFmt)
	case "create":
		fs := flag.NewFlagSet("user create", flag.ExitOnError)
		username := fs.String("username", "", "Username")
		email := fs.String("email", "", "Email")
		passwordStdin := fs.Bool("password-stdin", false, "Read the password from stdin")
		legacyPassword := fs.String("password", "", "removed; use --password-stdin")
		fullName := fs.String("full-name", "", "Full name")
		isAdmin := fs.Bool("admin", false, "Admin user")
		_ = fs.Parse(args[1:])

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
			"username":  *username,
			"email":     *email,
			"password":  password,
			"full_name": *fullName,
			"is_admin":  *isAdmin,
		}

		resp, err := client.doJSON("POST", "/api/v1/users", body)
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusCreated, http.StatusOK)
		fmt.Printf("User '%s' created.\n", *username)
	case "show":
		if len(args) < 2 {
			dief("Usage: oa user show <id>")
		}

		showGeneric(client, "/api/v1/users/"+args[1], outFmt)
	case "update":
		if len(args) < 2 {
			dief("Usage: oa user update <id> [flags]")
		}

		fs := flag.NewFlagSet("user update", flag.ExitOnError)
		email := fs.String("email", "", "Email")
		fullName := fs.String("full-name", "", "Full name")
		disabled := fs.Bool("disabled", false, "Disable user")
		_ = fs.Parse(args[2:])

		body := map[string]any{}
		if *email != "" {
			body["email"] = *email
		}

		if *fullName != "" {
			body["full_name"] = *fullName
		}

		body["disabled"] = *disabled

		resp, err := client.doJSON("PATCH", "/api/v1/users/"+args[1], body)
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Printf("User '%s' updated.\n", args[1])
	case "delete":
		if len(args) < 2 {
			dief("Usage: oa user delete <id>")
		}

		resp, err := client.do("DELETE", "/api/v1/users/"+args[1], nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Printf("User '%s' deleted.\n", args[1])
	default:
		dief("Unknown user subcommand: %s", args[0])
	}
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
		listGeneric(client, "/api/v1/groups", "groups", []string{"ID", "NAME"}, []string{"id", "name"}, outFmt)
	case "create":
		if len(args) < 2 {
			dief("Usage: oa group create <name>")
		}

		resp, err := client.doJSON("POST", "/api/v1/groups", map[string]string{"name": args[1]})
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusCreated, http.StatusOK)
		fmt.Printf("Group '%s' created.\n", args[1])
	case "show":
		if len(args) < 2 {
			dief("Usage: oa group show <id>")
		}

		showGeneric(client, "/api/v1/groups/"+args[1], outFmt)
	case "delete":
		if len(args) < 2 {
			dief("Usage: oa group delete <id>")
		}

		resp, err := client.do("DELETE", "/api/v1/groups/"+args[1], nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Printf("Group '%s' deleted.\n", args[1])
	case "members":
		if len(args) < 2 {
			dief("Usage: oa group members <id>")
		}

		listGenericRaw(client, "/api/v1/groups/"+args[1]+"/members", []string{"USER_ID", "USERNAME"}, []string{"user_id", "username"}, outFmt)
	case "add-member":
		if len(args) < 3 {
			dief("Usage: oa group add-member <id> <user-id>")
		}

		resp, err := client.do("POST", "/api/v1/groups/"+args[1]+"/members/"+args[2], nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent, http.StatusCreated)
		fmt.Println("Member added.")
	case "rm-member":
		if len(args) < 3 {
			dief("Usage: oa group rm-member <id> <user-id>")
		}

		resp, err := client.do("DELETE", "/api/v1/groups/"+args[1]+"/members/"+args[2], nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Println("Member removed.")
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
		body := map[string]any{
			"name":         args[1],
			"display_name": *displayName,
			"description":  *desc,
		}

		resp, err := client.doJSON("POST", "/api/v1/orgs", body)
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusCreated, http.StatusOK)
		fmt.Printf("Organization '%s' created.\n", args[1])
	case "show":
		if len(args) < 2 {
			dief("Usage: oa org show <name>")
		}

		showGeneric(client, "/api/v1/orgs/"+args[1], outFmt)
	case "delete":
		if len(args) < 2 {
			dief("Usage: oa org delete <name>")
		}

		resp, err := client.do("DELETE", "/api/v1/orgs/"+args[1], nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Printf("Organization '%s' deleted.\n", args[1])
	case "members":
		if len(args) < 2 {
			dief("Usage: oa org members <name>")
		}

		listGenericRaw(client, "/api/v1/orgs/"+args[1]+"/members", []string{"USER_ID", "ROLE"}, []string{"user_id", "role"}, outFmt)
	case "add-member":
		if len(args) < 3 {
			dief("Usage: oa org add-member <name> <user-id> [--role member|owner]")
		}

		fs := flag.NewFlagSet("org add-member", flag.ExitOnError)
		role := fs.String("role", "member", "Role: member|owner")
		_ = fs.Parse(args[3:])

		resp, err := client.doJSON("POST", "/api/v1/orgs/"+args[1]+"/members", map[string]string{
			"user_id": args[2],
			"role":    *role,
		})
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent, http.StatusCreated)
		fmt.Println("Member added.")
	case "rm-member":
		if len(args) < 3 {
			dief("Usage: oa org rm-member <name> <user-id>")
		}

		resp, err := client.do("DELETE", "/api/v1/orgs/"+args[1]+"/members/"+args[2], nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Println("Member removed.")
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
		listGeneric(client, "/api/v1/admin/tokens", "tokens", []string{"ID", "NAME", "PRINCIPAL"}, []string{"id", "name", "principal"}, outFmt)
	case "revoke":
		if len(args) < 2 {
			dief("Usage: oa token revoke <id>")
		}

		resp, err := client.do("DELETE", "/api/v1/admin/tokens/"+args[1], nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Println("Token revoked.")
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

		resp, err := client.doJSON("POST", "/api/v1/admin/gpg/keys", map[string]string{"armored_key": string(data)})
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusCreated, http.StatusOK)
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

		resp, err := client.doJSON("POST", "/api/v1/admin/gpg/keys/generate", map[string]any{
			"name":  *name,
			"email": *email,
			"bits":  *bits,
		})
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusCreated, http.StatusOK)

		var result map[string]any
		decodeJSON(resp, &result)

		if outFmt == "json" {
			printJSON(result)
			return
		}

		fmt.Printf("GPG key generated: %v\n", result["fingerprint"])
	case "delete":
		if len(args) < 2 {
			dief("Usage: oa gpg delete <fingerprint>")
		}

		resp, err := client.do("DELETE", "/api/v1/admin/gpg/keys/"+args[1], nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Println("GPG key deleted.")
	default:
		dief("Unknown gpg subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// STORAGE BACKEND (admin)
// ═══════════════════════════════════════════════════════════════════════════════

func handleStorage(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa storage <list|create|show|update|delete|test>")
	}

	switch args[0] {
	case "list":
		listGeneric(client, "/api/v1/admin/storage", "backends", []string{"NAME", "TYPE", "DEFAULT"}, []string{"name", "type", "is_default"}, outFmt)
	case "create":
		fs := flag.NewFlagSet("storage create", flag.ExitOnError)
		name := fs.String("name", "", "Backend name")
		sType := fs.String("type", "filesystem", "Backend type: filesystem|s3|gcs|azure|garage|seaweedfs|oci")
		config := fs.String("config", "{}", "JSON config object")
		isDefault := fs.Bool("default", false, "Set as default")
		_ = fs.Parse(args[1:])

		if *name == "" {
			dief("--name is required")
		}

		var cfgMap map[string]any
		if err := json.Unmarshal([]byte(*config), &cfgMap); err != nil {
			dief("invalid --config JSON: %v", err)
		}

		body := map[string]any{
			"name":       *name,
			"type":       *sType,
			"config":     cfgMap,
			"is_default": *isDefault,
		}

		resp, err := client.doJSON("POST", "/api/v1/admin/storage", body)
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusCreated, http.StatusOK)
		fmt.Printf("Storage backend '%s' created.\n", *name)
	case "show":
		if len(args) < 2 {
			dief("Usage: oa storage show <name>")
		}

		showGeneric(client, "/api/v1/admin/storage/"+args[1], outFmt)
	case "update":
		if len(args) < 2 {
			dief("Usage: oa storage update <name> [flags]")
		}

		fs := flag.NewFlagSet("storage update", flag.ExitOnError)
		config := fs.String("config", "", "JSON config object")
		isDefault := fs.Bool("default", false, "Set as default")
		_ = fs.Parse(args[2:])
		body := map[string]any{"is_default": *isDefault}

		if *config != "" {
			var cfgMap map[string]any
			if err := json.Unmarshal([]byte(*config), &cfgMap); err != nil {
				dief("invalid --config JSON: %v", err)
			}

			body["config"] = cfgMap
		}

		resp, err := client.doJSON("PATCH", "/api/v1/admin/storage/"+args[1], body)
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Printf("Storage backend '%s' updated.\n", args[1])
	case "delete":
		if len(args) < 2 {
			dief("Usage: oa storage delete <name>")
		}

		resp, err := client.do("DELETE", "/api/v1/admin/storage/"+args[1], nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Printf("Storage backend '%s' deleted.\n", args[1])
	case "test":
		if len(args) < 2 {
			dief("Usage: oa storage test <name>")
		}

		resp, err := client.do("POST", "/api/v1/admin/storage/"+args[1]+"/test", nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK)
		fmt.Println("Storage backend test passed.")
	default:
		dief("Unknown storage subcommand: %s", args[0])
	}
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
		listGeneric(client, "/api/v1/admin/backup", "backups", []string{"ID", "TYPE", "STATUS", "SIZE", "CREATED"}, []string{"id", "type", "status", "size", "created_at"}, outFmt)
	case "create":
		fs := flag.NewFlagSet("backup create", flag.ExitOnError)
		bType := fs.String("type", "full", "Backup type: full|metadata")
		_ = fs.Parse(args[1:])

		resp, err := client.doJSON("POST", "/api/v1/admin/backup", map[string]string{"type": *bType})
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusCreated, http.StatusOK, http.StatusAccepted)

		var result map[string]any
		decodeJSON(resp, &result)

		if outFmt == "json" {
			printJSON(result)
			return
		}

		fmt.Printf("Backup created: %v\n", result["id"])
	case "show":
		if len(args) < 2 {
			dief("Usage: oa backup show <id>")
		}

		showGeneric(client, "/api/v1/admin/backup/"+args[1], outFmt)
	case "download":
		if len(args) < 3 {
			dief("Usage: oa backup download <id> <dest>")
		}

		downloadFile(client, "/api/v1/admin/backup/"+args[1]+"/download", args[2])
	case "restore":
		if len(args) < 2 {
			dief("Usage: oa backup restore <id>")
		}

		resp, err := client.doJSON("POST", "/api/v1/admin/restore", map[string]string{"backup_id": args[1]})
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusAccepted)
		fmt.Println("Restore initiated.")
	default:
		dief("Unknown backup subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// AUDIT (admin)
// ═══════════════════════════════════════════════════════════════════════════════

func handleAudit(args []string, client *Client, outFmt string) {
	if len(args) < 1 || args[0] == "list" {
		fs := flag.NewFlagSet("audit list", flag.ExitOnError)
		limit := fs.Int("limit", 50, "Max entries")
		action := fs.String("action", "", "Filter by action")
		user := fs.String("user", "", "Filter by user")
		_ = fs.Parse(args)

		q := url.Values{}
		q.Set("limit", strconv.Itoa(*limit))

		if *action != "" {
			q.Set("action", *action)
		}

		if *user != "" {
			q.Set("user", *user)
		}

		resp, err := client.do("GET", "/api/v1/admin/audit?"+q.Encode(), nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp)

		var result struct {
			Entries []map[string]any `json:"entries"`
		}
		decodeJSON(resp, &result)

		if outFmt == "json" {
			printJSON(result.Entries)
			return
		}

		w := newTabWriter()

		_, _ = fmt.Fprintln(w, "TIME\tUSER\tACTION\tRESOURCE")
		for _, e := range result.Entries {
			_, _ = fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", e["created_at"], e["username"], e["action"], e["resource"])
		}

		_ = w.Flush()

		return
	}

	dief("Unknown audit subcommand: %s", args[0])
}

// ═══════════════════════════════════════════════════════════════════════════════
// WEBHOOK
// ═══════════════════════════════════════════════════════════════════════════════

func handleWebhook(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa webhook <list|create|show|update|delete|test|deliveries|repo>")
	}

	switch args[0] {
	case "list":
		listGeneric(client, "/api/v1/webhooks", "webhooks", []string{"ID", "URL", "ACTIVE"}, []string{"id", "url", "active"}, outFmt)
	case "create":
		fs := flag.NewFlagSet("webhook create", flag.ExitOnError)
		whURL := fs.String("url", "", "Webhook URL")
		secret := fs.String("secret", "", "Webhook secret")
		active := fs.Bool("active", true, "Active")
		_ = fs.Parse(args[1:])

		if *whURL == "" {
			dief("--url is required")
		}

		body := map[string]any{"url": *whURL, "secret": *secret, "active": *active}

		resp, err := client.doJSON("POST", "/api/v1/webhooks", body)
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusCreated, http.StatusOK)
		fmt.Println("Webhook created.")
	case "show":
		if len(args) < 2 {
			dief("Usage: oa webhook show <id>")
		}

		showGeneric(client, "/api/v1/webhooks/"+args[1], outFmt)
	case "update":
		if len(args) < 2 {
			dief("Usage: oa webhook update <id> [flags]")
		}

		fs := flag.NewFlagSet("webhook update", flag.ExitOnError)
		whURL := fs.String("url", "", "Webhook URL")
		active := fs.Bool("active", true, "Active")
		_ = fs.Parse(args[2:])

		body := map[string]any{"active": *active}
		if *whURL != "" {
			body["url"] = *whURL
		}

		resp, err := client.doJSON("PUT", "/api/v1/webhooks/"+args[1], body)
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Println("Webhook updated.")
	case "delete":
		if len(args) < 2 {
			dief("Usage: oa webhook delete <id>")
		}

		resp, err := client.do("DELETE", "/api/v1/webhooks/"+args[1], nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Println("Webhook deleted.")
	case "test":
		if len(args) < 2 {
			dief("Usage: oa webhook test <id>")
		}

		resp, err := client.do("POST", "/api/v1/webhooks/"+args[1]+"/test", nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK)
		fmt.Println("Webhook test sent.")
	case "deliveries":
		if len(args) < 2 {
			dief("Usage: oa webhook deliveries <id>")
		}

		listGenericRaw(client, "/api/v1/webhooks/"+args[1]+"/deliveries", []string{"ID", "STATUS", "CREATED"}, []string{"id", "status", "created_at"}, outFmt)
	case "repo":
		if len(args) < 2 {
			dief("Usage: oa webhook repo <list|create> <repo> [flags]")
		}

		switch args[1] {
		case "list":
			if len(args) < 3 {
				dief("Usage: oa webhook repo list <repo>")
			}

			listGeneric(client, "/api/v1/repositories/"+args[2]+"/webhooks", "webhooks", []string{"ID", "URL", "ACTIVE"}, []string{"id", "url", "active"}, outFmt)
		case "create":
			if len(args) < 3 {
				dief("Usage: oa webhook repo create <repo> --url U")
			}

			fs := flag.NewFlagSet("webhook repo create", flag.ExitOnError)
			whURL := fs.String("url", "", "Webhook URL")
			secret := fs.String("secret", "", "Secret")
			_ = fs.Parse(args[3:])

			if *whURL == "" {
				dief("--url is required")
			}

			resp, err := client.doJSON("POST", "/api/v1/repositories/"+args[2]+"/webhooks", map[string]any{
				"url": *whURL, "secret": *secret, "active": true,
			})
			if err != nil {
				dief("%v", err)
			}

			defer func() { _ = resp.Body.Close() }()

			checkOK(resp, http.StatusCreated, http.StatusOK)
			fmt.Println("Repo webhook created.")
		default:
			dief("Unknown webhook repo subcommand: %s", args[1])
		}
	default:
		dief("Unknown webhook subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// SBOM
// ═══════════════════════════════════════════════════════════════════════════════

func handleSBOM(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa sbom <list|generate|show|download|delete> <repo> [id]")
	}

	switch args[0] {
	case "list":
		if len(args) < 2 {
			dief("Usage: oa sbom list <repo>")
		}

		listGeneric(client, "/api/v1/repositories/"+args[1]+"/sbom", "sboms", []string{"ID", "FORMAT", "CREATED"}, []string{"id", "format", "created_at"}, outFmt)
	case "generate":
		if len(args) < 2 {
			dief("Usage: oa sbom generate <repo>")
		}

		resp, err := client.do("POST", "/api/v1/repositories/"+args[1]+"/sbom/generate", nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusCreated, http.StatusOK, http.StatusAccepted)
		fmt.Println("SBOM generation started.")
	case "show":
		if len(args) < 3 {
			dief("Usage: oa sbom show <repo> <id>")
		}

		showGeneric(client, "/api/v1/repositories/"+args[1]+"/sbom/"+args[2], outFmt)
	case "download":
		if len(args) < 4 {
			dief("Usage: oa sbom download <repo> <id> <dest>")
		}

		downloadFile(client, "/api/v1/repositories/"+args[1]+"/sbom/"+args[2]+"/download", args[3])
	case "delete":
		if len(args) < 3 {
			dief("Usage: oa sbom delete <repo> <id>")
		}

		resp, err := client.do("DELETE", "/api/v1/repositories/"+args[1]+"/sbom/"+args[2], nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Println("SBOM deleted.")
	default:
		dief("Unknown sbom subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// VULN
// ═══════════════════════════════════════════════════════════════════════════════

func handleVuln(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa vuln <scan|list|show|quarantine|delete> <repo> [id]")
	}

	switch args[0] {
	case "scan":
		if len(args) < 2 {
			dief("Usage: oa vuln scan <repo>")
		}

		resp, err := client.do("POST", "/api/v1/repositories/"+args[1]+"/vuln/scan", nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusCreated, http.StatusOK, http.StatusAccepted)
		fmt.Println("Vulnerability scan started.")
	case "list":
		if len(args) < 2 {
			dief("Usage: oa vuln list <repo>")
		}

		listGeneric(client, "/api/v1/repositories/"+args[1]+"/vuln/scans", "scans", []string{"ID", "STATUS", "CRITICAL", "HIGH", "CREATED"}, []string{"id", "status", "critical_count", "high_count", "created_at"}, outFmt)
	case "show":
		if len(args) < 3 {
			dief("Usage: oa vuln show <repo> <id>")
		}

		showGeneric(client, "/api/v1/repositories/"+args[1]+"/vuln/scans/"+args[2], outFmt)
	case "quarantine":
		if len(args) < 3 {
			dief("Usage: oa vuln quarantine <repo> <id>")
		}

		resp, err := client.do("POST", "/api/v1/repositories/"+args[1]+"/vuln/scans/"+args[2]+"/quarantine", nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK)
		fmt.Println("Package quarantined.")
	case "delete":
		if len(args) < 3 {
			dief("Usage: oa vuln delete <repo> <id>")
		}

		resp, err := client.do("DELETE", "/api/v1/repositories/"+args[1]+"/vuln/scans/"+args[2], nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Println("Scan deleted.")
	default:
		dief("Unknown vuln subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// LICENSE
// ═══════════════════════════════════════════════════════════════════════════════

func handleLicense(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa license <scan|list|show|delete> <repo> [id]")
	}

	switch args[0] {
	case "scan":
		if len(args) < 2 {
			dief("Usage: oa license scan <repo>")
		}

		resp, err := client.do("POST", "/api/v1/repositories/"+args[1]+"/license/scan", nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusCreated, http.StatusOK, http.StatusAccepted)
		fmt.Println("License scan started.")
	case "list":
		if len(args) < 2 {
			dief("Usage: oa license list <repo>")
		}

		listGeneric(client, "/api/v1/repositories/"+args[1]+"/license/scans", "scans", []string{"ID", "STATUS", "VIOLATIONS", "CREATED"}, []string{"id", "status", "violation_count", "created_at"}, outFmt)
	case "show":
		if len(args) < 3 {
			dief("Usage: oa license show <repo> <id>")
		}

		showGeneric(client, "/api/v1/repositories/"+args[1]+"/license/scans/"+args[2], outFmt)
	case "delete":
		if len(args) < 3 {
			dief("Usage: oa license delete <repo> <id>")
		}

		resp, err := client.do("DELETE", "/api/v1/repositories/"+args[1]+"/license/scans/"+args[2], nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Println("Scan deleted.")
	default:
		dief("Unknown license subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// SECRET
// ═══════════════════════════════════════════════════════════════════════════════

func handleSecret(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa secret <scan|list|show|quarantine|delete> <repo> [id]")
	}

	switch args[0] {
	case "scan":
		if len(args) < 2 {
			dief("Usage: oa secret scan <repo>")
		}

		resp, err := client.do("POST", "/api/v1/repositories/"+args[1]+"/secrets/scan", nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusCreated, http.StatusOK, http.StatusAccepted)
		fmt.Println("Secret scan started.")
	case "list":
		if len(args) < 2 {
			dief("Usage: oa secret list <repo>")
		}

		listGeneric(client, "/api/v1/repositories/"+args[1]+"/secrets/scans", "scans", []string{"ID", "STATUS", "FINDINGS", "CREATED"}, []string{"id", "status", "finding_count", "created_at"}, outFmt)
	case "show":
		if len(args) < 3 {
			dief("Usage: oa secret show <repo> <id>")
		}

		showGeneric(client, "/api/v1/repositories/"+args[1]+"/secrets/scans/"+args[2], outFmt)
	case "quarantine":
		if len(args) < 3 {
			dief("Usage: oa secret quarantine <repo> <id>")
		}

		resp, err := client.do("POST", "/api/v1/repositories/"+args[1]+"/secrets/scans/"+args[2]+"/quarantine", nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK)
		fmt.Println("Package quarantined.")
	case "delete":
		if len(args) < 3 {
			dief("Usage: oa secret delete <repo> <id>")
		}

		resp, err := client.do("DELETE", "/api/v1/repositories/"+args[1]+"/secrets/scans/"+args[2], nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Println("Scan deleted.")
	default:
		dief("Unknown secret subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// CLEANUP
// ═══════════════════════════════════════════════════════════════════════════════

func handleCleanup(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa cleanup <list|create|show|update|delete|run> <repo> [id]")
	}

	switch args[0] {
	case "list":
		if len(args) < 2 {
			dief("Usage: oa cleanup list <repo>")
		}

		listGeneric(client, "/api/v1/repositories/"+args[1]+"/cleanup-policies", "policies", []string{"ID", "NAME", "TYPE", "ENABLED"}, []string{"id", "name", "type", "enabled"}, outFmt)
	case "create":
		if len(args) < 2 {
			dief("Usage: oa cleanup create <repo> [flags]")
		}

		fs := flag.NewFlagSet("cleanup create", flag.ExitOnError)
		name := fs.String("name", "", "Policy name")
		pType := fs.String("type", "keep-last-n", "Type: keep-last-n|keep-by-age")
		keepCount := fs.Int("keep-count", 10, "Number of versions to keep")
		keepDays := fs.Int("keep-days", 30, "Days to keep")
		enabled := fs.Bool("enabled", true, "Enabled")
		_ = fs.Parse(args[2:])

		if *name == "" {
			dief("--name is required")
		}

		body := map[string]any{
			"name":       *name,
			"type":       *pType,
			"keep_count": *keepCount,
			"keep_days":  *keepDays,
			"enabled":    *enabled,
		}

		resp, err := client.doJSON("POST", "/api/v1/repositories/"+args[1]+"/cleanup-policies", body)
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusCreated, http.StatusOK)
		fmt.Println("Cleanup policy created.")
	case "show":
		if len(args) < 3 {
			dief("Usage: oa cleanup show <repo> <id>")
		}

		showGeneric(client, "/api/v1/repositories/"+args[1]+"/cleanup-policies/"+args[2], outFmt)
	case "update":
		if len(args) < 3 {
			dief("Usage: oa cleanup update <repo> <id> [flags]")
		}

		fs := flag.NewFlagSet("cleanup update", flag.ExitOnError)
		enabled := fs.Bool("enabled", true, "Enabled")
		keepCount := fs.Int("keep-count", 0, "Keep count")
		keepDays := fs.Int("keep-days", 0, "Keep days")
		_ = fs.Parse(args[3:])

		body := map[string]any{"enabled": *enabled}
		if *keepCount > 0 {
			body["keep_count"] = *keepCount
		}

		if *keepDays > 0 {
			body["keep_days"] = *keepDays
		}

		resp, err := client.doJSON("PUT", "/api/v1/repositories/"+args[1]+"/cleanup-policies/"+args[2], body)
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Println("Cleanup policy updated.")
	case "delete":
		if len(args) < 3 {
			dief("Usage: oa cleanup delete <repo> <id>")
		}

		resp, err := client.do("DELETE", "/api/v1/repositories/"+args[1]+"/cleanup-policies/"+args[2], nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Println("Cleanup policy deleted.")
	case "run":
		if len(args) < 3 {
			dief("Usage: oa cleanup run <repo> <id>")
		}

		resp, err := client.do("POST", "/api/v1/repositories/"+args[1]+"/cleanup-policies/"+args[2]+"/run", nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusAccepted)
		fmt.Println("Cleanup policy run initiated.")
	default:
		dief("Unknown cleanup subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// QUOTA
// ═══════════════════════════════════════════════════════════════════════════════

func handleQuota(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa quota <show|set|delete> <repo>")
	}

	switch args[0] {
	case "show":
		if len(args) < 2 {
			dief("Usage: oa quota show <repo>")
		}

		showGeneric(client, "/api/v1/repositories/"+args[1]+"/quota", outFmt)
	case "set":
		if len(args) < 2 {
			dief("Usage: oa quota set <repo> --limit N --unit MB|GB|TB")
		}

		fs := flag.NewFlagSet("quota set", flag.ExitOnError)
		limit := fs.Int64("limit", 0, "Quota limit")
		unit := fs.String("unit", "GB", "Unit: MB|GB|TB")
		_ = fs.Parse(args[2:])

		if *limit == 0 {
			dief("--limit is required")
		}

		multipliers := map[string]int64{"MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40}

		mult, ok := multipliers[strings.ToUpper(*unit)]
		if !ok {
			dief("invalid unit: %s (use MB, GB, or TB)", *unit)
		}

		body := map[string]any{"limit_bytes": *limit * mult}

		resp, err := client.doJSON("PUT", "/api/v1/repositories/"+args[1]+"/quota", body)
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Printf("Quota set to %d %s.\n", *limit, *unit)
	case "delete":
		if len(args) < 2 {
			dief("Usage: oa quota delete <repo>")
		}

		resp, err := client.do("DELETE", "/api/v1/repositories/"+args[1]+"/quota", nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Println("Quota removed.")
	default:
		dief("Unknown quota subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// PROPERTIES
// ═══════════════════════════════════════════════════════════════════════════════

func handleProps(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa props <list|set|delete|search> <repo> <artifact-id> [key] [value]")
	}

	switch args[0] {
	case "list":
		if len(args) < 3 {
			dief("Usage: oa props list <repo> <artifact-id>")
		}

		showGeneric(client, "/api/v1/repositories/"+args[1]+"/artifacts/"+args[2]+"/properties", outFmt)
	case "set":
		if len(args) < 5 {
			dief("Usage: oa props set <repo> <artifact-id> <key> <value>")
		}

		body := map[string]string{args[3]: args[4]}

		resp, err := client.doJSON("PATCH", "/api/v1/repositories/"+args[1]+"/artifacts/"+args[2]+"/properties", body)
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Printf("Property '%s' set.\n", args[3])
	case "delete":
		if len(args) < 4 {
			dief("Usage: oa props delete <repo> <artifact-id> <key>")
		}

		resp, err := client.do("DELETE", "/api/v1/repositories/"+args[1]+"/artifacts/"+args[2]+"/properties/"+args[3], nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp, http.StatusOK, http.StatusNoContent)
		fmt.Printf("Property '%s' deleted.\n", args[3])
	case "search":
		if len(args) < 4 {
			dief("Usage: oa props search <repo> <key> <value>")
		}

		q := url.Values{}
		q.Set("key", args[2])
		q.Set("value", args[3])

		resp, err := client.do("GET", "/api/v1/repositories/"+args[1]+"/artifacts/search?"+q.Encode(), nil, "")
		if err != nil {
			dief("%v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		checkOK(resp)

		var result []map[string]any
		decodeJSON(resp, &result)

		if outFmt == "json" {
			printJSON(result)
			return
		}

		w := newTabWriter()

		_, _ = fmt.Fprintln(w, "ID\tNAME\tVERSION")
		for _, a := range result {
			_, _ = fmt.Fprintf(w, "%v\t%v\t%v\n", a["id"], a["name"], a["version"])
		}

		_ = w.Flush()
	default:
		dief("Unknown props subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// DELTA
// ═══════════════════════════════════════════════════════════════════════════════

func handleDelta(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa delta <list|get> <repo> <pkg-id> [flags]")
	}

	switch args[0] {
	case "list":
		if len(args) < 3 {
			dief("Usage: oa delta list <repo> <pkg-id>")
		}

		listGeneric(client, "/api/v1/repositories/"+args[1]+"/packages/"+args[2]+"/deltas", "deltas", []string{"FROM_SHA256", "TO_SHA256", "SIZE"}, []string{"from_sha256", "to_sha256", "delta_size"}, outFmt)
	case "get":
		if len(args) < 3 {
			dief("Usage: oa delta get <repo> <pkg-id> --from <sha256> <dest>")
		}

		fs := flag.NewFlagSet("delta get", flag.ExitOnError)
		fromSHA := fs.String("from", "", "Source SHA256")
		_ = fs.Parse(args[3:])

		if *fromSHA == "" {
			dief("--from is required")
		}

		dest := fs.Arg(0)
		if dest == "" {
			dief("destination path is required")
		}

		q := url.Values{}
		q.Set("from", *fromSHA)
		downloadFile(client, "/api/v1/repositories/"+args[1]+"/packages/"+args[2]+"/delta?"+q.Encode(), dest)
	default:
		dief("Unknown delta subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// SYSTEM
// ═══════════════════════════════════════════════════════════════════════════════

func handleSystem(args []string, client *Client, outFmt string) {
	if len(args) < 1 || args[0] == "info" {
		resp, err := client.do("GET", "/api/v1/system/info", nil, "")
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

		for _, k := range []string{"version", "commit", "build_time", "go_version", "os", "arch"} {
			if v, ok := result[k]; ok {
				_, _ = fmt.Fprintf(w, "%s\t%v\n", k, v)
			}
		}

		_ = w.Flush()

		return
	}

	dief("Unknown system subcommand: %s", args[0])
}

// ═══════════════════════════════════════════════════════════════════════════════
// Generic helpers
// ═══════════════════════════════════════════════════════════════════════════════

// listGeneric fetches a paginated list endpoint and prints a table.
// The response is expected to be {"<listKey>": [...]} or a raw array.
func listGeneric(client *Client, path, listKey string, headers, fields []string, outFmt string) {
	resp, err := client.do("GET", path, nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)

	body, _ := io.ReadAll(resp.Body)

	// Try object with list key first
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(body, &wrapper); err == nil {
		if raw, ok := wrapper[listKey]; ok {
			var items []map[string]any
			if err := json.Unmarshal(raw, &items); err == nil {
				printTable(items, headers, fields, outFmt)
				return
			}
		}
		// Try any array value in the wrapper
		for _, raw := range wrapper {
			var items []map[string]any
			if err := json.Unmarshal(raw, &items); err == nil && len(items) > 0 {
				printTable(items, headers, fields, outFmt)
				return
			}
		}
	}

	// Try raw array
	var items []map[string]any
	if err := json.Unmarshal(body, &items); err == nil {
		printTable(items, headers, fields, outFmt)
		return
	}

	// Fallback: print raw
	fmt.Println(string(body))
}

// listGenericRaw is like listGeneric but expects a raw array response.
func listGenericRaw(client *Client, path string, headers, fields []string, outFmt string) {
	resp, err := client.do("GET", path, nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)

	var items []map[string]any
	decodeJSON(resp, &items)
	printTable(items, headers, fields, outFmt)
}

func printTable(items []map[string]any, headers, fields []string, outFmt string) {
	if outFmt == "json" {
		printJSON(items)
		return
	}

	w := newTabWriter()
	_, _ = fmt.Fprintln(w, strings.Join(headers, "\t"))

	for _, item := range items {
		vals := make([]string, len(fields))
		for i, f := range fields {
			vals[i] = fmt.Sprintf("%v", item[f])
		}

		_, _ = fmt.Fprintln(w, strings.Join(vals, "\t"))
	}

	_ = w.Flush()
}

func showGeneric(client *Client, path, outFmt string) {
	resp, err := client.do("GET", path, nil, "")
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
	for k, v := range result {
		_, _ = fmt.Fprintf(w, "%s\t%v\n", k, v)
	}

	_ = w.Flush()
}

func downloadFile(client *Client, path, dest string) {
	resp, err := client.do("GET", path, nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)

	n, err := downloadTo(resp, dest)
	if err != nil {
		dief("%v", err)
	}

	fmt.Printf("Downloaded %d bytes to '%s'.\n", n, dest)
}
