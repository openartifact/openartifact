// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// serviceAccountRank orders service accounts right after users and before
	// every kind that references an account: groups, projects, permission
	// targets and trust policies.
	serviceAccountRank = 12

	serviceAccountsPath = "/api/v1/service-accounts"

	// serviceAccountMaxDescription is the longest description the API accepts.
	serviceAccountMaxDescription = 255
)

var (
	// serviceAccountNameRe is the name rule of users and service accounts,
	// which share one namespace.
	serviceAccountNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,64}$`)

	errServiceAccountInvalid = errors.New("invalid ServiceAccount")
)

// serviceAccountKind manages service accounts: named non-human principals for
// automation. They cannot sign in; they hold project roles, group memberships
// and permission assignments like users do, and own API tokens that are minted
// through the API or by a trust policy. Unlike a user, deleting one removes the
// account together with its tokens.
type serviceAccountKind struct{}

// NewServiceAccountKind returns the ServiceAccount kind.
func NewServiceAccountKind() Kind { return serviceAccountKind{} }

func (serviceAccountKind) Name() string { return KindServiceAccount }

func (serviceAccountKind) Rank() int { return serviceAccountRank }

func (serviceAccountKind) MetaKeys() []string { return nil }

func (serviceAccountKind) Key(o *Object) string { return o.Name }

func (serviceAccountKind) Order(*Object) int { return 0 }

func (serviceAccountKind) Refs(*Object) []Ref { return nil }

func (serviceAccountKind) Schema() *Field {
	return &Field{Type: TObject, Fields: map[string]*Field{
		"description": {Type: TString},
		"disabled":    {Type: TBool},
	}}
}

// Validate implements Validator.
func (serviceAccountKind) Validate(o *Object) []error {
	var errs []error

	if !serviceAccountNameRe.MatchString(o.Name) {
		errs = append(errs, invalidf(errServiceAccountInvalid, "metadata.name must be 3-64 letters, digits, dash or underscore"))
	}

	description := Str(o.Spec, "description")
	if utf8.RuneCountInString(description) > serviceAccountMaxDescription || strings.ContainsFunc(description, isControlRune) {
		errs = append(errs, invalidf(errServiceAccountInvalid,
			"description must be at most %d characters without control characters", serviceAccountMaxDescription))
	}

	return errs
}

func isControlRune(r rune) bool { return r < ' ' || r == 0x7f }

// serviceAccountResponse is one account of GET /api/v1/service-accounts.
type serviceAccountResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Disabled    bool   `json:"disabled"`
}

func (a *serviceAccountResponse) live() *Live {
	return &Live{
		Kind: KindServiceAccount,
		Key:  a.Name,
		ID:   a.ID,
		Spec: map[string]any{"description": a.Description, "disabled": a.Disabled},
	}
}

// listServiceAccounts reads every service account. A server older than service
// accounts has no such endpoint and therefore none, which keeps manifests that
// only name users working against it: any user reference lists service accounts.
func listServiceAccounts(ctx context.Context, c *Client) ([]serviceAccountResponse, error) {
	accounts, err := listPaged[serviceAccountResponse](ctx, c, serviceAccountsPath)
	if IsStatus(err, http.StatusNotFound) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("list service accounts: %w", err)
	}

	return accounts, nil
}

func (serviceAccountKind) List(ctx context.Context, env *Env) ([]*Live, error) {
	accounts, err := listServiceAccounts(ctx, env.Client)
	if err != nil {
		return nil, err
	}

	out := make([]*Live, len(accounts))
	for i := range accounts {
		out[i] = accounts[i].live()
	}

	return out, nil
}

func (serviceAccountKind) Create(ctx context.Context, env *Env, o *Object) (string, error) {
	body := Pick(o.Spec, "description", "disabled")
	body["name"] = o.Name

	id, err := env.Client.create(ctx, serviceAccountsPath, body, "service account", o.Name)
	if err != nil {
		return "", err
	}

	env.ForgetIdents()

	return id, nil
}

func (serviceAccountKind) Update(ctx context.Context, env *Env, live *Live, _ *Object, changes []Change) (string, error) {
	return env.Client.updateChanges(ctx, http.MethodPatch, serviceAccountsPath+"/"+PathEscape(live.ID), live, changes, "service account")
}

func (serviceAccountKind) Delete(ctx context.Context, env *Env, live *Live) error {
	if err := env.Client.remove(ctx, serviceAccountsPath+"/"+PathEscape(live.ID), "service account", live.Key); err != nil {
		return err
	}

	env.ForgetIdents()

	return nil
}
