// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/openartifact/openartifact/internal/declarative"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Exit codes of `oa diff` (apply uses 0 and 1).
const (
	exitOK      = 0
	exitError   = 1
	exitChanges = 2
)

// declFlags are the flags shared by apply and diff.
type declFlags struct {
	files        []string
	kinds        []string
	recursive    bool
	prune        bool
	pruneAll     bool
	forceSecrets bool
	continueErr  bool
	validateOnly bool
	output       string
}

// declKinds are the kind names `--kind` accepts, from the declarative registry.
func declKinds() []string { return declarative.DefaultRegistry().Names() }

func bindDeclFlags(fs *pflag.FlagSet, f *declFlags, withApply bool) {
	fs.StringArrayVarP(&f.files, "file", "f", nil, "Manifest file, directory or - for stdin (repeatable)")
	fs.BoolVarP(&f.recursive, "recursive", "R", false, "Descend into subdirectories of -f directories")
	fs.StringArrayVar(&f.kinds, "kind", nil, "Only consider this kind (repeatable), e.g. Repository")
	fs.BoolVar(&f.prune, "prune", false, "Delete objects previously applied by oa that are no longer in the manifests")
	fs.BoolVar(
		&f.pruneAll,
		"prune-all",
		false,
		"Delete every object absent from the manifests, managed or not "+
			"(never seeded objects; Users and ServiceAccounts only with --kind User or --kind ServiceAccount)",
	)
	fs.BoolVar(&f.forceSecrets, "force-secrets", false, "Send every secret even if the server reports it as set")
	fs.StringVarP(&f.output, "output", "o", "", "Output format: json (default: human readable)")

	if withApply {
		fs.BoolVar(&f.continueErr, "continue-on-error", false, "Keep applying after a failure (exit status is still 1)")
		fs.BoolVar(&f.validateOnly, "validate-only", false, "Validate the manifests offline and exit")
	}
}

// declCmd builds `oa apply` (apply true) and `oa diff`: exit 0 when the server
// matches the manifests, 2 when diff finds a difference, 1 on error.
func declCmd(a *app, apply bool) *cobra.Command {
	var f declFlags

	cmd := &cobra.Command{
		Args:              usageArgs(0, 0),
		ValidArgsFunction: positional(),
		RunE: func(*cobra.Command, []string) error {
			return exitWith(runDecl(&f, a.client, a.outFmt, apply, os.Stdin, os.Stdout))
		},
	}

	if apply {
		cmd.Use = "apply -f FILE|DIR|-"
		cmd.Short = "Apply declarative manifests to the server"
		cmd.Long = "Apply the manifests given with -f (files, directories or - for stdin) so the server matches them.\n\n" +
			"Exit status 0 on success, 1 on error."
	} else {
		cmd.Use = "diff -f FILE|DIR|-"
		cmd.Short = "Show what apply would change"
		cmd.Long = "Show what `oa apply` would change for the manifests given with -f.\n\n" +
			"Exit status 0 when nothing changes, 2 when there are changes, 1 on error."
	}

	bindDeclFlags(cmd.Flags(), &f, apply)

	flagFiles(cmd, "file")
	flagEnum(cmd, "kind", declKinds()...)
	flagEnum(cmd, "output", formatJSON)

	return cmd
}

func newApplyCmd(a *app) *cobra.Command { return declCmd(a, true) }

func newDiffCmd(a *app) *cobra.Command { return declCmd(a, false) }

func declOptions(f *declFlags, client *Client, stdin io.Reader) declarative.Options {
	return declarative.Options{
		Registry: declarative.DefaultRegistry(),
		Client:   declarative.NewClient(client.baseURL, client.token, client.httpClient),
		Load:     declarative.LoadOptions{Paths: f.files, Recursive: f.recursive, Stdin: stdin},
		Kinds:    f.kinds, Prune: f.prune, PruneAll: f.pruneAll, ForceSecrets: f.forceSecrets,
		ContinueOnError: f.continueErr,
	}
}

// declJSON reports whether JSON output was requested by -o/--output json or --format json.
func declJSON(f *declFlags, globalFormat string) (bool, error) {
	switch f.output {
	case "":
		return globalFormat == "json", nil
	case "json":
		return true, nil
	case "table", "text":
		return false, nil
	}

	return false, fmt.Errorf("unknown output format %q (use json)", f.output) //nolint:err113 // user-facing message
}

func reportError(err error) int {
	if verrs, ok := errors.AsType[declarative.ValidationErrors](err); ok {
		fmt.Fprintf(os.Stderr, "Error: %d problem(s) in the manifests:\n", len(verrs))

		for _, e := range verrs {
			fmt.Fprintf(os.Stderr, "  %s\n", e)
		}

		return exitError
	}

	fmt.Fprintf(os.Stderr, "Error: %v\n", err)

	return exitError
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func runDecl(f *declFlags, client *Client, globalFormat string, apply bool, stdin io.Reader, out io.Writer) int {
	asJSON, err := declJSON(f, globalFormat)
	if err != nil {
		return reportError(err)
	}

	if len(f.files) == 0 {
		return reportError(errors.New("no manifests given: use -f FILE|DIR|-")) //nolint:err113 // user-facing message
	}

	opts := declOptions(f, client, stdin)

	if f.validateOnly {
		return validateOnly(opts, out)
	}

	ctx, stop := signalContext()
	defer stop()

	prepared, err := declarative.Prepare(ctx, opts)
	if err != nil {
		return reportError(err)
	}

	return finishDecl(ctx, opts, prepared, apply, asJSON, out)
}

func validateOnly(opts declarative.Options, out io.Writer) int {
	objs, err := declarative.Load(opts.Registry, opts.Load)
	if err != nil {
		return reportError(err)
	}

	fmt.Fprintf(out, "%d manifest(s) valid\n", len(objs))

	return exitOK
}

func finishDecl(ctx context.Context, opts declarative.Options, p *declarative.Prepared, apply, asJSON bool, out io.Writer) int {
	plan := p.Plan

	if errs := plan.Errors(); len(errs) > 0 {
		printPlan(plan, nil, asJSON, out)

		return planFailed(errs)
	}

	if errs := declarative.CheckSecrets(plan, opts.Resolver); len(errs) > 0 {
		return planFailed(errs)
	}

	if !apply {
		printPlan(plan, nil, asJSON, out)

		if plan.HasChanges() {
			return exitChanges
		}

		return exitOK
	}

	outcomes := declarative.Execute(ctx, opts, p)
	printPlan(plan, outcomes, asJSON, out)

	if failed := declarative.Failed(outcomes); len(failed) > 0 {
		for _, o := range failed {
			fmt.Fprintf(os.Stderr, "Error: %v\n", o.Err)
		}

		return exitError
	}

	return exitOK
}

func planFailed(errs []error) int {
	fmt.Fprintf(os.Stderr, "Error: %d problem(s) prevent applying; nothing was changed:\n", len(errs))

	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "  %v\n", e)
	}

	return exitError
}

func printPlan(plan *declarative.Plan, outcomes []declarative.Outcome, asJSON bool, out io.Writer) {
	if asJSON {
		if err := declarative.WriteJSON(out, plan, outcomes); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}

		return
	}

	if outcomes == nil {
		if !plan.HasChanges() {
			fmt.Fprintln(out, "No changes: the server matches the manifests.")
			noteAdoptions(plan, out)

			return
		}

		declarative.WriteDiff(out, plan)
		fmt.Fprintln(out)
		declarative.WriteSummary(out, plan)
		noteAdoptions(plan, out)

		return
	}

	declarative.WriteTable(out, plan, outcomes)
	fmt.Fprintln(out)
	declarative.WriteSummary(out, plan)
	noteAdoptions(plan, out)
}

// noteAdoptions mentions objects that exist but are not yet marked as managed.
func noteAdoptions(plan *declarative.Plan, out io.Writer) {
	n := 0

	for _, it := range plan.Items {
		if it.Adopt && it.Action == declarative.ActionUnchanged {
			n++
		}
	}

	if n > 0 {
		fmt.Fprintf(out, "%d existing object(s) match the manifests and will be recorded as managed by `oa apply`.\n", n)
	}
}

func newExportCmd(a *app) *cobra.Command {
	var (
		kinds       []string
		dir         string
		withMembers bool
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the server configuration as manifests",
		Long: "Export the server configuration as declarative manifests that `oa apply` takes: one YAML stream on " +
			"stdout, or one file per object with --output DIR.",
		Args:              usageArgs(0, 0),
		ValidArgsFunction: positional(),
		RunE: func(*cobra.Command, []string) error {
			return exitWith(runExport(a.client, kinds, dir, withMembers))
		},
	}

	cmd.Flags().StringArrayVar(&kinds, "kind", nil, "Only export this kind (repeatable); User is exported only when named")
	cmd.Flags().StringVarP(&dir, "output", "o", "", "Write one file per object into this directory (default: one YAML stream on stdout)")
	cmd.Flags().BoolVar(&withMembers, "with-members", false, "Include fields normally omitted, such as Group members")

	flagEnum(cmd, "kind", declKinds()...)
	flagFiles(cmd, "output")

	return cmd
}

func runExport(client *Client, kinds []string, dir string, withMembers bool) int {
	ctx, stop := signalContext()
	defer stop()

	docs, err := declarative.Export(ctx, declarative.ExportOptions{
		Registry: declarative.DefaultRegistry(),
		Client:   declarative.NewClient(client.baseURL, client.token, client.httpClient),
		Kinds:    kinds, WithMembers: withMembers,
	})
	if err != nil {
		return reportError(err)
	}

	if dir == "" {
		if err := declarative.WriteStream(os.Stdout, docs); err != nil {
			return reportError(err)
		}

		return exitOK
	}

	paths, err := declarative.WriteDir(dir, docs)
	if err != nil {
		return reportError(err)
	}

	fmt.Fprintf(os.Stderr, "Exported %d object(s) to %s\n", len(paths), dir)

	return exitOK
}
