// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"database/sql"
	"testing"
)

// projectsPrevious is the last migration number before the projects migration.
const projectsPrevious = "024"

const (
	projectsSchemaMigration   = "025_projects.sql"
	projectsValidateMigration = "025_projects_validate.no_tx.sql"
	projectsSystemID          = "00000000-0000-0000-0000-000000000001"
)

// projectsSeed holds the ids written under the pre-025 schema.
type projectsSeed struct {
	users map[string]string // username -> id
	// orgs: name -> id
	orgs map[string]string
	repo string
}

// seedPre025 writes organizations (besides the system one from 001) with every
// member role / member_role combination, a repository and audit rows.
func seedPre025(t *testing.T, db *sql.DB) projectsSeed {
	t.Helper()

	s := projectsSeed{users: map[string]string{}, orgs: map[string]string{}}

	for _, u := range []string{"owner", "adm", "mem-read", "mem-write", "mem-admin", "sys-mem"} {
		s.users[u] = mustScan[string](t, db, `INSERT INTO auth_users (username) VALUES ($1) RETURNING id`, u)
	}

	// organizations.member_role level per organization; 'Owner Org' keeps the
	// default (read), the others cover write and admin.
	for _, o := range []struct{ name, slug, memberRole string }{
		{"owner-org", "owner-org", "read"},
		{"write-org", "write-org", "write"},
		{"admin-org", "admin-org", "admin"},
	} {
		s.orgs[o.name] = mustScan[string](t, db,
			`INSERT INTO organizations (name, slug, owner_id, member_role) VALUES ($1, $2, $3, $4) RETURNING id`,
			o.name, o.slug, s.users["owner"], o.memberRole)
	}

	member := func(org, user, role string) {
		mustExec(t, db, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, $3)`, s.orgs[org], s.users[user], role)
	}

	member("owner-org", "owner", "owner")
	member("owner-org", "adm", "admin")
	member("owner-org", "mem-read", "member") // member_role read -> viewer
	member("write-org", "mem-write", "member")
	member("admin-org", "mem-admin", "member")
	member("admin-org", "adm", "admin")

	// The system organization keeps its (ignored) membership.
	mustExec(t, db, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, 'member')`, projectsSystemID, s.users["sys-mem"])

	s.repo = mustScan[string](t, db,
		`INSERT INTO repositories (name, format, org_id) VALUES ('owner-raw', 'raw', $1) RETURNING id`, s.orgs["owner-org"])

	mustExec(t, db, `INSERT INTO audit_logs (action, org_id) VALUES ('org.created', $1)`, s.orgs["owner-org"])
	mustExec(t, db, `INSERT INTO audit_logs (action, org_id) VALUES ('org.member_added', $1)`, s.orgs["owner-org"])

	return s
}

func projectsTableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()

	return mustScan[bool](t, db, `SELECT to_regclass('public.' || $1) IS NOT NULL`, table)
}

func projectsColumnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()

	return mustScan[bool](t, db,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2)`,
		table, column)
}

func projectsConstraintExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()

	return mustScan[bool](t, db, `SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = $1)`, name)
}

func assertProjectsRenames(t *testing.T, db *sql.DB) {
	t.Helper()

	for _, table := range []string{"organizations", "organization_members"} {
		if projectsTableExists(t, db, table) {
			t.Errorf("table %s still exists", table)
		}
	}

	for _, table := range []string{"projects", "project_members", "project_groups"} {
		if !projectsTableExists(t, db, table) {
			t.Errorf("table %s is missing", table)
		}
	}

	for _, table := range []string{"project_members", "auth_groups", "permission_targets", "repositories", "audit_logs"} {
		if projectsColumnExists(t, db, table, "org_id") {
			t.Errorf("%s.org_id still exists", table)
		}

		if !projectsColumnExists(t, db, table, "project_id") {
			t.Errorf("%s.project_id is missing", table)
		}
	}

	if projectsColumnExists(t, db, "projects", "member_role") {
		t.Error("projects.member_role was not dropped")
	}

	oldConstraints := []string{
		"organizations_pkey", "organizations_name_key", "organizations_slug_unique", "organizations_owner_id_fkey",
		"organization_members_pkey", "organization_members_org_id_fkey", "organization_members_user_id_fkey",
		"auth_groups_org_name_unique", "auth_groups_org_id_fkey", "permission_targets_org_name_unique",
		"permission_targets_org_id_fkey", "repositories_org_name_unique", "fk_repositories_org",
		"audit_logs_org_id_fkey", "chk_org_member_role", "chk_org_member_role_level",
	}
	for _, c := range oldConstraints {
		if projectsConstraintExists(t, db, c) {
			t.Errorf("constraint %s still exists", c)
		}
	}

	newConstraints := []string{
		"projects_pkey", "projects_name_key", "projects_slug_unique", "projects_owner_id_fkey",
		"project_members_pkey", "project_members_project_id_fkey", "project_members_user_id_fkey",
		"auth_groups_project_name_unique", "auth_groups_project_id_fkey", "permission_targets_project_name_unique",
		"permission_targets_project_id_fkey", "repositories_project_name_unique", "fk_repositories_project",
		"audit_logs_project_id_fkey", "chk_project_member_role", "chk_project_repo_types",
		"project_groups_pkey", "chk_project_group_role",
	}
	for _, c := range newConstraints {
		if !projectsConstraintExists(t, db, c) {
			t.Errorf("constraint %s is missing", c)
		}
	}

	// repositories.project_id still restricts the deletion of a project.
	if del := mustScan[string](t, db,
		`SELECT confdeltype::text FROM pg_constraint WHERE conname = 'fk_repositories_project'`); del != "r" {
		t.Errorf("fk_repositories_project confdeltype = %q, want r (RESTRICT)", del)
	}

	for _, idx := range []string{
		"idx_project_members_project_id", "idx_project_members_user_id", "idx_auth_groups_project_id",
		"idx_permission_targets_project_id", "idx_repositories_project_id", "idx_audit_logs_project_id",
		"idx_project_groups_group",
	} {
		if mustScan[bool](t, db, `SELECT to_regclass('public.' || $1) IS NULL`, idx) {
			t.Errorf("index %s is missing", idx)
		}
	}
}

func assertProjectsRoles(t *testing.T, db *sql.DB, s projectsSeed) {
	t.Helper()

	tests := []struct{ name, org, user, want string }{
		{"owner stays owner", "owner-org", "owner", "owner"},
		{"admin -> maintainer", "owner-org", "adm", "maintainer"},
		{"member + member_role read -> viewer", "owner-org", "mem-read", "viewer"},
		{"member + member_role write -> developer", "write-org", "mem-write", "developer"},
		{"member + member_role admin -> maintainer", "admin-org", "mem-admin", "maintainer"},
		{"admin in admin-org -> maintainer", "admin-org", "adm", "maintainer"},
	}

	for _, tt := range tests {
		got := mustScan[string](t, db, `SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2`, s.orgs[tt.org], s.users[tt.user])
		if got != tt.want {
			t.Errorf("%s: role = %q, want %q", tt.name, got, tt.want)
		}
	}

	if got := mustScan[string](t, db, `SELECT role FROM project_members WHERE project_id = $1`, projectsSystemID); got != "viewer" {
		t.Errorf("system project member role = %q, want viewer (default member_role)", got)
	}

	for _, role := range []string{"admin", "member"} {
		_, err := db.ExecContext(context.Background(),
			`UPDATE project_members SET role = $1 WHERE user_id = $2`, role, s.users["owner"])
		wantPgCode(t, err, pgCheckViolation, "chk_project_member_role accepting "+role)
	}
}

func assertProjectsPolicy(t *testing.T, db *sql.DB, s projectsSeed) {
	t.Helper()

	for name, id := range s.orgs {
		var (
			prefix  string
			maxRepo int
			ttl     int
			public  bool
			types   string
			formats sql.NullString
		)

		err := db.QueryRowContext(context.Background(),
			`SELECT repo_name_prefix, max_repositories, token_max_ttl_days, allow_public_repos, allowed_repo_types::text, allowed_formats::text
			   FROM projects WHERE id = $1`, id).Scan(&prefix, &maxRepo, &ttl, &public, &types, &formats)
		if err != nil {
			t.Fatalf("%s: read policy: %v", name, err)
		}

		if prefix != name+"-" || maxRepo != 50 || ttl != 365 || public || types != "{hosted}" || formats.Valid {
			t.Errorf("%s policy = prefix %q max %d ttl %d public %v types %s formats %v; want %q/50/365/false/{hosted}/NULL",
				name, prefix, maxRepo, ttl, public, types, formats, name+"-")
		}
	}

	if got := mustScan[string](t, db, `SELECT repo_name_prefix FROM projects WHERE id = $1`, projectsSystemID); got != "" {
		t.Errorf("system project prefix = %q, want empty", got)
	}

	for _, stmt := range []string{
		`UPDATE projects SET allowed_repo_types = '{bogus}' WHERE id = $1`,
		`UPDATE projects SET max_repositories = -1 WHERE id = $1`,
		`UPDATE projects SET token_max_ttl_days = -1 WHERE id = $1`,
	} {
		_, err := db.ExecContext(context.Background(), stmt, projectsSystemID)
		wantPgCode(t, err, pgCheckViolation, stmt)
	}
}

func assertProjectGroupsAndAudit(t *testing.T, db *sql.DB, s projectsSeed) {
	t.Helper()

	ctx := context.Background()

	group := mustScan[string](t, db, `INSERT INTO auth_groups (name) VALUES ('team') RETURNING id`)

	_, err := db.ExecContext(ctx, `INSERT INTO project_groups (project_id, group_id, role) VALUES ($1, $2, 'owner')`, s.orgs["owner-org"], group)
	wantPgCode(t, err, pgCheckViolation, "project_groups accepting owner")

	mustExec(t, db, `INSERT INTO project_groups (project_id, group_id, role) VALUES ($1, $2, 'developer')`, s.orgs["owner-org"], group)

	mustExec(t, db, `DELETE FROM auth_groups WHERE id = $1`, group)

	if n := mustScan[int](t, db, `SELECT count(*) FROM project_groups`); n != 0 {
		t.Errorf("project_groups holds %d rows after the group was deleted, want 0 (cascade)", n)
	}

	// Pre-existing org.* rows survive and are validated.
	if n := mustScan[int](t, db, `SELECT count(*) FROM audit_logs WHERE action LIKE 'org.%' AND project_id = $1`, s.orgs["owner-org"]); n != 2 {
		t.Errorf("legacy org.* audit rows = %d, want 2", n)
	}

	if !mustScan[bool](t, db, `SELECT convalidated FROM pg_constraint WHERE conname = 'chk_audit_action'`) {
		t.Error("chk_audit_action is not validated after 025_projects_validate.no_tx.sql")
	}

	for _, action := range []string{"project.created", "project.group_added", "project.repository_transferred", "project.cleanup_policy_changed"} {
		mustExec(t, db, `INSERT INTO audit_logs (action, project_id) VALUES ($1, $2)`, action, s.orgs["owner-org"])
	}

	_, err = db.ExecContext(ctx, `INSERT INTO audit_logs (action) VALUES ('project.bogus')`)
	wantPgCode(t, err, pgCheckViolation, "chk_audit_action accepting junk")

	// Deleting a project with repositories stays blocked; one without is fine
	// and detaches its audit rows.
	_, err = db.ExecContext(ctx, `DELETE FROM projects WHERE id = $1`, s.orgs["owner-org"])
	// ON DELETE RESTRICT reports restrict_violation (23001) on PostgreSQL 18 and
	// foreign_key_violation (23503) on 17 and older; both mean "blocked".
	if !IsForeignKeyViolation(err) {
		t.Errorf("deleting a project that owns a repository: err = %v, want a foreign key or restrict violation", err)
	}

	mustExec(t, db, `DELETE FROM projects WHERE id = $1`, s.orgs["write-org"])
}

func TestProjectsMigrationUpgradeFrom024(t *testing.T) {
	db := scratchDB(t)

	migrateThrough(t, db, projectsPrevious)

	if !projectsTableExists(t, db, "organizations") || projectsTableExists(t, db, "projects") {
		t.Fatal("schema through 024 must still have organizations and no projects")
	}

	s := seedPre025(t, db)

	migrateAll(t, db)

	for _, v := range []string{projectsSchemaMigration, projectsValidateMigration} {
		if n := mustScan[int](t, db, `SELECT count(*) FROM schema_migrations WHERE version = $1`, v); n != 1 {
			t.Errorf("schema_migrations has %d rows for %s, want 1", n, v)
		}
	}

	if n := mustScan[int](t, db, `SELECT count(*) FROM projects`); n != 4 {
		t.Fatalf("projects holds %d rows, want 4 (system + 3)", n)
	}

	if got := mustScan[string](t, db, `SELECT project_id FROM repositories WHERE id = $1`, s.repo); got != s.orgs["owner-org"] {
		t.Errorf("repository project_id = %s, want %s", got, s.orgs["owner-org"])
	}

	assertProjectsRenames(t, db)
	assertProjectsRoles(t, db, s)
	assertProjectsPolicy(t, db, s)
	assertProjectGroupsAndAudit(t, db, s)

	// Re-applying is a no-op.
	migrateAll(t, db)
}

// TestProjectsMigrationValidateRerun: the no_tx file validates a NOT VALID audit
// constraint when it runs again (a run interrupted between the two files).
func TestProjectsMigrationValidateRerun(t *testing.T) {
	db := scratchDB(t)

	migrateThrough(t, db, projectsPrevious)
	mustExec(t, db, `INSERT INTO audit_logs (action) VALUES ('org.created')`)
	migrateAll(t, db)

	if !mustScan[bool](t, db, `SELECT convalidated FROM pg_constraint WHERE conname = 'chk_audit_action'`) {
		t.Fatal("chk_audit_action not validated after the full run")
	}

	// Un-validate and re-run the validate file: it must validate again.
	mustExec(t, db, `ALTER TABLE audit_logs DROP CONSTRAINT chk_audit_action`)
	mustExec(t, db, `ALTER TABLE audit_logs ADD CONSTRAINT chk_audit_action CHECK (action LIKE '%.%') NOT VALID`)
	mustExec(t, db, `DELETE FROM schema_migrations WHERE version = $1`, projectsValidateMigration)

	migrateAll(t, db)

	if !mustScan[bool](t, db, `SELECT convalidated FROM pg_constraint WHERE conname = 'chk_audit_action'`) {
		t.Fatal("re-running the validate migration did not validate the constraint")
	}
}

func TestProjectsMigrationFreshInstall(t *testing.T) {
	db := scratchDB(t)

	migrateAll(t, db)

	if got := mustScan[string](t, db, `SELECT name || ':' || repo_name_prefix || ':' || max_repositories FROM projects WHERE id = $1`, projectsSystemID); got != "system::50" {
		t.Errorf("system project = %q, want system::50", got)
	}
}
