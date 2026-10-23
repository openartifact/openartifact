// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
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
	// TokenID is the server-side id of the token minted by `auth login`, so
	// `auth logout --revoke` can revoke it.
	TokenID string `json:"token_id,omitempty"`
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

	fmt.Fprintf(os.Stderr, "Error (%d): %s\n", resp.StatusCode, errorMessage(body))
}

// errorMessage extracts the human readable message of an API error body (its
// message, error or details field), falling back to the raw body.
func errorMessage(body []byte) string {
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

	return msg
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
		// A corrupt config must not block the commands that repair or do not
		// need it: `auth login` rewrites it; help and version never read it.
		if !tolerantOfBadConfig(os.Args[1:]) {
			dief("loading config: %v (fix or delete %s; `oa auth login` rewrites it)", err, configPath())
		}

		cfg = &Config{Server: "http://localhost:8080"}
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

	tokenFlagSet := false

	globalFlags.Visit(func(f *flag.Flag) { tokenFlagSet = tokenFlagSet || f.Name == "token" })
	warnCredentialExposure(os.Stderr, *serverURL, *token, tokenFlagSet)

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

	// ── Trash ─────────────────────────────────────────────────────────────
	case "trash":
		handleTrash(args, client, *outFmt)

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

	// ── Project ───────────────────────────────────────────────────────────
	case "project":
		handleProject(args, client, *outFmt)

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
	case "vex":
		handleVEX(args, client, *outFmt)
	case "dtrack":
		handleDTrack(args, client, *outFmt)

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

	// ── Declarative configuration ─────────────────────────────────────────
	case "apply":
		os.Exit(handleApply(args, client, *outFmt))
	case "diff":
		os.Exit(handleDiff(args, client, *outFmt))
	case "export":
		os.Exit(handleExport(args, client))

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

// tolerantOfBadConfig reports whether args name a command that can run with an
// unreadable config file: help, version, and `auth login`.
func tolerantOfBadConfig(args []string) bool {
	var words []string

	for i := 0; i < len(args) && len(words) < 2; i++ {
		if strings.HasPrefix(args[i], "-") {
			if args[i] == "-h" || args[i] == "--help" {
				return true
			}

			// Global flags that take a value as a separate argument.
			if (args[i] == "--server" || args[i] == "--token" || args[i] == "--format") && i+1 < len(args) {
				i++
			}

			continue
		}

		words = append(words, args[i])
	}

	switch {
	case len(words) == 0:
		return true
	case words[0] == "help" || words[0] == "version":
		return true
	case len(words) == 2 && words[0] == "auth" && words[1] == "login":
		return true
	}

	return false
}

// ─── Usage ────────────────────────────────────────────────────────────────────

func printUsage() {
	fmt.Fprintf(os.Stderr, "OpenArtifact CLI v%s\n\nUsage:\n  oa [--server URL] [--token TOKEN] [--format table|json] <command> [args]\n\nGlobal flags:\n  --server    Server URL  (default: http://localhost:8080, env: OA_SERVER)\n  --token     API token   (env: OA_TOKEN)\n  --format    Output format: table|json  (default: table)\n\nAuth:\n  auth login [--server URL] [--username U] [--password-stdin]\n                                     Login with username/password (prompted without echo) or token\n  auth logout [--revoke --username U --password-stdin]\n                                     Clear saved credentials (--revoke also revokes the server-side token)\n  auth whoami                        Show current user\n  auth passwd [--password-stdin]     Change your password (stdin: current, then new, one per line)\n  auth token list                    List your personal API tokens\n  auth token create --name N [--kind access|deploy] [--username U] [--password-stdin]\n                                     Create a personal API token (signs in with your password)\n  auth token revoke <id> [--username U] [--password-stdin]\n                                     Revoke a personal API token (signs in with your password)\n\nRepositories:\n  repo list                          List repositories\n  repo create <name> --format <fmt> [--description D] [--public] [--trash-retention-days N] [--immutable-releases] [--sign-packages]\n                                     Create repository\n  repo show <name>                   Show repository details\n  repo update <name> [--description D] [--public[=false]] [--trash-retention-days N|default] [--immutable-releases[=false]] [--sign-packages[=false]]\n                                     Update repository\n  repo delete <name> --confirm <name>  Delete repository (moved to the trash; prompts for the name on a terminal)\n  repo packages <name>               List packages in repository\n  repo members <name>                List the member repositories of a group repository\n\nTrash:\n  trash list [--repo NAME] [--kind K]  List trashed items (all: admin; --repo: repository delete permission)\n  trash restore <kind> <id> [--repo NAME]  Restore a trashed item or repository\n  trash purge <kind> <id> [--yes]    Permanently delete a trashed item now (admin)\n\nArtifacts:\n  artifact list <repo>               List packages in repository\n  artifact upload <repo> <file>      Upload artifact (auto-detects deb/rpm; files over 5 GiB switch to a multipart presigned upload)\n  artifact upload --presigned [--path P] [--content-type T] [--multipart] [--part-size SIZE] [--parallel N] <repo> <file>\n                                     Upload a raw artifact straight to the object store (presigned PUT; files over 5 GiB,\n                                     --multipart or --part-size (128M, 1G or bytes) upload in parts, --parallel 1-16, default 4)\n  artifact delete <repo> <id>        Delete artifact by ID\n\nSearch:\n  search <query> [--repo R] [--fmt F]  Search artifacts\n\nUsers (admin):\n  user list                          List users\n  user create --username U --email E [--password-stdin]  Create user (password prompted without echo)\n  show <id>                     Show user\n  update <id> [--email E] [--full-name N] [--disabled[=false]]  Update user\n  delete <id>                   Delete user\n\nGroups (admin):\n  group list                         List groups\n  group create <name>                Create group\n  show <id>                    Show group\n  delete <id>                  Delete group\n  members <id>                 List group members\n  group add-member <id> <user-id>    Add member to group\n  rm-member <id> <user-id>     Remove member from group\n\nProjects:\n  project list                       List projects\n  project create <name>              Create a project\n  project show <name>                Show project (includes my_role)\n  project update <name> [--display-name N] [--description D]\n                                     Update a project (owner); policy flags (--allowed-formats,\n                                     --allowed-repo-types, --repo-name-prefix, --allow-public-repos,\n                                     --max-repositories, --token-max-ttl-days) need a global admin\n  project delete <name>              Delete a project\n  project members <name>             List project members\n  project add-member <name> (<user-id> | --username U) [--role viewer|developer|maintainer|owner]\n                                     Add member to a project\n  project set-role <name> <user-id> <role>\n                                     Change a member's role\n  project rm-member <name> <user-id> Remove member from project\n  project groups <name>              List group grants\n  project add-group <name> <group-id|group-name> [--role viewer|developer|maintainer]\n                                     Grant a group access\n  project rm-group <name> <group-id|group-name>\n                                     Remove a group grant\n\nTokens (admin):\n  token list                         List all tokens\n  token revoke <id>                  Revoke any token\n\nGPG keys (admin):\n  gpg list                           List GPG keys\n  gpg import --key <file|-> [--name N] [--passphrase-stdin] [--activate|--no-activate]\n                                     Import a private key (passphrase: stdin or env OA_GPG_PASSPHRASE)\n  gpg generate [--name N] [--algorithm ed25519|rsa4096] [--user-name U] [--email E] [--comment C] [--activate|--no-activate]\n                                     Generate a signing key\n  gpg delete <key> [--force]         Delete a key (id, name or fingerprint)\n  gpg activate <key> [--repository R]    Make key the signer (global or per deb/rpm repo)\n  gpg deactivate <key> [--repository R]  Stop using key for signing\n  gpg export <key> [--output FILE]   Export the public key\n\nStorage backends (admin):\n  storage list                       List storage backends\n  storage create --name N [--type T] [--config-file F|-] [--default]\n                                     Create storage backend (credentials via --config-file, - = stdin)\n  storage show <name>                Show storage backend\n  storage update <name> [--config-file F|-] [--default[=false]]  Update storage backend\n  storage delete <name>              Delete storage backend\n  storage test <name>                Test storage backend\n\nBackup & restore (admin):\n  backup list                        List backups\n  backup create [--type full|metadata-only]  Create backup\n  show <id>                   Show backup\n  download <id> <dest>        Download backup\n  restore <archive-file>      Restore from a downloaded backup archive\n\nAudit log (admin):\n  audit list [--limit N] [--action A] [--actor A]  List audit entries\n  audit export [--format jsonl|csv] [--since T] [--until T] [--actor A] [--action A] [-o FILE]\n                                     Export the audit log (default: jsonl to stdout)\n\nWebhooks:\n  webhook list                       List global webhooks\n  webhook create --name N --url U --events E,E [--secret S] [--active=false]  Create webhook\n  show <id>                  Show webhook\n  update <id> [--name N] [--url U] [--events E,E] [--active[=false]]  Update webhook\n  delete <id>                Delete webhook\n  test <id>                  Test webhook\n  deliveries <id>            List webhook deliveries\n  webhook repo list <repo>           List repo webhooks\n  webhook repo create <repo> --name N --url U --events E,E [--secret S]  Create repo webhook\n\nSBOM:\n  sbom list <repo>                   List SBOMs for repository\n  sbom generate <repo> --artifact-id ID [--format cyclonedx|spdx|spdx-json|spdx3-json] [--spec-version V]\n                                     Generate an SBOM for one artifact (CycloneDX 1.4-1.6, SPDX 2.3)\n  sbom show <repo> <id>              Show SBOM\n  sbom download <repo> <id> <dest> [--attest true|bundle]   Download SBOM (or its signed attestation)\n  sbom delete <repo> <id>            Delete SBOM\n  vex get <repo> [--artifact-id ID] [--format openvex|cyclonedx] [--spec-version V] [--output FILE] [--attest true|bundle]\n                                     VEX document of an artifact or the whole repository (stdout without --output)\n  vex list <repo> [--artifact-id ID] List recorded VEX decisions\n  vex set <repo> --artifact-id ID --vuln CVE --status S [--justification J] [--impact TEXT] [--action TEXT]\n                                     Record a VEX decision (not_affected requires --justification)\n  vex delete <repo> <id>             Delete a VEX decision\n  dtrack show|test|pushes <repo>     Dependency-Track integration of a repository\n  dtrack push <repo> --artifact-id ID   Push an artifact to Dependency-Track\n\nVulnerability scanning:\n  vuln scan <repo> --artifact-id ID --package P --version V --ecosystem E [--scanner osv|trivy|grype]\n                                     Scan one package for vulnerabilities\n  vuln list <repo>                   List vulnerability scans\n  vuln show <repo> <id>              Show vulnerability scan\n  vuln quarantine <repo> <id>        Quarantine vulnerable package\n  vuln delete <repo> <id>            Delete vulnerability scan\n\nLicense scanning:\n  license scan <repo> --artifact-id ID  Scan one artifact for license issues\n  license list <repo>                List license scans\n  license show <repo> <id>           Show license scan\n\nSecret scanning:\n  secret scan <repo> --artifact-id ID [--content-file F|-]  Scan one artifact (and optional content) for secrets\n  secret list <repo>                 List secret scans\n  secret show <repo> <id>            Show secret scan\n  secret quarantine <repo> <id>      Quarantine secret\n  delete <repo> <id>          Delete secret scan\n\nCleanup policies:\n  cleanup list <repo>                List cleanup policies\n  cleanup create <repo> --name N [--keep-count N] [--keep-days N] [--name-pattern RE] [--schedule CRON] [--enabled=false]\n                                     Create cleanup policy (needs --keep-count or --keep-days)\n  cleanup show <repo> <id>           Show cleanup policy\n  cleanup update <repo> <id> [same flags]  Update cleanup policy\n  cleanup delete <repo> <id>         Delete cleanup policy\n  cleanup run <repo> <id>            Run cleanup policy now\n\nStorage quotas:\n  quota show <repo>                  Show repository quota\n  set <repo> [--limit N --unit MB|GB|TB] [--max-artifacts N] [--warn-percent P]  Set quota\n  delete <repo>                Remove quota\n\nArtifact properties:\n  props list <repo> <artifact-id>    List artifact properties\n  props set <repo> <artifact-id> <key> <value>  Set property\n  props delete <repo> <artifact-id> <key>       Delete property\n  props search <repo> <key> <value>  Search artifacts by property\n\nMigration:\n  import nexus|artifactory --source-url URL --repo SRC[:DST]... [--dry-run] [--create-repos]\n                                     [--concurrency N] [--state-file F]  Import from Nexus / Artifactory\n\nDeclarative configuration (admin):\n  apply -f FILE|DIR|- [-R] [--kind K] [--prune|--prune-all] [--force-secrets] [--continue-on-error] [--validate-only] [-o json]\n                                     Make the server match the YAML manifests\n  diff  -f FILE|DIR|- [-R] [--kind K] [--prune|--prune-all] [-o json]\n                                     Show what apply would change (exit 0 none, 2 changes, 1 error)\n  export [--kind K] [--with-members] [-o DIR]\n                                     Write the current configuration as manifests\n\nPackage deltas:\n  delta list <repo> <pkg-id>         List available deltas for package\n  delta get <repo> <pkg-id> --from <sha256> <dest>  Download delta patch\n\nSystem:\n  system info                        Show system information\n\n  version                            Show CLI version\n  help                               Show this help\n\n", version) //nolint:dupword // usage text repeats the command name
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
		// The global --server / OA_SERVER is the default for login too; the
		// subcommand's own --server still wins.
		loginCfg := *cfg
		loginCfg.Server = client.baseURL
		authLogin(args[1:], &loginCfg)
	case "logout":
		authLogout(args[1:], cfg)
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

	var savedToken, savedTokenID string

	if username == "" {
		savedToken = readPassword("API token: ")
	} else {
		var password string
		if *passwordStdin {
			password = readPasswordStdin()
		} else {
			password = readPassword("Password: ")
		}
		savedToken, savedTokenID = sessionLoginToken(newClient(*serverURL, ""), username, password, *passwordStdin)
	}

	newCfg := &Config{Server: *serverURL, Token: savedToken, TokenID: savedTokenID}
	if err := saveConfig(newCfg); err != nil {
		dief("saving config: %v", err)
	}

	fmt.Println("Credentials saved.")
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

// authLogout clears the saved credentials. The server-side access token minted
// by `auth login` does not expire and API tokens cannot revoke tokens, so a
// real revocation needs a session: with --revoke the CLI signs in with
// --username/--password-stdin and revokes the saved token id.
func authLogout(args []string, cfg *Config) {
	fs := flag.NewFlagSet("auth logout", flag.ExitOnError)
	revoke := fs.Bool("revoke", false, "Also revoke the saved token on the server (needs --username and --password-stdin)")
	usernameFlag := fs.String("username", "", "Username (with --revoke)")
	passwordStdin := fs.Bool("password-stdin", false, "Read the password from stdin (with --revoke)")
	_ = fs.Parse(args)

	tokenID := cfg.TokenID
	revoked := false

	if *revoke {
		if tokenID == "" || *usernameFlag == "" || !*passwordStdin {
			dief("--revoke needs a login saved by this version of oa, --username and --password-stdin")
		}

		c := newClient(cfg.Server, "")
		c.httpClient.Jar = newCookieJar()
		sessionSignIn(c, *usernameFlag, readPasswordStdin(), true)

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
		fmt.Printf("The access token (id %s) is still valid on the server; revoke it in the web UI, or run `oa auth logout --revoke --username U --password-stdin` before logging out.\n", tokenID)
	}
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

// ═══════════════════════════════════════════════════════════════════════════════
// Generic helpers
// ═══════════════════════════════════════════════════════════════════════════════

// getJSON issues a GET and decodes the (200) JSON response into v.
func getJSON(client *Client, path string, v any) {
	resp, err := client.do("GET", path, nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)
	decodeJSON(resp, v)
}

// sendJSON issues a request with a JSON body (nil for none), requires one of
// the expected statuses, and returns the decoded object response (nil when the
// response has no JSON object body, e.g. 204).
func sendJSON(client *Client, method, path string, body any, expected ...int) map[string]any {
	resp, err := client.doJSON(method, path, body)
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp, expected...)

	data, _ := io.ReadAll(resp.Body)

	var out map[string]any
	if json.Unmarshal(data, &out) != nil {
		return nil
	}

	return out
}

// listItems extracts the array under listKey from an object response, or
// accepts a bare array. Any other shape is an error rather than a guess.
var errNoList = errors.New("response has no list under key")

func listItems(body []byte, listKey string) ([]map[string]any, error) {
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(body, &wrapper); err == nil {
		raw, ok := wrapper[listKey]
		if !ok {
			return nil, fmt.Errorf("%w %q", errNoList, listKey)
		}

		if string(raw) == "null" {
			return nil, nil
		}

		var items []map[string]any
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("decoding %q list: %w", listKey, err)
		}

		return items, nil
	}

	var items []map[string]any
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("unexpected response: %w", err)
	}

	return items, nil
}

// fetchList GETs path and returns the list under listKey.
func fetchList(client *Client, path, listKey string) []map[string]any {
	resp, err := client.do("GET", path, nil, "")
	if err != nil {
		dief("%v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	checkOK(resp)

	body, _ := io.ReadAll(resp.Body)

	items, err := listItems(body, listKey)
	if err != nil {
		dief("%v", err)
	}

	return items
}

// fetchAllPages walks a page/size paginated endpoint (response carries "total"
// and the list under listKey) and returns every item. sizeParam is the page
// size query parameter ("limit" or "per_page").
func fetchAllPages(client *Client, path, listKey, sizeParam string, size int) []map[string]any {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}

	var all []map[string]any

	for page := 1; ; page++ {
		resp, err := client.do("GET", fmt.Sprintf("%s%s%s=%d&page=%d", path, sep, sizeParam, size, page), nil, "")
		if err != nil {
			dief("%v", err)
		}

		checkOK(resp)

		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		items, err := listItems(body, listKey)
		if err != nil {
			dief("%v", err)
		}

		var meta struct {
			Total int `json:"total"`
		}
		_ = json.Unmarshal(body, &meta)

		all = append(all, items...)

		if len(items) == 0 || len(items) < size || (meta.Total > 0 && len(all) >= meta.Total) {
			return all
		}
	}
}

// listGeneric fetches a list endpoint whose response is {"<listKey>": [...]}
// (or a bare array) and prints a table.
func listGeneric(client *Client, path, listKey string, headers, fields []string, outFmt string) {
	printTable(fetchList(client, path, listKey), headers, fields, outFmt)
}

func printTable(items []map[string]any, headers, fields []string, outFmt string) {
	if outFmt == "json" {
		if items == nil {
			items = []map[string]any{}
		}

		printJSON(items)

		return
	}

	w := newTabWriter()
	_, _ = fmt.Fprintln(w, strings.Join(headers, "\t"))

	for _, item := range items {
		vals := make([]string, len(fields))
		for i, f := range fields {
			vals[i] = cell(item[f])
		}

		_, _ = fmt.Fprintln(w, strings.Join(vals, "\t"))
	}

	_ = w.Flush()
}

// cell renders a JSON value for a table: nil as "-", numbers without an
// exponent, everything else with %v.
func cell(v any) string {
	switch t := v.(type) {
	case nil:
		return "-"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func showGeneric(client *Client, path, outFmt string) {
	var result map[string]any
	getJSON(client, path, &result)
	printObject(result, outFmt)
}

// printObject prints a JSON object as indented JSON or as sorted key/value rows.
func printObject(result map[string]any, outFmt string) {
	if outFmt == "json" {
		printJSON(result)
		return
	}

	keys := make([]string, 0, len(result))
	for k := range result {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	w := newTabWriter()
	for _, k := range keys {
		_, _ = fmt.Fprintf(w, "%s\t%s\n", k, cell(result[k]))
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

// setFlags returns the names of the flags explicitly passed on the command
// line, so partial-update commands send only what the user asked to change.
func setFlags(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}

	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	return set
}

// escape makes a user-supplied value safe as one URL path segment.
func escape(s string) string { return url.PathEscape(s) }

// warnCredentialExposure prints warnings on w for the two ways the CLI can leak
// its token: a --token argument (visible to every local user in the process
// list and kept in shell history) and a token sent over plain http to a
// non-loopback server.
func warnCredentialExposure(w io.Writer, serverURL, token string, tokenFlagSet bool) {
	if token == "" {
		return
	}

	if tokenFlagSet {
		fmt.Fprintln(w, "Warning: --token is visible in the process list and shell history; prefer OA_TOKEN or `oa auth login`.")
	}

	if u, err := url.Parse(serverURL); err == nil && u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		fmt.Fprintf(w, "Warning: sending the API token over plain http to %s; use https.\n", u.Host)
	}
}
