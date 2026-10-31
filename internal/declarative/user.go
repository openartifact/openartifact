// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"fmt"
	"net/http"
)

const (
	// userRank orders users before every kind that references them.
	userRank = 10

	// bootstrapUsername is the local admin account the server creates on first
	// start (auth.BootstrapUsername); it is never pruned.
	bootstrapUsername = "admin"

	usersPath = "/api/v1/users"
)

// userKind manages local user accounts without a password: such accounts
// cannot log in interactively, API tokens for them are minted through the API.
// Deleting a user through the API is a soft delete (the account is disabled and
// its sessions are revoked); it stays listed with disabled: true.
type userKind struct{}

// NewUserKind returns the User kind.
func NewUserKind() Kind { return userKind{} }

func (userKind) Name() string { return KindUser }

func (userKind) Rank() int { return userRank }

func (userKind) MetaKeys() []string { return nil }

// Prunable skips users that are already disabled: deleting a user only
// disables it, so it would stay a pending deletion forever.
func (userKind) Prunable(live *Live) bool { return !Bool(live.Spec, "disabled") }

func (userKind) Key(o *Object) string { return o.Name }

func (userKind) Order(*Object) int { return 0 }

func (userKind) Refs(*Object) []Ref { return nil }

func (userKind) Schema() *Field {
	return &Field{Type: TObject, Fields: map[string]*Field{
		"email":                {Type: TString},
		"full_name":            {Type: TString},
		"disabled":             {Type: TBool},
		"must_change_password": {Type: TBool},
	}}
}

// userResponse is one account of GET /api/v1/users.
type userResponse struct {
	ID                 string  `json:"id"`
	Username           string  `json:"username"`
	Email              *string `json:"email"`
	FullName           *string `json:"full_name"`
	Disabled           bool    `json:"disabled"`
	MustChangePassword bool    `json:"must_change_password"`
}

func (u *userResponse) live() *Live {
	return &Live{
		Kind: KindUser,
		Key:  u.Username,
		ID:   u.ID,
		Spec: map[string]any{
			"email":                nullStr(u.Email),
			"full_name":            nullStr(u.FullName),
			"disabled":             u.Disabled,
			"must_change_password": u.MustChangePassword,
		},
		Seeded: u.Username == bootstrapUsername,
	}
}

// nullStr maps the API's null strings to the empty string, which the server
// treats the same way.
func nullStr(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

func (userKind) List(ctx context.Context, env *Env) ([]*Live, error) {
	users, err := listPaged[userResponse](ctx, env.Client, usersPath)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}

	out := make([]*Live, len(users))
	for i := range users {
		out[i] = users[i].live()
	}

	return out, nil
}

// userRef is a reference to the account name, which is a User or a
// ServiceAccount: both resolve to the same principal on the server.
func userRef(name, field string) Ref {
	return Ref{Kind: KindUser, Also: []string{KindServiceAccount}, Key: name, Field: field}
}

func (userKind) Create(ctx context.Context, env *Env, o *Object) (string, error) {
	// The API stores an empty email as a value and the column is unique, so an
	// unset email is left out and stays NULL.
	body := Pick(o.Spec, "email", "full_name", "disabled", "must_change_password")
	if Str(body, "email") == "" {
		delete(body, "email")
	}

	body["username"] = o.Name

	id, err := env.Client.create(ctx, usersPath, body, "user", o.Name)
	if err != nil {
		return "", err
	}

	env.ForgetIdents()

	return id, nil
}

func (userKind) Update(ctx context.Context, env *Env, live *Live, o *Object, changes []Change) (string, error) {
	path := usersPath + "/" + PathEscape(live.ID)
	if err := env.Client.Do(ctx, http.MethodPatch, path, PatchBody(changes), nil); err != nil {
		return "", fmt.Errorf("update user %q: %w", o.Name, err)
	}

	return live.ID, nil
}

func (userKind) Delete(ctx context.Context, env *Env, live *Live) error {
	return env.Client.remove(ctx, usersPath+"/"+PathEscape(live.ID), "user", live.Key)
}
