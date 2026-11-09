// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/spf13/cobra"
)

// leafCmd builds a command that takes between minArgs and maxArgs positional
// arguments; comps complete them in order and run executes the command.
func leafCmd(
	use, short string, minArgs, maxArgs int, comps []completer, run func(cmd *cobra.Command, args []string),
) *cobra.Command {
	return &cobra.Command{
		Use:               use,
		Short:             short,
		Args:              usageArgs(minArgs, maxArgs),
		ValidArgsFunction: positional(comps...),
		Run:               run,
	}
}

// repoPath is the API path of a repository's sub-resource, e.g. repoPath("r", "sbom").
func repoPath(repo, sub string) string {
	return "/api/v1/repositories/" + escape(repo) + "/" + sub
}

// ═══════════════════════════════════════════════════════════════════════════════
// WEBHOOK
// ═══════════════════════════════════════════════════════════════════════════════

func webhookColumns() ([]string, []string) {
	return []string{"ID", "NAME", "URL", "ENABLED"}, []string{"id", "name", "url", "enabled"}
}

func newWebhookCmd(a *app) *cobra.Command {
	ids := a.listNames("/api/v1/webhooks", "webhooks", "id")

	return parentCmd("webhook", "Manage webhooks",
		leafCmd("list", "List global webhooks", 0, 0, nil, func(*cobra.Command, []string) {
			wh, wf := webhookColumns()
			listGeneric(a.client, "/api/v1/webhooks", "webhooks", wh, wf, a.outFmt)
		}),
		webhookCreateCmd(a),
		leafCmd("show <id>", "Show a webhook", 1, 1, []completer{ids}, func(_ *cobra.Command, args []string) {
			showGeneric(a.client, "/api/v1/webhooks/"+escape(args[0]), a.outFmt)
		}),
		webhookUpdateCmd(a),
		leafCmd("delete <id>", "Delete a webhook", 1, 1, []completer{ids}, func(_ *cobra.Command, args []string) {
			deleteResource(a.client, "/api/v1/webhooks/"+escape(args[0]), "Webhook deleted.")
		}),
		leafCmd("test <id>", "Send a test delivery to a webhook", 1, 1, []completer{ids}, func(_ *cobra.Command, args []string) {
			postEmpty(a.client, "/api/v1/webhooks/"+escape(args[0])+"/test", "Webhook test sent.", http.StatusAccepted)
		}),
		leafCmd("deliveries <id>", "List the deliveries of a webhook", 1, 1, []completer{ids}, func(_ *cobra.Command, args []string) {
			listGeneric(a.client, "/api/v1/webhooks/"+escape(args[0])+"/deliveries", "deliveries",
				[]string{"ID", "EVENT", "STATUS", "ATTEMPTS", "CREATED"},
				[]string{"id", "event_type", "status", "attempt_count", "created_at"}, a.outFmt)
		}),
		parentCmd("repo", "Manage the webhooks of a repository",
			leafCmd("list <repo>", "List the webhooks of a repository", 1, 1, []completer{a.repoNames()},
				func(_ *cobra.Command, args []string) {
					wh, wf := webhookColumns()
					listGeneric(a.client, repoPath(args[0], "webhooks"), "webhooks", wh, wf, a.outFmt)
				}),
			webhookRepoCreateCmd(a),
		),
	)
}

// webhookCreateFlags are the flags webhook create and webhook repo create share.
type webhookCreateFlags struct {
	name, url, secretFile, legacySecret, events string
}

func (f *webhookCreateFlags) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "Webhook name")
	fl.StringVar(&f.url, "url", "", "Webhook URL")
	fl.StringVar(&f.secretFile, "secret-file", "", webhookSecretUsage)
	fl.StringVar(&f.legacySecret, "secret", "", "removed; use --secret-file")
	fl.StringVar(&f.events, "events", "", "Comma-separated event types, e.g. artifact.uploaded (required)")

	_ = fl.MarkHidden("secret")

	flagFiles(cmd, "secret-file")
}

// secret validates the required flags and resolves the webhook secret.
func (f *webhookCreateFlags) secret() string {
	if f.name == "" || f.url == "" || f.events == "" {
		dief("--name, --url and --events are required")
	}

	return resolveWebhookSecret(f.secretFile, f.legacySecret)
}

func webhookCreateCmd(a *app) *cobra.Command {
	var f webhookCreateFlags

	cmd := leafCmd("create", "Create a webhook", 0, 0, nil, nil)
	f.bind(cmd)

	active := cmd.Flags().Bool("active", true, "Enabled")

	cmd.Run = func(*cobra.Command, []string) {
		secret := f.secret()

		sendJSON(a.client, "POST", "/api/v1/webhooks", webhookBody(f.name, f.url, secret, f.events, *active), http.StatusCreated)
		fmt.Println("Webhook created.")
	}

	return cmd
}

func webhookRepoCreateCmd(a *app) *cobra.Command {
	var f webhookCreateFlags

	cmd := leafCmd("create <repo>", "Create a webhook on a repository", 1, 1, []completer{a.repoNames()}, nil)
	f.bind(cmd)

	cmd.Run = func(_ *cobra.Command, args []string) {
		secret := f.secret()

		sendJSON(a.client, "POST", repoPath(args[0], "webhooks"), webhookBody(f.name, f.url, secret, f.events, true), http.StatusCreated)
		fmt.Println("Repo webhook created.")
	}

	return cmd
}

func webhookUpdateCmd(a *app) *cobra.Command {
	cmd := leafCmd("update <id>", "Update a webhook (only the flags you pass are sent)", 1, 1,
		[]completer{a.listNames("/api/v1/webhooks", "webhooks", "id")}, nil)

	fl := cmd.Flags()
	name := fl.String("name", "", "Webhook name")
	whURL := fl.String("url", "", "Webhook URL")
	events := fl.String("events", "", "Comma-separated event types")
	active := fl.Bool("active", true, "Enabled (--active=false to disable)")

	cmd.Run = func(cmd *cobra.Command, args []string) {
		set := cmd.Flags().Changed
		body := map[string]any{}

		if set("name") {
			body["name"] = *name
		}

		if set("events") {
			body["events"] = strings.Split(*events, ",")
		}

		if set("url") {
			body["url"] = *whURL
		}

		if set("active") {
			body["enabled"] = *active
		}

		if len(body) == 0 {
			dief("nothing to update: pass --name, --url, --events and/or --active[=false]")
		}

		sendJSON(a.client, "PUT", "/api/v1/webhooks/"+escape(args[0]), body, http.StatusOK)
		fmt.Println("Webhook updated.")
	}

	return cmd
}

// Webhook secrets never travel through argv.
const (
	envWebhookSecret   = "OA_WEBHOOK_SECRET" //nolint:gosec // env var name, not a credential
	webhookSecretUsage = "file holding the webhook secret, '-' for stdin (or " + envWebhookSecret + ")"
)

// resolveWebhookSecret reads the secret from --secret-file ('-' = stdin) or
// OA_WEBHOOK_SECRET, never from argv where ps and shell history expose it.
func resolveWebhookSecret(file, legacy string) string {
	if legacy != "" {
		dief(
			"--secret was removed because argv is visible in the process list; use --secret-file FILE (or '-' for stdin), or set %s",
			envWebhookSecret,
		)
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

// webhookBody is the create payload: the server reads "enabled", not "active".
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

// ═══════════════════════════════════════════════════════════════════════════════
// SBOM
// ═══════════════════════════════════════════════════════════════════════════════

// sbomSpecVersions maps an SBOM format to the spec version used when --spec-version is not given.
//
//nolint:gochecknoglobals // immutable lookup table
var sbomSpecVersions = map[string]string{"cyclonedx": "1.5", "spdx": "2.3", "spdx-json": "2.3", "spdx3-json": "3.0.1"}

// attestValues are the values of --attest: the DSSE envelope or the Sigstore bundle.
//
//nolint:gochecknoglobals // immutable list
var attestValues = []string{"true", "bundle"}

func newSBOMCmd(a *app) *cobra.Command {
	repos := []completer{a.repoNames()}
	reports := a.repoScanIDs("sbom", "reports")

	return parentCmd("sbom", "Manage SBOMs",
		leafCmd("list <repo>", "List the SBOMs of a repository", 1, 1, repos, func(_ *cobra.Command, args []string) {
			listGeneric(a.client, repoPath(args[0], "sbom"), "reports", []string{"ID", "ARTIFACT", "FORMAT", "COMPONENTS", "CREATED"},
				[]string{"id", "artifact_id", "format", "component_count", "created_at"}, a.outFmt)
		}),
		sbomGenerateCmd(a),
		leafCmd("show <repo> <id>", "Show an SBOM", 2, 2, reports, func(_ *cobra.Command, args []string) {
			showGeneric(a.client, repoPath(args[0], "sbom/"+escape(args[1])), a.outFmt)
		}),
		sbomDownloadCmd(a),
		leafCmd("delete <repo> <id>", "Delete an SBOM", 2, 2, reports, func(_ *cobra.Command, args []string) {
			deleteResource(a.client, repoPath(args[0], "sbom/"+escape(args[1])), "SBOM deleted.")
		}),
	)
}

// sbomGenerateCmd defines its own --format (the SBOM format), which shadows the
// global output format after the command name.
func sbomGenerateCmd(a *app) *cobra.Command {
	cmd := leafCmd("generate <repo>", "Generate an SBOM for one artifact", 1, 1, []completer{a.repoNames()}, nil)
	cmd.Long = "Generate an SBOM for one artifact (CycloneDX 1.4-1.6, SPDX 2.3, SPDX 3 3.0.1).\n\n" +
		"After the command name, --format is the SBOM format; to choose the output format " +
		"put the global flag before the command: oa --format json sbom generate REPO --artifact-id ID."

	fl := cmd.Flags()
	artifactID := fl.String("artifact-id", "", "Artifact (package) id to build the SBOM for")
	format := fl.String("format", "cyclonedx", "SBOM format: cyclonedx|spdx|spdx-json|spdx3-json")
	specVersion := fl.String(
		"spec-version",
		"",
		"Spec version (CycloneDX 1.4|1.5|1.6, SPDX 2.3, SPDX 3 3.0.1); default 1.5 / 2.3 / 3.0.1",
	)

	flagEnum(cmd, "format", "cyclonedx", "spdx", "spdx-json", "spdx3-json")
	flagEnum(cmd, "spec-version", "1.4", "1.5", "1.6", "2.3", "3.0.1")

	cmd.Run = func(_ *cobra.Command, args []string) {
		spec, ok := sbomSpecVersions[*format]
		if !ok {
			dief("--format must be cyclonedx, spdx, spdx-json or spdx3-json")
		}

		if *specVersion != "" {
			spec = *specVersion
		}

		if *artifactID == "" {
			dief("--artifact-id is required")
		}

		result := sendJSON(a.client, "POST", repoPath(args[0], "sbom/generate"), map[string]string{
			"artifact_id":  *artifactID,
			"format":       *format,
			"spec_version": spec,
		}, http.StatusCreated)
		printScanResult(result, a.outFmt, "SBOM %v generated (%v components).", "id", "component_count")
	}

	return cmd
}

func sbomDownloadCmd(a *app) *cobra.Command {
	cmd := leafCmd("download <repo> <id> <dest>", "Download an SBOM (or its signed attestation)", 3, 3,
		[]completer{a.repoNames(), a.repoIDs("sbom", "reports"), files}, nil)

	attest := cmd.Flags().String(
		"attest",
		"",
		"Download a signed in-toto attestation instead: true (DSSE envelope) or bundle (Sigstore bundle, for cosign)",
	)

	flagEnum(cmd, "attest", attestValues...)

	cmd.Run = func(_ *cobra.Command, args []string) {
		path := repoPath(args[0], "sbom/"+escape(args[1])+"/download")
		if *attest != "" {
			path += "?attest=" + url.QueryEscape(*attest)
		}

		downloadFile(a.client, path, args[2], 0o644)
	}

	return cmd
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

// osvEcosystems lists the OSV ecosystem names of the registered package formats.
func osvEcosystems() []string {
	var out []string

	for _, name := range repotype.Names() {
		if spec, ok := repotype.Lookup(name); ok && spec.OSVEcosystem != "" && !slices.Contains(out, spec.OSVEcosystem) {
			out = append(out, spec.OSVEcosystem)
		}
	}

	slices.Sort(out)

	return out
}

func newVulnCmd(a *app) *cobra.Command {
	repos := []completer{a.repoNames()}
	scans := a.repoScanIDs("vuln/scans", "scans")

	return parentCmd("vuln", "Vulnerability scanning",
		vulnScanCmd(a),
		leafCmd("list <repo>", "List vulnerability scans", 1, 1, repos, func(_ *cobra.Command, args []string) {
			listGeneric(a.client, repoPath(args[0], "vuln/scans"), "scans",
				[]string{"ID", "ARTIFACT", "STATUS", "CRITICAL", "HIGH", "QUARANTINED", "CREATED"},
				[]string{"id", "artifact_id", "scan_status", "critical_count", "high_count", "quarantined", "created_at"}, a.outFmt)
		}),
		leafCmd("show <repo> <id>", "Show a vulnerability scan", 2, 2, scans, func(_ *cobra.Command, args []string) {
			showGeneric(a.client, repoPath(args[0], "vuln/scans/"+escape(args[1])), a.outFmt)
		}),
		quarantineCmd(a, "Quarantine the vulnerable package (--release lifts it)", "vuln", scans),
		leafCmd("delete <repo> <id>", "Delete a vulnerability scan", 2, 2, scans, func(_ *cobra.Command, args []string) {
			deleteResource(a.client, repoPath(args[0], "vuln/scans/"+escape(args[1])), "Scan deleted.")
		}),
	)
}

func vulnScanCmd(a *app) *cobra.Command {
	cmd := leafCmd("scan <repo>", "Scan one package for vulnerabilities", 1, 1, []completer{a.repoNames()}, nil)

	fl := cmd.Flags()
	artifactID := fl.String("artifact-id", "", "Artifact (package) id")
	pkg := fl.String("package", "", "Package name")
	ver := fl.String("version", "", "Package version")
	ecosystem := fl.String("ecosystem", "", "Package ecosystem (e.g. npm, PyPI, Maven, Go)")
	scanner := fl.String("scanner", "osv", "Scanner: osv|trivy|grype")

	flagEnum(cmd, "ecosystem", osvEcosystems()...)
	flagEnum(cmd, "scanner", "osv", "trivy", "grype")

	cmd.Run = func(_ *cobra.Command, args []string) {
		if *artifactID == "" || *pkg == "" || *ver == "" || *ecosystem == "" {
			dief("--artifact-id, --package, --version and --ecosystem are required")
		}

		result := sendJSON(a.client, "POST", repoPath(args[0], "vuln/scan"), map[string]string{
			"artifact_id":     *artifactID,
			"package_name":    *pkg,
			"package_version": *ver,
			"ecosystem":       *ecosystem,
			"scanner":         *scanner,
		}, http.StatusOK)
		if a.outFmt != "json" && result["scan_status"] == "scanning" {
			// The server answers as soon as the scan is queued; the counts are not known yet.
			fmt.Printf("Vulnerability scan %v started; it is still running. Check the result with: oa vuln show %s %v\n",
				result["id"], args[0], result["id"])

			return
		}

		printScanResult(result, a.outFmt, "Vulnerability scan %v: %v (%v vulnerabilities).", "id", "scan_status", "vuln_count")
	}

	return cmd
}

// quarantineCmd quarantines the package of a scan result, or with --release
// lifts the quarantine. The server needs the wanted state in the body, so
// --release=false quarantines. sub is the API path segment of its scans (vuln, secrets);
// comps complete the repository and the scan id.
func quarantineCmd(a *app, short, sub string, comps []completer) *cobra.Command {
	cmd := leafCmd("quarantine <repo> <id>", short, 2, 2, comps, nil)

	release := cmd.Flags().Bool("release", false, "Lift the quarantine (--release=false quarantines)")

	cmd.Run = func(_ *cobra.Command, args []string) {
		quarantined := !*release

		sendJSON(a.client, http.MethodPost, repoPath(args[0], sub+"/scans/"+escape(args[1])+"/quarantine"),
			map[string]bool{"quarantined": quarantined}, http.StatusOK)

		if quarantined {
			fmt.Println("Package quarantined.")
		} else {
			fmt.Println("Package released from quarantine.")
		}
	}

	return cmd
}

// ═══════════════════════════════════════════════════════════════════════════════
// LICENSE
// ═══════════════════════════════════════════════════════════════════════════════

func newLicenseCmd(a *app) *cobra.Command {
	repos := []completer{a.repoNames()}

	scan := leafCmd("scan <repo>", "Scan one artifact for license issues", 1, 1, repos, nil)
	artifactID := scan.Flags().String("artifact-id", "", "Artifact (package) id")
	scan.Run = func(_ *cobra.Command, args []string) {
		if *artifactID == "" {
			dief("--artifact-id is required")
		}

		result := sendJSON(a.client, "POST", repoPath(args[0], "license/scan"),
			map[string]string{"artifact_id": *artifactID}, http.StatusOK)
		printScanResult(result, a.outFmt, "License scan %v: %v.", "id", "scan_status")
	}

	return parentCmd(
		"license",
		"License scanning",
		scan,
		leafCmd("list <repo>", "List license scans", 1, 1, repos, func(_ *cobra.Command, args []string) {
			printTable(fetchAllPages(a.client, repoPath(args[0], "license/scans"), "scans", "per_page", 100),
				[]string{"ID", "ARTIFACT", "STATUS", "LICENSES", "CREATED"},
				[]string{"id", "artifact_id", "scan_status", "licenses", "created_at"}, a.outFmt)
		}),
		leafCmd(
			"show <repo> <id>",
			"Show a license scan",
			2,
			2,
			a.repoScanIDs("license/scans", "scans"),
			func(_ *cobra.Command, args []string) {
				showGeneric(a.client, repoPath(args[0], "license/scans/"+escape(args[1])), a.outFmt)
			},
		),
	)
}

// ═══════════════════════════════════════════════════════════════════════════════
// SECRET
// ═══════════════════════════════════════════════════════════════════════════════

func newSecretCmd(a *app) *cobra.Command {
	repos := []completer{a.repoNames()}
	scans := a.repoScanIDs("secrets/scans", "scans")

	return parentCmd("secret", "Secret scanning",
		secretScanCmd(a),
		leafCmd("list <repo>", "List secret scans", 1, 1, repos, func(_ *cobra.Command, args []string) {
			listGeneric(a.client, repoPath(args[0], "secrets/scans"), "scans",
				[]string{"ID", "ARTIFACT", "STATUS", "FINDINGS", "QUARANTINED", "CREATED"},
				[]string{"id", "artifact_id", "scan_status", "finding_count", "quarantined", "created_at"}, a.outFmt)
		}),
		leafCmd("show <repo> <id>", "Show a secret scan", 2, 2, scans, func(_ *cobra.Command, args []string) {
			showGeneric(a.client, repoPath(args[0], "secrets/scans/"+escape(args[1])), a.outFmt)
		}),
		quarantineCmd(a, "Quarantine the package with the leaked secret (--release lifts it)", "secrets", scans),
		leafCmd("delete <repo> <id>", "Delete a secret scan", 2, 2, scans, func(_ *cobra.Command, args []string) {
			deleteResource(a.client, repoPath(args[0], "secrets/scans/"+escape(args[1])), "Scan deleted.")
		}),
	)
}

func secretScanCmd(a *app) *cobra.Command {
	cmd := leafCmd("scan <repo>", "Scan one artifact (and optional content) for secrets", 1, 1, []completer{a.repoNames()}, nil)

	artifactID := cmd.Flags().String("artifact-id", "", "Artifact (package) id")
	contentFile := cmd.Flags().String("content-file", "", "File with the content to scan, or - for stdin")

	flagFiles(cmd, "content-file")

	cmd.Run = func(_ *cobra.Command, args []string) {
		if *artifactID == "" {
			dief("--artifact-id is required")
		}

		body := map[string]string{"artifact_id": *artifactID}

		if *contentFile != "" {
			body["content"] = readContent(*contentFile)
		}

		result := sendJSON(a.client, "POST", repoPath(args[0], "secrets/scan"), body, http.StatusOK)
		printScanResult(result, a.outFmt, "Secret scan %v: %v (%v findings).", "id", "scan_status", "finding_count")
	}

	return cmd
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

// repoIDs completes the ids under a repository sub-resource (sbom, vuln/scans,
// packages...): the list at repoPath(<repo>, sub), key listKey, of the
// repository named first.
func (a *app) repoIDs(sub, listKey string) completer {
	return a.listNamesAt(func(args []string) string { return repoPath(args[0], sub) }, listKey, "id")
}

// repoScanIDs completes (repository, id) of an SBOM, scan or VEX decision.
func (a *app) repoScanIDs(sub, listKey string) []completer {
	return []completer{a.repoNames(), a.repoIDs(sub, listKey)}
}

// cleanupIDs completes the policy ids of the repository named first.
func (a *app) cleanupIDs() completer {
	return a.listNamesAt(func(args []string) string { return repoPath(args[0], "cleanup-policies") }, "policies", "id")
}

func newCleanupCmd(a *app) *cobra.Command {
	repos := []completer{a.repoNames()}
	policies := []completer{a.repoNames(), a.cleanupIDs()}

	return parentCmd("cleanup", "Manage cleanup policies",
		leafCmd("list <repo>", "List cleanup policies", 1, 1, repos, func(_ *cobra.Command, args []string) {
			listGeneric(a.client, repoPath(args[0], "cleanup-policies"), "policies",
				[]string{"ID", "NAME", "ENABLED", "KEEP_LAST", "KEEP_DAYS", "SCHEDULE"},
				[]string{"id", "name", "enabled", "keep_last_n", "keep_since_days", "schedule_cron"}, a.outFmt)
		}),
		cleanupCreateCmd(a),
		leafCmd("show <repo> <id>", "Show a cleanup policy", 2, 2, policies, func(_ *cobra.Command, args []string) {
			showGeneric(a.client, repoPath(args[0], "cleanup-policies/"+escape(args[1])), a.outFmt)
		}),
		cleanupUpdateCmd(a),
		leafCmd("delete <repo> <id>", "Delete a cleanup policy", 2, 2, policies, func(_ *cobra.Command, args []string) {
			deleteResource(a.client, repoPath(args[0], "cleanup-policies/"+escape(args[1])), "Cleanup policy deleted.")
		}),
		leafCmd("run <repo> <id>", "Run a cleanup policy now", 2, 2, policies, func(_ *cobra.Command, args []string) {
			postEmpty(a.client, repoPath(args[0], "cleanup-policies/"+escape(args[1])+"/run"),
				"Cleanup policy run initiated.", http.StatusAccepted)
		}),
	)
}

// cleanupFlags are the flags cleanup create and cleanup update share.
type cleanupFlags struct {
	name, pattern, schedule string
	keepCount, keepDays     int
	enabled                 bool
}

func (c *cleanupFlags) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&c.name, "name", "", "Policy name")
	fl.IntVar(&c.keepCount, "keep-count", 0, "Keep the newest N versions of each package")
	fl.IntVar(&c.keepDays, "keep-days", 0, "Keep versions newer than N days")
	fl.StringVar(&c.pattern, "name-pattern", "", "Regular expression selecting the package names the policy applies to")
	fl.StringVar(&c.schedule, "schedule", "", "Cron schedule (5 fields)")
	fl.BoolVar(&c.enabled, "enabled", true, "Enabled (--enabled=false to disable)")
}

// body returns the request payload from the flags that were given. The server
// reads keep_last_n / keep_since_days; at least one retention criterion is
// required by the server, so a missing one is reported up front on create.
func (c *cleanupFlags) body(set func(string) bool) map[string]any {
	body := map[string]any{}

	if set("name") {
		body["name"] = c.name
	}

	if set("keep-count") {
		body["keep_last_n"] = c.keepCount
	}

	if set("keep-days") {
		body["keep_since_days"] = c.keepDays
	}

	if set("name-pattern") {
		body["name_pattern"] = c.pattern
	}

	if set("schedule") {
		body["schedule_cron"] = c.schedule
	}

	if set("enabled") {
		body["enabled"] = c.enabled
	}

	return body
}

func cleanupCreateCmd(a *app) *cobra.Command {
	var f cleanupFlags

	cmd := leafCmd("create <repo>", "Create a cleanup policy (needs --keep-count or --keep-days)", 1, 1, []completer{a.repoNames()}, nil)
	f.bind(cmd)

	cmd.Run = func(cmd *cobra.Command, args []string) {
		if f.name == "" {
			dief("--name is required")
		}

		body := f.body(cmd.Flags().Changed)
		body["enabled"] = f.enabled // the flag defaults to true: send it, the server default is disabled

		if _, ok := body["keep_last_n"]; !ok {
			if _, ok := body["keep_since_days"]; !ok {
				dief("at least one of --keep-count or --keep-days is required")
			}
		}

		sendJSON(a.client, "POST", repoPath(args[0], "cleanup-policies"), body, http.StatusCreated)
		fmt.Println("Cleanup policy created.")
	}

	return cmd
}

func cleanupUpdateCmd(a *app) *cobra.Command {
	var f cleanupFlags

	cmd := leafCmd("update <repo> <id>", "Update a cleanup policy (only the flags you pass are sent)", 2, 2,
		[]completer{a.repoNames(), a.cleanupIDs()}, nil)
	f.bind(cmd)

	cmd.Run = func(cmd *cobra.Command, args []string) {
		body := f.body(cmd.Flags().Changed)
		if len(body) == 0 {
			dief("nothing to update: pass --name, --keep-count, --keep-days, --name-pattern, --schedule and/or --enabled[=false]")
		}

		sendJSON(a.client, "PUT", repoPath(args[0], "cleanup-policies/"+escape(args[1])), body, http.StatusOK)
		fmt.Println("Cleanup policy updated.")
	}

	return cmd
}

// ═══════════════════════════════════════════════════════════════════════════════
// QUOTA
// ═══════════════════════════════════════════════════════════════════════════════

func newQuotaCmd(a *app) *cobra.Command {
	repos := []completer{a.repoNames()}

	return parentCmd("quota", "Manage repository storage quotas",
		leafCmd("show <repo>", "Show the quota of a repository", 1, 1, repos, func(_ *cobra.Command, args []string) {
			showGeneric(a.client, repoPath(args[0], "quota"), a.outFmt)
		}),
		quotaSetCmd(a),
		leafCmd("delete <repo>", "Remove the quota of a repository", 1, 1, repos, func(_ *cobra.Command, args []string) {
			deleteResource(a.client, repoPath(args[0], "quota"), "Quota removed.")
		}),
	)
}

func quotaSetCmd(a *app) *cobra.Command {
	cmd := leafCmd("set <repo>", "Set the quota of a repository (only the flags you pass are sent)", 1, 1,
		[]completer{a.repoNames()}, nil)

	fl := cmd.Flags()
	limit := fl.Int64("limit", 0, "Size limit, 0 for unlimited")
	unit := fl.String("unit", "GB", "Unit for --limit: MB|GB|TB")
	maxArtifacts := fl.Int("max-artifacts", 0, "Maximum number of artifacts, 0 for unlimited")
	warn := fl.Int("warn-percent", 0, "Warn above this usage percentage (0-100)")

	flagEnum(cmd, "unit", "MB", "GB", "TB")

	cmd.Run = func(cmd *cobra.Command, args []string) {
		set := cmd.Flags().Changed
		body := map[string]any{}

		if set("limit") {
			multipliers := map[string]int64{"MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40}

			mult, ok := multipliers[strings.ToUpper(*unit)]
			if !ok {
				dief("invalid unit: %s (use MB, GB, or TB)", *unit)
			}

			if *limit < 0 || *limit > math.MaxInt64/mult {
				dief("--limit %d %s is out of range", *limit, strings.ToUpper(*unit))
			}

			body["max_size_bytes"] = *limit * mult
		}

		if set("max-artifacts") {
			body["max_artifact_count"] = *maxArtifacts
		}

		if set("warn-percent") {
			body["warn_threshold_percent"] = *warn
		}

		if len(body) == 0 {
			dief("nothing to set: pass --limit [--unit MB|GB|TB], --max-artifacts and/or --warn-percent")
		}

		sendJSON(a.client, "PUT", repoPath(args[0], "quota"), body, http.StatusOK)
		fmt.Println("Quota updated.")
	}

	return cmd
}

// ═══════════════════════════════════════════════════════════════════════════════
// PROPERTIES
// ═══════════════════════════════════════════════════════════════════════════════

func newPropsCmd(a *app) *cobra.Command {
	repos := []completer{a.repoNames()}
	artifacts := []completer{a.repoNames(), a.repoIDs("packages", "packages")}

	return parentCmd("props", "Manage artifact properties",
		leafCmd("list <repo> <artifact-id>", "List the properties of an artifact", 2, 2, artifacts, func(_ *cobra.Command, args []string) {
			showGeneric(a.client, repoPath(args[0], "artifacts/"+escape(args[1])+"/properties"), a.outFmt)
		}),
		leafCmd("set <repo> <artifact-id> <key> <value>", "Set a property of an artifact", 4, 4, artifacts,
			func(_ *cobra.Command, args []string) {
				sendJSON(a.client, "PATCH", repoPath(args[0], "artifacts/"+escape(args[1])+"/properties"),
					map[string]string{args[2]: args[3]}, http.StatusOK)
				fmt.Printf("Property '%s' set.\n", args[2])
			}),
		leafCmd("delete <repo> <artifact-id> <key>", "Delete a property of an artifact", 3, 3, artifacts,
			func(_ *cobra.Command, args []string) {
				deleteResource(
					a.client,
					repoPath(args[0], "artifacts/"+escape(args[1])+"/properties/"+escape(args[2])),
					fmt.Sprintf("Property '%s' deleted.", args[2]),
				)
			}),
		leafCmd("search <repo> <key> <value>", "Search artifacts by property", 3, 3, repos, func(_ *cobra.Command, args []string) {
			propsSearch(a.client, args[0], args[1], args[2], a.outFmt)
		}),
	)
}

// propsSearch prints the artifacts whose property key equals value: their ids with the
// package name each one names (empty when it names no stored file). The JSON output stays
// the plain list of ids.
func propsSearch(client *Client, repo, key, value, outFmt string) {
	q := url.Values{}
	q.Set("key", key)
	q.Set("value", value)

	var result struct {
		ArtifactIDs []string `json:"artifact_ids"`
		Artifacts   []struct {
			ArtifactID string `json:"artifact_id"`
			Name       string `json:"name"`
		} `json:"artifacts"`
	}

	getJSON(client, repoPath(repo, "artifacts/search?"+q.Encode()), &result)

	if outFmt == "json" {
		printJSON(result.ArtifactIDs)
		return
	}

	names := make(map[string]string, len(result.Artifacts))
	for _, a := range result.Artifacts {
		names[a.ArtifactID] = a.Name
	}

	w := newTabWriter()

	_, _ = fmt.Fprintln(w, "ARTIFACT_ID\tNAME")
	for _, id := range result.ArtifactIDs {
		_, _ = fmt.Fprintf(w, "%s\t%s\n", id, names[id])
	}

	_ = w.Flush()
}

// ═══════════════════════════════════════════════════════════════════════════════
// DELTA
// ═══════════════════════════════════════════════════════════════════════════════

func newDeltaCmd(a *app) *cobra.Command {
	get := leafCmd(
		"get <repo> <pkg-id> <dest>",
		"Download a delta patch",
		3,
		3,
		[]completer{a.repoNames(), a.repoIDs("packages", "packages"), files},
		nil,
	)
	fromSHA := get.Flags().String("from", "", "Source SHA256")
	get.Run = func(_ *cobra.Command, args []string) {
		if *fromSHA == "" {
			dief("--from is required")
		}

		q := url.Values{}
		q.Set("from", *fromSHA)
		downloadFile(a.client, repoPath(args[0], "packages/"+escape(args[1])+"/delta?"+q.Encode()), args[2], 0o644)
	}

	return parentCmd(
		"delta",
		"Package deltas",
		leafCmd(
			"list <repo> <pkg-id>",
			"List the available deltas of a package",
			2,
			2,
			[]completer{a.repoNames(), a.repoIDs("packages", "packages")},
			func(_ *cobra.Command, args []string) {
				listGeneric(a.client, repoPath(args[0], "packages/"+escape(args[1])+"/deltas"), "data",
					[]string{"FROM_SHA256", "TO_SHA256", "SIZE", "FORMAT"},
					[]string{"from_sha256", "to_sha256", "delta_size", "format"}, a.outFmt)
			},
		),
		get,
	)
}

// ═══════════════════════════════════════════════════════════════════════════════
// SYSTEM
// ═══════════════════════════════════════════════════════════════════════════════

// newSystemCmd groups the system commands; `oa system` alone shows the system
// information like `oa system info`.
func newSystemCmd(a *app) *cobra.Command {
	cmd := parentCmd("system", "System information",
		leafCmd("info", "Show system information", 0, 0, nil, func(*cobra.Command, []string) { systemInfo(a.client, a.outFmt) }),
	)
	cmd.RunE = func(*cobra.Command, []string) error {
		systemInfo(a.client, a.outFmt)

		return nil
	}

	return cmd
}

func systemInfo(client *Client, outFmt string) {
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

	for _, k := range []string{
		"version", "status", "commit", "build_time", "go_version", "os", "arch", "uptime",
		"db_status", "storage_kind", "storage_status",
	} {
		if v, ok := result[k]; ok {
			_, _ = fmt.Fprintf(w, "%s\t%s\n", k, cell(v))
		}
	}

	_ = w.Flush()
}
