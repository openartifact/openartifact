// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// The commands in this file are the ones the Tekton catalog steps (and any other
// CI) use around the supply-chain APIs: the OIDC token exchange, external
// check reports on a bundle, the gate of an artifact and its attestations.

const (
	// dockerConfigFile is the file name registry clients read inside a docker config directory.
	dockerConfigFile = "config.json"

	// dockerConfigUser is the user name written next to the token; the server ignores it.
	dockerConfigUser = "oa"

	// stdinFile is the --token-file value that reads the token from stdin.
	stdinFile = "-"

	maxTokenFileBytes = 64 << 10
)

var (
	errTokenFile   = errors.New("token file")
	errCheckStatus = errors.New("invalid check status")
	errGateTarget  = errors.New("gate target")
)

// checkStatuses are the states of an external check, as the server defines them.
//
//nolint:gochecknoglobals // immutable lookup table
var checkStatuses = []string{"pending", "success", "failure", "error"}

// ══════════════════════════════════════════════════════════════════════════════
// oa oidc exchange
// ══════════════════════════════════════════════════════════════════════════════

// oidcExchangeOut is the document of `oa oidc exchange --format json`: the
// server's answer to POST /api/v1/auth/oidc/exchange.
type oidcExchangeOut struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	ExpiresAt   string `json:"expires_at"`
}

func newOIDCCmd(a *app) *cobra.Command {
	return parentCmd("oidc", "Exchange CI identity tokens for access tokens", oidcExchangeCmd(a))
}

func oidcExchangeCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exchange",
		Short: "Trade a CI job's OIDC token for a short-lived deploy token",
		Long: "Read the OIDC ID token of a CI job (a GitHub Actions or GitLab CI token, a Kubernetes projected " +
			"service account token) from --token-file and exchange it, at the server's trust policies, for a short-lived " +
			"deploy token. The token is printed on stdout.\n\n" +
			"With --docker-config DIR the token is also stored in DIR/config.json as the registry credential of the " +
			"server's host, so docker, cosign, oras and skopeo can use it (set DOCKER_CONFIG=DIR); other hosts " +
			"already in the file are kept.\n\n" +
			"No stored credential is involved: the exchange is anonymous and --token and OA_TOKEN are not sent.",
		Args:              usageArgs(0, 0),
		ValidArgsFunction: positional(),
	}

	policy := cmd.Flags().String("policy", "", "Trust policy to use, by name (default: the one that matches the token)")
	tokenFile := cmd.Flags().String("token-file", "", "File holding the ID token, or - for stdin (required)")
	dockerConfig := cmd.Flags().String("docker-config", "", "Also write the token as registry credential into DIR/config.json")

	flagFiles(cmd, "token-file")
	flagFiles(cmd, "docker-config")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if *tokenFile == "" {
			return cliError("--token-file is required")
		}

		idToken, err := readTokenFile(*tokenFile, cmd.InOrStdin())
		if err != nil {
			return err
		}

		out, err := oidcExchange(context.Background(), newCIAPI(newClient(a.server, "")), idToken, *policy)
		if err != nil {
			return err
		}

		if *dockerConfig != "" {
			if err := writeDockerConfig(*dockerConfig, a.server, out.AccessToken); err != nil {
				return err
			}
		}

		ciPrint(a.outFmt, out, out.AccessToken)

		return nil
	}

	return cmd
}

// readTokenFile reads the ID token from path ("-": stdin) and trims it.
func readTokenFile(path string, stdin io.Reader) (string, error) {
	var (
		raw []byte
		err error
	)

	if path == stdinFile {
		raw, err = io.ReadAll(io.LimitReader(stdin, maxTokenFileBytes+1))
	} else {
		raw, err = readFileLimited(path, maxTokenFileBytes+1)
	}

	if err != nil {
		return "", fmt.Errorf("%w: %w", errTokenFile, err)
	}

	if len(raw) > maxTokenFileBytes {
		return "", fmt.Errorf("%w %s: larger than %d bytes", errTokenFile, path, maxTokenFileBytes)
	}

	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return "", fmt.Errorf("%w %s: empty", errTokenFile, path)
	}

	return tok, nil
}

func readFileLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	defer func() { _ = f.Close() }()

	return io.ReadAll(io.LimitReader(f, limit))
}

func oidcExchange(ctx context.Context, api *ciAPI, idToken, policy string) (oidcExchangeOut, error) {
	body := map[string]string{"token": idToken}
	if policy != "" {
		body["policy"] = policy
	}

	var out oidcExchangeOut
	if err := api.call(ctx, http.MethodPost, "/api/v1/auth/oidc/exchange", body, &out); err != nil {
		return oidcExchangeOut{}, err
	}

	if out.AccessToken == "" {
		return oidcExchangeOut{}, cliError("the server answered the exchange without a token")
	}

	return out, nil
}

// writeDockerConfig stores token as the registry credential of server's host in
// dir/config.json, keeping every other setting of an existing file. The file is
// private to the user: the token is a credential.
func writeDockerConfig(dir, server, token string) error {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" {
		return cliErrorf("cannot take a registry host from the server %q", server)
	}

	file := filepath.Join(dir, dockerConfigFile)
	doc := map[string]json.RawMessage{}

	switch raw, err := os.ReadFile(file); {
	case err == nil:
		if err := json.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("reading %s: %w", file, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}

	auths := map[string]json.RawMessage{}

	if prev, ok := doc["auths"]; ok {
		if err := json.Unmarshal(prev, &auths); err != nil {
			return fmt.Errorf("reading %s: %w", file, err)
		}
	}

	entry, err := json.Marshal(map[string]string{
		"auth": base64.StdEncoding.EncodeToString([]byte(dockerConfigUser + ":" + token)),
	})
	if err != nil {
		return err
	}

	auths[u.Host] = entry

	if doc["auths"], err = json.Marshal(auths); err != nil {
		return err
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	return os.WriteFile(file, append(out, '\n'), 0o600)
}

// ══════════════════════════════════════════════════════════════════════════════
// oa bundle check report
// ══════════════════════════════════════════════════════════════════════════════

// bundleCheckOut is an external check of a bundle as the server reports it.
type bundleCheckOut struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Summary    string `json:"summary"`
	DetailsURL string `json:"details_url"`
	ReportedBy string `json:"reported_by"`
	UpdatedAt  string `json:"updated_at"`
	ExpiresAt  string `json:"expires_at,omitempty"`
}

func (c bundleCheckOut) text() string {
	line := fmt.Sprintf("Check %s: %s", c.Name, c.Status)
	if c.Summary != "" {
		line += " (" + c.Summary + ")"
	}

	return line
}

func bundleCheckCmd(a *app) *cobra.Command {
	return parentCmd("check", "Report external checks on a bundle", bundleCheckReportCmd(a))
}

func bundleCheckReportCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report <name> <version>",
		Short: "Report the result of an external check (a policy run, a scan) on a bundle",
		Long: "Set the state of the check NAME on the bundle NAME VERSION. A promotion stage that lists the check in its " +
			"required_checks only promotes the bundle once the check is success. The caller needs write access on the " +
			"bundle's repository (a deploy token is enough). Reporting a check again replaces its state.",
		Args:              usageArgs(2, 2),
		ValidArgsFunction: bundleNameVersion(),
	}

	name := cmd.Flags().String("name", "", "Check name, as the stage's required_checks lists it (required)")
	status := cmd.Flags().String("status", "", "Check state: "+strings.Join(checkStatuses, ", ")+" (required)")
	summary := cmd.Flags().String("summary", "", "One line describing the result")
	detailsURL := cmd.Flags().String("details-url", "", "Link to the full report (a PipelineRun, a Conforma report)")
	expiresAt := cmd.Flags().String("expires-at", "", "RFC 3339 time after which the result no longer counts")

	flagEnum(cmd, "status", checkStatuses...)

	cmd.RunE = func(_ *cobra.Command, args []string) error {
		body, err := checkReportBody(*name, *status, *summary, *detailsURL, *expiresAt)
		if err != nil {
			return err
		}

		var out bundleCheckOut
		if err := newCIAPI(a.client).call(
			context.Background(), http.MethodPost, bundleAPIPath(args[0], args[1])+"/checks", body, &out,
		); err != nil {
			return err
		}

		ciPrint(a.outFmt, out, out.text())

		return nil
	}

	return cmd
}

// checkReportBody validates the flags of a check report and builds the request body.
func checkReportBody(name, status, summary, detailsURL, expiresAt string) (map[string]string, error) {
	switch {
	case name == "":
		return nil, cliError("--name is required")
	case !slices.Contains(checkStatuses, status):
		return nil, fmt.Errorf("%w %q: want one of %s", errCheckStatus, status, strings.Join(checkStatuses, ", "))
	}

	body := map[string]string{"name": name, "status": status, "summary": summary, "details_url": detailsURL}

	if expiresAt != "" {
		if _, err := time.Parse(time.RFC3339, expiresAt); err != nil {
			return nil, cliErrorf("--expires-at wants an RFC 3339 time, got %q", expiresAt)
		}

		body["expires_at"] = expiresAt
	}

	return body, nil
}

// ══════════════════════════════════════════════════════════════════════════════
// oa gate
// ══════════════════════════════════════════════════════════════════════════════

// gateCheck is one line of a gate: a named condition and whether it holds.
type gateCheck struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
}

// gateOut is the server's gate answer for an artifact or a scan.
type gateOut struct {
	Pass       bool            `json:"pass"`
	Reason     string          `json:"reason"`
	Checks     []gateCheck     `json:"checks"`
	ScanStatus json.RawMessage `json:"scan_status,omitempty"`
}

func (g gateOut) text() string {
	var b strings.Builder

	verdict := "PASS"
	if !g.Pass {
		verdict = "FAIL"
	}

	b.WriteString(verdict)

	if g.Reason != "" {
		b.WriteString(": " + g.Reason)
	}

	for _, c := range g.Checks {
		mark := "ok  "
		if !c.Pass {
			mark = "FAIL"
		}

		fmt.Fprintf(&b, "\n  %s %s", mark, c.Name)

		if c.Detail != "" {
			b.WriteString(": " + c.Detail)
		}
	}

	return b.String()
}

func newGateCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gate <repo> --digest D | --scan ID",
		Short: "Check whether an artifact may ship: vulnerabilities, licences, quarantine, attestations",
		Long: "Ask the server for the gate of one artifact of a repository: either the manifest or artifact with " +
			"--digest sha256:..., or the vulnerability scan --scan ID. The answer lists every check behind the verdict. " +
			"Reading it needs read access on the repository.\n\n" +
			"The exit status is 0 when the gate passes and 1 when it does not; the result is printed either way " +
			"(--format json for the full document).",
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(a.repoNames()),
	}

	digest := cmd.Flags().String("digest", "", "Digest of the artifact or image manifest (sha256:...)")
	scan := cmd.Flags().String("scan", "", "Id of a vulnerability scan of the repository")

	cmd.RunE = func(_ *cobra.Command, args []string) error {
		apiPath, err := gatePath(args[0], *digest, *scan)
		if err != nil {
			return err
		}

		var out gateOut
		if err := newCIAPI(a.client).call(context.Background(), http.MethodGet, apiPath, nil, &out); err != nil {
			return err
		}

		ciPrint(a.outFmt, out, out.text())

		if !out.Pass {
			return exitWith(1)
		}

		return nil
	}

	return cmd
}

// gatePath is the gate endpoint for exactly one of digest and scan.
func gatePath(repo, digest, scan string) (string, error) {
	switch {
	case digest != "" && scan != "":
		return "", fmt.Errorf("%w: give --digest or --scan, not both", errGateTarget)
	case digest != "":
		return repoPath(repo, "gate") + "?digest=" + url.QueryEscape(digest), nil
	case scan != "":
		return repoPath(repo, "vuln/scans/"+escape(scan)+"/gate"), nil
	}

	return "", fmt.Errorf("%w: give --digest or --scan", errGateTarget)
}

// ══════════════════════════════════════════════════════════════════════════════
// oa attestations
// ══════════════════════════════════════════════════════════════════════════════

func newAttestationsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attestations <repo> --subject D",
		Short: "List the signatures, attestations and SBOMs the server indexed for an image",
		Long: "Print, as JSON, what the server knows about the signatures, in-toto attestations and SBOM " +
			"attachments of the image manifest --subject (sha256:...): their type, the signer and whether they " +
			"verified.",
		Args:              usageArgs(1, 1),
		ValidArgsFunction: positional(a.repoNames()),
	}

	subject := cmd.Flags().String("subject", "", "Digest of the image manifest (sha256:..., required)")

	cmd.RunE = func(_ *cobra.Command, args []string) error {
		if *subject == "" {
			return cliError("--subject is required")
		}

		var doc json.RawMessage
		if err := newCIAPI(a.client).call(
			context.Background(), http.MethodGet, repoPath(args[0], "attestations")+"?subject="+url.QueryEscape(*subject), nil, &doc,
		); err != nil {
			return err
		}

		printJSON(doc)

		return nil
	}

	return cmd
}
