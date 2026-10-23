// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package declarative

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/openartifact/openartifact/internal/repotype"
)

const (
	repoPageSize = 100
	// repoFanout bounds the concurrent per-repository sub-resource fetches.
	repoFanout = 8

	repoFieldQuota    = "quota"
	repoFieldCleanup  = "cleanup_policies"
	repoTypeHosted    = "hosted"
	repoTypeProxy     = "proxy"
	repoTypeGroup     = "group"
	repoMaxMembers    = 200
	repoMaxCacheTTL   = 30 * 24 * 3600
	repoMaxNegTTL     = 24 * 3600
	repoMaxStale      = 365 * 24 * 3600
	repoMaxTrashDays  = 36500
	repoMaxWarnPct    = 100
	repoPathPrefix    = "/api/v1/repositories/"
	repoNamePatternRe = `^[a-zA-Z0-9_-]{1,100}$`
)

var (
	errRepositoryInvalid = errors.New("invalid Repository")

	repoNameRe = regexp.MustCompile(repoNamePatternRe)
)

// repoProxyOnly are the spec fields that only exist on proxy repositories.
//
//nolint:gochecknoglobals // immutable lookup table
var repoProxyOnly = []string{
	"upstream_url", "upstream_username", "upstream_password",
	"cache_ttl_seconds", "negative_cache_ttl_seconds", "max_stale_seconds",
}

// repoFormats mirrors the server's chk_repo_format constraint.
func repoFormats() []string {
	return []string{
		"cargo", "conan", "conda", "deb", "docker", "go", "helm", "ivy", "maven",
		"npm", "nuget", "pypi", "raw", "rpm", "rubygems", "sbt",
	}
}

// repositoryKind implements the Repository manifest kind.
type repositoryKind struct{}

// NewRepositoryKind returns the Repository kind. Its natural key is the
// repository name (metadata.name); the owning project is metadata.project.
func NewRepositoryKind() Kind { return repositoryKind{} }

func (repositoryKind) Name() string { return KindRepository }

func (repositoryKind) Rank() int { return 40 }

func (repositoryKind) MetaKeys() []string { return nil }

func (repositoryKind) Key(o *Object) string { return o.Name }

// Order puts group repositories after the hosted and proxy repositories they
// list as members.
func (repositoryKind) Order(o *Object) int {
	if Str(o.Spec, "repo_type") == repoTypeGroup {
		return 1
	}

	return 0
}

func (repositoryKind) Refs(o *Object) []Ref {
	var refs []Ref

	if p := Str(o.Spec, "project"); p != "" {
		refs = append(refs, Ref{Kind: KindProject, Key: p, Field: "project"})
	}

	for i, m := range StrList(o.Spec, "member_repos") {
		refs = append(refs, Ref{Kind: KindRepository, Key: m, Field: fmt.Sprintf("member_repos[%d]", i)})
	}

	return refs
}

func repoStrField() *Field { return &Field{Type: TString} }

func repoBoolField() *Field { return &Field{Type: TBool} }

func repoIntField(minimum float64) *Field {
	return &Field{Type: TInt, Min: minimum, HasMin: true}
}

func (repositoryKind) Schema() *Field {
	return &Field{Type: TObject, Fields: map[string]*Field{
		"project":                    {Type: TString, FromMeta: true},
		"format":                     {Type: TString, Required: true, Immutable: true, Enum: repoFormats()},
		"repo_type":                  {Type: TString, Immutable: true, Enum: []string{repoTypeHosted, repoTypeProxy, repoTypeGroup}},
		"description":                repoStrField(),
		"public":                     repoBoolField(),
		"upstream_url":               repoStrField(),
		"upstream_username":          repoStrField(),
		"upstream_password":          {Type: TString, Secret: true},
		"cache_ttl_seconds":          repoIntField(1),
		"negative_cache_ttl_seconds": repoIntField(1),
		"max_stale_seconds":          repoIntField(0),
		"member_repos":               {Type: TList, Elem: repoStrField()},
		"presign_downloads":          {Type: TBool, Nullable: true},
		"trash_retention_days":       {Type: TInt, Nullable: true, Min: 0, HasMin: true},
		"immutable_releases":         repoBoolField(),
		"merge_proxy_versions":       repoBoolField(),
		"sign_packages":              repoBoolField(),
		repoFieldQuota: {Type: TObject, Fields: map[string]*Field{
			"max_size_bytes":         repoIntField(0),
			"max_artifact_count":     repoIntField(0),
			"warn_threshold_percent": repoIntField(0),
		}},
		repoFieldCleanup: {Type: TSet, KeyBy: "name", Elem: &Field{Type: TObject, Fields: map[string]*Field{
			"name":            {Type: TString, Required: true},
			"enabled":         repoBoolField(),
			"keep_last_n":     repoIntField(1),
			"keep_since_days": repoIntField(1),
			"name_pattern":    repoStrField(),
			"schedule_cron":   repoStrField(),
		}}},
	}}
}

// Validate mirrors the server rules a schema cannot express: fields that only
// exist for one repository type, value ranges the server would clamp (which
// would show up as a permanent diff) and group member sanity.
func (repositoryKind) Validate(o *Object) []error {
	var errs []error

	bad := func(format string, args ...any) {
		errs = append(errs, invalidf(errRepositoryInvalid, format, args...))
	}

	if !repoNameRe.MatchString(o.Name) {
		bad("metadata.name %q must match %s", o.Name, repoNamePatternRe)
	}

	switch strings.ToLower(o.Name) {
	case "new", "settings", "trash", "raw", "maven", "sbt", "ivy":
		bad("metadata.name %q is reserved", o.Name)
	}

	repoType := Str(o.Spec, "repo_type")
	if repoType == "" {
		repoType = repoTypeHosted
	}

	format := Str(o.Spec, "format")

	errs = append(errs, repoValidateType(o, repoType, format)...)
	errs = append(errs, repoValidateRanges(o)...)
	errs = append(errs, repoValidateMembers(o)...)
	errs = append(errs, repoValidateCleanup(o)...)

	return errs
}

func repoValidateType(o *Object, repoType, format string) []error {
	var errs []error

	add := func(format string, args ...any) {
		errs = append(errs, invalidf(errRepositoryInvalid, format, args...))
	}

	if repoType != repoTypeProxy {
		for _, k := range repoProxyOnly {
			if _, ok := o.Spec[k]; ok {
				add("%s is only valid for proxy repositories", k)
			}
		}
	}

	if repoType != repoTypeGroup {
		for _, k := range []string{"member_repos", "merge_proxy_versions"} {
			if _, ok := o.Spec[k]; ok {
				add("%s is only valid for group repositories", k)
			}
		}
	}

	if _, ok := o.Spec["sign_packages"]; ok && (repoType != repoTypeHosted || format != "rpm") {
		add("sign_packages is only valid for hosted rpm repositories")
	}

	switch {
	case repoType == repoTypeProxy && format != "" && !repotype.ProxySupported(format):
		add("proxy repositories are not supported for format %q (supported: %s)", format, repotype.ProxyFormats())
	case repoType == repoTypeGroup && format != "" && !repotype.GroupSupported(format):
		add("group repositories are not supported for format %q (supported: %s)", format, repotype.GroupFormats())
	}

	if raw := Str(o.Spec, "upstream_url"); raw != "" && !plainBaseURL(raw) {
		add("upstream_url must be a plain http(s) base URL without credentials, query or fragment")
	}

	return errs
}

// plainBaseURL reports whether raw is an http(s) URL with a host and no
// credentials, query or fragment.
func plainBaseURL(raw string) bool {
	u, err := url.Parse(raw)

	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" &&
		u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

// repoRange is an inclusive bound the server enforces by clamping or rejecting.
type repoRange struct {
	field    string
	min, max float64
}

func repoValidateRanges(o *Object) []error {
	var errs []error

	ranges := []repoRange{
		{"cache_ttl_seconds", 1, repoMaxCacheTTL},
		{"negative_cache_ttl_seconds", 1, repoMaxNegTTL},
		{"max_stale_seconds", 0, repoMaxStale},
		{"trash_retention_days", 0, repoMaxTrashDays},
	}

	for _, r := range ranges {
		if v, ok := o.Spec[r.field].(float64); ok && (v < r.min || v > r.max) {
			errs = append(errs, fmt.Errorf("%w: %s must be between %d and %d", errRepositoryInvalid, r.field, int64(r.min), int64(r.max)))
		}
	}

	if q, ok := o.Spec[repoFieldQuota].(map[string]any); ok {
		if v, ok := q["warn_threshold_percent"].(float64); ok && v > repoMaxWarnPct {
			errs = append(errs, fmt.Errorf("%w: quota.warn_threshold_percent must be between 0 and %d", errRepositoryInvalid, repoMaxWarnPct))
		}
	}

	return errs
}

func repoValidateMembers(o *Object) []error {
	members := StrList(o.Spec, "member_repos")

	var errs []error

	if len(members) > repoMaxMembers {
		errs = append(errs, fmt.Errorf("%w: a group may have at most %d members", errRepositoryInvalid, repoMaxMembers))
	}

	seen := make(map[string]bool, len(members))

	for _, m := range members {
		switch {
		case m == o.Name:
			errs = append(errs, fmt.Errorf("%w: a group cannot contain itself", errRepositoryInvalid))
		case seen[m]:
			errs = append(errs, fmt.Errorf("%w: duplicate member repository %q", errRepositoryInvalid, m))
		}

		seen[m] = true
	}

	return errs
}

func repoValidateCleanup(o *Object) []error {
	var errs []error

	list, _ := o.Spec[repoFieldCleanup].([]any)

	for i, e := range list {
		p, _ := e.(map[string]any)
		name := Str(p, "name")

		if strings.TrimSpace(name) == "" {
			errs = append(errs, fmt.Errorf("%w: %s[%d].name must not be blank", errRepositoryInvalid, repoFieldCleanup, i))
		}

		if pat := Str(p, "name_pattern"); pat != "" {
			if _, err := regexp.Compile(pat); err != nil {
				errs = append(errs, fmt.Errorf("%w: %s[%d].name_pattern is not a valid regular expression: %w", errRepositoryInvalid, repoFieldCleanup, i, err))
			}
		}
	}

	return errs
}

// repoAPI is the repository object of GET /api/v1/repositories.
type repoAPI struct {
	ID                      string  `json:"id"`
	Name                    string  `json:"name"`
	Format                  string  `json:"format"`
	Description             *string `json:"description"`
	Public                  bool    `json:"public"`
	RepoType                string  `json:"repo_type"`
	Project                 string  `json:"project"`
	PresignDownloads        *bool   `json:"presign_downloads"`
	TrashRetentionDays      *int    `json:"trash_retention_days"`
	ImmutableReleases       bool    `json:"immutable_releases"`
	MergeProxyVersions      bool    `json:"merge_proxy_versions"`
	SignPackages            bool    `json:"sign_packages"`
	UpstreamURL             string  `json:"upstream_url"`
	UpstreamUsername        string  `json:"upstream_username"`
	CacheTTLSeconds         *int    `json:"cache_ttl_seconds"`
	NegativeCacheTTLSeconds *int    `json:"negative_cache_ttl_seconds"`
	MaxStaleSeconds         *int    `json:"max_stale_seconds"`
}

// repoNum converts an optional API integer into its canonical form (nil stays
// nil).
func repoNum(v *int) any {
	if v == nil {
		return nil
	}

	return float64(*v)
}

// spec renders the configuration fields that exist for the repository's type.
// Fields of other types are left out so exports validate and apply cleanly.
func (r *repoAPI) spec() map[string]any {
	repoType := r.RepoType
	if repoType == "" {
		repoType = repoTypeHosted
	}

	s := map[string]any{
		"format":               r.Format,
		"repo_type":            repoType,
		"description":          "",
		"public":               r.Public,
		"presign_downloads":    nil,
		"trash_retention_days": repoNum(r.TrashRetentionDays),
		"immutable_releases":   r.ImmutableReleases,
	}

	if r.Description != nil {
		s["description"] = *r.Description
	}

	if r.PresignDownloads != nil {
		s["presign_downloads"] = *r.PresignDownloads
	}

	if r.Project != "" {
		s["project"] = r.Project
	}

	switch repoType {
	case repoTypeProxy:
		s["upstream_url"] = r.UpstreamURL
		s["upstream_username"] = r.UpstreamUsername
		s["cache_ttl_seconds"] = repoNum(r.CacheTTLSeconds)
		s["negative_cache_ttl_seconds"] = repoNum(r.NegativeCacheTTLSeconds)
		s["max_stale_seconds"] = repoNum(r.MaxStaleSeconds)
	case repoTypeGroup:
		s["merge_proxy_versions"] = r.MergeProxyVersions
	case repoTypeHosted:
		if r.Format == "rpm" {
			s["sign_packages"] = r.SignPackages
		}
	}

	return s
}

// repoList pages through GET /api/v1/repositories.
func repoList(ctx context.Context, c *Client) ([]repoAPI, error) {
	var out []repoAPI

	for page := 1; ; page++ {
		var resp struct {
			Total        int       `json:"total"`
			Repositories []repoAPI `json:"repositories"`
		}

		q := url.Values{"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(repoPageSize)}}
		if err := c.Get(ctx, "/api/v1/repositories?"+q.Encode(), &resp); err != nil {
			return nil, fmt.Errorf("list repositories: %w", err)
		}

		out = append(out, resp.Repositories...)

		if len(resp.Repositories) < repoPageSize || len(out) >= resp.Total {
			return out, nil
		}
	}
}

func repoURL(name string, parts ...string) string {
	return repoPathPrefix + PathEscape(name) + strings.Join(append([]string{""}, parts...), "/")
}

// repoMembers returns the member repository names of a group in priority order.
func repoMembers(ctx context.Context, c *Client, name string) ([]any, error) {
	var resp struct {
		Members []struct {
			Name     string `json:"name"`
			Priority int    `json:"priority"`
		} `json:"members"`
	}

	if err := c.Get(ctx, repoURL(name, "members"), &resp); err != nil {
		return nil, fmt.Errorf("list members of repository %q: %w", name, err)
	}

	sort.SliceStable(resp.Members, func(i, j int) bool { return resp.Members[i].Priority < resp.Members[j].Priority })

	out := make([]any, 0, len(resp.Members))
	for _, m := range resp.Members {
		out = append(out, m.Name)
	}

	return out, nil
}

// repoQuota returns the quota limits of a repository. A repository without a
// quota reads as all zeros.
func repoQuota(ctx context.Context, c *Client, name string) (map[string]any, error) {
	var q struct {
		MaxSizeBytes         int64 `json:"max_size_bytes"`
		MaxArtifactCount     int   `json:"max_artifact_count"`
		WarnThresholdPercent int   `json:"warn_threshold_percent"`
	}

	if err := c.Get(ctx, repoURL(name, "quota"), &q); err != nil {
		return nil, fmt.Errorf("get quota of repository %q: %w", name, err)
	}

	return map[string]any{
		"max_size_bytes":         float64(q.MaxSizeBytes),
		"max_artifact_count":     float64(q.MaxArtifactCount),
		"warn_threshold_percent": float64(q.WarnThresholdPercent),
	}, nil
}

// repoPolicy is a cleanup policy of GET .../cleanup-policies.
type repoPolicy struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Enabled       bool    `json:"enabled"`
	KeepLastN     *int    `json:"keep_last_n"`
	KeepSinceDays *int    `json:"keep_since_days"`
	NamePattern   *string `json:"name_pattern"`
	ScheduleCron  *string `json:"schedule_cron"`
}

// elem renders the policy in manifest shape. The server cannot report
// keep_downloaded_since_days as set (it rejects it), so it is not part of the
// schema.
func (p *repoPolicy) elem() map[string]any {
	e := map[string]any{
		"name":          p.Name,
		"enabled":       p.Enabled,
		"name_pattern":  "",
		"schedule_cron": "",
	}

	if p.KeepLastN != nil {
		e["keep_last_n"] = float64(*p.KeepLastN)
	}

	if p.KeepSinceDays != nil {
		e["keep_since_days"] = float64(*p.KeepSinceDays)
	}

	if p.NamePattern != nil {
		e["name_pattern"] = *p.NamePattern
	}

	if p.ScheduleCron != nil {
		e["schedule_cron"] = *p.ScheduleCron
	}

	return e
}

func repoPolicies(ctx context.Context, c *Client, name string) ([]repoPolicy, error) {
	var resp struct {
		Policies []repoPolicy `json:"policies"`
	}

	if err := c.Get(ctx, repoURL(name, "cleanup-policies"), &resp); err != nil {
		return nil, fmt.Errorf("list cleanup policies of repository %q: %w", name, err)
	}

	return resp.Policies, nil
}

func (repositoryKind) List(ctx context.Context, env *Env) ([]*Live, error) {
	repos, err := repoList(ctx, env.Client)
	if err != nil {
		return nil, err
	}

	out := make([]*Live, len(repos))

	var g errgroup.Group

	g.SetLimit(repoFanout)

	for i := range repos {
		g.Go(func() error {
			live, err := repoLive(ctx, env.Client, &repos[i])
			out[i] = live

			return err
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return out, nil
}

// repoLive builds the live object of r, fetching the sub-resources the list
// answer does not carry: group members, quota and cleanup policies.
func repoLive(ctx context.Context, c *Client, r *repoAPI) (*Live, error) {
	spec := r.spec()

	if spec["repo_type"] == repoTypeGroup {
		members, err := repoMembers(ctx, c, r.Name)
		if err != nil {
			return nil, err
		}

		spec["member_repos"] = members
	}

	quota, err := repoQuota(ctx, c, r.Name)
	if err != nil {
		return nil, err
	}

	spec[repoFieldQuota] = quota

	policies, err := repoPolicies(ctx, c, r.Name)
	if err != nil {
		return nil, err
	}

	elems := make([]any, 0, len(policies))
	for i := range policies {
		elems = append(elems, policies[i].elem())
	}

	spec[repoFieldCleanup] = elems

	return &Live{Kind: KindRepository, Key: r.Name, ID: r.ID, Spec: spec, Secrets: map[string]bool{}}, nil
}

// Create posts the repository and then reconciles its quota and cleanup
// policies. When a follow-up step fails the repository id is still returned
// with the error: the repository exists and a later apply completes it.
func (repositoryKind) Create(ctx context.Context, env *Env, o *Object) (string, error) {
	body := Omit(o.Spec, repoFieldQuota, repoFieldCleanup)
	body["name"] = o.Name

	var resp struct {
		ID string `json:"id"`
	}

	if err := env.Client.Do(ctx, http.MethodPost, "/api/v1/repositories", body, &resp); err != nil {
		return "", fmt.Errorf("create repository %q: %w", o.Name, err)
	}

	if q, ok := o.Spec[repoFieldQuota].(map[string]any); ok && len(q) > 0 {
		if err := repoPutQuota(ctx, env.Client, o.Name, q); err != nil {
			return resp.ID, err
		}
	}

	if want, ok := o.Spec[repoFieldCleanup].([]any); ok {
		if err := repoReconcilePolicies(ctx, env.Client, o.Name, want); err != nil {
			return resp.ID, err
		}
	}

	return resp.ID, nil
}

// Update PATCHes the changed scalar fields (member_repos and project included)
// and reconciles the quota and cleanup policies when they changed.
func (repositoryKind) Update(ctx context.Context, env *Env, live *Live, o *Object, changes []Change) (string, error) {
	var (
		scalar []Change
		quota  []Change
	)

	top := ChangedTop(changes)

	for _, c := range changes {
		switch TopField(c.Path) {
		case repoFieldQuota:
			quota = append(quota, Change{Path: strings.TrimPrefix(c.Path, repoFieldQuota+"."), New: c.New})
		case repoFieldCleanup:
		default:
			scalar = append(scalar, c)
		}
	}

	if err := repoCheckCredentials(live, o, scalar); err != nil {
		return live.ID, err
	}

	if len(scalar) > 0 {
		if err := env.Client.Do(ctx, http.MethodPatch, "/api/v1/repository/"+PathEscape(live.Key), PatchBody(scalar), nil); err != nil {
			return live.ID, fmt.Errorf("update repository %q: %w", live.Key, err)
		}
	}

	if top[repoFieldQuota] {
		if err := repoPutQuota(ctx, env.Client, live.Key, PatchBody(quota)); err != nil {
			return live.ID, err
		}
	}

	if want, ok := o.Spec[repoFieldCleanup].([]any); ok && top[repoFieldCleanup] {
		if err := repoReconcilePolicies(ctx, env.Client, live.Key, want); err != nil {
			return live.ID, err
		}
	}

	return live.ID, nil
}

// repoCheckCredentials refuses an update the server would answer by silently
// dropping the stored upstream password: moving a proxy to another origin or
// username invalidates it, and the manifest cannot say it is unchanged.
func repoCheckCredentials(live *Live, o *Object, scalar []Change) error {
	if _, managed := o.Spec["upstream_password"]; !managed {
		return nil
	}

	retarget := false

	for _, c := range scalar {
		switch c.Path {
		case "upstream_password":
			return nil
		case "upstream_username":
			retarget = true
		case "upstream_url":
			if !repoSameOrigin(Str(live.Spec, "upstream_url"), fmt.Sprint(c.New)) {
				retarget = true
			}
		}
	}

	if retarget {
		return fmt.Errorf("update repository %q: changing upstream_url origin or upstream_username makes the server drop the stored upstream_password; re-run with --force-secrets to send it again: %w", live.Key, errRepositoryInvalid)
	}

	return nil
}

// repoSameOrigin mirrors the server's origin rule for upstream credentials.
func repoSameOrigin(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)

	if errA != nil || errB != nil {
		return false
	}

	return strings.EqualFold(ua.Scheme, ub.Scheme) &&
		strings.EqualFold(ua.Hostname(), ub.Hostname()) &&
		repoPort(ua) == repoPort(ub)
}

func repoPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}

	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}

	return "80"
}

func repoPutQuota(ctx context.Context, c *Client, name string, body map[string]any) error {
	if len(body) == 0 {
		return nil
	}

	if err := c.Do(ctx, http.MethodPut, repoURL(name, "quota"), body, nil); err != nil {
		return fmt.Errorf("set quota of repository %q: %w", name, err)
	}

	return nil
}

// repoReconcilePolicies makes the repository's cleanup policies match want by
// name: missing ones are created, differing ones updated (only the differing
// fields), unlisted ones deleted.
func repoReconcilePolicies(ctx context.Context, c *Client, name string, want []any) error {
	current, err := repoPolicies(ctx, c, name)
	if err != nil {
		return err
	}

	byName := make(map[string]*repoPolicy, len(current))
	for i := range current {
		byName[current[i].Name] = &current[i]
	}

	wanted := make(map[string]bool, len(want))

	for _, e := range want {
		w, _ := e.(map[string]any)
		pname := Str(w, "name")
		wanted[pname] = true

		p, exists := byName[pname]
		if !exists {
			if err := c.Do(ctx, http.MethodPost, repoURL(name, "cleanup-policies"), w, nil); err != nil {
				return fmt.Errorf("create cleanup policy %q of repository %q: %w", pname, name, err)
			}

			continue
		}

		patch := repoPolicyPatch(w, p.elem())
		if len(patch) == 0 {
			continue
		}

		if err := c.Do(ctx, http.MethodPut, repoURL(name, "cleanup-policies", PathEscape(p.ID)), patch, nil); err != nil {
			return fmt.Errorf("update cleanup policy %q of repository %q: %w", pname, name, err)
		}
	}

	for i := range current {
		p := &current[i]
		if wanted[p.Name] {
			continue
		}

		if err := c.Do(ctx, http.MethodDelete, repoURL(name, "cleanup-policies", PathEscape(p.ID)), nil, nil); err != nil {
			return fmt.Errorf("delete cleanup policy %q of repository %q: %w", p.Name, name, err)
		}
	}

	return nil
}

// repoPolicyPatch returns the desired fields (besides the name) that differ
// from the current policy.
func repoPolicyPatch(want, current map[string]any) map[string]any {
	patch := map[string]any{}

	for k, v := range want {
		if k == "name" {
			continue
		}

		if cur, ok := current[k]; !ok || !reflect.DeepEqual(cur, v) {
			patch[k] = v
		}
	}

	return patch
}

// Delete soft-deletes the repository (it lands in the trash unless its
// retention is 0); the server requires the name as confirmation.
func (repositoryKind) Delete(ctx context.Context, env *Env, live *Live) error {
	path := "/api/v1/repository/" + PathEscape(live.Key) + "?confirm=" + url.QueryEscape(live.Key)

	if err := env.Client.Do(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return fmt.Errorf("delete repository %q: %w", live.Key, err)
	}

	return nil
}
