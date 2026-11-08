// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhook

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// stored mimics what the dispatcher reads back: the payload as decoded JSON.
func stored(t *testing.T, event string, payload map[string]any) *models.WebhookDelivery {
	t.Helper()

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}

	return &models.WebhookDelivery{ID: "5b1f3c1e-0000-4000-8000-00000000e001", EventType: event, Payload: back}
}

func vulnScan() ScanCompleted {
	return ScanCompleted{
		Repository: "npm-hosted", Format: "npm", RepoType: "hosted", ScanID: "scan-1", Kind: ScanKindVulnerability,
		Status: "completed", ArtifactID: "lodash@4.17.20", Package: "lodash", Version: "4.17.20",
		Counts: map[string]int{"total": 3, "critical": 1, "high": 2, "medium": 0, "low": 0},
	}
}

func newEventPayloads(t *testing.T) map[string]map[string]any {
	t.Helper()

	expires := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	return map[string]map[string]any{
		EventScanCompleted:      ScanPayload(EventScanCompleted, vulnScan()),
		EventVulnerabilityFound: ScanPayload(EventVulnerabilityFound, vulnScan()),
		EventQuarantineChanged: QuarantineChangedPayload(QuarantineChange{
			Repository: "npm-hosted", Format: "npm", RepoType: "hosted", Quarantined: true, Kind: ScanKindLicense,
			ArtifactID: "lodash@4.17.20", Reason: "blocking license policy", Actor: "alice",
		}),
		EventBundleCheckUpdated: BundleCheckUpdatedPayload(BundleCheckUpdate{
			Repository: "src", Format: "raw", Bundle: "shop", Version: "1.0", Check: "conforma", Status: "success",
			Summary: "all rules passed", DetailsURL: "https://ci.example.com/run/1", ReportedBy: "tekton", ExpiresAt: &expires,
		}),
	}
}

func TestNewEventsAreSubscribable(t *testing.T) {
	for _, event := range []string{EventScanCompleted, EventVulnerabilityFound, EventQuarantineChanged, EventBundleCheckUpdated} {
		if !IsSubscribableEvent(event) {
			t.Errorf("%s must be subscribable", event)
		}
	}

	want := map[string]string{
		EventScanCompleted: "scan.completed", EventVulnerabilityFound: "vulnerability.found",
		EventQuarantineChanged: "quarantine.changed", EventBundleCheckUpdated: "bundle.check_updated",
	}
	for got, name := range want {
		if got != name {
			t.Errorf("event constant = %q, want %q", got, name)
		}
	}
}

func TestNewEventsRenderInEveryFormat(t *testing.T) {
	schema := loadSchema(t, "cloudevents-schema-1.0.2.json")

	for event, payload := range newEventPayloads(t) {
		d := stored(t, event, payload)

		ce := decode(t, firstOf(render(t, models.PayloadFormatCloudEvents, d)))
		if ce["type"] != "io.openartifact."+event {
			t.Errorf("%s: cloudevents type = %v", event, ce["type"])
		}

		if err := validate(t, schema, ce); err != nil {
			t.Errorf("%s: not a valid CloudEvents event: %v", event, err)
		}

		data, _ := ce["data"].(map[string]any)
		if data["event"] != event {
			t.Errorf("%s: data.event = %v", event, data["event"])
		}

		for _, format := range PayloadFormats() {
			body, _ := render(t, format, d)
			if !json.Valid(body) {
				t.Errorf("%s/%s: invalid JSON", event, format)
			}
		}
	}
}

func TestNewEventSummaries(t *testing.T) {
	tests := []struct {
		event    string
		headline string
		facts    map[string]string
		severity severity
	}{
		{
			EventScanCompleted, "Scan completed: lodash 4.17.20 (completed)",
			map[string]string{"Scan": "vulnerability", "Findings": "critical 1, high 2", "Repository": "npm-hosted"}, severityInfo,
		},
		{
			EventVulnerabilityFound, "Vulnerabilities found: lodash 4.17.20 (completed)",
			map[string]string{"Findings": "critical 1, high 2"}, severityBad,
		},
		{
			EventQuarantineChanged, "Quarantine changed: lodash@4.17.20 quarantined",
			map[string]string{"State": "quarantined", "Reason": "blocking license policy", "By": "alice"}, severityBad,
		},
		{
			EventBundleCheckUpdated, "Bundle check updated: conforma success on shop 1.0",
			map[string]string{"Bundle": "shop 1.0", "Check": "conforma", "Status": "success", "Reported by": "tekton"}, severityGood,
		},
	}

	payloads := newEventPayloads(t)

	for _, tt := range tests {
		s := summarize(stored(t, tt.event, payloads[tt.event]))
		if s.headline != tt.headline {
			t.Errorf("%s: headline = %q, want %q", tt.event, s.headline, tt.headline)
		}

		if s.severity != tt.severity {
			t.Errorf("%s: severity = %v, want %v", tt.event, s.severity, tt.severity)
		}

		got := map[string]string{}
		for _, f := range s.facts {
			got[f.name] = f.value
		}

		for name, value := range tt.facts {
			if got[name] != value {
				t.Errorf("%s: fact %q = %q, want %q (facts %v)", tt.event, name, got[name], value, got)
			}
		}
	}
}

func TestQuarantineChangedPayloadStates(t *testing.T) {
	for quarantined, state := range map[bool]string{true: "quarantined", false: "released"} {
		p := QuarantineChangedPayload(QuarantineChange{Quarantined: quarantined, Kind: ScanKindSecret, ArtifactID: "a"})

		block, _ := p["quarantine"].(map[string]any)
		if block["state"] != state || p["event"] != EventQuarantineChanged {
			t.Errorf("quarantined=%v: payload = %v", quarantined, p)
		}
	}

	released := summarize(
		stored(t, EventQuarantineChanged, QuarantineChangedPayload(QuarantineChange{Kind: ScanKindSecret, ArtifactID: "a"})),
	)
	if released.severity != severityGood || !strings.HasSuffix(released.headline, "released") {
		t.Errorf("released summary = %+v", released)
	}
}

func TestBundleCheckUpdatedPayloadExpiry(t *testing.T) {
	without := BundleCheckUpdatedPayload(BundleCheckUpdate{Check: "scan", Status: "pending"})

	check, _ := without["check"].(map[string]any)
	if v, ok := check["expires_at"]; !ok || v != nil {
		t.Errorf("a check without expiry carries expires_at: null, got %v", check)
	}

	exp := time.Date(2026, 3, 1, 12, 0, 0, 0, time.FixedZone("x", 3600))
	with := BundleCheckUpdatedPayload(BundleCheckUpdate{Check: "scan", Status: "success", ExpiresAt: &exp})

	check, _ = with["check"].(map[string]any)
	if check["expires_at"] != "2026-03-01T11:00:00Z" {
		t.Errorf("expires_at = %v, want UTC RFC 3339", check["expires_at"])
	}
}

func firstOf(b []byte, _ string) []byte { return b }
