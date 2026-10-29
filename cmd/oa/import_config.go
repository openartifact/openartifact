// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/openartifact/openartifact/internal/declarative"
)

// `oa import config` carries the configuration of a running OpenArtifact
// instance over to another one as declarative manifests: it reads the source
// with the same exporter `oa export` uses and writes the manifests a fresh
// instance takes with `oa apply -f DIR -R`. Beside them it writes a mapping
// report that says, per object, whether it translated as it was, was
// approximated (and what has to be done by hand) or was skipped, and lists what
// is not reachable through an API at all.
//
// Only what both a REST read and a declarative write exist for is carried. The
// command reads; it never writes to the source.

const (
	// mappingReportFile is the report's name inside the output directory. It is
	// not YAML, so `oa apply -R` does not read it as a manifest.
	mappingReportFile = "mapping-report.json"

	mapTranslated   = "translated"
	mapApproximated = "approximated"
	mapSkipped      = "skipped"

	exitUsageError = 2
)

// mappingEntry is the fate of one exported object.
type mappingEntry struct {
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	File    string   `json:"file,omitempty"`
	Status  string   `json:"status"`
	Reasons []string `json:"reasons,omitempty"`
}

// mappingOutOfScope is something the migration does not carry, with the reason.
type mappingOutOfScope struct {
	Item   string `json:"item"`
	Reason string `json:"reason"`
}

// mappingReport is what `oa import config` writes next to the manifests.
type mappingReport struct {
	Source       string              `json:"source"`
	Translated   int                 `json:"translated"`
	Approximated int                 `json:"approximated"`
	Skipped      int                 `json:"skipped"`
	Entries      []mappingEntry      `json:"entries"`
	OutOfScope   []mappingOutOfScope `json:"out_of_scope"`
}

// configOutOfScope lists what no manifest can carry. It is the same for every
// run: the report states it explicitly rather than leaving it to be noticed.
//
//nolint:gochecknoglobals // immutable list
var configOutOfScope = []mappingOutOfScope{
	{
		"scan results, vulnerability findings and SBOMs",
		"derived data: the new instance scans what `oa import` publishes and builds its own",
	},
	{"audit log", "history of the old instance; it has no write API and is kept with the old instance"},
	{"user sessions", "bound to the old instance's session keys"},
	{
		"API tokens and CI tokens",
		"only a hash is stored and it is not readable; mint new tokens on the new instance and update the clients",
	},
	{"user passwords and password hashes", "never exported; every user is created without a password and flagged for a reset"},
	{
		"secrets (webhook secrets, proxy upstream passwords, SSO client secrets)",
		"write-only: the manifests carry secretRef placeholders; export the named environment variables before `oa apply`",
	},
	{
		"GPG, apk and nix signing keys",
		"private keys are not readable through an API: the new instance generates its own, and clients have to trust them",
	},
	{
		"storage backends and server configuration",
		"set in the server's configuration, not through the API; storage tiers only name a backend",
	},
	{"proxy repository cache", "the cache holds no data of its own and fills again from the upstream"},
	{"items in the trash", "deleted items are not part of the content to carry"},
	{
		"built-in objects (the admin user, the primary storage tier, seeded projects and permission targets)",
		"every instance has them already",
	},
}

// importConfigOptions are the inputs of `oa import config`.
type importConfigOptions struct {
	SourceURL string
	Token     string
	// Dir is where the manifests and the report are written.
	Dir string
	// Kinds restricts the export; empty means every kind, users included.
	Kinds []string
	HTTP  *http.Client
}

// resetUserKind is the User kind with every listed account flagged for a
// password reset: the accounts are created without a password.
type resetUserKind struct{ declarative.Kind }

func (k resetUserKind) List(ctx context.Context, env *declarative.Env) ([]*declarative.Live, error) {
	list, err := k.Kind.List(ctx, env)
	for _, l := range list {
		if l.Spec == nil {
			l.Spec = map[string]any{}
		}

		l.Spec["must_change_password"] = true
	}

	return list, err
}

// configRegistry is the default registry with the users flagged for a reset.
func configRegistry() *declarative.Registry {
	kinds := declarative.DefaultRegistry().Ordered()
	for i, k := range kinds {
		if k.Name() == declarative.KindUser {
			kinds[i] = resetUserKind{k}
		}
	}

	return declarative.NewRegistry(kinds...)
}

// runImportConfig reads the source, writes the manifests and the report below
// opts.Dir and returns the report.
func runImportConfig(ctx context.Context, opts importConfigOptions) (*mappingReport, error) {
	reg := configRegistry()

	kinds := opts.Kinds
	if len(kinds) == 0 {
		kinds = reg.Names() // naming the kinds is what includes the users
	}

	docs, err := declarative.Export(ctx, declarative.ExportOptions{
		Registry:    reg,
		Client:      declarative.NewClient(opts.SourceURL, opts.Token, opts.HTTP),
		Kinds:       kinds,
		WithMembers: true,
	})
	if err != nil {
		return nil, err
	}

	paths, err := declarative.WriteDir(opts.Dir, docs)
	if err != nil {
		return nil, err
	}

	rep := &mappingReport{Source: opts.SourceURL, Entries: make([]mappingEntry, 0, len(docs)), OutOfScope: configOutOfScope}

	for i, d := range docs {
		e, err := classifyConfigDoc(d)
		if err != nil {
			return nil, err
		}

		if rel, err := filepath.Rel(opts.Dir, paths[i]); err == nil {
			e.File = filepath.ToSlash(rel)
		}

		rep.add(e)
	}

	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding the mapping report: %w", err)
	}

	if err := os.WriteFile(filepath.Join(opts.Dir, mappingReportFile), append(data, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("writing the mapping report: %w", err)
	}

	return rep, nil
}

func (r *mappingReport) add(e mappingEntry) {
	switch e.Status {
	case mapTranslated:
		r.Translated++
	case mapApproximated:
		r.Approximated++
	default:
		r.Skipped++
	}

	r.Entries = append(r.Entries, e)
}

// classifyConfigDoc says what became of an exported object, from its manifest.
func classifyConfigDoc(d declarative.ExportDoc) (mappingEntry, error) {
	var doc struct {
		Spec map[string]any `yaml:"spec"`
	}

	if err := yaml.Unmarshal(d.YAML, &doc); err != nil {
		return mappingEntry{}, fmt.Errorf("reading the exported %s %q: %w", d.Kind, d.Key, err)
	}

	e := mappingEntry{Kind: d.Kind, Name: d.Key, Status: mapTranslated}
	approx := func(reason string) {
		e.Status = mapApproximated
		e.Reasons = append(e.Reasons, reason)
	}

	switch d.Kind {
	case declarative.KindUser:
		approx("created without a password and flagged must_change_password: set one with an admin password reset, or sign in through SSO")
	case declarative.KindRepository:
		switch s, _ := doc.Spec["repo_type"].(string); s {
		case "proxy":
			e.Reasons = append(e.Reasons, "recreated empty: the proxy cache is not copied")

			// The API does not say whether an upstream password is set, and the
			// exporter only writes a placeholder for a secret it knows is set.
			if u, _ := doc.Spec["upstream_username"].(string); u != "" && len(secretRefEnvs(doc.Spec)) == 0 {
				approx(
					"authenticates to its upstream as " + u + ": the password is not readable from the source; add upstream_password to the manifest",
				)
			}
		case "group":
			e.Reasons = append(e.Reasons, "members are recreated by name; hosted members need their content from `oa import`")
		default:
			e.Reasons = append(e.Reasons, "the definition only: the content is copied by `oa import`")
		}

		if v, _ := doc.Spec["sign_packages"].(bool); v {
			approx("package signing is on: the new instance signs with a key of its own, which clients have to trust")
		}
	case declarative.KindStorageTier:
		if b, _ := doc.Spec["backend"].(string); b != "" {
			approx(fmt.Sprintf("names the storage backend %q, which must be configured on the new instance", b))
		}
	}

	if envs := secretRefEnvs(doc.Spec); len(envs) > 0 {
		approx("write-only secret not readable from the source: export " + strings.Join(envs, ", ") + " before `oa apply`")
	}

	return e, nil
}

// secretRefEnvs names the environment variables of the secretRef placeholders
// in a spec.
func secretRefEnvs(v any) []string {
	var out []string

	var walk func(any)

	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if ref, ok := t["secretRef"].(map[string]any); ok {
				if env, _ := ref["env"].(string); env != "" {
					out = append(out, env)
				}

				return
			}

			for _, sub := range t {
				walk(sub)
			}
		case []any:
			for _, sub := range t {
				walk(sub)
			}
		}
	}

	walk(v)
	slices.Sort(out)

	return slices.Compact(out)
}

// print writes the human summary of the report.
func (r *mappingReport) print(w io.Writer, dir string) {
	_, _ = fmt.Fprintf(w, "Configuration of %s\n", r.Source)
	_, _ = fmt.Fprintf(w, "  %-14s %d\n", "translated:", r.Translated)
	_, _ = fmt.Fprintf(w, "  %-14s %d\n", "approximated:", r.Approximated)
	_, _ = fmt.Fprintf(w, "  %-14s %d\n", "skipped:", r.Skipped)

	for _, status := range []string{mapApproximated, mapSkipped} {
		for _, e := range r.Entries {
			if e.Status != status {
				continue
			}

			_, _ = fmt.Fprintf(w, "\n  %s %s/%s\n", status, e.Kind, e.Name)

			for _, why := range e.Reasons {
				_, _ = fmt.Fprintf(w, "    - %s\n", why)
			}
		}
	}

	_, _ = fmt.Fprintln(w, "\nNot carried over:")

	for _, o := range r.OutOfScope {
		_, _ = fmt.Fprintf(w, "  - %s: %s\n", o.Item, o.Reason)
	}

	_, _ = fmt.Fprintf(w, "\nManifests and %s are in %s; apply them with:\n  oa apply -f %s -R\n", mappingReportFile, dir, dir)
}

var errImportConfigUsage = errors.New("invalid arguments")

// handleImportConfig implements `oa import config [openartifact] ...`.
func handleImportConfig(args []string, outFmt string) int {
	opts, err := parseImportConfigFlags(args, os.Getenv, os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return exitUsageError
	}

	ctx, stop := signalContext()
	defer stop()

	opts.HTTP = newHTTPClient()

	rep, err := runImportConfig(ctx, opts)
	if err != nil {
		return reportError(err)
	}

	if outFmt == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")

		if err := enc.Encode(rep); err != nil {
			return reportError(err)
		}

		return exitOK
	}

	rep.print(os.Stdout, opts.Dir)

	return exitOK
}

func parseImportConfigFlags(args []string, getenv func(string) string, stdin io.Reader) (importConfigOptions, error) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if args[0] != kindOpenArtifact {
			return importConfigOptions{}, fmt.Errorf(
				"%w: config import supports an openartifact source only, not %q",
				errImportConfigUsage,
				args[0],
			)
		}

		args = args[1:]
	}

	var (
		opts  importConfigOptions
		kinds stringList
		buf   bytes.Buffer
	)

	fs := flag.NewFlagSet("import config", flag.ContinueOnError)
	fs.SetOutput(&buf)

	sourceURL := fs.String("source-url", getenv(envSourceURL), "base URL of the source OpenArtifact instance")
	tokenFile := fs.String("source-token-file", "", "file holding the source token, '-' for stdin (or OA_IMPORT_SOURCE_TOKEN)")

	fs.StringVar(&opts.Dir, "output", "", "directory to write the manifests and the mapping report to")
	fs.Var(&kinds, "kind", "only this kind (repeatable); default: every kind, users included")

	if err := fs.Parse(args); err != nil {
		return importConfigOptions{}, fmt.Errorf("%w: %s", errImportConfigUsage, strings.TrimSpace(buf.String()))
	}

	if opts.Dir == "" {
		return importConfigOptions{}, fmt.Errorf("%w: --output is required", errImportConfigUsage)
	}

	cfg := importConfig{}
	if err := cfg.resolveSource(*sourceURL, *tokenFile, getenv, stdin); err != nil {
		return importConfigOptions{}, err
	}

	if cfg.SourceToken == "" {
		return importConfigOptions{}, fmt.Errorf(
			"%w: the source token is required (--source-token-file or %s)",
			errImportConfigUsage,
			envSourceToken,
		)
	}

	opts.SourceURL = strings.TrimSuffix(cfg.SourceURL.String(), "/")
	opts.Token = cfg.SourceToken
	opts.Kinds = kinds

	return opts, nil
}
