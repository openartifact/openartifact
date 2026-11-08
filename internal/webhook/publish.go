// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhook

import (
	"context"
	"time"
)

// Publisher is the slice of *Dispatcher that request handlers use to announce
// events. Handlers hold it as an optional dependency: a nil Publisher (tests,
// webhooks not wired) turns every Emit into a no-op.
type Publisher interface {
	Dispatch(ctx context.Context, repoID, eventType string, payload map[string]any)
}

// emitTimeout bounds one Emit: Dispatch performs a webhook lookup plus one
// insert per matching webhook.
const emitTimeout = 10 * time.Second

// Emit publishes an event through p, if any. The event is detached from the
// request's cancellation (a client hanging up right after a successful upload
// must not lose the event) but bounded by emitTimeout. Failures are logged by
// the dispatcher and never surface to the caller: an event that cannot be
// recorded must not fail the operation that produced it.
func Emit(ctx context.Context, p Publisher, repoID, eventType string, payload map[string]any) {
	if p == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), emitTimeout)
	defer cancel()

	p.Dispatch(ctx, repoID, eventType, payload)
}

// BundlePromotion describes a completed release bundle promotion.
type BundlePromotion struct {
	Repository string // target repository name
	Format     string // target repository format
	Bundle     string // bundle name
	Version    string // bundle version
	FromStage  string // stage the bundle left ("" when it had none)
	ToStage    string // stage the bundle reached
	Items      int    // bundle items promoted
	Copied     int    // artifacts newly written to the target
	Existing   int    // artifacts the target already held with the same content
	Replaced   int    // artifacts a forced promotion moved to the target's trash
}

// BundlePromotedPayload builds the JSON payload of a bundle.promoted event.
func BundlePromotedPayload(b BundlePromotion) map[string]any {
	return map[string]any{
		"event":      EventBundlePromoted,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"repository": b.Repository,
		"format":     b.Format,
		"type":       "hosted",
		"bundle":     map[string]any{"name": b.Bundle, "version": b.Version},
		"from_stage": b.FromStage,
		"to_stage":   b.ToStage,
		"items":      b.Items,
		"copied":     b.Copied,
		"existing":   b.Existing,
		"replaced":   b.Replaced,
	}
}

// VulnerabilityFinding is one advisory of a vulnerability.new event.
type VulnerabilityFinding struct {
	ID       string
	Severity string
	CVSS     float64
	Summary  string
	FixedIn  string
}

// VulnerabilityDetected describes the stored package version a
// vulnerability.new event is about and the advisories newly affecting it.
type VulnerabilityDetected struct {
	Artifact Artifact
	// RepoType is "hosted" or "proxy".
	RepoType string
	Findings []VulnerabilityFinding
}

// VulnerabilityNewPayload builds the JSON payload of a vulnerability.new event.
// The "source" is always "osv-mirror": the findings come from the local OSV
// mirror (OSV data, CC-BY 4.0, https://osv.dev).
func VulnerabilityNewPayload(v VulnerabilityDetected) map[string]any {
	payload := ArtifactPayload(EventVulnerabilityNew, v.Artifact)
	findings := make([]any, len(v.Findings))

	for i, f := range v.Findings {
		finding := map[string]any{"id": f.ID, "severity": f.Severity, "cvss": f.CVSS}
		if f.Summary != "" {
			finding["summary"] = f.Summary
		}

		if f.FixedIn != "" {
			finding["fixed_in"] = f.FixedIn
		}

		findings[i] = finding
	}

	payload["type"] = v.RepoType
	payload["source"] = "osv-mirror"
	payload["vulnerabilities"] = findings

	return payload
}

// Artifact describes the artifact an artifact.* event is about.
type Artifact struct {
	Repository string // repository name
	Format     string // repository format (deb, npm, ...)
	Name       string // package / artifact name
	Version    string // version, when the format has one
	Path       string // path or file name within the repository
	SHA256     string // content digest, when known
	Size       int64  // size in bytes, when known
}

// ArtifactPayload builds the JSON payload of an artifact.* event.
func ArtifactPayload(eventType string, a Artifact) map[string]any {
	artifact := map[string]any{
		"name": a.Name,
		"path": a.Path,
	}

	if a.Version != "" {
		artifact["version"] = a.Version
	}

	if a.SHA256 != "" {
		artifact["sha256"] = a.SHA256
	}

	if a.Size > 0 {
		artifact["size"] = a.Size
	}

	return map[string]any{
		"event":      eventType,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"repository": a.Repository,
		"format":     a.Format,
		"artifact":   artifact,
	}
}

// RepositoryPayload builds the JSON payload of a repository.* event.
func RepositoryPayload(eventType, name, format, repoType string) map[string]any {
	return map[string]any{
		"event":      eventType,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"repository": name,
		"format":     format,
		"type":       repoType,
	}
}

// ScanKind names what a scan examines.
type ScanKind string

const (
	ScanKindVulnerability ScanKind = "vulnerability"
	ScanKindLicense       ScanKind = "license"
	ScanKindSecret        ScanKind = "secret"
)

// ScanCompleted describes a scan that reached a final status: the subject of
// the scan.completed and vulnerability.found events.
type ScanCompleted struct {
	Repository string // repository name
	Format     string // repository format
	RepoType   string // "hosted", "proxy" or "group"
	ScanID     string // id of the stored scan result
	Kind       ScanKind
	Status     string // scan_status of the stored result (completed, partial, failed, clean, flagged)
	ArtifactID string // the scan's artifact identifier
	Package    string // package name, when the scan names one
	Version    string // package version, when the scan names one
	// Counts are the findings of the scan: critical, high, medium, low and
	// total for a vulnerability scan; findings for a secret scan; licenses for
	// a license scan.
	Counts map[string]int
}

// ScanPayload builds the JSON payload of a scan.completed or
// vulnerability.found event (event names which).
func ScanPayload(event string, s ScanCompleted) map[string]any {
	counts := make(map[string]any, len(s.Counts))
	for k, v := range s.Counts {
		counts[k] = v
	}

	scan := map[string]any{
		"id":          s.ScanID,
		"kind":        string(s.Kind),
		"status":      s.Status,
		"artifact_id": s.ArtifactID,
		"counts":      counts,
	}

	if s.Package != "" {
		scan["package"] = s.Package
	}

	if s.Version != "" {
		scan["version"] = s.Version
	}

	return map[string]any{
		"event":      event,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"repository": s.Repository,
		"format":     s.Format,
		"type":       s.RepoType,
		"scan":       scan,
	}
}

// QuarantineChange describes an artifact quarantined or released.
type QuarantineChange struct {
	Repository  string
	Format      string
	RepoType    string
	Quarantined bool // the new state: true quarantined, false released
	Kind        ScanKind
	ArtifactID  string
	Package     string
	Version     string
	Reason      string // why: "review by an administrator", "blocking license policy"
	Actor       string // the user or token that did it; empty for the system
}

// QuarantineChangedPayload builds the JSON payload of a quarantine.changed event.
func QuarantineChangedPayload(q QuarantineChange) map[string]any {
	state := "released"
	if q.Quarantined {
		state = "quarantined"
	}

	block := map[string]any{
		"state":       state,
		"kind":        string(q.Kind),
		"artifact_id": q.ArtifactID,
		"reason":      q.Reason,
		"actor":       q.Actor,
	}

	if q.Package != "" {
		block["package"] = q.Package
	}

	if q.Version != "" {
		block["version"] = q.Version
	}

	return map[string]any{
		"event":      EventQuarantineChanged,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"repository": q.Repository,
		"format":     q.Format,
		"type":       q.RepoType,
		"quarantine": block,
	}
}

// BundleCheckUpdate describes a check result reported against a release bundle.
type BundleCheckUpdate struct {
	Repository string // first source repository of the bundle; empty when it has no items
	Format     string
	Bundle     string
	Version    string
	Check      string
	Status     string
	Summary    string
	DetailsURL string
	ReportedBy string
	ExpiresAt  *time.Time
}

// BundleCheckUpdatedPayload builds the JSON payload of a bundle.check_updated event.
func BundleCheckUpdatedPayload(c BundleCheckUpdate) map[string]any {
	check := map[string]any{
		"name":        c.Check,
		"status":      c.Status,
		"summary":     c.Summary,
		"details_url": c.DetailsURL,
		"reported_by": c.ReportedBy,
		"expires_at":  nil,
	}

	if c.ExpiresAt != nil {
		check["expires_at"] = c.ExpiresAt.UTC().Format(time.RFC3339)
	}

	return map[string]any{
		"event":      EventBundleCheckUpdated,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"repository": c.Repository,
		"format":     c.Format,
		"type":       "hosted",
		"bundle":     map[string]any{"name": c.Bundle, "version": c.Version},
		"check":      check,
	}
}
