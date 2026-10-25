// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
)

//nolint:gochecknoglobals // benchmark sizes
var benchTargetCounts = []int{1, 10, 100}

// benchAuthDB serves the lookups the permission evaluator and token validation
// make on the request path.
type benchAuthDB struct {
	databasetest.BaseMockDB

	targets []*models.PermissionTarget
	repos   map[string]*models.Repository
	tokens  map[string]*models.Token
	users   map[string]*models.User
}

func (d *benchAuthDB) GetPermissionTargetsForPrincipal(context.Context, string, []string) ([]*models.PermissionTarget, error) {
	return d.targets, nil
}

func (d *benchAuthDB) GetPermissionTargetByName(context.Context, string) (*models.PermissionTarget, error) {
	return nil, nil
}

func (d *benchAuthDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return d.repos[name], nil
}

func (d *benchAuthDB) GetTokenByHash(_ context.Context, hash string) (*models.Token, error) {
	return d.tokens[hash], nil
}

func (d *benchAuthDB) GetUserByID(_ context.Context, id string) (*models.User, error) {
	return d.users[id], nil
}

func (d *benchAuthDB) CreateToken(_ context.Context, t *models.Token) (*models.Token, error) {
	return t, nil
}

func benchTargets(n int) []*models.PermissionTarget {
	targets := make([]*models.PermissionTarget, n)

	for i := range targets {
		targets[i] = &models.PermissionTarget{
			ID:               "target-" + strconv.Itoa(i),
			Name:             "team-" + strconv.Itoa(i),
			RepositoryFilter: fmt.Sprintf("team-%d-.*", i),
			PathPattern:      "libs/.*",
			Actions:          []string{"read", "write", "delete"},
		}
	}

	return targets
}

func benchPrincipal() Principal {
	uid := "user-1"

	return Principal{UserID: &uid, Username: "jane", GroupIDs: []string{"g1", "g2", "g3"}, Credential: CredentialSession}
}

// BenchmarkEvaluate measures Evaluator.Evaluate for a user principal with a
// warm regexp cache: "match_last" is the worst allowed case (only the final
// target matches), "no_match" walks every target and denies.
func BenchmarkEvaluate(b *testing.B) {
	principal := benchPrincipal()

	for _, n := range benchTargetCounts {
		for _, tc := range []struct {
			name string
			repo string
			want bool
		}{
			{"match_last", fmt.Sprintf("team-%d-app", n-1), true},
			{"no_match", "other-app", false},
		} {
			db := &benchAuthDB{targets: benchTargets(n)}
			e := NewEvaluator(db, nil, false)

			// Warm the regexp cache.
			if got, _ := e.Evaluate(context.Background(), principal, tc.repo, "libs/x.jar", "read"); got != tc.want {
				b.Fatalf("warmup: got %v, want %v", got, tc.want)
			}

			b.Run(fmt.Sprintf("targets=%d/%s", n, tc.name), func(b *testing.B) {
				b.ReportAllocs()

				ctx := context.Background()

				for b.Loop() {
					if got, err := e.Evaluate(ctx, principal, tc.repo, "libs/x.jar", "read"); err != nil || got != tc.want {
						b.Fatalf("Evaluate = %v, %v", got, err)
					}
				}
			})
		}
	}
}

// BenchmarkEvaluateScopedToken adds the token-scope filtering of
// principalTargets: a token scoped to 3 of the user's targets.
func BenchmarkEvaluateScopedToken(b *testing.B) {
	for _, n := range benchTargetCounts {
		principal := benchPrincipal()
		principal.TokenKind = TokenKindAccess
		principal.TokenScopes = []string{"target-0", "target-" + strconv.Itoa(n/2), "target-" + strconv.Itoa(n-1)}

		db := &benchAuthDB{targets: benchTargets(n)}
		e := NewEvaluator(db, nil, false)
		repo := fmt.Sprintf("team-%d-app", n-1)

		b.Run(fmt.Sprintf("targets=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			ctx := context.Background()

			for b.Loop() {
				if got, err := e.Evaluate(ctx, principal, repo, "libs/x.jar", "read"); err != nil || !got {
					b.Fatalf("Evaluate = %v, %v", got, err)
				}
			}
		})
	}
}

func BenchmarkMatchTarget(b *testing.B) {
	target := benchTargets(1)[0]
	e := NewEvaluator(&benchAuthDB{}, nil, false)

	if ok, _ := e.matchTarget(target, "team-0-app", "libs/x.jar", "read"); !ok {
		b.Fatal("warmup: target should match")
	}

	b.ReportAllocs()

	for b.Loop() {
		_, _ = e.matchTarget(target, "team-0-app", "libs/x.jar", "read")
	}
}

// BenchmarkCompilePattern is the cost the regexp cache avoids on every request.
func BenchmarkCompilePattern(b *testing.B) {
	b.ReportAllocs()

	for b.Loop() {
		if _, err := CompilePattern("team-12-.*"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEvaluateAnonymousPublicRepo(b *testing.B) {
	db := &benchAuthDB{repos: map[string]*models.Repository{"pub": {Name: "pub", Public: true}}}
	e := NewEvaluator(db, nil, false)
	anon := Principal{IsAnonymous: true}

	b.ReportAllocs()

	ctx := context.Background()

	for b.Loop() {
		if ok, err := e.Evaluate(ctx, anon, "pub", "x", "read"); err != nil || !ok {
			b.Fatalf("Evaluate = %v, %v", ok, err)
		}
	}
}

// BenchmarkRepoReadFilter builds the per-request filter and applies it to
// repos repositories, the way the repository list handler does.
func BenchmarkRepoReadFilter(b *testing.B) {
	principal := benchPrincipal()

	for _, n := range []int{10, 100} {
		for _, repos := range []int{100, 1000, 10000} {
			db := &benchAuthDB{targets: benchTargets(n)}
			e := NewEvaluator(db, nil, false)

			names := make([]string, repos)
			for i := range names {
				names[i] = fmt.Sprintf("team-%d-app", i%(n+5))
			}

			b.Run(fmt.Sprintf("targets=%d/repos=%d", n, repos), func(b *testing.B) {
				b.ReportAllocs()

				ctx := context.Background()

				for b.Loop() {
					canRead, err := e.RepoReadFilter(ctx, principal)
					if err != nil {
						b.Fatal(err)
					}

					for _, name := range names {
						_, _ = canRead(&models.Repository{Name: name})
					}
				}
			})
		}
	}
}

func benchTokenDB(b *testing.B) (*benchAuthDB, string) {
	b.Helper()

	db := &benchAuthDB{
		tokens: map[string]*models.Token{},
		users:  map[string]*models.User{"user-1": {ID: "user-1", Username: "jane"}},
	}

	raw, tok, err := CreateToken(context.Background(), db, TokenKindAccess, "ci", "user-1", nil, nil)
	if err != nil {
		b.Fatal(err)
	}

	db.tokens[tok.TokenHash] = tok

	return db, raw
}

// BenchmarkValidateToken is the per-request bearer/basic token check (SHA-256
// of the body, token lookup, user lookup).
func BenchmarkValidateToken(b *testing.B) {
	db, raw := benchTokenDB(b)

	b.ReportAllocs()

	ctx := context.Background()

	for b.Loop() {
		if tok, user, err := ValidateToken(ctx, db, raw); err != nil || tok == nil || user == nil {
			b.Fatalf("ValidateToken = %v, %v, %v", tok, user, err)
		}
	}
}

func BenchmarkCreateToken(b *testing.B) {
	db := &benchAuthDB{}

	b.ReportAllocs()

	ctx := context.Background()

	for b.Loop() {
		if _, _, err := CreateToken(ctx, db, TokenKindAccess, "ci", "user-1", nil, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTouchTokenThrottled(b *testing.B) {
	db := &benchAuthDB{}
	now := time.Now()
	tok := &models.Token{ID: "t1", LastUsedAt: &now}

	b.ReportAllocs()

	ctx := context.Background()

	for b.Loop() {
		TouchToken(ctx, db, tok)
	}
}

func BenchmarkValidateSession(b *testing.B) {
	db := &benchSessionDB{sessions: map[string]*models.Session{}}

	cookie, sess, err := CreateSession(context.Background(), db, "user-1", "203.0.113.5:4711", "bench")
	if err != nil {
		b.Fatal(err)
	}

	db.sessions[sess.TokenHash] = sess

	b.ReportAllocs()

	ctx := context.Background()

	for b.Loop() {
		if s, err := ValidateSession(ctx, db, cookie); err != nil || s == nil {
			b.Fatalf("ValidateSession = %v, %v", s, err)
		}
	}
}

type benchSessionDB struct {
	databasetest.BaseMockDB

	sessions map[string]*models.Session
}

func (d *benchSessionDB) CreateSession(_ context.Context, s *models.Session) (*models.Session, error) {
	return s, nil
}

func (d *benchSessionDB) GetSessionByTokenHash(_ context.Context, hash string) (*models.Session, error) {
	return d.sessions[hash], nil
}

// BenchmarkVerifyPassword is the Argon2id login cost (intentionally slow;
// reported so login throughput can be reasoned about).
func BenchmarkVerifyPassword(b *testing.B) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()

	for b.Loop() {
		if ok, err := VerifyPassword("correct horse battery staple", hash); err != nil || !ok {
			b.Fatalf("VerifyPassword = %v, %v", ok, err)
		}
	}
}
