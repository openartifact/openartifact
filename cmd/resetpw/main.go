// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command resetpw resets a local user's password directly in the database
// (break-glass recovery when nobody can log in).
//
// The new password is never taken from the command line (it would leak
// through `ps` and shell history). Supply it through the
// OPENARTIFACT_RESET_PASSWORD environment variable or by piping one line to
// stdin:
//
//	printf '%s\n' "$NEW_PASSWORD" | resetpw -username admin
//
// The database connection is read from DB_HOST, DB_PORT, DB_USER,
// DB_PASSWORD, DB_NAME and DB_SSLMODE (or -sslmode).
package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/openartifact/openartifact/internal/auth"
)

const (
	passwordEnv     = "OPENARTIFACT_RESET_PASSWORD"
	usernameEnv     = "OPENARTIFACT_RESET_USERNAME"
	defaultUsername = "admin"
	defaultSSLMode  = "require" // same default as the server (DB_SSLMODE); "prefer" silently falls back to cleartext
	resetTimeout    = 30 * time.Second
	maxPasswordLine = 4096
)

var (
	errNoPassword  = errors.New("no password supplied: set " + passwordEnv + " or pipe it to stdin")
	errInteractive = errors.New("refusing to read a password from an interactive terminal (input would be echoed): " +
		"set " + passwordEnv + " or pipe the password to stdin")
	errUserNotFound = errors.New("user not found")
	errBadSSLMode   = errors.New("invalid sslmode")
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}

// buildDSN assembles a postgres:// URL with every component properly escaped,
// so credentials or names containing spaces, quotes or '@' cannot break or
// inject into the connection string.
func buildDSN(host, port, user, password, dbname, sslmode string) (string, error) {
	switch sslmode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		return "", fmt.Errorf("%w: %q", errBadSSLMode, sslmode)
	}

	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     net.JoinHostPort(host, port),
		Path:     "/" + dbname,
		RawQuery: url.Values{"sslmode": {sslmode}}.Encode(),
	}

	return u.String(), nil
}

// stdinIsTerminal reports whether stdin is an interactive terminal.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()

	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// readPassword returns the new password from the environment or, failing that,
// one line from in. It never reads from os.Args.
func readPassword(in io.Reader, interactive bool) (string, error) {
	if pw := os.Getenv(passwordEnv); pw != "" {
		return pw, nil
	}

	if interactive {
		return "", errInteractive
	}

	line, err := bufio.NewReaderSize(in, maxPasswordLine).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}

	pw := strings.TrimRight(line, "\r\n")
	if pw == "" {
		return "", errNoPassword
	}

	return pw, nil
}

func resetPassword(ctx context.Context, db *sql.DB, username, hash string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	// Same semantics as before: the operator chose this password, so the
	// forced-change flag is cleared.
	var userID string

	err = tx.QueryRowContext(ctx,
		`UPDATE auth_users SET password_hash = $1, must_change_password = false WHERE username = $2 RETURNING id`,
		hash, username,
	).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %q", errUserNotFound, username)
	}

	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	// Whoever may have held a session under the old password is logged out.
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

func run() error {
	username := flag.String("username", envOr(usernameEnv, defaultUsername), "user whose password is reset (env "+usernameEnv+")")
	sslmode := flag.String("sslmode", envOr("DB_SSLMODE", defaultSSLMode), "PostgreSQL sslmode (env DB_SSLMODE)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: resetpw [-username NAME] [-sslmode MODE]\n")
		fmt.Fprintf(os.Stderr, "the new password is read from %s or from one line on stdin, never from arguments\n", passwordEnv)
		fmt.Fprintln(os.Stderr, "database connection is read from DB_HOST/DB_PORT/DB_USER/DB_PASSWORD/DB_NAME/DB_SSLMODE")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() > 0 {
		return errors.New("unexpected arguments: the password must not be passed on the command line") //nolint:err113 // CLI usage error
	}

	pw, err := readPassword(os.Stdin, stdinIsTerminal())
	if err != nil {
		return err
	}

	if err := auth.ValidatePassword(pw); err != nil {
		return err
	}

	dsn, err := buildDSN(
		envOr("DB_HOST", "localhost"),
		envOr("DB_PORT", "5432"),
		envOr("DB_USER", "postgres"),
		os.Getenv("DB_PASSWORD"),
		envOr("DB_NAME", "artifact_repo"),
		*sslmode,
	)
	if err != nil {
		return err
	}

	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), resetTimeout)
	defer cancel()

	if err := resetPassword(ctx, db, *username, hash); err != nil {
		return err
	}

	fmt.Printf("password for %q reset successfully; existing sessions revoked\n", *username)

	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
