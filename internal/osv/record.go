// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package osv

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// record is the subset of an OSV record (OSV schema 1.x) the mirror keeps.
type record struct {
	ID        string          `json:"id"`
	Modified  time.Time       `json:"modified"`
	Published *time.Time      `json:"published"`
	Withdrawn *time.Time      `json:"withdrawn"`
	Aliases   []string        `json:"aliases"`
	Summary   string          `json:"summary"`
	Severity  json.RawMessage `json:"severity"`
	Affected  []struct {
		Package struct {
			Ecosystem string `json:"ecosystem"`
			Name      string `json:"name"`
		} `json:"package"`
		Ranges   json.RawMessage `json:"ranges"`
		Versions []string        `json:"versions"`
	} `json:"affected"`
	References []struct {
		URL string `json:"url"`
	} `json:"references"`
	DatabaseSpecific struct {
		Severity string   `json:"severity"`
		CWEIDs   []string `json:"cwe_ids"`
	} `json:"database_specific"`
}

var errNoRecordID = errors.New("record has no id")

// maxRefs bounds the references kept per advisory.
const maxRefs = 50

// ParseRecord decodes one OSV record. Only the affected packages of the wanted
// ecosystems are kept, with their names normalized for matching. wanted maps
// an OSV ecosystem name to true.
func ParseRecord(raw []byte, wanted map[string]bool) (*models.OSVAdvisory, error) {
	var rec record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("decode osv record: %w", err)
	}

	if rec.ID == "" {
		return nil, errNoRecordID
	}

	adv := &models.OSVAdvisory{
		ID:         rec.ID,
		Modified:   rec.Modified,
		Published:  rec.Published,
		Withdrawn:  rec.Withdrawn,
		Summary:    rec.Summary,
		Severity:   rec.Severity,
		DBSeverity: rec.DatabaseSpecific.Severity,
		CWEIDs:     rec.DatabaseSpecific.CWEIDs,
	}

	for _, alias := range rec.Aliases {
		if alias != "" && alias != rec.ID {
			adv.Aliases = append(adv.Aliases, alias)
		}
	}

	for _, ref := range rec.References {
		if ref.URL != "" && len(adv.Refs) < maxRefs {
			adv.Refs = append(adv.Refs, ref.URL)
		}
	}

	for _, a := range rec.Affected {
		if !wanted[a.Package.Ecosystem] || a.Package.Name == "" {
			continue
		}

		adv.Affected = append(adv.Affected, models.OSVAffected{
			Ecosystem: a.Package.Ecosystem,
			Name:      a.Package.Name,
			NameNorm:  NormalizeName(a.Package.Ecosystem, a.Package.Name),
			Ranges:    a.Ranges,
			Versions:  a.Versions,
		})
	}

	return adv, nil
}
