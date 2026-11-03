// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	defaultServer = "http://localhost:8080"

	// formatTable and formatJSON are the two values of the global --format.
	formatTable = "table"
	formatJSON  = "json"
)

// app is what every command reads: the resolved configuration, the HTTP client
// and the output format. The flags --server, --token and --format are bound to
// its fields; PersistentPreRunE (preRun) resolves them (flag, then environment,
// then config file) into client and outFmt before any command body runs. Help,
// version and completion never reach it.
type app struct {
	cfg    *Config
	client *Client
	outFmt string

	server string
	token  string
	format string
}

// exitStatusError is returned from a command body to end the process with that status
// without printing anything (diff exits 2 on changes, import exits 1 on failed
// items): the command has already reported.
// cliError is a user-facing error: its text is the whole message.
type cliError string

func (e cliError) Error() string { return string(e) }

func cliErrorf(format string, a ...any) error { return cliError(fmt.Sprintf(format, a...)) }

type exitStatusError int

func (e exitStatusError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// exitWith turns a status returned by a command body into the error cobra
// returns up to run: nil for 0.
func exitWith(code int) error {
	if code == 0 {
		return nil
	}

	return exitStatusError(code)
}

// newRootCmd builds the whole command tree. Every call returns a fresh tree, so
// tests can run commands in one process.
func newRootCmd() *cobra.Command {
	a := &app{}

	root := &cobra.Command{
		Use:   "oa",
		Short: "OpenArtifact command-line client",
		Long: "oa is the command-line client of an OpenArtifact server: repositories, packages, users, " +
			"scans, CI uploads, declarative configuration and migrations.\n\n" +
			"The server and token come from --server and --token, else the OA_SERVER and OA_TOKEN " +
			"environment variables, else the file written by `oa auth login` (~/.config/oa/config.json).\n\n" +
			"Flags take two dashes (--format json); put names that start with - after a bare -- " +
			"(oa repo show -- -legacy).",
		Version:           version,
		SilenceUsage:      true,
		SilenceErrors:     true,
		TraverseChildren:  true,
		PersistentPreRunE: a.preRun,
		Args:              groupArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return usageFailure(cmd)
		},
	}

	root.SetVersionTemplate("oa version {{.Version}}\n")
	// Defining --version here keeps cobra from also claiming -v for it.
	root.Flags().Bool("version", false, "Print the version")
	root.SetFlagErrorFunc(flagError)

	pf := root.PersistentFlags()
	pf.StringVar(&a.server, "server", "", "Server URL (default "+defaultServer+", env OA_SERVER)")
	pf.StringVar(&a.token, "token", "", "API token (env OA_TOKEN)")
	pf.StringVar(&a.format, "format", formatTable, "Output format: table or json")
	mustRegister(root.RegisterFlagCompletionFunc("format", enumCompletion(formatTable, formatJSON)))

	root.CompletionOptions.DisableDefaultCmd = true

	root.AddCommand(
		newVersionCmd(a),
		newCompletionCmd(),
	)
	addCommandGroups(root, a)

	return root
}

// run executes the CLI with args (without the program name) and returns the
// process exit status. Help and completion output go to stdout, errors to
// stderr; command results are written to the process's own stdout.
func run(args []string, stdout, stderr io.Writer) int {
	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	if len(args) == 0 || args[0] != cobra.ShellCompRequestCmd {
		if err := rejectSingleDashLong(root, args); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
	}

	err := root.Execute()

	var code exitStatusError

	switch {
	case err == nil:
		return 0
	case errors.As(err, &code):
		return int(code)
	default:
		fmt.Fprintf(stderr, "Error: %v\n", err)

		return 1
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// usageFailure prints the help of cmd to stderr and fails: a command group run
// without a subcommand (or the bare program) is a usage error, not a success.
func usageFailure(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	cmd.SetOut(cmd.ErrOrStderr())

	_ = cmd.Help()

	cmd.SetOut(out)

	return exitStatusError(1)
}

// preRun resolves the connection settings before a command that talks to the
// server runs.
func (a *app) preRun(cmd *cobra.Command, _ []string) error {
	if offline(cmd) {
		// `oa version` has a machine-readable form; it still needs no config.
		if top(cmd) == "version" {
			if err := checkFormat(a.format); err != nil {
				return err
			}

			a.outFmt = a.format
		}

		return nil
	}

	cfg, err := loadConfig()
	if err != nil {
		// A corrupt config must not block the commands that repair it
		// (`auth login` rewrites it); everything else needs it.
		if !repairsConfig(cmd) {
			return fmt.Errorf("loading config: %w (fix or delete %s; `oa auth login` rewrites it)", err, configPath())
		}

		cfg = &Config{Server: defaultServer}
	}

	a.cfg = cfg

	if err := checkFormat(a.format); err != nil {
		return err
	}

	a.outFmt = a.format
	a.server = a.resolve(cmd, "server", a.server, "OA_SERVER", cfg.Server)
	a.token = a.resolve(cmd, "token", a.token, "OA_TOKEN", cfg.Token)

	warnCredentialExposure(os.Stderr, a.server, a.token, cmd.Root().PersistentFlags().Changed("token"))

	a.client = newClient(a.server, a.token)

	return nil
}

// resolve returns the flag value when it was given, else the environment
// variable, else the config file value.
func (*app) resolve(cmd *cobra.Command, flagName, flagValue, envName, fromConfig string) string {
	if cmd.Root().PersistentFlags().Changed(flagName) {
		return flagValue
	}

	return getEnv(envName, fromConfig)
}

// offline reports whether cmd never touches the server or the config: help,
// version and the completion machinery.
func offline(cmd *cobra.Command) bool {
	switch top(cmd) {
	case "help", "version", "completion", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
		return true
	}

	return false
}

// repairsConfig reports whether cmd may run with an unreadable config file.
func repairsConfig(cmd *cobra.Command) bool {
	return cmd.CommandPath() == "oa auth login"
}

// top returns the name of the first command below the root on cmd's path.
func top(cmd *cobra.Command) string {
	for cmd.HasParent() && cmd.Parent().HasParent() {
		cmd = cmd.Parent()
	}

	return cmd.Name()
}

func newVersionCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the CLI version (--format json for machine use)",
		Args:  usageArgs(0, 0),
		Run: func(*cobra.Command, []string) {
			ciPrint(a.outFmt, currentVersionInfo(), "oa version "+version)
		},
	}
}

// checkFormat refuses an output format other than table or json.
func checkFormat(format string) error {
	if format != formatTable && format != formatJSON {
		return cliErrorf("invalid --format %q: want table or json", format)
	}

	return nil
}

// mustRegister panics on a programming error in the command tree (a completion
// registered for a flag that does not exist); the tree test catches it.
func mustRegister(err error) {
	if err != nil {
		panic(err)
	}
}

// parentCmd builds a command that only holds subcommands. Run alone it is a
// usage error; an unknown subcommand is reported with suggestions.
func parentCmd(use, short string, children ...*cobra.Command) *cobra.Command {
	g := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  groupArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return usageFailure(cmd)
		},
	}

	g.AddCommand(children...)

	return g
}

func groupArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}

	msg := fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath())

	// cobra applies its default distance of 2 only on its own error path.
	if cmd.SuggestionsMinimumDistance <= 0 {
		cmd.SuggestionsMinimumDistance = 2
	}

	if s := cmd.SuggestionsFor(args[0]); len(s) > 0 {
		msg += "\n\nDid you mean this?\n\t" + strings.Join(s, "\n\t")
	}

	return cliError(msg)
}

// usageArgs validates the number of positional arguments (max < 0: no upper
// bound). The error is `Usage: oa repo show <name>`, built from the command's
// Use line.
func usageArgs(minArgs, maxArgs int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) >= minArgs && (maxArgs < 0 || len(args) <= maxArgs) {
			return nil
		}

		use := cmd.CommandPath()
		if _, rest, ok := strings.Cut(cmd.Use, " "); ok {
			use += " " + rest
		}

		return cliErrorf("Usage: %s (see `%s --help`)", use, cmd.CommandPath())
	}
}

// flagError decorates the flag-parsing errors of pflag: the usual cause of a
// stray -x is a name that starts with a dash.
func flagError(cmd *cobra.Command, err error) error {
	return fmt.Errorf("%w (see `%s --help`; use -- before names starting with -)", err, cmd.CommandPath())
}

// lookupFlag finds the flag a command line accepts as --name: the command's
// own flags, then the persistent flags of it and its parents.
func lookupFlag(cmd *cobra.Command, name string) *pflag.Flag {
	cmd.InitDefaultHelpFlag()

	if f := cmd.Flags().Lookup(name); f != nil {
		return f
	}

	for c := cmd; c != nil; c = c.Parent() {
		if f := c.PersistentFlags().Lookup(name); f != nil {
			return f
		}
	}

	return nil
}

// rejectSingleDashLong refuses -name where name is a long flag of the command:
// pflag would read it as the bundled shorthands -n -a -m -e, one of which may
// take the rest as its value. Long flags need two dashes.
func rejectSingleDashLong(root *cobra.Command, args []string) error {
	cur := root

	for i := 0; i < len(args); i++ {
		a := args[i]

		switch {
		case a == "--":
			return nil
		case strings.HasPrefix(a, "--"):
			name, _, attached := strings.Cut(a[2:], "=")
			if f := lookupFlag(cur, name); f != nil && !attached && takesValue(f) {
				i++
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			name, _, attached := strings.Cut(a[1:], "=")

			if len(name) > 1 {
				if lookupFlag(cur, name) != nil {
					return cliErrorf("unknown flag -%s: did you mean --%s? (long flags take two dashes)", name, name)
				}

				continue
			}

			if f := shorthand(cur, name); f != nil && !attached && takesValue(f) {
				i++
			}
		default:
			if child := findChild(cur, a); child != nil {
				cur = child
			}
		}
	}

	return nil
}

func takesValue(f *pflag.Flag) bool { return f.NoOptDefVal == "" }

func shorthand(cmd *cobra.Command, name string) *pflag.Flag {
	cmd.InitDefaultHelpFlag()

	if f := cmd.Flags().ShorthandLookup(name); f != nil {
		return f
	}

	for c := cmd; c != nil; c = c.Parent() {
		if f := c.PersistentFlags().ShorthandLookup(name); f != nil {
			return f
		}
	}

	return nil
}

func findChild(cmd *cobra.Command, name string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Name() == name || c.HasAlias(name) {
			return c
		}
	}

	return nil
}
