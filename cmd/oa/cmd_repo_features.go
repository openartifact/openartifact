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
	"strings"
)

// ═══════════════════════════════════════════════════════════════════════════════
// WEBHOOK
// ═══════════════════════════════════════════════════════════════════════════════

func webhookColumns() ([]string, []string) {
	return []string{"ID", "NAME", "URL", "ENABLED"}, []string{"id", "name", "url", "enabled"}
}

func handleWebhook(args []string, client *Client, outFmt string) {
	if len(args) < 1 {
		dief("Usage: oa webhook <list|create|show|update|delete|test|deliveries|repo>")
	}

	switch args[0] {
	case "list":
		wh, wf := webhookColumns()
		listGeneric(client, "/api/v1/webhooks", "webhooks", wh, wf, outFmt)
	case "create":
		fs := flag.NewFlagSet("webhook create", flag.ExitOnError)
		name := fs.String("name", "", "Webhook name")
		whURL := fs.String("url", "", "Webhook URL")
		secretFile := fs.String("secret-file", "", webhookSecretUsage)
		legacySecret := fs.String("secret", "", "removed; use --secret-file")
		events := fs.String("events", "", "Comma-separated event types, e.g. artifact.uploaded (required)")
		active := fs.Bool("active", true, "Enabled")
		_ = fs.Parse(args[1:])

		if *name == "" || *whURL == "" || *events == "" {
			dief("--name, --url and --events are required")
		}

		secret := resolveWebhookSecret(*secretFile, *legacySecret)

		sendJSON(client, "POST", "/api/v1/webhooks", webhookBody(*name, *whURL, secret, *events, *active), http.StatusCreated)
		fmt.Println("Webhook created.")
	case "show":
		if len(args) < 2 {
			dief("Usage: oa webhook show <id>")
		}

		showGeneric(client, "/api/v1/webhooks/"+escape(args[1]), outFmt)
	case "update":
		if len(args) < 2 {
			dief("Usage: oa webhook update <id> [--name N] [--url U] [--events E,E] [--active=false]")
		}

		webhookUpdate(client, args[1], args[2:])
	case "delete":
		if len(args) < 2 {
			dief("Usage: oa webhook delete <id>")
		}

		deleteResource(client, "/api/v1/webhooks/"+escape(args[1]), "Webhook deleted.")
	case "test":
		if len(args) < 2 {
			dief("Usage: oa webhook test <id>")
		}

		postEmpty(client, "/api/v1/webhooks/"+escape(args[1])+"/test", "Webhook test sent.", http.StatusAccepted)
	case "deliveries":
		if len(args) < 2 {
			dief("Usage: oa webhook deliveries <id>")
		}

		listGeneric(client, "/api/v1/webhooks/"+escape(args[1])+"/deliveries", "deliveries",
			[]string{"ID", "EVENT", "STATUS", "ATTEMPTS", "CREATED"},
			[]string{"id", "event_type", "status", "attempt_count", "created_at"}, outFmt)
	case "repo":
		webhookRepo(args[1:], client, outFmt)
	default:
		dief("Unknown webhook subcommand: %s", args[0])
	}
}

// webhookBody is the create payload: the server reads "enabled", not "active".
const (
	envWebhookSecret   = "OA_WEBHOOK_SECRET" //nolint:gosec // env var name, not a credential
	webhookSecretUsage = "file holding the webhook secret, '-' for stdin (or " + envWebhookSecret + ")"
)

// resolveWebhookSecret reads the secret from --secret-file ('-' = stdin) or
// OA_WEBHOOK_SECRET, never from argv where ps and shell history expose it.
func resolveWebhookSecret(file, legacy string) string {
	if legacy != "" {
		dief("--secret was removed because argv is visible in the process list; use --secret-file FILE (or '-' for stdin), or set %s", envWebhookSecret)
	}

	switch file {
	case "":
		return strings.TrimSpace(os.Getenv(envWebhookSecret))
	case "-":
		line, err := readLine(stdinReader)
		if err != nil {
			dief("reading webhook secret from stdin: %v", err)
		}

		return strings.TrimSpace(line)
	}

	raw, err := os.ReadFile(file)
	if err != nil {
		dief("reading webhook secret file: %v", err)
	}

	return strings.TrimSpace(string(raw))
}

func webhookBody(name, whURL, secret, events string, enabled bool) map[string]any {
	body := map[string]any{"name": name, "url": whURL, "enabled": enabled}
	if events != "" {
		body["events"] = strings.Split(events, ",")
	}

	if secret != "" {
		body["secret"] = secret
	}

	return body
}

func webhookUpdate(client *Client, id string, args []string) {
	fs := flag.NewFlagSet("webhook update", flag.ExitOnError)
	name := fs.String("name", "", "Webhook name")
	whURL := fs.String("url", "", "Webhook URL")
	events := fs.String("events", "", "Comma-separated event types")
	active := fs.Bool("active", true, "Enabled (--active=false to disable)")
	_ = fs.Parse(args)

	set := setFlags(fs)
	body := map[string]any{}

	if set["name"] {
		body["name"] = *name
	}

	if set["events"] {
		body["events"] = strings.Split(*events, ",")
	}

	if set["url"] {
		body["url"] = *whURL
	}

	if set["active"] {
		body["enabled"] = *active
	}

	if len(body) == 0 {
		dief("nothing to update: pass --name, --url, --events and/or --active[=false]")
	}

	sendJSON(client, "PUT", "/api/v1/webhooks/"+escape(id), body, http.StatusOK)
	fmt.Println("Webhook updated.")
}

func webhookRepo(args []string, client *Client, outFmt string) {
	if len(args) < 2 {
		dief("Usage: oa webhook repo <list|create> <repo> [flags]")
	}

	path := "/api/v1/repositories/" + escape(args[1]) + "/webhooks"

	switch args[0] {
	case "list":
		wh, wf := webhookColumns()
		listGeneric(client, path, "webhooks", wh, wf, outFmt)
	case "create":
		fs := flag.NewFlagSet("webhook repo create", flag.ExitOnError)
		name := fs.String("name", "", "Webhook name")
		whURL := fs.String("url", "", "Webhook URL")
		secretFile := fs.String("secret-file", "", webhookSecretUsage)
		legacySecret := fs.String("secret", "", "removed; use --secret-file")
		events := fs.String("events", "", "Comma-separated event types, e.g. artifact.uploaded (required)")
		_ = fs.Parse(args[2:])

		if *name == "" || *whURL == "" || *events == "" {
			dief("--name, --url and --events are required")
		}

		secret := resolveWebhookSecret(*secretFile, *legacySecret)

		sendJSON(client, "POST", path, webhookBody(*name, *whURL, secret, *events, true), http.StatusCreated)
		fmt.Println("Repo webhook created.")
	default:
		dief("Unknown webhook repo subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// SBOM
// ═══════════════════════════════════════════════════════════════════════════════

// sbomSpecVersions maps an SBOM format to the spec version used when --spec-version is not given.
//
//nolint:gochecknoglobals // immutable lookup table
var sbomSpecVersions = map[string]string{"cyclonedx": "1.5", "spdx": "2.3", "spdx-json": "2.3"}

func handleSBOM(args []string, client *Client, outFmt string) {
	if len(args) < 2 {
		dief("Usage: oa sbom <list|generate|show|download|delete> <repo> [id]")
	}

	base := "/api/v1/repositories/" + escape(args[1]) + "/sbom"

	switch args[0] {
	case "list":
		listGeneric(client, base, "reports", []string{"ID", "ARTIFACT", "FORMAT", "COMPONENTS", "CREATED"},
			[]string{"id", "artifact_id", "format", "component_count", "created_at"}, outFmt)
	case "generate":
		fs := flag.NewFlagSet("sbom generate", flag.ExitOnError)
		artifactID := fs.String("artifact-id", "", "Artifact (package) id to build the SBOM for")
		format := fs.String("format", "cyclonedx", "SBOM format: cyclonedx|spdx|spdx-json")
		specVersion := fs.String("spec-version", "", "Spec version (CycloneDX 1.4|1.5|1.6, SPDX 2.3); default 1.5 / 2.3")
		_ = fs.Parse(args[2:])

		spec, ok := sbomSpecVersions[*format]
		if !ok {
			dief("--format must be cyclonedx, spdx or spdx-json")
		}

		if *specVersion != "" {
			spec = *specVersion
		}

		if *artifactID == "" {
			dief("--artifact-id is required")
		}

		result := sendJSON(client, "POST", base+"/generate", map[string]string{
			"artifact_id":  *artifactID,
			"format":       *format,
			"spec_version": spec,
		}, http.StatusCreated)
		printScanResult(result, outFmt, "SBOM %v generated (%v components).", "id", "component_count")
	case "show":
		needArgs(args, 3, "Usage: oa sbom show <repo> <id>")
		showGeneric(client, base+"/"+escape(args[2]), outFmt)
	case "download":
		needArgs(args, 4, "Usage: oa sbom download <repo> <id> <dest>")
		downloadFile(client, base+"/"+escape(args[2])+"/download", args[3])
	case "delete":
		needArgs(args, 3, "Usage: oa sbom delete <repo> <id>")
		deleteResource(client, base+"/"+escape(args[2]), "SBOM deleted.")
	default:
		dief("Unknown sbom subcommand: %s", args[0])
	}
}

func needArgs(args []string, n int, usage string) {
	if len(args) < n {
		dief("%s", usage)
	}
}

// printScanResult prints the object the server returned for a scan or
// generate request: JSON in json mode, otherwise one summary line.
func printScanResult(result map[string]any, outFmt, format string, keys ...string) {
	if outFmt == "json" {
		printJSON(result)
		return
	}

	vals := make([]any, len(keys))
	for i, k := range keys {
		vals[i] = result[k]
	}

	fmt.Printf(format+"\n", vals...)
}

// ═══════════════════════════════════════════════════════════════════════════════
// VULN
// ═══════════════════════════════════════════════════════════════════════════════

func handleVuln(args []string, client *Client, outFmt string) {
	if len(args) < 2 {
		dief("Usage: oa vuln <scan|list|show|quarantine|delete> <repo> [id]")
	}

	base := "/api/v1/repositories/" + escape(args[1]) + "/vuln"

	switch args[0] {
	case "scan":
		fs := flag.NewFlagSet("vuln scan", flag.ExitOnError)
		artifactID := fs.String("artifact-id", "", "Artifact (package) id")
		pkg := fs.String("package", "", "Package name")
		ver := fs.String("version", "", "Package version")
		ecosystem := fs.String("ecosystem", "", "Package ecosystem (e.g. npm, PyPI, Maven, Go)")
		scanner := fs.String("scanner", "osv", "Scanner: osv|trivy|grype")
		_ = fs.Parse(args[2:])

		if *artifactID == "" || *pkg == "" || *ver == "" || *ecosystem == "" {
			dief("--artifact-id, --package, --version and --ecosystem are required")
		}

		result := sendJSON(client, "POST", base+"/scan", map[string]string{
			"artifact_id":     *artifactID,
			"package_name":    *pkg,
			"package_version": *ver,
			"ecosystem":       *ecosystem,
			"scanner":         *scanner,
		}, http.StatusOK)
		printScanResult(result, outFmt, "Vulnerability scan %v: %v (%v vulnerabilities).", "id", "scan_status", "vuln_count")
	case "list":
		listGeneric(client, base+"/scans", "scans",
			[]string{"ID", "ARTIFACT", "STATUS", "CRITICAL", "HIGH", "QUARANTINED", "CREATED"},
			[]string{"id", "artifact_id", "scan_status", "critical_count", "high_count", "quarantined", "created_at"}, outFmt)
	case "show":
		needArgs(args, 3, "Usage: oa vuln show <repo> <id>")
		showGeneric(client, base+"/scans/"+escape(args[2]), outFmt)
	case "quarantine":
		needArgs(args, 3, "Usage: oa vuln quarantine <repo> <id>")
		postEmpty(client, base+"/scans/"+escape(args[2])+"/quarantine", "Package quarantined.", http.StatusOK)
	case "delete":
		needArgs(args, 3, "Usage: oa vuln delete <repo> <id>")
		deleteResource(client, base+"/scans/"+escape(args[2]), "Scan deleted.")
	default:
		dief("Unknown vuln subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// LICENSE
// ═══════════════════════════════════════════════════════════════════════════════

func handleLicense(args []string, client *Client, outFmt string) {
	if len(args) < 2 {
		dief("Usage: oa license <scan|list|show> <repo> [id]")
	}

	base := "/api/v1/repositories/" + escape(args[1]) + "/license"

	switch args[0] {
	case "scan":
		fs := flag.NewFlagSet("license scan", flag.ExitOnError)
		artifactID := fs.String("artifact-id", "", "Artifact (package) id")
		_ = fs.Parse(args[2:])

		if *artifactID == "" {
			dief("--artifact-id is required")
		}

		result := sendJSON(client, "POST", base+"/scan", map[string]string{"artifact_id": *artifactID}, http.StatusOK)
		printScanResult(result, outFmt, "License scan %v: %v.", "id", "scan_status")
	case "list":
		listGeneric(client, base+"/scans", "scans",
			[]string{"ID", "ARTIFACT", "STATUS", "LICENSES", "CREATED"},
			[]string{"id", "artifact_id", "scan_status", "licenses", "created_at"}, outFmt)
	case "show":
		needArgs(args, 3, "Usage: oa license show <repo> <id>")
		showGeneric(client, base+"/scans/"+escape(args[2]), outFmt)
	default:
		dief("Unknown license subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// SECRET
// ═══════════════════════════════════════════════════════════════════════════════

func handleSecret(args []string, client *Client, outFmt string) {
	if len(args) < 2 {
		dief("Usage: oa secret <scan|list|show|quarantine|delete> <repo> [id]")
	}

	base := "/api/v1/repositories/" + escape(args[1]) + "/secrets"

	switch args[0] {
	case "scan":
		fs := flag.NewFlagSet("secret scan", flag.ExitOnError)
		artifactID := fs.String("artifact-id", "", "Artifact (package) id")
		contentFile := fs.String("content-file", "", "File with the content to scan, or - for stdin")
		_ = fs.Parse(args[2:])

		if *artifactID == "" {
			dief("--artifact-id is required")
		}

		body := map[string]string{"artifact_id": *artifactID}

		if *contentFile != "" {
			body["content"] = readContent(*contentFile)
		}

		result := sendJSON(client, "POST", base+"/scan", body, http.StatusOK)
		printScanResult(result, outFmt, "Secret scan %v: %v (%v findings).", "id", "scan_status", "finding_count")
	case "list":
		listGeneric(client, base+"/scans", "scans",
			[]string{"ID", "ARTIFACT", "STATUS", "FINDINGS", "QUARANTINED", "CREATED"},
			[]string{"id", "artifact_id", "scan_status", "finding_count", "quarantined", "created_at"}, outFmt)
	case "show":
		needArgs(args, 3, "Usage: oa secret show <repo> <id>")
		showGeneric(client, base+"/scans/"+escape(args[2]), outFmt)
	case "quarantine":
		needArgs(args, 3, "Usage: oa secret quarantine <repo> <id>")
		postEmpty(client, base+"/scans/"+escape(args[2])+"/quarantine", "Package quarantined.", http.StatusOK)
	case "delete":
		needArgs(args, 3, "Usage: oa secret delete <repo> <id>")
		deleteResource(client, base+"/scans/"+escape(args[2]), "Scan deleted.")
	default:
		dief("Unknown secret subcommand: %s", args[0])
	}
}

// readContent reads a whole file, or stdin for "-".
func readContent(path string) string {
	var (
		data []byte
		err  error
	)

	if path == "-" {
		data, err = io.ReadAll(stdinReader)
	} else {
		data, err = os.ReadFile(path)
	}

	if err != nil {
		dief("reading %s: %v", path, err)
	}

	return string(data)
}

// ═══════════════════════════════════════════════════════════════════════════════
// CLEANUP
// ═══════════════════════════════════════════════════════════════════════════════

func handleCleanup(args []string, client *Client, outFmt string) {
	if len(args) < 2 {
		dief("Usage: oa cleanup <list|create|show|update|delete|run> <repo> [id]")
	}

	base := "/api/v1/repositories/" + escape(args[1]) + "/cleanup-policies"

	switch args[0] {
	case "list":
		listGeneric(client, base, "policies",
			[]string{"ID", "NAME", "ENABLED", "KEEP_LAST", "KEEP_DAYS", "SCHEDULE"},
			[]string{"id", "name", "enabled", "keep_last_n", "keep_since_days", "schedule_cron"}, outFmt)
	case "create":
		cleanupCreate(client, base, args[2:])
	case "show":
		needArgs(args, 3, "Usage: oa cleanup show <repo> <id>")
		showGeneric(client, base+"/"+escape(args[2]), outFmt)
	case "update":
		needArgs(args, 3, "Usage: oa cleanup update <repo> <id> [flags]")
		cleanupUpdate(client, base+"/"+escape(args[2]), args[3:])
	case "delete":
		needArgs(args, 3, "Usage: oa cleanup delete <repo> <id>")
		deleteResource(client, base+"/"+escape(args[2]), "Cleanup policy deleted.")
	case "run":
		needArgs(args, 3, "Usage: oa cleanup run <repo> <id>")
		postEmpty(client, base+"/"+escape(args[2])+"/run", "Cleanup policy run initiated.", http.StatusAccepted)
	default:
		dief("Unknown cleanup subcommand: %s", args[0])
	}
}

type cleanupFlags struct {
	fs        *flag.FlagSet
	name      *string
	keepCount *int
	keepDays  *int
	pattern   *string
	schedule  *string
	enabled   *bool
}

func newCleanupFlags(name string) *cleanupFlags {
	fs := flag.NewFlagSet(name, flag.ExitOnError)

	return &cleanupFlags{
		fs:        fs,
		name:      fs.String("name", "", "Policy name"),
		keepCount: fs.Int("keep-count", 0, "Keep the newest N versions of each package"),
		keepDays:  fs.Int("keep-days", 0, "Keep versions newer than N days"),
		pattern:   fs.String("name-pattern", "", "Regular expression selecting the package names the policy applies to"),
		schedule:  fs.String("schedule", "", "Cron schedule (5 fields)"),
		enabled:   fs.Bool("enabled", true, "Enabled (--enabled=false to disable)"),
	}
}

// body returns the request payload from the flags that were given. The server
// reads keep_last_n / keep_since_days; at least one retention criterion is
// required by the server, so a missing one is reported up front on create.
func (c *cleanupFlags) body() map[string]any {
	set := setFlags(c.fs)
	body := map[string]any{}

	if set["name"] {
		body["name"] = *c.name
	}

	if set["keep-count"] {
		body["keep_last_n"] = *c.keepCount
	}

	if set["keep-days"] {
		body["keep_since_days"] = *c.keepDays
	}

	if set["name-pattern"] {
		body["name_pattern"] = *c.pattern
	}

	if set["schedule"] {
		body["schedule_cron"] = *c.schedule
	}

	if set["enabled"] {
		body["enabled"] = *c.enabled
	}

	return body
}

func cleanupCreate(client *Client, base string, args []string) {
	f := newCleanupFlags("cleanup create")
	_ = f.fs.Parse(args)

	if *f.name == "" {
		dief("--name is required")
	}

	body := f.body()
	if _, ok := body["keep_last_n"]; !ok {
		if _, ok := body["keep_since_days"]; !ok {
			dief("at least one of --keep-count or --keep-days is required")
		}
	}

	sendJSON(client, "POST", base, body, http.StatusCreated)
	fmt.Println("Cleanup policy created.")
}

func cleanupUpdate(client *Client, path string, args []string) {
	f := newCleanupFlags("cleanup update")
	_ = f.fs.Parse(args)

	body := f.body()
	if len(body) == 0 {
		dief("nothing to update: pass --name, --keep-count, --keep-days, --name-pattern, --schedule and/or --enabled[=false]")
	}

	sendJSON(client, "PUT", path, body, http.StatusOK)
	fmt.Println("Cleanup policy updated.")
}

// ═══════════════════════════════════════════════════════════════════════════════
// QUOTA
// ═══════════════════════════════════════════════════════════════════════════════

func handleQuota(args []string, client *Client, outFmt string) {
	if len(args) < 2 {
		dief("Usage: oa quota <show|set|delete> <repo>")
	}

	path := "/api/v1/repositories/" + escape(args[1]) + "/quota"

	switch args[0] {
	case "show":
		showGeneric(client, path, outFmt)
	case "set":
		quotaSet(client, path, args[2:])
	case "delete":
		deleteResource(client, path, "Quota removed.")
	default:
		dief("Unknown quota subcommand: %s", args[0])
	}
}

func quotaSet(client *Client, path string, args []string) {
	fs := flag.NewFlagSet("quota set", flag.ExitOnError)
	limit := fs.Int64("limit", 0, "Size limit, 0 for unlimited")
	unit := fs.String("unit", "GB", "Unit for --limit: MB|GB|TB")
	maxArtifacts := fs.Int("max-artifacts", 0, "Maximum number of artifacts, 0 for unlimited")
	warn := fs.Int("warn-percent", 0, "Warn above this usage percentage (0-100)")
	_ = fs.Parse(args)

	set := setFlags(fs)
	body := map[string]any{}

	if set["limit"] {
		multipliers := map[string]int64{"MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40}

		mult, ok := multipliers[strings.ToUpper(*unit)]
		if !ok {
			dief("invalid unit: %s (use MB, GB, or TB)", *unit)
		}

		body["max_size_bytes"] = *limit * mult
	}

	if set["max-artifacts"] {
		body["max_artifact_count"] = *maxArtifacts
	}

	if set["warn-percent"] {
		body["warn_threshold_percent"] = *warn
	}

	if len(body) == 0 {
		dief("nothing to set: pass --limit [--unit MB|GB|TB], --max-artifacts and/or --warn-percent")
	}

	sendJSON(client, "PUT", path, body, http.StatusOK)
	fmt.Println("Quota updated.")
}

// ═══════════════════════════════════════════════════════════════════════════════
// PROPERTIES
// ═══════════════════════════════════════════════════════════════════════════════

func handleProps(args []string, client *Client, outFmt string) {
	if len(args) < 3 {
		dief("Usage: oa props <list|set|delete|search> <repo> <artifact-id> [key] [value]")
	}

	repo := "/api/v1/repositories/" + escape(args[1])

	switch args[0] {
	case "list":
		showGeneric(client, repo+"/artifacts/"+escape(args[2])+"/properties", outFmt)
	case "set":
		needArgs(args, 5, "Usage: oa props set <repo> <artifact-id> <key> <value>")
		sendJSON(client, "PATCH", repo+"/artifacts/"+escape(args[2])+"/properties", map[string]string{args[3]: args[4]}, http.StatusOK)
		fmt.Printf("Property '%s' set.\n", args[3])
	case "delete":
		needArgs(args, 4, "Usage: oa props delete <repo> <artifact-id> <key>")
		deleteResource(client, repo+"/artifacts/"+escape(args[2])+"/properties/"+escape(args[3]), fmt.Sprintf("Property '%s' deleted.", args[3]))
	case "search":
		needArgs(args, 4, "Usage: oa props search <repo> <key> <value>")
		propsSearch(client, repo, args[2], args[3], outFmt)
	default:
		dief("Unknown props subcommand: %s", args[0])
	}
}

// propsSearch prints the ids of the artifacts whose property key equals value.
func propsSearch(client *Client, repo, key, value, outFmt string) {
	q := url.Values{}
	q.Set("key", key)
	q.Set("value", value)

	var result struct {
		ArtifactIDs []string `json:"artifact_ids"`
	}

	getJSON(client, repo+"/artifacts/search?"+q.Encode(), &result)

	if outFmt == "json" {
		printJSON(result.ArtifactIDs)
		return
	}

	w := newTabWriter()

	_, _ = fmt.Fprintln(w, "ARTIFACT_ID")
	for _, id := range result.ArtifactIDs {
		_, _ = fmt.Fprintln(w, id)
	}

	_ = w.Flush()
}

// ═══════════════════════════════════════════════════════════════════════════════
// DELTA
// ═══════════════════════════════════════════════════════════════════════════════

func handleDelta(args []string, client *Client, outFmt string) {
	if len(args) < 3 {
		dief("Usage: oa delta <list|get> <repo> <pkg-id> [flags]")
	}

	base := "/api/v1/repositories/" + escape(args[1]) + "/packages/" + escape(args[2])

	switch args[0] {
	case "list":
		listGeneric(client, base+"/deltas", "data",
			[]string{"FROM_SHA256", "TO_SHA256", "SIZE", "FORMAT"},
			[]string{"from_sha256", "to_sha256", "delta_size", "format"}, outFmt)
	case "get":
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
		downloadFile(client, base+"/delta?"+q.Encode(), dest)
	default:
		dief("Unknown delta subcommand: %s", args[0])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// SYSTEM
// ═══════════════════════════════════════════════════════════════════════════════

func handleSystem(args []string, client *Client, outFmt string) {
	if len(args) >= 1 && args[0] != "info" {
		dief("Unknown system subcommand: %s", args[0])
	}

	// The detailed endpoint is admin-only; other callers get the public one
	// (version and status).
	path := "/api/v1/admin/system/info"

	resp, err := client.do("GET", path, nil, "")
	if err != nil {
		dief("%v", err)
	}

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()

		resp, err = client.do("GET", "/api/v1/system/info", nil, "")
		if err != nil {
			dief("%v", err)
		}
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

	for _, k := range []string{"version", "status", "commit", "build_time", "go_version", "os", "arch", "uptime", "db_status", "storage_kind", "storage_status"} {
		if v, ok := result[k]; ok {
			_, _ = fmt.Fprintf(w, "%s\t%s\n", k, cell(v))
		}
	}

	_ = w.Flush()
}
