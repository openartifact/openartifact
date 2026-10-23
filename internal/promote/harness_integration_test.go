// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

const promoteOrgID = "00000000-0000-0000-0000-000000000001"

// sqlExpr is a value inlined verbatim into a seed INSERT (array and jsonb
// literals); every other value is a bind parameter.
type sqlExpr string

// harness is a migrated scratch database plus seeding helpers.
type harness struct {
	t   *testing.T
	pg  *database.PostgresDB
	log *logger.Logger
	seq int
}

// newHarness creates an empty database on the TEST_DATABASE_URL server and
// applies the embedded migrations. The test is skipped (or failed under CI)
// when TEST_DATABASE_URL is not set.
func newHarness(t *testing.T) *harness {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL not set in CI; integration services are required")
		}

		t.Skip("TEST_DATABASE_URL not set")
	}

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}

	t.Cleanup(func() { _ = admin.Close() })

	template := migratedTemplate(t, admin, dsn)
	dbName := fmt.Sprintf("oa_promote_%d", time.Now().UnixNano())

	if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE `+dbName+` TEMPLATE `+template); err != nil {
		t.Fatalf("create database: %v", err)
	}

	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+dbName+` WITH (FORCE)`)
	})

	log, pg := openDB(t, dsn, dbName)

	return &harness{t: t, pg: pg, log: log}
}

// sharedTemplate is the migrated database every test database is cloned from,
// so the migrations run once per test binary instead of once per test.
//
//nolint:gochecknoglobals // one template per test binary
var sharedTemplate struct {
	once sync.Once
	name string
	dsn  string
}

// migratedTemplate builds the template database on first use.
func migratedTemplate(t *testing.T, admin *sql.DB, dsn string) string {
	t.Helper()

	sharedTemplate.once.Do(func() {
		sharedTemplate.dsn = dsn
		sharedTemplate.name = fmt.Sprintf("oa_promote_tpl_%d", time.Now().UnixNano())

		if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE `+sharedTemplate.name); err != nil {
			t.Fatalf("create template database: %v", err)
		}

		log, pg := openDB(t, dsn, sharedTemplate.name)
		defer func() { _ = pg.Close() }()

		if err := pg.Migrate(context.Background()); err != nil {
			t.Fatalf("apply migrations: %v", err)
		}

		_ = log.Sync()
	})

	if sharedTemplate.name == "" {
		t.Fatal("the migrated template database could not be created")
	}

	return sharedTemplate.name
}

// TestMain drops the shared template database once every test has run.
func TestMain(m *testing.M) {
	code := m.Run()

	if sharedTemplate.name != "" {
		if admin, err := sql.Open("pgx", sharedTemplate.dsn); err == nil {
			_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+sharedTemplate.name+` WITH (FORCE)`)
			_ = admin.Close()
		}
	}

	os.Exit(code)
}

// openDB connects to the named database of the TEST_DATABASE_URL server.
func openDB(t *testing.T, dsn, dbName string) (*logger.Logger, *database.PostgresDB) {
	t.Helper()

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}

	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()

	sslmode := u.Query().Get("sslmode")
	if sslmode == "" {
		sslmode = "disable"
	}

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}

	pg, err := database.NewPostgresDB(database.DatabaseConfig{
		Host: u.Hostname(), Port: port, User: u.User.Username(), Password: pw,
		Database: dbName, SSLMode: sslmode, MaxOpenConns: 8, MaxIdleConns: 2,
	}, log)
	if err != nil {
		t.Fatalf("NewPostgresDB: %v", err)
	}

	t.Cleanup(func() { _ = pg.Close() })

	return log, pg
}

func (h *harness) exec(query string, args ...any) {
	h.t.Helper()

	if _, err := h.pg.GetDB().ExecContext(context.Background(), query, args...); err != nil {
		h.t.Fatalf("exec %q: %v", query, err)
	}
}

func (h *harness) count(query string, args ...any) int {
	h.t.Helper()

	var n int
	if err := h.pg.GetDB().QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		h.t.Fatalf("query %q: %v", query, err)
	}

	return n
}

func (h *harness) str(query string, args ...any) string {
	h.t.Helper()

	var s sql.NullString
	if err := h.pg.GetDB().QueryRowContext(context.Background(), query, args...).Scan(&s); err != nil {
		h.t.Fatalf("query %q: %v", query, err)
	}

	return s.String
}

// repo creates a hosted repository of the format.
func (h *harness) repo(format string, immutable bool) *models.Repository {
	h.t.Helper()

	h.seq++
	org := promoteOrgID

	repo := &models.Repository{Name: fmt.Sprintf("%s-%d", format, h.seq), Format: format, RepoType: "hosted", ProjectID: &org, ImmutableReleases: immutable}
	if err := h.pg.CreateRepository(context.Background(), repo); err != nil {
		h.t.Fatalf("CreateRepository: %v", err)
	}

	got, err := h.pg.GetRepositoryByID(context.Background(), repo.ID)
	if err != nil || got == nil {
		h.t.Fatalf("GetRepositoryByID: %v, %v", got, err)
	}

	return got
}

// blob inserts a blobs row for content (no bytes: promotion never reads them)
// and returns its id and sha256.
func (h *harness) blob(content string) (string, string) {
	h.t.Helper()

	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])

	var id string
	if err := h.pg.GetDB().QueryRowContext(context.Background(),
		`INSERT INTO blobs (sha256, size_bytes, blob_key) VALUES ($1, $2, $3)
		 ON CONFLICT (sha256) DO UPDATE SET sha256 = EXCLUDED.sha256 RETURNING id`,
		sha, len(content), sha).Scan(&id); err != nil {
		h.t.Fatalf("insert blob: %v", err)
	}

	return id, sha
}

// row inserts one row into table for repoID, with blobID in blobCol when set.
func (h *harness) row(table, repoID, blobCol, blobID string, cols map[string]any) string {
	h.t.Helper()

	names := []string{"repository_id"}
	marks := []string{"$1"}
	args := []any{repoID}

	if blobCol != "" {
		names = append(names, blobCol)
		marks = append(marks, "$2")
		args = append(args, blobID)
	}

	keys := make([]string, 0, len(cols))
	for k := range cols {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, k := range keys {
		names = append(names, k)

		if e, ok := cols[k].(sqlExpr); ok {
			marks = append(marks, string(e))
			continue
		}

		args = append(args, cols[k])
		marks = append(marks, "$"+strconv.Itoa(len(args)))
	}

	var id string

	query := "INSERT INTO " + table + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(marks, ", ") + ") RETURNING id"
	if err := h.pg.GetDB().QueryRowContext(context.Background(), query, args...).Scan(&id); err != nil {
		h.t.Fatalf("seed %s: %v", table, err)
	}

	return id
}

// promoteOnce runs p in its own transaction, committing on success.
func (h *harness) promoteOnce(p Promoter, req Request) (*Result, error) {
	h.t.Helper()

	ctx := context.Background()

	tx, err := h.pg.BeginTx(ctx)
	if err != nil {
		h.t.Fatalf("begin: %v", err)
	}

	res, err := p.Promote(ctx, tx, req)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		h.t.Fatalf("commit: %v", err)
	}

	return res, nil
}

// promoterFor returns the registered promoter of format.
func promoterFor(t *testing.T, format string) Promoter {
	t.Helper()

	for _, p := range Promoters() {
		if p.Format() == format {
			return p
		}
	}

	t.Fatalf("no promoter registered for %s", format)

	return nil
}
