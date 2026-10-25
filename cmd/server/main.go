// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database"
	formatgomod "github.com/openartifact/openartifact/internal/format/gomod"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// telemetryFlushTimeout bounds the final span/metric export at shutdown.
const telemetryFlushTimeout = 5 * time.Second

var (
	version = "dev"
	//nolint:gochecknoglobals // build-time injection via -ldflags
	commit = "unknown"
	//nolint:gochecknoglobals // build-time injection via -ldflags
	buildTime = "unknown"
)

// versionLine renders the build metadata stamped via -ldflags in the form
// "<version> <commit> <build time> <go>/<os>/<arch>"; unstamped builds print
// the "dev"/"unknown" defaults.
func versionLine() string {
	return fmt.Sprintf("%s %s %s %s/%s/%s", version, commit, buildTime, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		ee, hasCode := errors.AsType[exitError](err)
		if _, reported := errors.AsType[reportedError](err); !reported && !hasCode {
			fmt.Fprintf(os.Stderr, "openartifact-server: %v\n", err)
		}

		if hasCode {
			os.Exit(ee.code)
		}

		os.Exit(1)
	}
}

var (
	errGomodRepairBusy       = errors.New("gomod repair: another instance is already running it")
	errGomodRepairUnrepaired = errors.New("go module versions could not be repaired and must be re-uploaded")
)

var errVerifyUsage = errors.New("usage: openartifact-server verify <config.yaml>")

// reportedError marks a failure the operator has already seen (structured log
// line or flag usage), so main exits non-zero without printing it again.
type reportedError struct{ error }

func (e reportedError) Unwrap() error { return e.error }

// run is the whole server lifetime: it returns nil after a clean shutdown (or
// for -migrate, verify and -h) and an error for every startup or runtime
// failure, so the process never exits 0 while the service is not running.
func run(args []string) error {
	if len(args) > 0 && args[0] == "rebuild" {
		return runRebuild(args[1:])
	}

	opts, handled, err := parseOptions(args)
	if handled || err != nil {
		return err
	}

	return serve(opts)
}

// parseOptions parses the flags and answers the ones that do not start the
// server (-version, -healthcheck, verify, -h): the bool result is then true and
// the error is the result of the command.
func parseOptions(args []string) (options, bool, error) {
	flags := flag.NewFlagSet("openartifact-server", flag.ContinueOnError)
	migrateFlag := flags.Bool("migrate", false, "run migrations and exit")
	repairGomodFlag := flags.Bool("repair-gomod", false, "relink Go module zips damaged by the <=0.4.1 shared-blob bug, then exit")
	versionFlag := flags.Bool("version", false, "print version, commit, build time and Go runtime, then exit")
	healthcheckFlag := flags.Bool("healthcheck", false, "GET /health on 127.0.0.1 at the configured port and exit 0 on 200, 1 otherwise")
	configFile := flags.String("config", os.Getenv("OPENARTIFACT_CONFIG"), "path to YAML config file")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options{}, true, nil
		}

		// The FlagSet already printed the problem and the usage text.
		return options{}, true, reportedError{err}
	}

	if *versionFlag {
		fmt.Println(versionLine())

		return options{}, true, nil
	}

	if *healthcheckFlag {
		return options{}, true, runHealthcheck(*configFile)
	}

	// Handle 'verify' subcommand: openartifact-server verify config.yaml
	if flags.NArg() > 0 && flags.Arg(0) == "verify" {
		return options{}, true, runVerify(*configFile, flags.Args()[1:])
	}

	return options{migrate: *migrateFlag, repairGomod: *repairGomodFlag, configFile: *configFile}, false, nil
}

// runVerify is the verify subcommand: it validates the config file named by
// the first argument, else the one of -config.
func runVerify(configFile string, args []string) error {
	path := configFile
	if len(args) > 0 {
		path = args[0]
	}

	if path == "" {
		return errVerifyUsage
	}

	if err := config.ValidateFile(path); err != nil {
		return fmt.Errorf("config validation failed: %w", err)
	}

	fmt.Printf("Config file %q is valid.\n", path)

	return nil
}

// runGomodRepair is the -repair-gomod subcommand: one repair pass in the
// foreground, with a summary on stdout and a non-zero exit when anything
// could not be relinked.
func runGomodRepair(ctx context.Context, db *database.PostgresDB, store storage.BlobStore, log *logger.Logger) error {
	res, err := formatgomod.RepairZipBlobs(ctx, db, store, log)
	if err != nil {
		return fmt.Errorf("gomod repair: %w", err)
	}

	if res.LockBusy {
		return errGomodRepairBusy
	}

	fmt.Printf("gomod repair: %d affected, %d repaired, %d unrepairable, %d invalid blob row(s) removed\n",
		res.Affected, res.Repaired, len(res.Unrepairable), res.BlobRowsDeleted)

	for _, i := range res.Unrepairable {
		fmt.Printf("  unrepairable: %s\n", i)
	}

	if res.ListUnsupported {
		fmt.Println("  the storage backend cannot list objects: only the shared blob's own zip could be recovered")
	}

	if len(res.Unrepairable) > 0 {
		return reportedError{fmt.Errorf("%w: %d", errGomodRepairUnrepaired, len(res.Unrepairable))}
	}

	return nil
}
