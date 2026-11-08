// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/webhook"
)

// The scan and quarantine webhook events of the three scan kinds share their
// payloads (webhook.ScanCompleted, webhook.QuarantineChange); the helpers here
// fill in the repository and the actor, then emit. Every one is a no-op for a
// nil publisher.

// vulnFindingStatus reports whether a vulnerability scan status carries findings.
func vulnFindingStatus(status string) bool { return status == "completed" || status == "partial" }

// emitScanCompleted announces scan.completed for s, a scan of repo.
func emitScanCompleted(ctx context.Context, p webhook.Publisher, repo *models.Repository, s webhook.ScanCompleted) {
	if p == nil {
		return
	}

	s.Repository, s.Format, s.RepoType = repo.Name, repo.Format, repo.RepoType
	webhook.Emit(ctx, p, repo.ID, webhook.EventScanCompleted, webhook.ScanPayload(webhook.EventScanCompleted, s))
}

// emitQuarantineChanged announces quarantine.changed for a change made by the
// request's principal (empty actor: the system).
func emitQuarantineChanged(r *http.Request, p webhook.Publisher, repo *models.Repository, q webhook.QuarantineChange) {
	if p == nil {
		return
	}

	q.Actor = actorOf(r)
	emitQuarantine(r.Context(), p, repo, q)
}

// actorOf names the request's principal for an event payload; empty when the
// request is anonymous.
func actorOf(r *http.Request) string {
	if pr := currentPrincipal(r); pr != nil && !pr.IsAnonymous {
		return pr.Username
	}

	return ""
}

func emitQuarantine(ctx context.Context, p webhook.Publisher, repo *models.Repository, q webhook.QuarantineChange) {
	q.Repository, q.Format, q.RepoType = repo.Name, repo.Format, repo.RepoType
	webhook.Emit(ctx, p, repo.ID, webhook.EventQuarantineChanged, webhook.QuarantineChangedPayload(q))
}

// reviewQuarantine is the quarantine.changed payload of an administrator's
// decision on a scan result; it names the artifact the way the audit trail does.
func reviewQuarantine(quarantined bool, kind webhook.ScanKind, artifactID, pkg, version string) webhook.QuarantineChange {
	return webhook.QuarantineChange{
		Quarantined: quarantined, Kind: kind, ArtifactID: artifactID, Package: pkg, Version: version,
		Reason: "review of a " + string(kind) + " scan",
	}
}

// announceScan announces a stored vulnerability scan that reached a final
// status: scan.completed, and vulnerability.found when it has findings.
func (h *VulnHandler) announceScan(ctx context.Context, res *models.VulnScanResult) {
	if h.publisher == nil {
		return
	}

	repo, err := h.db.GetRepositoryByID(ctx, res.RepositoryID)
	if err != nil || repo == nil {
		h.log.Errorw("vuln scan: reading the repository for the scan events", "repository_id", res.RepositoryID, "error", err)
		return
	}

	s := webhook.ScanCompleted{
		Repository: repo.Name, Format: repo.Format, RepoType: repo.RepoType,
		ScanID: res.ID, Kind: webhook.ScanKindVulnerability, Status: res.ScanStatus,
		ArtifactID: res.ArtifactID, Package: res.PackageName, Version: res.PackageVersion,
		Counts: map[string]int{
			"total": res.VulnCount, "critical": res.CriticalCount, "high": res.HighCount,
			"medium": res.MediumCount, "low": res.LowCount,
		},
	}

	webhook.Emit(ctx, h.publisher, repo.ID, webhook.EventScanCompleted, webhook.ScanPayload(webhook.EventScanCompleted, s))

	if vulnFindingStatus(res.ScanStatus) && res.VulnCount > 0 {
		webhook.Emit(ctx, h.publisher, repo.ID, webhook.EventVulnerabilityFound, webhook.ScanPayload(webhook.EventVulnerabilityFound, s))
	}
}

// priorLicenseScan reads the stored license scan of an artifact before it is
// re-evaluated, so announceLicenseScan can tell a change of quarantine from a
// repeat. It reads nothing (nil, nil) without a publisher.
func priorLicenseScan(
	ctx context.Context,
	db LicenseStore,
	p webhook.Publisher,
	repoID, artifactID string,
) (*models.LicenseScanResult, error) {
	if p == nil {
		return nil, nil //nolint:nilnil // nobody listens: no previous state needed
	}

	return db.GetLicenseScanResult(ctx, repoID, artifactID) //nolint:wrapcheck // callers log and map to 500
}

// announceLicenseScan announces a stored license scan: scan.completed, and
// quarantine.changed when the result's quarantine differs from prior's (a
// blocking policy quarantined the artifact, or an earlier approval no longer
// holds). actor is who asked for the scan (actorOf), empty for the system.
func announceLicenseScan(
	ctx context.Context,
	p webhook.Publisher,
	repo *models.Repository,
	prior, res *models.LicenseScanResult,
	actor string,
) {
	if p == nil {
		return
	}

	emitScanCompleted(ctx, p, repo, webhook.ScanCompleted{
		ScanID: res.ID, Kind: webhook.ScanKindLicense, Status: res.ScanStatus, ArtifactID: res.ArtifactID,
		Counts: map[string]int{"licenses": len(res.Licenses)},
	})

	if wasQuarantined := prior != nil && prior.Quarantined; wasQuarantined != res.Quarantined {
		emitQuarantine(ctx, p, repo, webhook.QuarantineChange{
			Quarantined: res.Quarantined, Kind: webhook.ScanKindLicense, ArtifactID: res.ArtifactID,
			Reason: "blocking license policy", Actor: actor,
		})
	}
}
