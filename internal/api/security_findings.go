// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/base64"
	"errors"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/ai"
	"github.com/openartifact/openartifact/internal/api/responses"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/exploit"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/vuln"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	securityFindingsDefaultLimit = 50
	securityFindingsMaxLimit     = 200
	// securityQueryMaxLen bounds the free-text search.
	securityQueryMaxLen = 255
	// findingPriorityCursorTag marks a cursor of the priority order.
	findingPriorityCursorTag = "p"
)

// SecurityHandler serves the cross-type security findings (vulnerabilities,
// licenses, secrets, AI analysis) of every repository, the admin security health
// and the counts-only security summary of one repository
// (docs/security/findings.md).
type SecurityHandler struct {
	db        database.Database
	log       *logger.Logger
	evaluator *auth.Evaluator
	scanners  *vuln.Registry
	generator *sbom.Generator
	// exploit supplies the EPSS threshold of the priority rank.
	exploit *exploit.Enricher
	// envAI are the AI settings of the process environment, under the stored ones.
	envAI ai.Settings
}

// NewSecurityHandler builds the handler. scanners reports the scanner readiness
// and generator builds the VEX export; envAI are the env/YAML AI defaults.
func NewSecurityHandler(db database.Database, log *logger.Logger, evaluator *auth.Evaluator,
	scanners *vuln.Registry, generator *sbom.Generator, envAI ai.Settings,
) *SecurityHandler {
	return &SecurityHandler{
		db: db, log: log, evaluator: evaluator, scanners: scanners, generator: generator, envAI: envAI,
		exploit: exploit.NewEnricher(nil, exploit.DefaultEPSSThreshold),
	}
}

// WithExploit sets the enricher whose EPSS threshold derives the finding priority.
func (h *SecurityHandler) WithExploit(e *exploit.Enricher) *SecurityHandler {
	h.exploit = e

	return h
}

// RegisterRoutes registers the security routes on the authenticated router.
// Findings are for global admins and project maintainers or owners (each sees
// their projects' repositories); health is administrative; the repository
// summary needs read access to the repository.
func (h *SecurityHandler) RegisterRoutes(authedRouter chi.Router) {
	authedRouter.Get("/api/v1/security/findings", h.handleList)
	authedRouter.Get("/api/v1/security/findings/summary", h.handleSummary)
	authedRouter.Get("/api/v1/security/findings/export", h.handleExport)
	authedRouter.Get("/api/v1/security/health", h.handleHealth)
	authedRouter.Get("/api/v1/repositories/{name}/security/summary", h.handleRepoSummary)
}

// findingScope returns the repositories the caller may see: all for a global
// admin, else those of the projects they maintain or own. Everyone else
// (readers, anonymous callers, scoped and deploy tokens) gets 403 and false.
func (h *SecurityHandler) findingScope(w http.ResponseWriter, r *http.Request) (models.FindingScope, bool) {
	ctx := r.Context()
	p := currentPrincipal(r)

	// A project token's service account is a repository credential: like deploy
	// tokens, it gets no list endpoint (ProjectRoles refuses deploy tokens itself).
	if p == nil || p.IsAnonymous || p.ServiceProjectID != "" {
		writeError(w, http.StatusForbidden, "forbidden", "maintainer access required")
		return models.FindingScope{}, false
	}

	isAdmin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")
		return models.FindingScope{}, false
	}

	if isAdmin {
		return models.FindingScope{All: true}, true
	}

	roles, err := h.evaluator.ProjectRoles(ctx, *p)
	if err != nil {
		h.log.Errorw("security findings: project roles", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")

		return models.FindingScope{}, false
	}

	minRank := models.ProjectRoleRank(models.ProjectRoleMaintainer)
	ids := make([]string, 0, len(roles))

	for id, role := range roles {
		if models.ProjectRoleRank(role) >= minRank {
			ids = append(ids, id)
		}
	}

	if len(ids) == 0 {
		writeError(w, http.StatusForbidden, "forbidden", "maintainer access required")
		return models.FindingScope{}, false
	}

	slices.Sort(ids)

	return models.FindingScope{ProjectIDs: ids}, true
}

// securityFilter reads ?repo and ?project; it returns the message of the first
// invalid value, or "".
func securityFilter(r *http.Request, scope models.FindingScope) (models.FindingFilter, string) {
	q := r.URL.Query()
	f := models.FindingFilter{Scope: scope, Repo: q.Get("repo"), Project: q.Get("project")}

	if len(f.Repo) > maxNameLen {
		return f, "repo is too long"
	}

	if len(f.Project) > maxNameLen {
		return f, "project is too long"
	}

	return f, ""
}

// securityTypes parses ?type, a comma-separated subset of vuln, license, secret
// and ai (default all of them), in canonical order without duplicates.
func securityTypes(raw string) ([]string, bool) {
	if raw == "" {
		return models.FindingTypes(), true
	}

	want := map[string]bool{}

	for part := range strings.SplitSeq(raw, ",") {
		t := strings.TrimSpace(part)
		if !slices.Contains(models.FindingTypes(), t) {
			return nil, false
		}

		want[t] = true
	}

	out := make([]string, 0, len(want))

	for _, t := range models.FindingTypes() {
		if want[t] {
			out = append(out, t)
		}
	}

	return out, true
}

// securityExploitParams reads ?sort, ?kev, ?epss_min and ?priority into params;
// it returns the message of the first invalid value, or "".
func securityExploitParams(q url.Values, params *models.FindingListParams) string {
	switch params.Sort = q.Get("sort"); params.Sort {
	case "":
		params.Sort = models.FindingSortSeverity
	case models.FindingSortSeverity, models.FindingSortPriority:
	default:
		return "sort must be one of: severity, priority"
	}

	if raw := q.Get("kev"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil || (raw != "true" && raw != "false") {
			return "kev must be true or false"
		}

		params.KEV = &v
	}

	if raw := q.Get("epss_min"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(v) || v < 0 || v > 1 {
			return "epss_min must be a number between 0 and 1"
		}

		params.EPSSMin = &v
	}

	if raw := q.Get("priority"); raw != "" {
		p, ok := exploit.ParsePriority(raw)
		if !ok || p == exploit.PriorityNone {
			return "priority must be one of: kev, epss, critical, high, medium, low"
		}

		rank := p.Rank()
		params.PriorityRank = &rank
	}

	return ""
}

// securityListParams reads and validates the findings query string. It returns
// the message of the first invalid value, or "".
func (h *SecurityHandler) securityListParams(r *http.Request, scope models.FindingScope) (models.FindingListParams, string) {
	q := r.URL.Query()

	params := models.FindingListParams{
		Severity: q.Get("severity"),
		Status:   q.Get("status"),
		Q:        strings.TrimSpace(q.Get("q")),
		Limit:    queryIntParam(r, "limit", securityFindingsDefaultLimit, 1, securityFindingsMaxLimit),

		EPSSThreshold: h.exploit.Threshold(),
	}

	var msg string

	if params.FindingFilter, msg = securityFilter(r, scope); msg != "" {
		return params, msg
	}

	var ok bool

	if params.Types, ok = securityTypes(q.Get("type")); !ok {
		return params, "type must be a comma-separated list of: vuln, license, secret, ai"
	}

	switch params.Severity {
	case "", models.FindingSeverityCritical, models.FindingSeverityHigh, models.FindingSeverityMedium, models.FindingSeverityLow:
	default:
		return params, "severity must be one of: critical, high, medium, low"
	}

	switch params.Status {
	case "", models.FindingStatusOpen, models.FindingStatusQuarantined, models.FindingStatusSuppressed:
	default:
		return params, "status must be one of: open, quarantined, suppressed"
	}

	if len(params.Q) > securityQueryMaxLen {
		return params, "q is too long"
	}

	if msg = securityExploitParams(q, &params); msg != "" {
		return params, msg
	}

	if raw := q.Get("cursor"); raw != "" {
		cursor, err := decodeFindingCursor(raw)
		if err != nil || cursor.Sort != params.Sort {
			return params, "invalid cursor"
		}

		params.After = cursor
	}

	return params, ""
}

// handleList lists findings of every type across repositories, worst first, one
// keyset page at a time.
func (h *SecurityHandler) handleList(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.findingScope(w, r)
	if !ok {
		return
	}

	params, msg := h.securityListParams(r, scope)
	if msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	page, err := h.db.ListSecurityFindings(r.Context(), params)
	if err != nil {
		if errors.Is(err, database.ErrInvalidFindingFilter) {
			writeError(w, http.StatusBadRequest, "validation_failed", "invalid filter")
			return
		}

		h.log.Errorw("security findings: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	resp := responses.SecurityFindingsResponse{
		Findings: make([]responses.SecurityFindingResponse, 0, len(page.Findings)),
		HasMore:  page.Next != nil,
	}

	for _, f := range page.Findings {
		resp.Findings = append(resp.Findings, h.findingToResponse(f))
	}

	if page.Next != nil {
		next := encodeFindingCursor(page.Next)
		resp.NextCursor = &next
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleSummary counts findings per type and the scanned repositories of the
// caller's scope, optionally narrowed by ?repo and ?project.
func (h *SecurityHandler) handleSummary(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.findingScope(w, r)
	if !ok {
		return
	}

	filter, msg := securityFilter(r, scope)
	if msg != "" {
		writeError(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}

	s, err := h.db.SecurityFindingsSummary(r.Context(), filter)
	if err != nil {
		h.log.Errorw("security findings summary: db error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	typeSummary := func(t string) responses.SecurityTypeSummaryResponse {
		v := s.ByType[t]

		return responses.SecurityTypeSummaryResponse{
			Total: v.Total, Critical: v.Critical, High: v.High, Medium: v.Medium, Low: v.Low, Quarantined: v.Quarantined,
		}
	}

	writeJSON(w, http.StatusOK, responses.SecuritySummaryResponse{
		ByType: responses.SecurityByTypeResponse{
			Vuln:    typeSummary(models.FindingVuln),
			License: typeSummary(models.FindingLicense),
			Secret:  typeSummary(models.FindingSecret),
			AI:      typeSummary(models.FindingAI),
		},
		ScannedRepositories: s.ScannedRepositories,
		TotalRepositories:   s.TotalRepositories,
		LastScanAt:          formatTimePtr(s.LastScanAt),
	})
}

// findingToResponse renders a finding with its exploit priority, derived from
// the row aggregates and the finding's severity rank.
func (h *SecurityHandler) findingToResponse(f *models.Finding) responses.SecurityFindingResponse {
	return responses.SecurityFindingResponse{
		ID:             f.ID,
		Type:           f.Type,
		Repository:     f.Repository,
		Format:         f.Format,
		PackageName:    f.PackageName,
		PackageVersion: f.PackageVersion,
		ArtifactID:     f.ArtifactID,
		Severity:       f.Severity(),
		Counts:         responses.SecurityFindingCounts{Critical: f.Critical, High: f.High, Medium: f.Medium, Low: f.Low},
		Title:          f.Title,
		Scanner:        f.Scanner,
		Status:         f.Status,
		Quarantined:    f.Quarantined,
		VEXStatus:      f.VEXStatus,
		KEVCount:       f.KEVCount,
		MaxEPSS:        f.MaxEPSS,
		Priority:       string(exploit.RowPriority(f.KEVCount, f.MaxEPSS, f.SeverityRank, h.exploit.Threshold())),
		ScannedAt:      formatTime(f.ScannedAt),
	}
}

var errInvalidFindingCursor = errors.New("invalid security finding cursor")

// encodeFindingCursor renders a keyset position as an opaque URL-safe token:
// base64url("<rank>:<scanned at unix microseconds>:<finding id>") for the
// severity order (the format before sort existed) and
// base64url("p:<rank>:<scanned at unix microseconds>:<finding id>") for the
// priority order, so a cursor only decodes under the order that minted it.
// Postgres stores timestamptz at microsecond precision, so the round trip is exact.
func encodeFindingCursor(c *models.FindingCursor) string {
	prefix := ""
	if c.Sort == models.FindingSortPriority {
		prefix = findingPriorityCursorTag + ":"
	}

	raw := prefix + strconv.Itoa(c.Rank) + ":" + strconv.FormatInt(c.ScannedAt.UnixMicro(), 10) + ":" + c.ID

	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeFindingCursor parses a token produced by encodeFindingCursor. A forged
// value is an error here, never a failing query.
func decodeFindingCursor(s string) (*models.FindingCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, errInvalidFindingCursor
	}

	parts := strings.Split(string(raw), ":")
	sort, maxRank := models.FindingSortSeverity, 4

	if len(parts) == 4 && parts[0] == findingPriorityCursorTag {
		sort, maxRank, parts = models.FindingSortPriority, 6, parts[1:]
	}

	if len(parts) != 3 {
		return nil, errInvalidFindingCursor
	}

	rank, err := strconv.Atoi(parts[0])
	if err != nil || rank < 0 || rank > maxRank {
		return nil, errInvalidFindingCursor
	}

	// time.UnixMicro wraps outside Postgres's timestamptz range.
	us, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || us < minAuditCursorMicros || us > maxAuditCursorMicros {
		return nil, errInvalidFindingCursor
	}

	id, err := uuid.Parse(parts[2])
	if err != nil {
		return nil, errInvalidFindingCursor
	}

	return &models.FindingCursor{Sort: sort, Rank: rank, ScannedAt: time.UnixMicro(us).UTC(), ID: id.String()}, nil
}
